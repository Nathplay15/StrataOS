package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"strataos/internal/store"
)

// The app shows the complete web interface in a WebView. It trades its device
// token for a one-time code (POST /api/mobile/websession), then opens
// /api/mobile/websession/{code}: that sets an ordinary session cookie and
// redirects to the interface. The session remembers its device: revoking the
// device ends it, and the device restrictions (deviceDenied) apply to it too.

const webSessionCodeTTL = time.Minute

type webSessionCode struct {
	user, device string
	expires      time.Time
}

var webSessionCodes = struct {
	sync.Mutex
	m map[string]webSessionCode
}{m: map[string]webSessionCode{}}

// newWebSessionCode keeps one live code per device (hashed, in memory).
func newWebSessionCode(user, device string) string {
	code := token()
	webSessionCodes.Lock()
	defer webSessionCodes.Unlock()
	for k, v := range webSessionCodes.m {
		if time.Now().After(v.expires) || (v.user == user && v.device == device) {
			delete(webSessionCodes.m, k)
		}
	}
	webSessionCodes.m[key(code)] = webSessionCode{user: user, device: device, expires: time.Now().Add(webSessionCodeTTL)}
	return code
}

// takeWebSessionCode consumes a code (false: unknown or expired).
func takeWebSessionCode(code string) (webSessionCode, bool) {
	webSessionCodes.Lock()
	defer webSessionCodes.Unlock()
	k := key(strings.TrimSpace(code))
	c, ok := webSessionCodes.m[k]
	delete(webSessionCodes.m, k)
	if !ok || time.Now().After(c.expires) {
		return webSessionCode{}, false
	}
	return c, true
}

func (s *Server) mobileWebSessionRoutes() {
	s.user("POST /api/mobile/websession", func(r *http.Request) (any, error) {
		dev := deviceFrom(r.Context())
		if dev == nil {
			return nil, badRequest("only the mobile app can do this")
		}
		code := newWebSessionCode(dev.user, dev.id)
		return map[string]any{"code": code, "path": "/api/mobile/websession/" + code, "expires": time.Now().Add(webSessionCodeTTL)}, nil
	})

	s.mux.HandleFunc("GET /api/mobile/websession/{code}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many failed attempts, try again in a few minutes"})
			return
		}
		c, ok := takeWebSessionCode(r.PathValue("code"))
		var u store.User
		var d store.MobileDevice
		if ok {
			ok = false
			s.store.Read(func(st *store.State) {
				if p := st.User(c.user); p != nil && !p.Disabled {
					if pd := p.MobileDevice(c.device); pd != nil {
						u, d, ok = *p, *pd, true
					}
				}
			})
		}
		if !ok {
			s.store.Log("warning", "auth", "", "Invalid web sign-in code of the mobile app from "+ip)
			s.guardFail(ip, "mobile app", "")
			writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "invalid or expired sign-in code: open the web interface from the app again"})
			return
		}
		s.limiter.reset(ip)
		ttl := s.sessionTTL()
		// One web session per device: opening it again replaces the previous one.
		s.sessions.dropDevice(u.Username, d.ID)
		t := s.sessions.add(&session{User: u.Username, Role: u.Role, Expires: time.Now().Add(ttl), Created: time.Now(), IP: ip, Device: d.ID})
		s.setCookie(w, r, t, ttl)
		s.store.Log("info", "auth", u.Username, "Web interface opened from the mobile app ("+d.Name+") from "+ip)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}
