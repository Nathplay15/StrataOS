package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"strataos/internal/store"
)

func sessionsServer(t *testing.T) *Server {
	s := newTestServer(t)
	s.sessionRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users,
			&store.User{Username: "admin", Role: "admin", PasswordHash: hash},
			&store.User{Username: "bob", PasswordHash: hash})
		return nil
	})
	return s
}

// getList calls an endpoint answering a JSON array.
func getList(t *testing.T, c *jarClient, path string) (int, []map[string]any) {
	t.Helper()
	w := c.req("GET", path, nil)
	var out []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestSessionsListAndRevoke(t *testing.T) {
	s := sessionsServer(t)
	adminTok := s.sessions.create("admin", "admin", "192.0.2.1", time.Hour)
	bob := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("bob", "", "192.0.2.2", time.Hour)}}
	bobTok2 := s.sessions.create("bob", "", "192.0.2.3", time.Hour)
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: adminTok}}

	code, list := getList(t, bob, "/api/sessions")
	if code != 200 || len(list) != 2 {
		t.Fatalf("bob sees his sessions: %d %v", code, list)
	}
	var current map[string]any
	for _, x := range list {
		if x["current"] == true {
			current = x
		}
	}
	if current == nil || current["user"] != "bob" {
		t.Fatalf("current session marked: %v", list)
	}

	// A user may not end somebody else's session.
	if code, _ := bob.do("DELETE", "/api/sessions/"+key(adminTok), nil); code != http.StatusForbidden {
		t.Fatalf("bob revoking admin: %d", code)
	}

	// Sign out everywhere else keeps the current session only.
	code, out := bob.do("DELETE", "/api/sessions/others", nil)
	if code != 200 || out["ended"].(float64) != 1 {
		t.Fatalf("others: %d %v", code, out)
	}
	code, list = getList(t, bob, "/api/sessions")
	if code != 200 || len(list) != 1 {
		t.Fatalf("after others: %d %v", code, list)
	}
	if _, ok := s.sessions.m[key(bobTok2)]; ok {
		t.Fatal("the other session is still alive")
	}

	// An administrator sees and ends every session.
	code, list = getList(t, admin, "/api/sessions")
	if code != 200 || len(list) != 2 {
		t.Fatalf("admin sees all sessions: %d %v", code, list)
	}
	bobID := ""
	for _, x := range list {
		if x["user"] == "bob" {
			bobID = x["id"].(string)
		}
	}
	if code, _ = admin.do("DELETE", "/api/sessions/"+bobID, nil); code != 200 {
		t.Fatalf("admin revoking bob: %d", code)
	}
	if s.sessions.get(bobTok2) != nil {
		t.Fatal("bob's session survives")
	}
	if code, _ := bob.do("GET", "/api/sessions", nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked session still accepted: %d", code)
	}
}

func TestLoginHistory(t *testing.T) {
	s := sessionsServer(t)
	bob := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("bob", "", "192.0.2.2", time.Hour)}}
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.1", time.Hour)}}

	s.store.Login("bob", "192.0.2.2", "TestBrowser/1.0", true, "password")
	s.store.Login("bob", "192.0.2.9", "TestBrowser/2.0", false, "password")
	s.store.Login("admin", "192.0.2.1", "TestBrowser/1.0", true, "passkey")
	s.store.Login("bob", "192.0.2.2", "TestBrowser/1.0", true, "password")

	code, list := getList(t, bob, "/api/me/logins")
	if code != 200 || len(list) != 3 {
		t.Fatalf("bob's history: %d %v", code, list)
	}
	last := list[0]
	if last["ok"] != true || last["how"] != "password" || last["ip"] != "192.0.2.2" {
		t.Fatalf("newest first: %v", last)
	}
	if last["newDevice"] != nil { // omitempty: false is absent in JSON
		t.Fatalf("same browser again: %v, want no newDevice", last)
	}
	if list[2]["newDevice"] != true || list[2]["ua"] != "TestBrowser/1.0" {
		t.Fatalf("first sign-in of the browser must be newDevice: %v", list[2])
	}
	if list[1]["newDevice"] != nil {
		t.Fatalf("a failed attempt is never newDevice: %v", list[1])
	}

	code, list = getList(t, admin, "/api/logins")
	if code != 200 || len(list) != 4 {
		t.Fatalf("admin history: %d %v", code, list)
	}
	code, list = getList(t, admin, "/api/logins?user=bob")
	if code != 200 || len(list) != 3 {
		t.Fatalf("admin history filtered: %d %v", code, list)
	}
	code, list = getList(t, admin, "/api/logins?ok=1")
	if code != 200 || len(list) != 3 {
		t.Fatalf("admin history ok only: %d %v", code, list)
	}
	for _, l := range list {
		if l["ok"] != true {
			t.Fatalf("ok=1 kept a failed attempt: %v", l)
		}
	}

	// A plain user may not read the history of the others.
	if code, _ := bob.do("GET", "/api/logins", nil); code != http.StatusForbidden {
		t.Fatalf("bob reading all logins: %d", code)
	}
}

func TestRecordLoginNewDevice(t *testing.T) {
	s := authServer(t)
	s.sessionRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin", PasswordHash: hash})
		return nil
	})
	login := func(ua string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"username": "admin", "password": "Secret-Pass-1"})
		r := httptest.NewRequest("POST", "https://"+testHost+"/api/login", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	for _, ua := range []string{"TestUA/1", "TestUA/1", "TestUA/2"} {
		if w := login(ua); w.Code != http.StatusOK {
			t.Fatalf("login %q: %d %s", ua, w.Code, w.Body.String())
		}
	}
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.1", time.Hour)}}
	code, list := getList(t, admin, "/api/logins")
	if code != 200 || len(list) != 3 {
		t.Fatalf("history: %d %v", code, list)
	}
	// Newest first: TestUA/2 (new), TestUA/1 (known), TestUA/1 (new, first seen).
	if list[0]["ua"] != "TestUA/2" || list[0]["newDevice"] != true {
		t.Fatalf("new browser must be newDevice: %v", list[0])
	}
	if list[1]["newDevice"] != nil {
		t.Fatalf("same browser again must not be newDevice: %v", list[1])
	}
	if list[2]["newDevice"] != true || list[2]["ua"] != "TestUA/1" {
		t.Fatalf("first browser use must be newDevice: %v", list[2])
	}
}
