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
	"time"

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

func (s *Server) hdmiRoutes() {
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
