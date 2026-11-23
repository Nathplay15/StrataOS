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

// hdmiRemoteWS is the kiosk page's listening channel: it signs in with the
// kiosk token in the query string, from the loopback interface only (the
// kiosk browser has no session cookie).
func (s *Server) hdmiRemoteWS(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("t")
	want := hdmi.Token(s.dataDir)
	if want == "" || !net.ParseIP(clientIP(r)).IsLoopback() || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
		writeErr(w, errUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	hdmiRemoteHub.add(c)
	defer hdmiRemoteHub.remove(c)
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	<-ctx.Done()
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
		}, nil
	})
	s.admin("PUT /api/system/hdmi", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := s.store.Update(func(st *store.State) error {
			st.HDMI = store.HDMIConsole{Off: !req.Enabled, Decided: true}
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

// hdmiKioskAuth authenticates the kiosk browser of the HDMI output: the token
// is a random secret of the NAS (dataDir/hdmi.token), accepted from the
// loopback interface only, and the session it gives is read-only — GET/HEAD
// requests, never the admin routes.
func (s *Server) hdmiKioskAuth(w http.ResponseWriter, r *http.Request, next http.Handler, adminOnly bool, tok string) {
	ip := clientIP(r)
	want := hdmi.Token(s.dataDir)
	if want == "" || !net.ParseIP(ip).IsLoopback() || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
		s.guardFail(ip, "hdmi kiosk", "")
		writeErr(w, errUnauthorized)
		return
	}
	if adminOnly || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		writeErr(w, errForbidden)
		return
	}
	ss := &session{User: "hdmi", Role: "admin", Expires: time.Now().Add(24 * time.Hour), Created: time.Now(), IP: ip}
	ctx := context.WithValue(r.Context(), ctxKey{}, ss)
	next.ServeHTTP(w, r.WithContext(ctx))
}
