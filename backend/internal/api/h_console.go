package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"strataos/internal/accounts"
	"strataos/internal/console"
	"strataos/internal/network"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

// The appliance console: strata-console.service shows the address of the NAS,
// its health and a few local actions on tty1 instead of a login prompt
// (tty2-6 and serial consoles keep theirs). It reaches the daemon through a
// unix socket that only root can open; whoever stands at the machine gets
// limited, logged actions, and a shell only through a normal login.
const (
	consoleUnit     = "strata-console.service"
	consoleUnitFile = "/usr/lib/systemd/system/strata-console.service"
	consoleGetty    = "getty@tty1.service"
)

var (
	consoleCodes    = newRecoveryCodes(time.Now)
	recoveryLimiter = &limiter{hits: map[string][]time.Time{}}
	// consoleNet holds the confirmation token of a network reset started from
	// the console: the console keeps or undoes it without seeing the token.
	consoleNet struct {
		sync.Mutex
		token string
	}
	// consoleHealth caches the storage part of the status (lsblk, mdadm, zpool).
	consoleHealth struct {
		sync.Mutex
		at      time.Time
		alerts  []console.Alert
		volumes []console.Volume
	}
)

// A graphical login may run on tty1: there the screen stays off until an
// administrator turns it on.
func displayManager() bool {
	_, err := os.Lstat("/etc/systemd/system/display-manager.service")
	return err == nil
}

func consoleInstalled() bool {
	_, err1 := os.Stat(consoleUnitFile)
	_, err2 := os.Stat("/dev/tty1")
	return err1 == nil && err2 == nil
}

func (s *Server) consoleSettings() store.Console {
	var c store.Console
	s.store.Read(func(st *store.State) { c = st.Console })
	return c
}

func consoleWanted(c store.Console, graphical bool) bool {
	return !c.Off && (c.Decided || !graphical)
}

func unitState(verb, unit string) string {
	out, _ := sys.Run("systemctl", verb, unit)
	return strings.TrimSpace(out)
}

// setConsole gives tty1 to the screen or back to getty. getty@tty1 is masked
// rather than disabled so that nothing starts it in the screen's place.
func setConsole(on bool) error {
	if on {
		if _, err := sys.Run("systemctl", "mask", consoleGetty); err != nil {
			return err
		}
		_, err := sys.Run("systemctl", "enable", "--now", consoleUnit)
		return err
	}
	if _, err := sys.Run("systemctl", "disable", "--now", consoleUnit); err != nil {
		return err
	}
	if _, err := sys.Run("systemctl", "unmask", consoleGetty); err != nil {
		return err
	}
	_, err := sys.Run("systemctl", "start", consoleGetty)
	return err
}

// consoleLoop serves the console socket and puts tty1 in the configured state
// (new installations and upgrades get the screen).
func (s *Server) consoleLoop() {
	if os.Geteuid() != 0 {
		return
	}
	go s.serveConsole()
	if !consoleInstalled() {
		return
	}
	want := consoleWanted(s.consoleSettings(), displayManager())
	enabled := unitState("is-enabled", consoleUnit) == "enabled"
	active := unitState("is-active", consoleUnit) == "active"
	if want != enabled || want && !active {
		if err := setConsole(want); err != nil {
			slog.Warn("console screen", "err", err)
		}
	}
}

type peerKey struct{}

// peerUID reads the user of the process on the other end of a unix socket.
func peerUID(c net.Conn) (uint32, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) { cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || cerr != nil {
		return 0, false
	}
	return cred.Uid, true
}

func (s *Server) serveConsole() {
	path := console.Socket
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("console socket", "err", err)
		return
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		slog.Warn("console socket", "err", err)
		return
	}
	// Root only: the mode, and the peer check of each connection below.
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return
	}
	mux := http.NewServeMux()
	h := func(pattern string, fn handler) {
		mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if uid, ok := r.Context().Value(peerKey{}).(uint32); !ok || uid != 0 {
				writeErr(w, &Error{Status: http.StatusForbidden, Msg: "root only"})
				return
			}
			s.wrap(fn).ServeHTTP(w, r)
		}))
	}
	h("GET /status", func(r *http.Request) (any, error) { return s.consoleStatus(), nil })
	h("GET /network", func(r *http.Request) (any, error) {
		n := system.GetNetwork()
		out := console.Network{Gateway: n.Gateway, DNS: n.DNS, Interfaces: []console.Iface{}}
		for _, i := range n.Interfaces {
			out.Interfaces = append(out.Interfaces, console.Iface{Name: i.Name, MAC: i.MAC, State: i.State, Addresses: i.Addresses, Speed: i.Speed, Kind: i.Kind})
		}
		return out, nil
	})
	h("POST /recovery", func(r *http.Request) (any, error) { return s.consoleRecoveryCode() })
	h("POST /network/reset", func(r *http.Request) (any, error) { return s.consoleResetNetwork() })
	h("POST /network/keep", func(r *http.Request) (any, error) { return nil, s.consoleKeepNetwork() })
	h("POST /network/undo", func(r *http.Request) (any, error) {
		consoleNet.Lock()
		defer consoleNet.Unlock()
		if consoleNet.token == "" {
			return nil, badRequest("no network reset is waiting")
		}
		consoleNet.token = ""
		if err := network.Rollback("manual"); err != nil {
			return nil, badRequest(err.Error())
		}
		return nil, nil
	})
	h("POST /power/{action}", func(r *http.Request) (any, error) {
		a := r.PathValue("action")
		msg := map[string]string{"reboot": "Restart requested from the NAS console", "poweroff": "Shutdown requested from the NAS console"}[a]
		if msg == "" {
			return nil, badRequest("invalid power action")
		}
		s.store.Log("warning", "system", "", msg)
		return nil, system.Power(a)
	})
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if uid, ok := peerUID(c); ok {
				return context.WithValue(ctx, peerKey{}, uid)
			}
			return ctx
		}}
	slog.Warn("console socket", "err", hs.Serve(l))
}

func (s *Server) consoleStatus() console.Status {
	host, _ := hostname()
	c := s.consoleSettings()
	setup := !s.setupNeeded()
	st := console.Status{Hostname: host, Version: s.version, SetupDone: setup, HTTPPort: s.httpPort, HTTPSPort: "7443",
		Recovery: setup && !c.NoRecovery}
	if s.certs != nil && s.certs.HTTPSPort != "" {
		st.HTTPSPort = s.certs.HTTPSPort
	}
	consoleHealth.Lock()
	if time.Since(consoleHealth.at) > 10*time.Second {
		raids, _ := storage.ListRaid()
		pools, _ := storage.ListZPools(false)
		vols := s.volumeSummaries(raids, pools)
		consoleHealth.alerts, consoleHealth.volumes = []console.Alert{}, []console.Volume{}
		for _, a := range s.alerts(raids, pools, vols) {
			consoleHealth.alerts = append(consoleHealth.alerts, console.Alert{Level: a.Level, Message: a.Message})
		}
		for _, v := range vols {
			consoleHealth.volumes = append(consoleHealth.volumes, console.Volume{Name: v.Name, Used: v.Used, Total: v.Total, Health: v.Health})
		}
		consoleHealth.at = time.Now()
	}
	st.Alerts, st.Volumes = consoleHealth.alerts, consoleHealth.volumes
	consoleHealth.Unlock()
	consoleNet.Lock()
	if consoleNet.token != "" {
		if p := network.Current(); p != nil {
			st.NetworkDeadline = &p.Deadline
		} else {
			consoleNet.token = "" // confirmed from elsewhere, or rolled back
		}
	}
	consoleNet.Unlock()
	return st
}

func (s *Server) consoleRecoveryCode() (*console.Code, error) {
	if s.setupNeeded() {
		return nil, badRequest("the NAS is not set up yet: open its address to create the administrator")
	}
	if s.consoleSettings().NoRecovery {
		return nil, &Error{Status: http.StatusForbidden, Msg: "recovery codes are disabled in Settings"}
	}
	code, exp, err := consoleCodes.issue()
	if err != nil {
		return nil, err
	}
	msg := "Administrator recovery code generated on the NAS console (valid 15 minutes)"
	s.store.Log("warning", "auth", "", msg)
	s.notify("warning", "Recovery code generated", "Someone at the NAS generated an administrator recovery code on its screen. It is valid for 15 minutes and lets them reset an administrator password.")
	return &console.Code{Code: code, Expires: exp}, nil
}

// consoleResetNetwork puts every physical interface back on DHCP, on trial
// like a change made in the web interface.
func (s *Server) consoleResetNetwork() (*console.Pending, error) {
	consoleNet.Lock()
	defer consoleNet.Unlock()
	var physical []string
	next := network.Config{Interfaces: []network.Iface{}, Bonds: []network.Bond{}, Bridges: []network.Bridge{}, VLANs: []network.VLAN{}}
	for _, i := range system.GetNetwork().Interfaces {
		// Real devices only (bond and bridge members included), not the
		// bonds, bridges and VLANs themselves.
		if _, err := os.Stat(filepath.Join("/sys/class/net", i.Name, "device")); err != nil || i.Kind == "docker" {
			continue
		}
		physical = append(physical, i.Name)
		next.Interfaces = append(next.Interfaces, network.Iface{Name: i.Name, IP: network.IP{Mode: "dhcp"}})
	}
	if len(physical) == 0 {
		return nil, badRequest("no network interface found")
	}
	if err := network.Validate(&next, physical); err != nil {
		return nil, badRequest(err.Error())
	}
	var prev network.Config
	s.store.Read(func(st *store.State) {
		if st.Network != nil {
			prev = *st.Network
		}
	})
	p, err := network.Apply(next, prev, 3*time.Minute)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	consoleNet.token = p.Token
	s.store.Log("warning", "network", "", "Network reset to DHCP from the NAS console, on trial")
	return &console.Pending{Deadline: p.Deadline}, nil
}

func (s *Server) consoleKeepNetwork() error {
	consoleNet.Lock()
	defer consoleNet.Unlock()
	if consoleNet.token == "" {
		return badRequest("no network reset is waiting")
	}
	cfg, err := network.Confirm(consoleNet.token)
	if errors.Is(err, network.ErrNotReady) {
		return &Error{Status: http.StatusConflict, Msg: err.Error()}
	}
	if err != nil {
		return badRequest(err.Error())
	}
	consoleNet.token = ""
	_ = s.store.Update(func(st *store.State) error {
		st.Network = cfg
		return nil
	})
	msg := "Network configuration reset to DHCP from the NAS console"
	s.store.Log("warning", "network", "", msg)
	s.notify("warning", "Network reset", msg)
	return nil
}

type recoveryAdmin struct {
	Username  string `json:"username"`
	FullName  string `json:"fullName"`
	TwoFactor bool   `json:"twoFactor"`
}

// recoveryAdmins lists the local administrators a recovery code can reset;
// with none, it may create one.
func (s *Server) recoveryAdmins() []recoveryAdmin {
	out := []recoveryAdmin{}
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if u.Role == "admin" && u.Source == "" && !u.Disabled {
				out = append(out, recoveryAdmin{Username: u.Username, FullName: u.FullName, TwoFactor: u.TOTPSecret != ""})
			}
		}
	})
	return out
}

var errRecoveryCode = badRequest("invalid or expired recovery code")

// recoveryGate runs the checks shared by both steps of the web form.
func (s *Server) recoveryGate(r *http.Request, code string) error {
	ip := clientIP(r)
	if !recoveryLimiter.attempt(ip) {
		return &Error{Status: http.StatusTooManyRequests, Msg: "too many attempts, try again in a few minutes"}
	}
	if s.setupNeeded() || s.consoleSettings().NoRecovery {
		return &Error{Status: http.StatusForbidden, Msg: "recovery codes are disabled"}
	}
	if !consoleCodes.check(code) {
		s.store.Log("warning", "auth", "", "Wrong recovery code from "+ip)
		return errRecoveryCode
	}
	return nil
}

func (s *Server) consoleRoutes() {
	s.admin("GET /api/system/console", func(r *http.Request) (any, error) {
		c := s.consoleSettings()
		graphical := displayManager()
		return map[string]any{
			"enabled": consoleWanted(c, graphical), "recovery": !c.NoRecovery, "graphical": graphical,
			"installed": consoleInstalled(), "active": unitState("is-active", consoleUnit) == "active",
		}, nil
	})
	s.admin("PUT /api/system/console", func(r *http.Request) (any, error) {
		var req struct {
			Enabled  bool `json:"enabled"`
			Recovery bool `json:"recovery"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		prev := s.consoleSettings()
		next := store.Console{Off: !req.Enabled, Decided: true, NoRecovery: !req.Recovery}
		if err := s.store.Update(func(st *store.State) error { st.Console = next; return nil }); err != nil {
			return nil, err
		}
		if next.NoRecovery {
			consoleCodes.clear()
		}
		enabled := unitState("is-enabled", consoleUnit) == "enabled"
		active := unitState("is-active", consoleUnit) == "active"
		if consoleInstalled() && (req.Enabled != enabled || req.Enabled && !active) {
			if err := setConsole(req.Enabled); err != nil {
				return nil, err
			}
		}
		if req.Enabled {
			s.audit(r, "system", "Console screen enabled")
		} else {
			s.audit(r, "system", "Console screen disabled")
		}
		if prev.NoRecovery != next.NoRecovery {
			if req.Recovery {
				s.audit(r, "system", "Recovery codes from the console allowed")
			} else {
				s.audit(r, "system", "Recovery codes from the console forbidden")
			}
		}
		return nil, nil
	})

	// Sign-in page, "Forgot password?": first the code alone (which accounts
	// can be reset), then the code and the new password, which uses it up.
	s.public("POST /api/login/recovery/check", func(r *http.Request) (any, error) {
		var req struct {
			Code string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := s.recoveryGate(r, req.Code); err != nil {
			return nil, err
		}
		admins := s.recoveryAdmins()
		return map[string]any{"admins": admins, "canCreate": len(admins) == 0}, nil
	})
	s.public("POST /api/login/recovery/reset", func(r *http.Request) (any, error) {
		var req struct {
			Code            string `json:"code"`
			Username        string `json:"username"`
			Password        string `json:"password"`
			FullName        string `json:"fullName"`
			Create          bool   `json:"create"`
			RemoveTwoFactor bool   `json:"removeTwoFactor"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := s.recoveryGate(r, req.Code); err != nil {
			return nil, err
		}
		// Mistakes in the form do not use the code up.
		req.Username = strings.ToLower(strings.TrimSpace(req.Username))
		if err := accounts.ValidPassword(req.Password); err != nil {
			return nil, badRequest(err.Error())
		}
		admins := s.recoveryAdmins()
		if req.Create {
			if len(admins) > 0 {
				return nil, badRequest("an administrator account exists: reset its password instead")
			}
			if err := accounts.ValidName(req.Username); err != nil {
				return nil, badRequest(err.Error())
			}
			var exists bool
			s.store.Read(func(st *store.State) { exists = st.User(req.Username) != nil })
			if exists {
				return nil, badRequest("user already exists")
			}
		} else if !func() bool {
			for _, a := range admins {
				if a.Username == req.Username {
					return true
				}
			}
			return false
		}() {
			return nil, badRequest("not an administrator of this NAS")
		}
		if !consoleCodes.consume(req.Code) {
			return nil, errRecoveryCode
		}
		ip := clientIP(r)
		recoveryLimiter.reset(ip)
		var msg string
		if req.Create {
			if err := s.createRecoveryAdmin(req.Username, req.FullName, req.Password); err != nil {
				return nil, err
			}
			msg = "Administrator " + req.Username + " created with a recovery code from " + ip
		} else {
			if err := s.changePassword(req.Username, req.Password); err != nil {
				return nil, err
			}
			msg = "Password of administrator " + req.Username + " reset with a recovery code from " + ip
			if req.RemoveTwoFactor {
				_ = s.store.Update(func(st *store.State) error {
					if u := st.User(req.Username); u != nil {
						u.TOTPSecret, u.TOTPPending, u.TOTPLast, u.RecoveryCodes = "", "", 0, nil
					}
					return nil
				})
				msg += " (two-factor authentication removed)"
			}
			s.sessions.dropUser(req.Username)
		}
		s.store.Log("warning", "auth", req.Username, msg)
		s.notify("warning", "Administrator account recovered", msg)
		return nil, nil
	})
}

// createRecoveryAdmin creates an administrator like the setup wizard does.
func (s *Server) createRecoveryAdmin(name, fullName, password string) error {
	if err := accounts.Create(name, fullName, password, nil, true); err != nil {
		return err
	}
	if _, err := user.LookupGroup("sudo"); err == nil {
		if _, err := sys.Run("usermod", "-a", "-G", "sudo", name); err != nil {
			slog.Warn("add recovered admin to sudo", "err", err)
		}
	}
	_ = s.addToDockerGroup(name)
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	if err := s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: name, FullName: fullName, Role: "admin", PasswordHash: hash, SSH: true, CreatedAt: time.Now()})
		return nil
	}); err != nil {
		return err
	}
	s.applyShares()
	go s.applyFileServices()
	return nil
}
