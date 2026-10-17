package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/docker"
	"strataos/internal/storage"
	"strataos/internal/store"
)

type alert struct {
	Level   string `json:"level"`
	Message string `json:"message"`
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
	for _, r := range raids {
		switch r.Health {
		case "degraded", "failed":
			out = append(out, alert{"error", fmt.Sprintf("La grappe RAID %s (%s) est %s", r.Label, r.Name, map[string]string{"degraded": "dégradée", "failed": "en panne"}[r.Health])})
		case "rebuilding":
			out = append(out, alert{"warning", fmt.Sprintf("Reconstruction de la grappe RAID %s en cours (%.1f %%)", r.Label, r.SyncProgress)})
		}
	}
	if storage.ZFSBroken() {
		out = append(out, alert{"error", "Le module ZFS ne se charge pas (noyau mis à jour ?) : les pools ZFS sont indisponibles. Réinstallez ZFS depuis Stockage → ZFS"})
	}
	for _, p := range pools {
		if p.Health != "ONLINE" {
			out = append(out, alert{"error", fmt.Sprintf("Le pool ZFS %s est en état %s", p.Name, p.Health)})
		}
	}
	for _, v := range vols {
		if v.Health == "unmounted" {
			out = append(out, alert{"warning", fmt.Sprintf("Le volume %s n'est pas monté", v.Name)})
		}
		if v.Total > 0 && float64(v.Used)/float64(v.Total) > 0.9 {
			out = append(out, alert{"warning", fmt.Sprintf("Le volume %s est plein à %.0f %%", v.Name, 100*float64(v.Used)/float64(v.Total))})
		}
	}
	return out
}

// healthWatcher periodically checks storage health and notifies on new problems.
func (s *Server) healthWatcher() {
	known := map[string]bool{}
	var mu sync.Mutex
	check := func() {
		s.recoverStorage()
		raids, _ := storage.ListRaid()
		pools, _ := storage.ListZPools(false)
		vols := s.volumeSummaries(raids, pools)
		cur := map[string]bool{}
		for _, a := range s.alerts(raids, pools, vols) {
			if strings.HasPrefix(a.Message, "Reconstruction") {
				continue // progress changes constantly
			}
			cur[a.Message] = true
			mu.Lock()
			isNew := !known[a.Message]
			mu.Unlock()
			if isNew {
				s.store.Log(a.Level, "health", "", a.Message)
				s.notify("Alerte", a.Message)
			}
		}
		disks, _ := storage.ListDisks()
		for _, d := range disks {
			info, err := storage.Smart(d.Path)
			if err != nil || info.Passed == nil || *info.Passed {
				continue
			}
			msg := fmt.Sprintf("Le disque %s (%s %s) échoue au contrôle de santé SMART : remplacez-le", d.Path, d.Model, d.Serial)
			cur[msg] = true
			if !known[msg] {
				s.store.Log("error", "health", "", msg)
				s.notify("Disque défaillant", msg)
			}
		}
		mu.Lock()
		known = cur
		mu.Unlock()
	}
	time.Sleep(30 * time.Second)
	// First pass after boot: recover storage, then regenerate Samba/NFS config so an
	// upgraded StrataOS applies its current templates.
	s.recoverStorage()
	s.reapplySharePermissions()
	_ = s.applyShares()
	_ = s.applyFileServices()
	for {
		check()
		time.Sleep(5 * time.Minute)
	}
}

func (s *Server) notify(subject, msg string) {
	go func() {
		if err := s.sendNotification(subject, msg); err != nil {
			slog.Warn("notification failed", "err", err)
		}
	}()
}

func (s *Server) sendNotification(subject, msg string) error {
	var set store.Settings
	s.store.Read(func(st *store.State) { set = st.Settings })
	host, _ := hostname()
	var errs []string
	sent := false
	if set.WebhookURL != "" {
		sent = true
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
	if set.SMTPHost != "" && set.NotifyEmail != "" {
		sent = true
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
	if !sent {
		return badRequest("no notification channel configured")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
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
