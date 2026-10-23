package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"regexp"
	"strataos/internal/accounts"
	"strataos/internal/i18n"
	"strconv"
	"strings"
	"time"

	"strataos/internal/docker"
	"strataos/internal/storage"
	"strataos/internal/store"
)

type alert struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	// Key identifies the problem for notifications when the message holds
	// changing numbers (percentages, counts); "" = the message itself.
	// "progress:" keys are operations notified by the progress tracker.
	Key string `json:"-"`
	// Subject of the notification ("" = Alert).
	Subject string `json:"-"`
}

func (a alert) dedupKey() string {
	if a.Key != "" {
		return a.Key
	}
	return a.Message
}

// freshAlerts returns the alerts not present at the previous check (known),
// and the keys of the current ones. Progress alerts are left out.
func freshAlerts(known map[string]bool, list []alert) ([]alert, map[string]bool) {
	cur := map[string]bool{}
	var fresh []alert
	for _, a := range list {
		k := a.dedupKey()
		if strings.HasPrefix(k, "progress:") || cur[k] {
			continue
		}
		cur[k] = true
		if !known[k] {
			fresh = append(fresh, a)
		}
	}
	return fresh, cur
}

type volumeSummary struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	MountPoint string `json:"mountPoint"`
	storage.Usage
	Health string `json:"health"`
}

// overview is the dashboard summary.
func (s *Server) overview() map[string]any {
	disks, _ := storage.ListDisks()
	raids, _ := storage.ListRaid()
	pools, _ := storage.ListZPools(false)
	vols := s.volumeSummaries(raids, pools)
	alerts := s.alerts(raids, pools, vols)
	var users, shares int
	s.store.Read(func(st *store.State) { users, shares = len(st.Users), len(st.Shares) })
	ctr := map[string]int{"running": 0, "total": 0}
	if docker.Available() {
		var list []struct {
			State string `json:"State"`
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if s.docker.Call(ctx, "GET", "/containers/json", map[string][]string{"all": {"1"}}, nil, &list) == nil {
			ctr["total"] = len(list)
			for _, c := range list {
				if c.State == "running" {
					ctr["running"]++
				}
			}
		}
	}
	return map[string]any{
		"disks": len(disks), "raids": len(raids), "pools": len(pools), "volumes": vols,
		"users": users, "shares": shares, "containers": ctr, "alerts": alerts,
	}
}

func (s *Server) volumeSummaries(raids []storage.RaidArray, pools []storage.ZPool) []volumeSummary {
	out := []volumeSummary{}
	var vols []store.Volume
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			vols = append(vols, *v)
		}
	})
	for _, v := range vols {
		vs := volumeSummary{Name: v.Name, Kind: v.FSType, MountPoint: v.MountPoint, Health: "healthy"}
		if storage.IsMountpoint(v.MountPoint) {
			vs.Usage, _ = storage.StatFS(v.MountPoint)
		} else {
			vs.Health = "unmounted"
		}
		devices := storage.DevicesByUUID(v.UUID)
		if len(devices) == 0 {
			devices = v.Devices
		}
		for _, r := range raids {
			for _, d := range devices {
				if d == r.Device || strings.HasSuffix(d, "/"+r.Label) || strings.HasSuffix(d, "/"+r.Name) {
					if r.Health != "healthy" {
						vs.Health = r.Health
					}
				}
			}
		}
		out = append(out, vs)
	}
	for _, p := range pools {
		h := "healthy"
		if p.Health != "ONLINE" {
			h = strings.ToLower(p.Health)
		}
		out = append(out, volumeSummary{Name: p.Name, Kind: "zfs", MountPoint: storage.MountRoot + "/" + p.Name,
			Usage: storage.Usage{Total: p.Size, Used: p.Alloc, Free: p.Free}, Health: h})
	}
	return out
}

func (s *Server) alerts(raids []storage.RaidArray, pools []storage.ZPool, vols []volumeSummary) []alert {
	out := []alert{}
	// Disk health, refreshed by the health watcher (SMART is too slow to read per request).
	if d := s.diskAlerts.Load(); d != nil {
		out = append(out, (*d)...)
	}
	return append(out, storageAlerts(raids, pools, vols, storage.ZFSBroken())...)
}

func storageAlerts(raids []storage.RaidArray, pools []storage.ZPool, vols []volumeSummary, zfsBroken bool) []alert {
	var out []alert
	for _, r := range raids {
		switch r.Health {
		case "degraded", "failed":
			out = append(out, alert{Level: "error", Message: fmt.Sprintf("RAID array %s (%s) %s", r.Label, r.Name, map[string]string{"degraded": "is degraded", "failed": "has failed"}[r.Health])})
		case "rebuilding":
			// The dashboard shows the progress; notifications come from the progress tracker.
			out = append(out, alert{Level: "warning", Message: fmt.Sprintf("RAID array %s is rebuilding (%.1f%%)", r.Label, r.SyncProgress), Key: "progress:" + raidOpKey(r)})
		}
	}
	if zfsBroken {
		out = append(out, alert{Level: "error", Message: "The ZFS module does not load (kernel updated?): ZFS pools are unavailable. Reinstall ZFS from Storage → ZFS"})
	}
	for _, p := range pools {
		if p.Health != "ONLINE" {
			out = append(out, alert{Level: "error", Message: fmt.Sprintf("ZFS pool %s is %s", p.Name, p.Health)})
		}
	}
	for _, v := range vols {
		if v.Health == "unmounted" {
			out = append(out, alert{Level: "warning", Message: fmt.Sprintf("Volume %s is not mounted", v.Name)})
		}
		if v.Total > 0 && float64(v.Used)/float64(v.Total) > 0.9 {
			out = append(out, alert{Level: "warning", Message: fmt.Sprintf("Volume %s is %.0f%% full", v.Name, 100*float64(v.Used)/float64(v.Total)), Key: "volume-full:" + v.Name})
		}
	}
	return out
}

var digitsRe = regexp.MustCompile(`[0-9]+`)

// weakDiskKey identifies the warnings of a disk by their kind, not their
// counts: a growing sector count or a temperature that moves is not new.
func weakDiskKey(path, serial string, warnings []string) string {
	kinds := make([]string, len(warnings))
	for i, w := range warnings {
		kinds[i] = digitsRe.ReplaceAllString(w, "#")
	}
	return "disk-weak:" + path + ":" + serial + ":" + strings.Join(kinds, ",")
}

// healthWatcher periodically checks storage health and notifies on new problems.
func (s *Server) healthWatcher() {
	known := map[string]bool{}
	var progress progressTracker
	check := func() {
		s.recoverStorage()
		raids, _ := storage.ListRaid()
		pools, _ := storage.ListZPools(false)
		vols := s.volumeSummaries(raids, pools)
		disks, _ := storage.ListDisks()
		var weak []alert
		for _, d := range disks {
			info, err := storage.SmartIfAwake(d.Path)
			if err != nil {
				continue
			}
			if !info.Standby {
				smartCache.Store(d.Path, smartEntry{info: *info, model: d.Model, serial: d.Serial})
			}
			if info.Passed != nil && !*info.Passed {
				weak = append(weak, alert{Level: "error", Message: fmt.Sprintf("Disk %s (%s %s) fails its SMART health check: replace it", d.Path, d.Model, d.Serial), Subject: "Failing disk"})
				continue
			}
			if len(info.Warnings) > 0 {
				msg := fmt.Sprintf("Disk %s (%s %s) is getting weak: %s. Check your backups and plan its replacement", d.Path, d.Model, d.Serial, strings.Join(info.Warnings, ", "))
				weak = append(weak, alert{Level: "warning", Message: msg, Key: weakDiskKey(d.Path, d.Serial, info.Warnings), Subject: "Disk weakening"})
			}
		}
		s.diskAlerts.Store(&weak)
		var fresh []alert
		fresh, known = freshAlerts(known, s.alerts(raids, pools, vols))
		for _, a := range fresh {
			subject := firstNonEmpty(a.Subject, "Alert")
			s.store.Log(a.Level, "health", "", a.Message)
			s.notify(a.Level, subject, a.Message)
		}
		// Long operations: once when they start, once when they end.
		scans := map[string]storage.ZScan{}
		scanned := true
		for _, p := range pools {
			z, err := storage.ZPoolScan(p.Name)
			if err != nil {
				scanned = false // unknown this time: don't take it for an end
				break
			}
			scans[p.Name] = z
		}
		if scanned {
			ops := append(raidOps(raids), zfsOps(scans)...)
			for _, e := range progress.update(ops, time.Now(), storageEnd(raids, scans)) {
				s.store.Log(e.level, "health", "", e.msg)
				s.notify(e.level, "Storage", e.msg)
			}
		}
	}
	time.Sleep(30 * time.Second)
	// First pass after boot: recover storage, then regenerate Samba/NFS config so an
	// upgraded StrataOS applies its current templates.
	s.recoverStorage()
	s.checkMissingAccounts()
	s.reapplySharePermissions()
	_ = s.applyShares()
	_ = s.applyFileServices()
	for {
		check()
		time.Sleep(5 * time.Minute)
	}
}

// notify sends a notification of level info, warning or error to the
// channels that accept it (switch, minimum level, quiet hours).
func (s *Server) notify(level, subject, msg string) {
	go func() {
		if _, err := s.sendNotification(level, subject, msg); err != nil && !errors.Is(err, errNoChannel) {
			slog.Warn("notification failed", "err", err)
		}
	}()
}

var errNoChannel = badRequest("no notification channel configured")

// sendNotification delivers a notification and returns the channels used.
// Level "" is an explicit send (test, monthly report): it ignores the
// minimum levels and quiet hours, but not a disabled channel.
func (s *Server) sendNotification(level, subject, msg string) ([]string, error) {
	var set store.Settings
	s.store.Read(func(st *store.State) { set = st.Settings })
	subject, msg = i18n.Lines(set.NotifyLang, subject), i18n.Lines(set.NotifyLang, msg)
	host, _ := hostname()
	quiet := !quietAllows(set.QuietHours, level, time.Now())
	ch := set.NotifyChannels
	configured, enabled := false, false
	// use tells whether a configured channel sends this notification.
	use := func(c store.NotifyChannel) bool {
		configured = true
		enabled = enabled || !c.Disabled
		return !quiet && channelAccepts(c, level)
	}
	var errs, sent []string
	if set.WebhookURL != "" && use(ch.Webhook) {
		sent = append(sent, "webhook")
		body, _ := json.Marshal(map[string]string{"title": subject, "message": msg, "host": host, "text": subject + ": " + msg, "content": subject + ": " + msg})
		cl := &http.Client{Timeout: 10 * time.Second}
		resp, err := cl.Post(set.WebhookURL, "application/json", bytes.NewReader(body))
		if err != nil {
			errs = append(errs, "webhook: "+err.Error())
		} else {
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				errs = append(errs, fmt.Sprintf("webhook: HTTP %d", resp.StatusCode))
			}
		}
	}
	if set.TelegramToken != "" && set.TelegramChatID != "" && use(ch.Telegram) {
		sent = append(sent, "telegram")
		if err := sendTelegram(set.TelegramToken, set.TelegramChatID, "🖥 "+host+"\n<b>"+html.EscapeString(subject)+"</b>\n"+html.EscapeString(msg)); err != nil {
			errs = append(errs, "telegram: "+err.Error())
		}
	}
	if set.NtfyURL != "" && use(ch.Ntfy) {
		sent = append(sent, "ntfy")
		if err := sendNtfy(set.NtfyURL, set.NtfyToken, "["+host+"] "+subject, msg); err != nil {
			errs = append(errs, "ntfy: "+err.Error())
		}
	}
	if ok, err := s.mobileNotify(level, subject, msg, use, ch.Mobile); ok { // mobile_push.go
		sent = append(sent, "mobile")
		if err != nil {
			errs = append(errs, "mobile: "+err.Error())
		}
	}
	if set.SMTPHost != "" && set.NotifyEmail != "" && use(ch.Email) {
		sent = append(sent, "email")
		from := set.SMTPFrom
		if from == "" {
			from = set.SMTPUser
		}
		port := set.SMTPPort
		if port == 0 {
			port = 587
		}
		var auth smtp.Auth
		if set.SMTPUser != "" {
			auth = smtp.PlainAuth("", set.SMTPUser, set.SMTPPassword, set.SMTPHost)
		}
		hdr := func(v string) string { return strings.NewReplacer("\r", "", "\n", " ").Replace(v) }
		body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n",
			hdr(from), hdr(set.NotifyEmail), mime.QEncoding.Encode("utf-8", hdr("["+host+"] "+subject)), time.Now().Format(time.RFC1123Z), token()[:24], hdr(host), msg)
		if err := sendMail(set.SMTPHost, port, set.SMTPSecurity, auth, hdr(from), hdr(set.NotifyEmail), []byte(body)); err != nil {
			errs = append(errs, "email: "+err.Error())
		}
	}
	switch {
	case !configured:
		return nil, errNoChannel
	case !enabled:
		return nil, badRequest("all notification channels are disabled")
	case len(errs) > 0:
		return sent, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return sent, nil
}

// sendMail sends one message. security: "tls" (implicit TLS, usually port 465),
// "starttls" (required), "none", or "auto" (TLS on 465, otherwise STARTTLS when offered).
func sendMail(host string, port int, security string, auth smtp.Auth, from, to string, msg []byte) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if security == "" || security == "auto" {
		security = "opportunistic"
		if port == 465 {
			security = "tls"
		}
	}
	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if security == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if security == "starttls" || security == "opportunistic" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return err
			}
		} else if security == "starttls" {
			return fmt.Errorf("the SMTP server does not offer STARTTLS")
		}
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// telegramBase is replaced in tests.
var telegramBase = "https://api.telegram.org"

var telegramTokenRe = regexp.MustCompile(`^[0-9]{5,15}:[A-Za-z0-9_-]{20,64}$`)

func telegramAPI(token, method string) (string, error) {
	if !telegramTokenRe.MatchString(token) {
		return "", fmt.Errorf("invalid bot token (format 123456:ABC…)")
	}
	return telegramBase + "/bot" + token + "/" + method, nil
}

func sendTelegram(token, chat, text string) error {
	u, err := telegramAPI(token, "sendMessage")
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true})
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		// The URL holds the token: never let it reach logs or the UI.
		return fmt.Errorf("Telegram unreachable")
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if !r.OK {
		return fmt.Errorf("%s", r.Description)
	}
	return nil
}

// telegramChats lists the chats that recently wrote to the bot, to find the chat ID.
func telegramChats(token string) ([]map[string]any, error) {
	u, err := telegramAPI(token, "getUpdates")
	if err != nil {
		return nil, err
	}
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Get(u)
	if err != nil {
		return nil, fmt.Errorf("Telegram unreachable")
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      []struct {
			Message *struct {
				Chat struct {
					ID        int64  `json:"id"`
					Type      string `json:"type"`
					Title     string `json:"title"`
					Username  string `json:"username"`
					FirstName string `json:"first_name"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || !r.OK {
		return nil, fmt.Errorf("Telegram refused the token: %s", r.Description)
	}
	seen := map[int64]bool{}
	out := []map[string]any{}
	for _, u := range r.Result {
		if u.Message == nil || seen[u.Message.Chat.ID] {
			continue
		}
		c := u.Message.Chat
		seen[c.ID] = true
		name := c.Title
		if name == "" {
			name = strings.TrimSpace(c.FirstName + " @" + c.Username)
		}
		out = append(out, map[string]any{"id": strconv.FormatInt(c.ID, 10), "name": name, "type": c.Type})
	}
	return out, nil
}

func sendNtfy(topicURL, token, title, msg string) error {
	u, err := url.Parse(topicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(strings.Trim(u.Path, "/")) == 0 {
		return fmt.Errorf("the ntfy address must be like https://ntfy.sh/my-topic")
	}
	req, _ := http.NewRequest("POST", topicURL, strings.NewReader(msg))
	req.Header.Set("Title", mime.QEncoding.Encode("utf-8", title))
	req.Header.Set("Priority", "high")
	req.Header.Set("Tags", "floppy_disk")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// checkMissingAccounts reports StrataOS users without a system account (after
// going back to a system restore point): their password must be set again.
func (s *Server) checkMissingAccounts() {
	var missing []string
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if u.Source == "" && !accounts.LinuxUserExists(u.Username) {
				missing = append(missing, u.Username)
			}
		}
	})
	if len(missing) == 0 {
		return
	}
	msg := "Accounts missing from the system (back to a restore point?): " + strings.Join(missing, ", ") + ". Set their password again in Users to recreate them."
	s.store.Log("warning", "users", "", msg)
	s.notify("warning", "Accounts to recreate", msg)
}
