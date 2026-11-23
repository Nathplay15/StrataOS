package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

// The config arrays must always be present in the JSON (even empty): the
// frontend reads config.peers / config.links without a guard, and an omitted
// key crashed the WireGuard page ("Cannot read properties of undefined").
func TestVPNConfigArraysAlwaysPresent(t *testing.T) {
	s := newTestServer(t)
	s.vpnRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	req := httptest.NewRequest("GET", "/api/vpn", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	req.Header.Set("X-Strata", "1")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("get vpn: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"peers":[]`, `"links":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /api/vpn missing %s: %s", want, body)
		}
	}
}
