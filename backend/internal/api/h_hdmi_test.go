package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/hdmi"
	"strataos/internal/store"
)

func TestHDMIKioskAuth(t *testing.T) {
	s := newTestServer(t)
	s.cinemaRoutes()
	s.hdmiRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"}, &store.User{Username: "bob", Role: "user"})
		return nil
	})
	tok, err := hdmi.EnsureToken(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	req := func(method, path, token, remoteAddr string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = remoteAddr
		if token != "" {
			r.Header.Set("Authorization", "Bearer hdmi:"+token)
		}
		r.Header.Set("X-Strata", "1")
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		return w
	}
	// Valid token from the loopback: the read routes answer.
	if w := req("GET", "/api/cinema", tok, "127.0.0.1:5555"); w.Code != 200 {
		t.Fatalf("kiosk GET /api/cinema: %d %s", w.Code, w.Body.String())
	}
	// Wrong token, no token, token from outside the loopback: 401.
	if w := req("GET", "/api/cinema", "wrong", "127.0.0.1:5555"); w.Code != 401 {
		t.Errorf("wrong token: %d", w.Code)
	}
	if w := req("GET", "/api/cinema", "", "127.0.0.1:5555"); w.Code != 401 {
		t.Errorf("no token: %d", w.Code)
	}
	if w := req("GET", "/api/cinema", tok, "192.0.2.10:5555"); w.Code != 401 {
		t.Errorf("not loopback: %d", w.Code)
	}
	// The kiosk session is read-only: writes and admin routes are refused.
	if w := req("POST", "/api/cinema/scan", tok, "127.0.0.1:5555"); w.Code != 403 {
		t.Errorf("kiosk POST: %d", w.Code)
	}
	if w := req("GET", "/api/system/hdmi", tok, "127.0.0.1:5555"); w.Code != 403 {
		t.Errorf("kiosk admin route: %d", w.Code)
	}
}

func TestHDMISettingsRoute(t *testing.T) {
	s := newTestServer(t)
	s.hdmiRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"}, &store.User{Username: "bob", Role: "user"})
		return nil
	})
	tokA := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	tokB := s.sessions.create("bob", "user", "127.0.0.1", time.Hour)
	do := func(token, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		r.Header.Set("X-Strata", "1")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, r)
		return rec
	}
	// A plain user cannot read or change it.
	if rec := do(tokB, "GET", "/api/system/hdmi", ""); rec.Code != 403 && rec.Code != 401 {
		t.Errorf("user GET: %d", rec.Code)
	}
	if rec := do(tokB, "PUT", "/api/system/hdmi", `{"enabled":false}`); rec.Code != 403 && rec.Code != 401 {
		t.Errorf("user PUT: %d", rec.Code)
	}
	// The admin reads the state and toggles it (the unit is not installed in
	// the test environment, so no systemctl call happens).
	rec := do(tokA, "GET", "/api/system/hdmi", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) || !strings.Contains(rec.Body.String(), `"installed":false`) {
		t.Fatalf("admin GET: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(tokA, "PUT", "/api/system/hdmi", `{"enabled":false}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("admin PUT: %d %s", rec.Code, rec.Body.String())
	}
	var c store.HDMIConsole
	s.store.Read(func(st *store.State) { c = st.HDMI })
	if !c.Off || !c.Decided {
		t.Errorf("setting not stored: %+v", c)
	}
}
