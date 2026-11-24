package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"strataos/internal/hdmi"
	"strataos/internal/store"
)

func TestHDMIKioskIsNotAdmin(t *testing.T) {
	s := newTestServer(t)
	tok, err := hdmi.EnsureToken(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var got *session
	var admin bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got, admin = sessionFrom(r.Context()), isAdmin(r) })
	call := func(method, path, ip, tk string) int {
		got = nil
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = ip + ":5000"
		w := httptest.NewRecorder()
		s.hdmiKioskAuth(w, r, next, false, tk)
		if got != nil {
			return 200
		}
		return w.Code
	}
	// No account chosen for the TV: nothing.
	if c := call("GET", "/api/cinema", "127.0.0.1", tok); c != http.StatusForbidden {
		t.Fatalf("no account: %d", c)
	}
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "tv", Role: "user"})
		st.HDMI.Account = "tv"
		return nil
	})
	if c := call("GET", "/api/cinema", "127.0.0.1", tok); c != 200 || admin || got.User != "tv" || got.Role == "admin" {
		t.Fatalf("kiosk session: %d %+v admin=%v", c, got, admin)
	}
	for _, p := range []string{"/api/files", "/api/files/download", "/api/settings", "/api/system/hdmi", "/api/users"} {
		if c := call("GET", p, "127.0.0.1", tok); c != http.StatusForbidden {
			t.Errorf("%s reachable by the kiosk: %d", p, c)
		}
	}
	if c := call("PUT", "/api/cinema/position", "127.0.0.1", tok); c != http.StatusForbidden {
		t.Errorf("write: %d", c)
	}
	if c := call("GET", "/api/cinema", "192.168.1.50", tok); c != http.StatusUnauthorized {
		t.Errorf("from the LAN: %d", c)
	}
	if c := call("GET", "/api/cinema", "127.0.0.1", "wrong"); c != http.StatusUnauthorized {
		t.Errorf("bad token: %d", c)
	}
}

func TestHDMIRemoteAllowed(t *testing.T) {
	s := newTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.HDMI.Remote = []string{"bob"}
		return nil
	})
	with := func(ss *session) *http.Request {
		r := httptest.NewRequest("POST", "/api/hdmi/remote/key", nil)
		return r.WithContext(context.WithValue(r.Context(), ctxKey{}, ss))
	}
	if !s.hdmiRemoteAllowed(with(&session{User: "root", Role: "admin"})) {
		t.Error("admin refused")
	}
	if !s.hdmiRemoteAllowed(with(&session{User: "bob", Role: "user"})) {
		t.Error("allowed account refused")
	}
	if s.hdmiRemoteAllowed(with(&session{User: "eve", Role: "user"})) {
		t.Error("other account allowed")
	}
	if s.hdmiRemoteAllowed(with(&session{User: "bob", Role: "user", Scopes: []string{hdmiKioskScope}})) {
		t.Error("kiosk allowed to drive itself")
	}
}
