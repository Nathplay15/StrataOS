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

func TestNvrEventClips(t *testing.T) {
	s := newTestServer(t)
	s.nvrRoutes()
	dir := t.TempDir()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		st.NVR.Folder = dir
		st.NVR.Cameras = []*store.Camera{{ID: "c1", Name: "Door", Enabled: true}}
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		req.Header.Set("X-Strata", "1") // what fetch() from the pages sends
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	clipsDir := filepath.Join(dir, "c1", "clips")
	if err := os.MkdirAll(clipsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(clipsDir, "2026-11-23_14h05m09s.mp4")
	if err := os.WriteFile(clip, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clipsDir, "junk.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// List: the clips, newest first, without the other files.
	rec := do("GET", "/api/nvr/cameras/c1/clips")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"name":"2026-11-23_14h05m09s.mp4"`) || !strings.Contains(body, `"at":"2026-11-23T14:05:09`) || strings.Contains(body, "junk") {
		t.Fatalf("list: %d %s", rec.Code, body)
	}

	// Serve: the file as a video, and as a download.
	rec = do("GET", "/api/nvr/cameras/c1/clips/2026-11-23_14h05m09s.mp4")
	if rec.Code != 200 || rec.Body.String() != "video" || rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("serve: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	rec = do("GET", "/api/nvr/cameras/c1/clips/2026-11-23_14h05m09s.mp4?download=1")
	if rec.Header().Get("Content-Disposition") == "" {
		t.Error("download disposition missing")
	}

	// Bad names and paths are refused.
	for _, p := range []string{
		"/api/nvr/cameras/c1/clips/junk.txt",
		"/api/nvr/cameras/c1/clips/20261123-140509.mp4", // segment name, not a clip
		"/api/nvr/cameras/c1/clips/..%2F..%2Fstate.json",
		"/api/nvr/cameras/cX/clips/2026-11-23_14h05m09s.mp4",
	} {
		if rec := do("GET", p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d %s", p, rec.Code, rec.Body.String())
		}
	}

	// Delete.
	if rec := do("DELETE", "/api/nvr/cameras/c1/clips/2026-11-23_14h05m09s.mp4"); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(clip); !os.IsNotExist(err) {
		t.Error("the clip must be deleted")
	}
	if rec := do("DELETE", "/api/nvr/cameras/c1/clips/2026-11-23_14h05m09s.mp4"); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: %d", rec.Code)
	}
}

func TestNvrClipsDayFilter(t *testing.T) {
	s := newTestServer(t)
	s.nvrRoutes()
	dir := t.TempDir()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		st.NVR.Folder = dir
		st.NVR.Cameras = []*store.Camera{{ID: "c1", Name: "Door", Enabled: true}}
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		req.Header.Set("X-Strata", "1")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	clipsDir := filepath.Join(dir, "c1", "clips")
	if err := os.MkdirAll(clipsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-11-22_23h59m59s.mp4", "2026-11-23_00h00m01s.mp4", "2026-11-23_14h05m09s.mp4", "2026-11-24_00h00m01s.mp4"} {
		if err := os.WriteFile(filepath.Join(clipsDir, name), []byte("v"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec := do("/api/nvr/cameras/c1/clips?day=2026-11-23")
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("day filter: %d %s", rec.Code, body)
	}
	if strings.Contains(body, "2026-11-22") || strings.Contains(body, "2026-11-24") {
		t.Errorf("other days leaked: %s", body)
	}
	if !strings.Contains(body, "2026-11-23_00h00m01s.mp4") || !strings.Contains(body, "2026-11-23_14h05m09s.mp4") {
		t.Errorf("day clips missing: %s", body)
	}
	if rec := do("/api/nvr/cameras/c1/clips?day=not-a-day"); rec.Code != 400 {
		t.Errorf("invalid day: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("/api/nvr/cameras/c1/clips"); !strings.Contains(rec.Body.String(), "2026-11-22_23h59m59s.mp4") {
		t.Errorf("no filter should list all: %s", rec.Body.String())
	}
}
