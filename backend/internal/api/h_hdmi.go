package api

// The HDMI output: plugged to a TV, the NAS shows its cinema menu
// full-screen there — a kiosk browser on the local /hdmi page, kept on the
// connected display by strataos-hdmi.service ("strata hdmi"). The kiosk
// signs in read-only with a random token of the NAS, accepted from the
// loopback interface only.

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"strataos/internal/hdmi"
	"strataos/internal/store"
	"strataos/internal/sys"
)

const (
	hdmiUnit        = "strataos-hdmi.service"
	hdmiUnitFile    = "/usr/lib/systemd/system/strataos-hdmi.service"
	hdmiTokenPrefix = "hdmi:"
)

func hdmiInstalled() bool {
	_, err := os.Stat(hdmiUnitFile)
	return err == nil
}

func (s *Server) hdmiSettings() store.HDMIConsole {
	var c store.HDMIConsole
	s.store.Read(func(st *store.State) { c = st.HDMI })
	return c
}

func setHDMI(on bool) error {
	if on {
		_, err := sys.Run("systemctl", "enable", "--now", hdmiUnit)
		return err
	}
	_, err := sys.Run("systemctl", "disable", "--now", hdmiUnit)
	return err
}

// remoteHub broadcasts remote-control keys to the connected kiosk pages
// (the PWA drives the menu shown on the TV).
type remoteHub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
}

func (h *remoteHub) add(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *remoteHub) remove(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *remoteHub) broadcast(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.Write(ctx, websocket.MessageText, []byte(key))
		cancel()
	}
}

var hdmiRemoteHub = &remoteHub{clients: map[*websocket.Conn]struct{}{}}

// remoteKeys are the keys a remote may send to the TV menu.
var remoteKeys = map[string]bool{"up": true, "down": true, "left": true, "right": true, "ok": true, "back": true}

// hdmiRemoteWS is the kiosk page's listening channel, from the loopback
// interface only. The kiosk browser has no session cookie: its first message
// is the kiosk token (never in the address, which may be logged).
func (s *Server) hdmiRemoteWS(w http.ResponseWriter, r *http.Request) {
	want := hdmi.Token(s.dataDir)
	if want == "" || !net.ParseIP(clientIP(r)).IsLoopback() {
		writeErr(w, errUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(256)
	actx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	_, msg, err := c.Read(actx)
	cancel()
	if err != nil || subtle.ConstantTimeCompare(msg, []byte(want)) != 1 {
		s.guardFail(clientIP(r), "hdmi kiosk", "")
		c.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	hdmiRemoteHub.add(c)
	defer hdmiRemoteHub.remove(c)
	ctx := c.CloseRead(r.Context())
	<-ctx.Done()
}

// hdmiRemoteAllowed: administrators, and the accounts they allowed.
func (s *Server) hdmiRemoteAllowed(r *http.Request) bool {
	if isAdmin(r) {
		return true
	}
	if ss := sessionFrom(r.Context()); ss != nil && hasScope(ss.Scopes, hdmiKioskScope) {
		return false
	}
	return slices.Contains(s.hdmiSettings().Remote, userOf(r))
}

func (s *Server) hdmiRoutes() {
	s.record("GET /api/hdmi/remote", "public")
	s.mux.Handle("GET /api/hdmi/remote", http.HandlerFunc(s.hdmiRemoteWS))
	// A signed-in user (e.g. the PWA) sends one key to the TV menu.
	s.user("POST /api/hdmi/remote/key", func(r *http.Request) (any, error) {
		var req struct {
			Key string `json:"key"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if !s.hdmiRemoteAllowed(r) {
			return nil, forbidden("the TV remote is not allowed for this account")
		}
		key := strings.ToLower(strings.TrimSpace(req.Key))
		if !remoteKeys[key] {
			return nil, badRequest("invalid key")
		}
		hdmiRemoteHub.broadcast(key)
		return map[string]any{"sent": true}, nil
	})
	s.admin("GET /api/system/hdmi", func(r *http.Request) (any, error) {
		c := s.hdmiSettings()
		d, ok := hdmi.Connected()
		var display any
		if ok {
			display = map[string]any{"connector": d.Connector, "mode": d.Mode}
		}
		return map[string]any{
			"enabled":   !c.Off,
			"display":   display,
			"installed": hdmiInstalled(),
			"active":    unitState("is-active", hdmiUnit) == "active",
			"browser":   hdmi.Browser() != "",
			"account":   c.Account,
			"remote":    append([]string{}, c.Remote...),
		}, nil
	})
	s.admin("PUT /api/system/hdmi", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool      `json:"enabled"`
			Account *string   `json:"account"`
			Remote  *[]string `json:"remote"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		cur := s.hdmiSettings()
		if req.Account != nil {
			cur.Account = strings.TrimSpace(*req.Account)
			if cur.Account != "" {
				if _, err := s.usersExist([]string{cur.Account}); err != nil {
					return nil, err
				}
			}
		}
		if req.Remote != nil {
			with, err := s.usersExist(*req.Remote)
			if err != nil {
				return nil, err
			}
			cur.Remote = with
		}
		if err := s.store.Update(func(st *store.State) error {
			st.HDMI = store.HDMIConsole{Off: !req.Enabled, Decided: true, Account: cur.Account, Remote: cur.Remote}
			return nil
		}); err != nil {
			return nil, err
		}
		if hdmiInstalled() {
			on := unitState("is-enabled", hdmiUnit) == "enabled"
			active := unitState("is-active", hdmiUnit) == "active"
			if req.Enabled != on || (req.Enabled && !active) {
				if err := setHDMI(req.Enabled); err != nil {
					return nil, err
				}
			}
		}
		s.audit(r, "system", map[bool]string{true: "HDMI menu enabled", false: "HDMI menu disabled"}[req.Enabled])
		return map[string]any{"enabled": req.Enabled}, nil
	})
}

// hdmiKioskScope marks the session of the kiosk browser.
const hdmiKioskScope = "hdmi-kiosk"

// hdmiKioskPaths are the only routes the kiosk may call (GET): the cinema
// library and its posters and trailers, and the playback of its videos.
var hdmiKioskPaths = []string{"/api/cinema", "/api/cinema/lists", "/api/cinema/profiles", "/api/media/night", "/api/cinema/trailer", "/api/cinema/poster/",
	"/api/files/probe", "/api/files/play", "/api/files/subtitle"}

func hdmiKioskPathAllowed(p string) bool {
	for _, a := range hdmiKioskPaths {
		if p == a || (strings.HasSuffix(a, "/") && strings.HasPrefix(p, a)) {
			return true
		}
	}
	return false
}

// hdmiKioskAuth authenticates the kiosk browser of the HDMI output: the token
// is a random secret of the NAS (dataDir/hdmi.token), accepted from the
// loopback interface only. The session acts as the account chosen for the TV
// (Settings → Console screen), never as an administrator, for GET requests
// to the cinema and video playback routes only.
func (s *Server) hdmiKioskAuth(w http.ResponseWriter, r *http.Request, next http.Handler, adminOnly bool, tok string) {
	ip := clientIP(r)
	want := hdmi.Token(s.dataDir)
	if want == "" || !net.ParseIP(ip).IsLoopback() || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
		s.guardFail(ip, "hdmi kiosk", "")
		writeErr(w, errUnauthorized)
		return
	}
	if adminOnly || (r.Method != http.MethodGet && r.Method != http.MethodHead) || !hdmiKioskPathAllowed(r.URL.Path) {
		writeErr(w, errForbidden)
		return
	}
	acct := s.hdmiSettings().Account
	known := false
	s.store.Read(func(st *store.State) { known = acct != "" && st.User(acct) != nil })
	if !known {
		writeErr(w, &Error{Status: http.StatusForbidden, Msg: "no account is chosen for the TV (Settings, Console screen)"})
		return
	}
	ss := &session{User: acct, Role: "user", Expires: time.Now().Add(24 * time.Hour), Created: time.Now(), IP: ip, Scopes: []string{hdmiKioskScope}}
	ctx := context.WithValue(r.Context(), ctxKey{}, ss)
	next.ServeHTTP(w, r.WithContext(ctx))
}
