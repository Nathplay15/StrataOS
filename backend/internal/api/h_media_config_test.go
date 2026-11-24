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
// frontend reads config.folders / config.shares without a guard, and an
// omitted key crashed the media page ("Cannot read properties of undefined
// (reading 'find')") right after installing the DLNA app.
func TestMediaConfigArraysAlwaysPresent(t *testing.T) {
	s := newTestServer(t)
	s.mediaRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"}, &store.User{Username: "bob", Role: "user"})
		st.Shares = append(st.Shares, &store.Share{Name: "videos", Path: "/srv/strata/videos"})
		return nil
	})
	tokA := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	tokB := s.sessions.create("bob", "bob", "127.0.0.1", time.Hour)
	do := func(token, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		req.Header.Set("X-Strata", "1")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(tokA, "GET", "/api/media/dlna", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"folders":[]`) {
		t.Errorf("dlna config without folders array: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(tokB, "GET", "/api/media/music", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"shares":[]`) {
		t.Errorf("music config without shares array: %d %s", rec.Code, rec.Body.String())
	}
	// A saved folder still round-trips (enabled=false: no readymedia needed here).
	if rec := do(tokA, "PUT", "/api/media/dlna", `{"enabled":false,"folders":[{"share":"videos","type":"video"}]}`); rec.Code != 200 {
		t.Fatalf("dlna put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(tokA, "GET", "/api/media/dlna", ""); !strings.Contains(rec.Body.String(), `"share":"videos"`) {
		t.Errorf("folder not saved: %s", rec.Body.String())
	}
}
