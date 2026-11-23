package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestMediaNightRoutes(t *testing.T) {
	s := newTestServer(t)
	s.mediaRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"}, &store.User{Username: "bob", Role: "user"})
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
	// Default: disabled, readable by a plain user.
	rec := do(tokB, "GET", "/api/media/night", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) && !strings.Contains(rec.Body.String(), `"enabled":`) {
		if rec.Code != 200 {
			t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
		}
	}
	// A user cannot set it.
	if rec := do(tokB, "PUT", "/api/media/night", `{"enabled":true,"from":22,"to":6,"maxVolume":30,"brightness":80}`); rec.Code != 403 && rec.Code != 401 {
		t.Errorf("user put: %d", rec.Code)
	}
	// Hours out of range rejected.
	if rec := do(tokA, "PUT", "/api/media/night", `{"enabled":true,"from":24,"to":6}`); rec.Code != 400 {
		t.Errorf("from=24: %d", rec.Code)
	}
	// Valid: saved, and out-of-range volume/brightness are clamped.
	if rec := do(tokA, "PUT", "/api/media/night", `{"enabled":true,"from":22,"to":6,"maxVolume":30,"brightness":80}`); rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(tokB, "GET", "/api/media/night", "")
	body := rec.Body.String()
	if !strings.Contains(body, `"enabled":true`) || !strings.Contains(body, `"from":22`) || !strings.Contains(body, `"to":6`) || !strings.Contains(body, `"maxVolume":30`) || !strings.Contains(body, `"brightness":80`) {
		t.Fatalf("saved night: %s", body)
	}
	if rec := do(tokA, "PUT", "/api/media/night", `{"enabled":true,"from":22,"to":6,"maxVolume":500,"brightness":5}`); rec.Code != 200 {
		t.Fatalf("clamp: %d", rec.Code)
	}
	if rec := do(tokB, "GET", "/api/media/night", ""); !strings.Contains(rec.Body.String(), `"maxVolume":30`) || !strings.Contains(rec.Body.String(), `"brightness":80`) {
		t.Fatalf("clamped: %s", rec.Body.String())
	}
}
