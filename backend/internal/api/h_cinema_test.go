package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/cinema"
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
	// Wait for the background scan to finish: it writes in the test's TempDir.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if st := cinema.LoadState(filepath.Join(s.dataDir, "cinema.json")); !st.Checked.IsZero() && !st.Running {
			break
		}
		time.Sleep(50 * time.Millisecond)
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

func TestCinemaWatchLists(t *testing.T) {
	s := newTestServer(t)
	s.cinemaRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"}, &store.User{Username: "bob", Role: "user"})
		return nil
	})
	// A library with two known videos.
	statePath, _, _ := s.cinemaPaths()
	if err := cinema.SaveState(statePath, cinema.State{Items: []cinema.Item{
		{Path: "videos/m1.mkv", Title: "Movie 1", Kind: "movie"},
		{Path: "videos/s1e1.mkv", Title: "Show", Kind: "episode", Season: 1, Episode: 1},
	}}); err != nil {
		t.Fatal(err)
	}
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
	// Empty at first.
	if rec := do(tokA, "GET", "/api/cinema/lists", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "[]") {
		t.Fatalf("lists: %d %s", rec.Code, rec.Body.String())
	}
	// Bad name.
	if rec := do(tokA, "POST", "/api/cinema/lists", `{"name":""}`); rec.Code != 400 {
		t.Errorf("empty name: %d", rec.Code)
	}
	// Create.
	rec := do(tokA, "POST", "/api/cinema/lists", `{"name":"Weekend"}`)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(tokA, "GET", "/api/cinema/lists", "")
	body := rec.Body.String()
	if !strings.Contains(body, `"name":"Weekend"`) || !strings.Contains(body, `"mine":true`) {
		t.Fatalf("created: %s", body)
	}
	var lists []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lists); err != nil || len(lists) != 1 {
		t.Fatalf("parse lists: %v %s", err, body)
	}
	id := lists[0].ID
	// Bob does not see it yet.
	if rec := do(tokB, "GET", "/api/cinema/lists", ""); strings.Contains(rec.Body.String(), "Weekend") {
		t.Fatalf("bob sees a private list: %s", rec.Body.String())
	}
	// Add items: unknown paths are dropped, dupes too.
	if rec := do(tokA, "PUT", "/api/cinema/lists/"+id, `{"items":["videos/m1.mkv","videos/s1e1.mkv","videos/m1.mkv","videos/ghost.mkv"]}`); rec.Code != 200 {
		t.Fatalf("items: %d %s", rec.Code, rec.Body.String())
	}
	// Share with bob; unknown account refused.
	if rec := do(tokA, "PUT", "/api/cinema/lists/"+id, `{"with":["nobody"]}`); rec.Code != 400 {
		t.Errorf("unknown user: %d", rec.Code)
	}
	if rec := do(tokA, "PUT", "/api/cinema/lists/"+id, `{"with":["bob"]}`); rec.Code != 200 {
		t.Fatalf("share: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(tokB, "GET", "/api/cinema/lists", "")
	body = rec.Body.String()
	if !strings.Contains(body, `"name":"Weekend"`) || !strings.Contains(body, `"mine":false`) || !strings.Contains(body, `"title":"Movie 1"`) {
		t.Fatalf("bob list: %s", body)
	}
	// Bob (member) may change items, not the name nor the sharing.
	if rec := do(tokB, "PUT", "/api/cinema/lists/"+id, `{"items":["videos/s1e1.mkv"]}`); rec.Code != 200 {
		t.Errorf("member items: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(tokB, "PUT", "/api/cinema/lists/"+id, `{"name":"hax"}`); rec.Code != 403 {
		t.Errorf("member rename: %d", rec.Code)
	}
	if rec := do(tokB, "PUT", "/api/cinema/lists/"+id, `{"with":["admin"]}`); rec.Code != 403 {
		t.Errorf("member share: %d", rec.Code)
	}
	if rec := do(tokB, "DELETE", "/api/cinema/lists/"+id, ""); rec.Code != 403 {
		t.Errorf("member delete: %d", rec.Code)
	}
	// Owner deletes.
	if rec := do(tokA, "DELETE", "/api/cinema/lists/"+id, ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(tokA, "GET", "/api/cinema/lists", ""); !strings.Contains(rec.Body.String(), "[]") {
		t.Fatalf("not deleted: %s", rec.Body.String())
	}
}

func TestCinemaProfiles(t *testing.T) {
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
	if rec := do(tokB, "GET", "/api/cinema/profiles", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "[]") {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(tokB, "POST", "/api/cinema/profiles", `{"name":"Kids"}`); rec.Code != 403 && rec.Code != 401 {
		t.Errorf("user create: %d", rec.Code)
	}
	if rec := do(tokA, "POST", "/api/cinema/profiles", `{"name":"","maxRating":6}`); rec.Code != 400 {
		t.Errorf("empty name: %d", rec.Code)
	}
	if rec := do(tokA, "POST", "/api/cinema/profiles", `{"name":"Kids","maxRating":11}`); rec.Code != 400 {
		t.Errorf("rating 11: %d", rec.Code)
	}
	if rec := do(tokA, "POST", "/api/cinema/profiles", `{"name":"Kids","maxRating":6}`); rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(tokB, "GET", "/api/cinema/profiles", "")
	body := rec.Body.String()
	if !strings.Contains(body, `"name":"Kids"`) || !strings.Contains(body, `"maxRating":6`) {
		t.Fatalf("saved: %s", body)
	}
	var profs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &profs); err != nil || len(profs) != 1 {
		t.Fatalf("parse: %v %s", err, body)
	}
	if rec := do(tokA, "DELETE", "/api/cinema/profiles/"+profs[0].ID, ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(tokB, "GET", "/api/cinema/profiles", ""); !strings.Contains(rec.Body.String(), "[]") {
		t.Fatalf("not deleted: %s", rec.Body.String())
	}
}

func TestCinemaTrailerRoute(t *testing.T) {
	// Fake TMDB with a videos endpoint that counts its calls.
	mux := http.NewServeMux()
	calls := 0
	mux.HandleFunc("/movie/603/videos", func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"key": "yt1", "site": "YouTube", "type": "Trailer", "name": "Official trailer", "official": true},
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := cinema.TMDBBase
	cinema.TMDBBase = srv.URL
	t.Cleanup(func() { cinema.TMDBBase = old })

	s := newTestServer(t)
	s.cinemaRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
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
	// No key yet: 409.
	if rec := do("GET", "/api/cinema/trailer?kind=movie&id=603", ""); rec.Code != 409 {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("PUT", "/api/cinema/settings", `{"tmdbKey":"abc"}`); rec.Code != 200 {
		t.Fatalf("set key: %d %s", rec.Code, rec.Body.String())
	}
	// Bad params: 400.
	if rec := do("GET", "/api/cinema/trailer?kind=book&id=603", ""); rec.Code != 400 {
		t.Errorf("bad kind: %d", rec.Code)
	}
	if rec := do("GET", "/api/cinema/trailer?kind=movie&id=abc", ""); rec.Code != 400 {
		t.Errorf("bad id: %d", rec.Code)
	}
	// Happy path, then served from the cache.
	if rec := do("GET", "/api/cinema/trailer?kind=movie&id=603", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"key":"yt1"`) {
		t.Fatalf("trailer: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("GET", "/api/cinema/trailer?kind=movie&id=603", ""); rec.Code != 200 {
		t.Fatalf("trailer again: %d %s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Errorf("TMDB called %d times, want 1 (cached)", calls)
	}
}
