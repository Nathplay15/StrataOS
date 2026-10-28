package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

// webSessionCookie opens the web interface like the app does and returns the cookie.
func webSessionCookie(t *testing.T, s *Server, tok string) *http.Cookie {
	t.Helper()
	w := doReq(s, "POST", "/api/mobile/websession", tok)
	if w.Code != 200 {
		t.Fatalf("websession: %d %s", w.Code, w.Body)
	}
	var res struct{ Code, Path string }
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if len(res.Code) != 64 || res.Path != "/api/mobile/websession/"+res.Code {
		t.Fatalf("answer %s", w.Body)
	}
	w = doReq(s, "GET", res.Path, "")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("code exchange: %d %s", w.Code, w.Body)
	}
	if w := doReq(s, "GET", res.Path, ""); w.Code != 401 {
		t.Errorf("code used twice: %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName && c.HttpOnly && c.SameSite == http.SameSiteStrictMode {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func cookieReq(s *Server, method, path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("X-Strata", "1")
	req.AddCookie(c)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}

func TestWebSession(t *testing.T) {
	s, tok := mobileTestServer(t)
	s.mobileWebSessionRoutes()
	s.user("POST /api/me/password", func(r *http.Request) (any, error) { return nil, nil })
	s.userRaw("POST /api/logout", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })

	c := webSessionCookie(t, s, tok)
	if w := cookieReq(s, "GET", "/api/mobile/test", c); w.Code != 200 || !strings.Contains(w.Body.String(), `"user":"alice"`) {
		t.Fatalf("web session: %d %s", w.Code, w.Body)
	}
	// The device restrictions follow the web session (logout excepted).
	if w := cookieReq(s, "POST", "/api/me/password", c); w.Code != 403 || !strings.Contains(w.Body.String(), "in a browser") {
		t.Errorf("password change from the device's web session: %d %s", w.Code, w.Body)
	}
	if w := cookieReq(s, "POST", "/api/logout", c); w.Code != 200 {
		t.Errorf("logout: %d", w.Code)
	}
	// A cookie cannot ask for a code: only the token can.
	if w := cookieReq(s, "POST", "/api/mobile/websession", c); w.Code != 400 {
		t.Errorf("code from a cookie session: %d", w.Code)
	}
	// Opening it again replaces the previous session of the device.
	c2 := webSessionCookie(t, s, tok)
	if w := cookieReq(s, "GET", "/api/mobile/test", c); w.Code != 401 {
		t.Errorf("previous web session still valid: %d", w.Code)
	}
	var id string
	s.store.Read(func(st *store.State) { id = st.User("alice").MobileDevices[0].ID })
	if err := s.removeDevice("alice", id, "revoked "); err != nil {
		t.Fatal(err)
	}
	if w := cookieReq(s, "GET", "/api/mobile/test", c2); w.Code != 401 {
		t.Errorf("web session of a revoked device: %d", w.Code)
	}
}

func TestWebSessionCodes(t *testing.T) {
	if w := doReq(newTestServerWithWebSession(t), "GET", "/api/mobile/websession/"+strings.Repeat("a", 64), ""); w.Code != 401 {
		t.Errorf("unknown code: %d", w.Code)
	}
	c1 := newWebSessionCode("alice", "d1")
	c2 := newWebSessionCode("alice", "d1")
	if _, ok := takeWebSessionCode(c1); ok {
		t.Error("older code of the same device still valid")
	}
	c3 := newWebSessionCode("bob", "d2")
	if v, ok := takeWebSessionCode(c2); !ok || v.user != "alice" || v.device != "d1" {
		t.Error("code lost")
	}
	webSessionCodes.Lock()
	for k, v := range webSessionCodes.m {
		v.expires = time.Now().Add(-time.Second)
		webSessionCodes.m[k] = v
	}
	webSessionCodes.Unlock()
	if _, ok := takeWebSessionCode(c3); ok {
		t.Error("expired code accepted")
	}
}

func newTestServerWithWebSession(t *testing.T) *Server {
	s := newTestServer(t)
	s.mobileWebSessionRoutes()
	return s
}
