package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/docker"
	"strataos/internal/store"
)

func TestFrigateRecordings(t *testing.T) {
	var queries []string
	frig := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/events":
			w.Write([]byte(`[{"id":"1718049375.1-a","camera":"door","label":"person","start_time":1718049375.1,"end_time":1718049390.5,"has_clip":true,"top_score":0.9},
				{"id":"1718049380.1-b","camera":"garden","label":"cat","start_time":1718049380.1,"has_clip":true}]`))
		case "/api/events/1718049375.1-a":
			w.Write([]byte(`{"id":"1718049375.1-a","camera":"door","label":"person","start_time":1718049375.1,"has_clip":true}`))
		case "/api/events/1718049380.1-b":
			w.Write([]byte(`{"id":"1718049380.1-b","camera":"garden","label":"cat","start_time":1718049380.1,"has_clip":true}`))
		case "/api/events/1718049375.1-a/clip.mp4":
			if r.Header.Get("Range") != "bytes=0-3" {
				t.Errorf("range not forwarded: %q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 0-3/10")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("ftyp"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer frig.Close()
	u, _ := url.Parse(frig.URL)
	prev := docker.StacksDir
	docker.StacksDir = t.TempDir()
	t.Cleanup(func() { docker.StacksDir = prev })
	dir := filepath.Join(docker.StacksDir, docker.FrigateStack)
	_ = os.MkdirAll(dir, 0o750)
	_ = os.WriteFile(filepath.Join(dir, ".strata-app"), []byte("frigate\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("API_PORT="+u.Port()+"\nWEB_PORT=8971\n"), 0o600)

	s := newTestServer(t)
	s.nvrRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		st.NVR.Cameras = []*store.Camera{{ID: "c1", Name: "Door", Engine: "frigate", Enabled: true}, {ID: "c2", Name: "Hall", Enabled: true}}
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	get := func(path string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		if len(hdr) == 2 {
			req.Header.Set(hdr[0], hdr[1])
		}
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	rec := get("/api/nvr/cameras/c1/frigate/events?day=2024-06-10")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"id":"1718049375.1-a"`) || strings.Contains(body, "1718049380.1-b") || !strings.Contains(body, `"webPort":"8971"`) {
		t.Fatalf("events: %d %s", rec.Code, body)
	}
	if len(queries) == 0 || !strings.Contains(queries[0], "cameras=door") || !strings.Contains(queries[0], "has_clip=1") {
		t.Errorf("query sent to Frigate: %v", queries)
	}
	if rec := get("/api/nvr/cameras/c2/frigate/events"); rec.Code != http.StatusBadRequest {
		t.Errorf("built-in camera: %d %s", rec.Code, rec.Body.String())
	}
	rec = get("/api/nvr/cameras/c1/frigate/events/1718049375.1-a/clip.mp4", "Range", "bytes=0-3")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "ftyp" || rec.Header().Get("Content-Type") != "video/mp4" || rec.Header().Get("Content-Range") != "bytes 0-3/10" {
		t.Fatalf("clip: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	// An event of another camera, a file that is not a clip, a path.
	for _, p := range []string{"/api/nvr/cameras/c1/frigate/events/1718049380.1-b/clip.mp4", "/api/nvr/cameras/c1/frigate/events/1718049375.1-a/config",
		"/api/nvr/cameras/c1/frigate/events/..%2Fconfig/clip.mp4"} {
		if rec := get(p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
}
