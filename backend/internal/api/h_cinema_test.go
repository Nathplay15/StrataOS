package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestCinemaRoutes(t *testing.T) {
	s := newTestServer(t)
	s.cinemaRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"}, &store.User{Username: "bob", Role: "user"})
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	tokUser := s.sessions.create("bob", "bob", "127.0.0.1", time.Hour)
	do := func(token, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		req.Header.Set("X-Strata", "1")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}

	// Empty library, no key.
	rec := do(tokUser, "GET", "/api/cinema", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"keySet":false`) || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}

	// A plain user cannot set the key or scan.
	if rec := do(tokUser, "PUT", "/api/cinema/settings", `{"tmdbKey":"abc"}`); rec.Code != 403 && rec.Code != 401 {
		t.Errorf("user put: %d", rec.Code)
	}
	if rec := do(tokUser, "POST", "/api/cinema/scan", ""); rec.Code != 403 && rec.Code != 401 {
		t.Errorf("user scan: %d", rec.Code)
	}

	// Admin sets the key; it is stored but never returned.
	if rec := do(tok, "PUT", "/api/cinema/settings", `{"tmdbKey":"abc123"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"keySet":true`) {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	keyPath := filepath.Join(s.dataDir, "cinema.key")
	b, err := os.ReadFile(keyPath)
	if err != nil || strings.TrimSpace(string(b)) != "abc123" {
		t.Fatalf("key file: %v %q", err, b)
	}
	if rec := do(tok, "GET", "/api/cinema", ""); strings.Contains(rec.Body.String(), "abc123") {
		t.Fatalf("key leaked: %s", rec.Body.String())
	}
	// "********" keeps the stored key.
	if rec := do(tok, "PUT", "/api/cinema/settings", `{"tmdbKey":"********"}`); rec.Code != 200 {
		t.Fatalf("keep key: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(keyPath); strings.TrimSpace(string(b)) != "abc123" {
		t.Fatalf("key overwritten: %q", b)
	}
	// Oversized key rejected.
	if rec := do(tok, "PUT", "/api/cinema/settings", `{"tmdbKey":"`+strings.Repeat("x", 100)+`"}`); rec.Code != 400 {
		t.Errorf("long key: %d", rec.Code)
	}

	// Scan trigger: starts once, refuses while running.
	rec = do(tok, "POST", "/api/cinema/scan", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"started":true`) {
		t.Fatalf("scan: %d %s", rec.Code, rec.Body.String())
	}
	// Poster without data: 404.
	if rec := do(tokUser, "GET", "/api/cinema/poster/603", ""); rec.Code != 404 {
		t.Errorf("poster: %d", rec.Code)
	}
	if rec := do(tokUser, "GET", "/api/cinema/poster/abc", ""); rec.Code != 400 {
		t.Errorf("bad poster id: %d", rec.Code)
	}
}

func TestCinemaPositions(t *testing.T) {
	s := newTestServer(t)
	s.cinemaRoutes()
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
	// Invalid positions are rejected.
	for _, body := range []string{
		`{"path":"","at":1,"duration":10}`,
		`{"path":"a","at":-1,"duration":10}`,
		`{"path":"a","at":1,"duration":0}`,
		`{"path":"a","at":100,"duration":10}`,
	} {
		if rec := do(tokA, "PUT", "/api/cinema/position", body); rec.Code != 400 {
			t.Errorf("accepted %s: %d", body, rec.Code)
		}
	}
	// Save a position for admin; it shows in its GET, not in bob's.
	if rec := do(tokA, "PUT", "/api/cinema/position", `{"path":"videos/m.mkv","at":30,"duration":100}`); rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(tokA, "GET", "/api/cinema", "")
	if !strings.Contains(rec.Body.String(), `"positions":{"videos/m.mkv":{"at":30`) {
		t.Fatalf("admin positions: %s", rec.Body.String())
	}
	if rec := do(tokB, "GET", "/api/cinema", ""); strings.Contains(rec.Body.String(), "m.mkv") {
		t.Fatalf("bob sees admin's positions: %s", rec.Body.String())
	}
	// Finished (>= 95%): the entry is removed.
	if rec := do(tokA, "PUT", "/api/cinema/position", `{"path":"videos/m.mkv","at":96,"duration":100}`); rec.Code != 200 {
		t.Fatalf("finish: %d", rec.Code)
	}
	if rec := do(tokA, "GET", "/api/cinema", ""); !strings.Contains(rec.Body.String(), `"positions":{}`) {
		t.Fatalf("position not cleared: %s", rec.Body.String())
	}
}
