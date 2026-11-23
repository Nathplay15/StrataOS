package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestValidFleetHost(t *testing.T) {
	for _, bad := range []string{"", "  ", "a/b", "a b", "a@b", "a?b", "a#b", "https://x"} {
		if _, err := validFleetHost(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, ok := range []string{"192.168.1.30", " nas.lan ", "[::1]", "nas.lan:7443"} {
		h, err := validFleetHost(ok)
		if err != nil || strings.TrimSpace(h) != strings.TrimSpace(ok) {
			t.Errorf("rejected %q: %v", ok, err)
		}
	}
}

func TestApplyWatchedResult(t *testing.T) {
	st := &store.State{}
	st.Fleet.Watched = []store.FleetWatched{
		{ID: "w1", Name: "NAS1", Host: "192.168.1.31", LastError: "old error"},
		{ID: "w2", Name: "NAS2", Host: "192.168.1.32"},
	}
	// Success: seen now, error cleared, summary kept.
	sum := map[string]any{"hostname": "nas1", "version": "0.15.0"}
	applyWatchedResult(st, "w1", sum, nil)
	w := st.Fleet.Watched[0]
	if w.LastError != "" || w.Summary["hostname"] != "nas1" || w.LastSeen.IsZero() || time.Since(w.LastSeen) > time.Minute {
		t.Errorf("success not recorded: %+v", w)
	}
	// Failure: error kept, summary dropped, seen untouched.
	before := st.Fleet.Watched[1].LastSeen
	applyWatchedResult(st, "w2", nil, errors.New("connection refused"))
	w = st.Fleet.Watched[1]
	if w.LastError != "connection refused" || w.Summary != nil || !w.LastSeen.Equal(before) {
		t.Errorf("error not recorded: %+v", w)
	}
	// Peer error is formatted with the HTTP status.
	applyWatchedResult(st, "w2", nil, &peerError{Status: 401, Msg: "bad token"})
	if st.Fleet.Watched[1].LastError != "HTTP 401: bad token" {
		t.Errorf("peer error: %q", st.Fleet.Watched[1].LastError)
	}
	// Unknown id: no-op, no panic.
	applyWatchedResult(st, "nope", sum, nil)
	if len(st.Fleet.Watched) != 2 {
		t.Error("list changed")
	}
}

func TestFleetWatchedRoutes(t *testing.T) {
	s := newTestServer(t)
	s.fleetWatchedRoutes()
	seen := time.Now().Add(-3 * time.Minute)
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		st.Fleet.Watched = []store.FleetWatched{
			{ID: "w1", Name: "NAS1", Host: "192.168.1.31", Cert: "aa", Token: "t", Added: seen.Add(-24 * time.Hour), LastSeen: seen, Summary: map[string]any{"hostname": "nas1", "version": "0.15.0"}},
			{ID: "w2", Name: "NAS2", Host: "192.168.1.32", Cert: "bb", Token: "t", Added: seen.Add(-24 * time.Hour), LastError: "connection refused"},
		}
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		req.Header.Set("X-Strata", "1")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}

	// List without refresh: the stored state, online only when seen.
	rec := do("GET", "/api/fleet/watched", "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"id":"w1"`) || !strings.Contains(body, `"online":true`) || !strings.Contains(body, `"version":"0.15.0"`) {
		t.Fatalf("list: %d %s", rec.Code, body)
	}
	if !strings.Contains(body, `"id":"w2"`) || !strings.Contains(body, `"error":"connection refused"`) || strings.Contains(body, `"online":true,"error"`) {
		t.Fatalf("offline row wrong: %s", body)
	}

	// Add: validation happens before any network call.
	if rec := do("POST", "/api/fleet/watched", `{"host":"","code":"X"}`); rec.Code != 400 {
		t.Errorf("empty host: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/api/fleet/watched", `{"host":"a/b","code":"X"}`); rec.Code != 400 {
		t.Errorf("bad host: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/api/fleet/watched", `{"host":"192.168.1.99","code":""}`); rec.Code != 400 {
		t.Errorf("empty code: %d %s", rec.Code, rec.Body.String())
	}

	// Delete.
	if rec := do("DELETE", "/api/fleet/watched/w2", ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("GET", "/api/fleet/watched", "")
	if strings.Contains(rec.Body.String(), "w2") {
		t.Fatalf("not deleted: %s", rec.Body.String())
	}
}
