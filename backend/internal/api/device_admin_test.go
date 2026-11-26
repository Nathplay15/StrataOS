package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"strataos/internal/store"
)

// An administrator's phone cannot take over another administrator account
// (nor weaken the sign-in policy); a regular account stays manageable.
func TestDeviceCannotTouchAdminAccounts(t *testing.T) {
	s, _ := mobileTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "root2", Role: "admin"})
		return nil
	})
	tok, _, err := s.addDevice("root1", "Pixel", "Google Pixel 8", "0.7.0", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	s.userRoutes()
	s.twoFactorRoutes()
	s.accessRoutes()
	call := func(method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.10:5555"
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		return w.Code
	}
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/users/root2", `{"role":"admin","password":"Another-password-123"}`},
		{"PUT", "/api/users/root2", `{"role":"user"}`},
		{"PUT", "/api/users/alice", `{"role":"admin"}`},
		{"POST", "/api/users", `{"username":"eve","password":"Another-password-123","role":"admin"}`},
		{"POST", "/api/users/root2/2fa/reset", `{}`},
		{"DELETE", "/api/users/root2", ``},
		{"PUT", "/api/security/2fa-policy", `{}`},
	} {
		if code := call(c.method, c.path, c.body); code != 403 {
			t.Errorf("%s %s %s from the phone: %d, want 403", c.method, c.path, c.body, code)
		}
	}
	if code := call("POST", "/api/users/alice/2fa/reset", `{}`); code == 403 {
		t.Errorf("resetting a regular account's second factor refused")
	}
}
