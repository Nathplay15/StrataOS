package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strataos/internal/antivirus"
	"strataos/internal/directory"
	"strataos/internal/fileservices"
	"strataos/internal/ha"
	"strataos/internal/i18n"
	"strataos/internal/media"
	"strataos/internal/nvr"
	"strataos/internal/photos"
	"strataos/internal/vpn"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"strataos/internal/iscsi"
	"strataos/internal/jobs"
	"strataos/internal/network"
	"strataos/internal/remote"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

func hostname() (string, error) { return os.Hostname() }

const scriptsDir = "/usr/lib/strata/scripts"

func (s *Server) systemRoutes() {
	s.user("GET /api/system/info", func(r *http.Request) (any, error) {
		return system.GetInfo(s.version), nil
	})

	s.user("GET /api/system/stats", func(r *http.Request) (any, error) {
		h, cores := s.monitor.History()
		return map[string]any{"history": h, "cores": cores}, nil
	})

	// Dashboard summary combining storage health and usage.
	s.user("GET /api/system/overview", func(r *http.Request) (any, error) {
		return s.overview(), nil
	})

	s.admin("GET /api/system/services", func(r *http.Request) (any, error) {
		return system.Services(), nil
	})
	s.admin("POST /api/system/services/{unit}/{action}", func(r *http.Request) (any, error) {
		unit, action := r.PathValue("unit"), r.PathValue("action")
		if err := system.ServiceAction(unit, action); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "system", "Service "+unit+": "+action)
		return nil, nil
	})

	s.admin("GET /api/system/logs", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		return system.Logs(q.Get("unit"), qInt(r, "priority", -1), qInt(r, "lines", 300), q.Get("grep"))
	})

	s.admin("GET /api/system/events", func(r *http.Request) (any, error) {
		var ev []store.Event
		s.store.Read(func(st *store.State) {
			ev = make([]store.Event, len(st.Events))
			for i := range st.Events {
				ev[len(st.Events)-1-i] = st.Events[i]
			}
		})
		if len(ev) > 500 {
			ev = ev[:500]
		}
		return ev, nil
	})

	// Job titles and logs describe administrative operations (devices, commands,
	// package output): non-administrators only see the jobs they started.
	s.user("GET /api/jobs", func(r *http.Request) (any, error) {
		all := s.jobs.List()
		if isAdmin(r) {
			return all, nil
		}
		mine := []jobs.Snapshot{}
		for _, j := range all {
			if j.User == userOf(r) {
				mine = append(mine, j)
			}
		}
		return mine, nil
	})
	s.user("GET /api/jobs/{id}", func(r *http.Request) (any, error) {
		j, ok := s.jobs.Get(r.PathValue("id"))
		if !ok || (!isAdmin(r) && j.User != userOf(r)) {
			return nil, notFound("job not found")
		}
		return j, nil
	})
	s.admin("POST /api/jobs/{id}/cancel", func(r *http.Request) (any, error) {
		if !s.jobs.Cancel(r.PathValue("id")) {
			return nil, notFound("job not found")
		}
		return nil, nil
	})

	s.admin("GET /api/system/updates", func(r *http.Request) (any, error) {
		return system.ListUpgradable()
	})
	s.admin("POST /api/system/updates/refresh", func(r *http.Request) (any, error) {
		return s.jobs.Start("apt", "Checking for updates", userOf(r), s.updateJob("check", false)), nil
	})
	s.admin("POST /api/system/updates/upgrade", func(r *http.Request) (any, error) {
		s.audit(r, "system", "System upgrade started")
		return s.jobs.Start("apt", "System update", userOf(r), s.updateJob("system", false)), nil
	})
	// Updates StrataOS alone. The service restarts at the end: the browser reconnects.
	s.admin("POST /api/system/updates/strata", func(r *http.Request) (any, error) {
		s.audit(r, "system", "StrataOS update started from "+s.version)
		return s.jobs.Start("apt", "StrataOS update", userOf(r), s.updateJob("strata", false)), nil
	})
	s.admin("GET /api/system/updates/strata", func(r *http.Request) (any, error) {
		installed, candidate := strataPolicy()
		if installed == "" {
			installed = s.version
		}
		_, err := os.Stat("/etc/apt/sources.list.d/strataos.sources")
		return map[string]any{"running": s.version, "installed": installed, "candidate": candidate, "repository": err == nil}, nil
	})

	s.admin("GET /api/system/network", func(r *http.Request) (any, error) {
		var cfg *network.Config
		s.store.Read(func(st *store.State) { cfg = st.Network })
		if cfg == nil {
			cfg = &network.Config{Interfaces: []network.Iface{}, Bonds: []network.Bond{}, VLANs: []network.VLAN{}}
		}
		return map[string]any{"status": system.GetNetwork(), "manager": network.Manager(), "config": cfg, "pending": network.Current()}, nil
	})

	// Applies a network configuration on trial; it is rolled back unless confirmed.
	s.admin("PUT /api/system/network/config", func(r *http.Request) (any, error) {
		var req struct {
			Config  network.Config `json:"config"`
			Timeout int            `json:"timeout"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var physical []string
		for _, i := range system.GetNetwork().Interfaces {
			if i.Kind == "physical" && !strings.HasPrefix(i.Name, "bond") {
				physical = append(physical, i.Name)
			}
		}
		// Bond members are enslaved and may not show as physical any more: keep them valid.
		var prev network.Config
		s.store.Read(func(st *store.State) {
			if st.Network != nil {
				prev = *st.Network
			}
		})
		for _, b := range prev.Bonds {
			physical = append(physical, b.Members...)
		}
		if err := network.Validate(&req.Config, physical); err != nil {
			return nil, badRequest(err.Error())
		}
		timeout := time.Duration(req.Timeout) * time.Second
		if timeout < 30*time.Second || timeout > 10*time.Minute {
			timeout = 120 * time.Second
		}
		p, err := network.Apply(req.Config, prev, timeout)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "network", "Network configuration applied on trial")
		return p, nil
	})

	// Public on purpose: after an address change the browser reaches the NAS on a new
	// origin without its session cookie. The random token proves the change was made
	// by the administrator who applied it.
	s.public("POST /api/system/network/confirm", func(r *http.Request) (any, error) {
		var req struct {
			Token string `json:"token"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		cfg, err := network.Confirm(req.Token)
		if errors.Is(err, network.ErrNotReady) {
			return nil, &Error{Status: http.StatusConflict, Msg: err.Error()}
		}
		if err != nil {
			return nil, badRequest(err.Error())
		}
		_ = s.store.Update(func(st *store.State) error {
			st.Network = cfg
			return nil
		})
		s.store.Log("info", "network", "", "Network configuration confirmed")
		return nil, nil
	})

	s.admin("POST /api/system/network/rollback", func(r *http.Request) (any, error) {
		if err := network.Rollback("manual"); err != nil {
			return nil, badRequest(err.Error())
		}
		return nil, nil
	})

	s.admin("PUT /api/system/hostname", func(r *http.Request) (any, error) {
		var req struct {
			Hostname string `json:"hostname"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := system.SetHostname(req.Hostname); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "system", "Hostname set to "+req.Hostname)
		return nil, nil
	})

	s.admin("GET /api/system/timezones", func(r *http.Request) (any, error) { return system.Timezones(), nil })
	s.admin("PUT /api/system/time", func(r *http.Request) (any, error) {
		var req struct {
			Timezone string `json:"timezone"`
			NTP      *bool  `json:"ntp"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Timezone != "" {
			if err := system.SetTimezone(req.Timezone); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		if req.NTP != nil {
			if err := system.SetNTP(*req.NTP); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})

	s.admin("POST /api/system/power/{action}", func(r *http.Request) (any, error) {
		a := r.PathValue("action")
		if err := system.Power(a); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "system", "Power action: "+a)
		return nil, nil
	})

	s.admin("GET /api/settings", func(r *http.Request) (any, error) {
		var set store.Settings
		s.store.Read(func(st *store.State) { set = st.Settings })
		for _, p := range []*string{&set.SMTPPassword, &set.TelegramToken, &set.NtfyToken, &set.MetricsToken} {
			if *p != "" {
				*p = "********"
			}
		}
		return set, nil
	})
	s.admin("PUT /api/settings", func(r *http.Request) (any, error) {
		var req store.Settings
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if strings.ContainsAny(req.Workgroup+req.ServerString, "\n\r[]\\") {
			return nil, badRequest("invalid characters")
		}
		switch req.SMTPSecurity {
		case "", "auto", "tls", "starttls", "none":
		default:
			return nil, badRequest("invalid SMTP security mode")
		}
		req.TelegramToken, req.TelegramChatID, req.NtfyURL, req.NtfyToken = strings.TrimSpace(req.TelegramToken), strings.TrimSpace(req.TelegramChatID), strings.TrimSpace(req.NtfyURL), strings.TrimSpace(req.NtfyToken)
		if req.TelegramChatID != "" && !regexp.MustCompile(`^-?[0-9]{1,20}$|^@[A-Za-z0-9_]{4,64}$`).MatchString(req.TelegramChatID) {
			return nil, badRequest("invalid Telegram chat ID")
		}
		if strings.ContainsAny(req.NtfyToken+req.NtfyURL+req.TelegramToken, "\n\r ") {
			return nil, badRequest("invalid characters")
		}
		if req.NotifyLang != "" && !i18n.Valid(req.NotifyLang) {
			return nil, badRequest("invalid language")
		}
		if err := validNotifyPrefs(req); err != nil {
			return nil, err
		}
		switch req.SMBMinProto {
		case "", "SMB2", "SMB2_10", "SMB3", "SMB3_11":
		default:
			return nil, badRequest("invalid SMB protocol")
		}
		err := s.store.Update(func(st *store.State) error {
			if req.SMTPPassword == "********" {
				req.SMTPPassword = st.Settings.SMTPPassword
			}
			if req.TelegramToken == "********" {
				req.TelegramToken = st.Settings.TelegramToken
			}
			if req.NtfyToken == "********" {
				req.NtfyToken = st.Settings.NtfyToken
			}
			req.MetricsToken = st.Settings.MetricsToken // managed by PUT /api/metrics
			if req.SessionHours <= 0 || req.SessionHours > 24*30 {
				req.SessionHours = 12
			}
			st.Settings = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.applyShares()
		s.audit(r, "system", "Settings updated")
		return nil, nil
	})
	// Chats that wrote to the Telegram bot (token typed in the form, or the saved one).
	s.admin("POST /api/settings/telegram-chats", func(r *http.Request) (any, error) {
		var req struct {
			Token string `json:"token"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Token == "" || req.Token == "********" {
			s.store.Read(func(st *store.State) { req.Token = st.Settings.TelegramToken })
		}
		chats, err := telegramChats(strings.TrimSpace(req.Token))
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return chats, nil
	})
	// Test notification: by default on every enabled channel; with a level,
	// filtered like a real event of that level (minimum levels, quiet hours).
	s.admin("POST /api/settings/test-notification", func(r *http.Request) (any, error) {
		var req struct {
			Level string `json:"level"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if !validLevel(req.Level) {
			return nil, badRequest("invalid notification level")
		}
		sent, err := s.sendNotification(req.Level, "Test de notification", "StrataOS notifications work.")
		if err != nil {
			return nil, err
		}
		if sent == nil {
			sent = []string{}
		}
		return map[string]any{"ok": true, "sent": sent}, nil
	})

	// Optional components installed through bundled scripts.
	s.admin("GET /api/system/components", func(r *http.Request) (any, error) {
		return map[string]bool{
			"zfs":       storage.ZFSAvailable(),
			"docker":    sys.Has("docker"),
			"compose":   composeAvailable(),
			"samba":     sys.Has("smbd"),
			"nfs":       sys.Has("exportfs"),
			"btrfs":     sys.Has("mkfs.btrfs"),
			"lvm":       sys.Has("lvcreate"),
			"mdadm":     sys.Has("mdadm"),
			"smart":     sys.Has("smartctl"),
			"restic":    sys.Has("restic"),
			"nut":       sys.Has("upsmon"),
			"vms":       sys.Has("virsh"),
			"rclone":    sys.Has("rclone"),
			"ftp":       fileservices.FTPInstalled(),
			"directory": directory.ADInstalled() && directory.LDAPInstalled(),
			"antivirus": antivirus.Installed(),
			"ha":        ha.Installed(),
			"vpn":       vpn.Installed(),
			"photos":    photos.Installed(),
			"iscsi":     iscsi.Installed(),
			"nvr":       nvr.Installed(),
			"rsync":     sys.Has("rsync"),
			"xfs":       sys.Has("mkfs.xfs"),
			"tailscale": remote.TailscaleInstalled(),
			"tunnel":    remote.CloudflaredInstalled(),
			"dlna":      media.DLNAInstalled(),
		}, nil
	})
	s.admin("POST /api/system/components/{name}/install", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		script := map[string]string{"zfs": "install-zfs.sh", "docker": "install-docker.sh", "nut": "install-nut.sh", "vms": "install-vms.sh", "rclone": "install-rclone.sh", "ftp": "install-ftp.sh", "directory": "install-directory.sh", "antivirus": "install-antivirus.sh", "ha": "install-ha.sh", "vpn": "install-vpn.sh", "photos": "install-photos.sh", "tailscale": "install-tailscale.sh", "tunnel": "install-cloudflared.sh", "iscsi": "install-iscsi.sh", "dlna": "install-dlna.sh", "nvr": "install-surveillance.sh"}[name]
		if script == "" {
			return nil, badRequest("unknown component")
		}
		s.audit(r, "system", "Installing component "+name)
		return s.jobs.Start("install", "Install "+name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			_, err := sys.RunOpts(ctx, sys.Opts{Output: j, Timeout: time.Hour, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}, "/bin/bash", scriptsDir+"/"+script)
			return err
		}), nil
	})

	s.adminRaw("GET /api/system/terminal", s.hostTerminal)
}

func composeAvailable() bool {
	if _, err := sys.Run("docker", "compose", "version"); err == nil {
		return true
	}
	return sys.Has("docker-compose")
}

type termMsg struct {
	Type string `json:"type"` // input | resize
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// hostTerminal opens a root login shell over a websocket (administrators only).
func (s *Server) hostTerminal(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "HOME=/root")
	cmd.Dir = "/root"
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 32})
	if err != nil {
		_ = c.Write(ctx, websocket.MessageText, []byte("failed to start shell: "+err.Error()))
		return
	}
	defer f.Close()
	s.audit(r, "system", "Opened web terminal")
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if c.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m termMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "input":
			_, _ = f.Write([]byte(m.Data))
		case "resize":
			if m.Cols > 0 && m.Rows > 0 {
				_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)})
			}
		}
	}
}

// aptRun runs apt in its own transient systemd unit. Upgrading StrataOS restarts this
// service; running apt as our child would kill it half-way through, leaving dpkg
// broken. In its own unit it survives the restart, and its output still streams here.
func aptRun(ctx context.Context, j *jobs.Job, args ...string) error {
	if out, _ := sys.Run("systemctl", "is-active", "strata-apt.service"); strings.TrimSpace(out) == "active" {
		return fmt.Errorf("another package operation is already running")
	}
	full := append([]string{"--unit=strata-apt", "--collect", "--wait", "--pipe", "--quiet",
		"--setenv=DEBIAN_FRONTEND=noninteractive", "--setenv=NEEDRESTART_MODE=a", "--property=TimeoutStopSec=1h"}, args...)
	_, err := sys.RunOpts(ctx, sys.Opts{Output: j, Timeout: 2 * time.Hour}, "systemd-run", full...)
	return err
}
