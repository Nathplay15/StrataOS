package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos/internal/cinema"
	"strataos/internal/store"
	"strataos/internal/subs"
)

// subsTestServer builds a server with one share at /srv/strata/main holding a
// video (movie.mkv, IMDb tt603 in the cinema library) and the OpenSubtitles
// credentials in the settings (skipped when /srv cannot be created).
func subsTestServer(t *testing.T) (*Server, *jarClient) {
	t.Helper()
	s := newTestServer(t)
	s.viewerRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		st.Shares = append(st.Shares, &store.Share{Name: "main", Path: "/srv/strata/main", Permissions: map[string]string{}})
		st.Settings.OSApiKey = "key-1"
		st.Settings.OSUser = "bob"
		st.Settings.OSPass = "secret"
		st.Settings.SubLangs = "fr, en"
		return nil
	})
	root := "/srv/strata/main"
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Skip("cannot create /srv/strata in the test environment")
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	os.WriteFile(filepath.Join(root, "movie.mkv"), []byte("video"), 0o644)
	os.WriteFile(filepath.Join(root, "other.mkv"), []byte("video2"), 0o644)
	os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello"), 0o644)
	statePath, _, _ := s.cinemaPaths()
	_ = cinema.SaveState(statePath, cinema.State{Items: []cinema.Item{
		{Path: root + "/movie.mkv", IMDB: "tt603"},
		{Path: root + "/other.mkv", IMDB: "tt999"},
	}})
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}
	return s, admin
}

// fakeOSSubs is a local OpenSubtitles recording the last search query.
func fakeOSSubs(t *testing.T) (lastQuery func() url.Values) {
	t.Helper()
	var mu sync.Mutex
	var last url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-1", "status": "ok"})
	})
	mux.HandleFunc("/subtitles", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last = r.URL.Query()
		mu.Unlock()
		if r.URL.Query().Get("imdb_id") == "603" {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"attributes": map[string]any{"language": "en", "rating": 4.0, "download_count": 900,
					"files":           []map[string]any{{"file_id": 222, "file_name": "matrix.en.srt"}},
					"feature_details": map[string]any{"hearing_impaired": true}}},
				{"attributes": map[string]any{"language": "fr", "rating": 4.5, "download_count": 1200,
					"files":           []map[string]any{{"file_id": 111, "file_name": "matrix.fr.srt"}},
					"feature_details": map[string]any{"hearing_impaired": false}}},
			}})
			return
		}
		if r.URL.Query().Get("imdb_id") == "777" {
			// A malicious language: must never become a path component.
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"attributes": map[string]any{"language": "../../etc", "rating": 5.0, "download_count": 5,
					"files":           []map[string]any{{"file_id": 444, "file_name": "evil.srt"}},
					"feature_details": map[string]any{"hearing_impaired": false}}},
			}})
			return
		}
		if r.URL.Query().Get("moviehash") != "" {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"attributes": map[string]any{"language": "en", "rating": 3.0, "download_count": 10,
					"files":           []map[string]any{{"file_id": 333, "file_name": "hash.en.srt"}},
					"feature_details": map[string]any{"hearing_impaired": false}}},
			}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		id := int(body["file_id"].(float64))
		json.NewEncoder(w).Encode(map[string]string{"link": "http://" + r.Host + "/file/" + strconv.Itoa(id)})
	})
	mux.HandleFunc("/file/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nSous-titre\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := subs.Base
	subs.Base = srv.URL
	t.Cleanup(func() { subs.Base = old })
	return func() url.Values {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func TestSubtitlesFetchRoute(t *testing.T) {
	s, admin := subsTestServer(t)
	last := fakeOSSubs(t)

	// Credentials not configured: 409.
	_ = s.store.Update(func(st *store.State) error {
		st.Settings.OSApiKey, st.Settings.OSUser, st.Settings.OSPass = "", "", ""
		return nil
	})
	if w := admin.req("POST", "/api/files/subtitles/fetch", map[string]string{"path": "/srv/strata/main/movie.mkv"}); w.Code != 409 {
		t.Fatalf("no creds: %d %s", w.Code, w.Body.String())
	}
	_ = s.store.Update(func(st *store.State) error {
		st.Settings.OSApiKey, st.Settings.OSUser, st.Settings.OSPass = "key-1", "bob", "secret"
		return nil
	})

	// Happy path: search by IMDb id (cinema library), fr preferred over the
	// hearing-impaired en result, file written next to the video.
	w := admin.req("POST", "/api/files/subtitles/fetch", map[string]string{"path": "/srv/strata/main/movie.mkv"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"lang":"fr"`) || !strings.Contains(w.Body.String(), `"name":"movie.fr.srt"`) {
		t.Fatalf("fetch: %d %s", w.Code, w.Body.String())
	}
	if q := last(); q.Get("imdb_id") != "603" || q.Get("languages") != "fr,en" {
		t.Errorf("search query: %v", q)
	}
	data, err := os.ReadFile("/srv/strata/main/movie.fr.srt")
	if err != nil || !strings.Contains(string(data), "Sous-titre") {
		t.Fatalf("srt not written: %v %q", err, data)
	}

	// No IMDb id in the library: fallback to the moviehash.
	statePath, _, _ := s.cinemaPaths()
	_ = cinema.SaveState(statePath, cinema.State{Items: []cinema.Item{{Path: "/srv/strata/main/movie.mkv"}}})
	if w := admin.req("POST", "/api/files/subtitles/fetch", map[string]string{"path": "/srv/strata/main/movie.mkv"}); w.Code != 200 || !strings.Contains(w.Body.String(), `"lang":"en"`) {
		t.Fatalf("hash fallback: %d %s", w.Code, w.Body.String())
	}
	if q := last(); q.Get("moviehash") == "" || q.Get("imdb_id") != "" {
		t.Errorf("hash query: %v", q)
	}
	if _, err := os.Stat("/srv/strata/main/movie.en.srt"); err != nil {
		t.Errorf("en srt not written: %v", err)
	}

	// Malicious language from the API: sanitized, nothing written outside the folder.
	_ = cinema.SaveState(statePath, cinema.State{Items: []cinema.Item{{Path: "/srv/strata/main/other.mkv", IMDB: "tt777"}}})
	if w := admin.req("POST", "/api/files/subtitles/fetch", map[string]string{"path": "/srv/strata/main/other.mkv"}); w.Code != 200 || !strings.Contains(w.Body.String(), `"lang":"etc"`) {
		t.Fatalf("malicious lang: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat("/srv/strata/main/other.etc.srt"); err != nil {
		t.Errorf("sanitized srt not written: %v", err)
	}
	if _, err := os.Stat("/srv/strata/etc.srt"); err == nil {
		t.Error("path traversal: a file was written outside the share")
	}

	// No result at all: 404 (other.mkv has IMDb tt999, unknown to the API).
	_ = cinema.SaveState(statePath, cinema.State{Items: []cinema.Item{
		{Path: "/srv/strata/main/movie.mkv", IMDB: "tt603"},
		{Path: "/srv/strata/main/other.mkv", IMDB: "tt999"},
	}})
	if w := admin.req("POST", "/api/files/subtitles/fetch", map[string]string{"path": "/srv/strata/main/other.mkv"}); w.Code != 404 {
		t.Errorf("no result: %d %s", w.Code, w.Body.String())
	}
	// Not a media file: 400. Unknown path: not 200.
	if w := admin.req("POST", "/api/files/subtitles/fetch", map[string]string{"path": "/srv/strata/main/note.txt"}); w.Code != 400 {
		t.Errorf("not media: %d", w.Code)
	}
	if w := admin.req("POST", "/api/files/subtitles/fetch", map[string]string{"path": "/srv/strata/main/nope.mkv"}); w.Code == 200 {
		t.Errorf("missing file: %d", w.Code)
	}
}
