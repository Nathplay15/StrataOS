package subs

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMovieHash(t *testing.T) {
	// Known vector: the OpenSubtitles moviehash of a file is MD5(head64k +
	// tail64k + size LE). Verify the structure against a hand-computed hash.
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	data := make([]byte, 200<<10) // 200 KB: head and tail overlap avoided
	for i := range data {
		data[i] = byte(i * 7)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := MovieHash(p)
	if err != nil {
		t.Fatal(err)
	}
	man := md5.New()
	man.Write(data[:64<<10])
	man.Write(data[len(data)-64<<10:])
	var size [8]byte
	for i := 0; i < 8; i++ {
		size[i] = byte(int64(len(data)) >> (8 * i))
	}
	man.Write(size[:])
	if h != hex.EncodeToString(man.Sum(nil)) {
		t.Errorf("hash mismatch: %s vs %s", h, hex.EncodeToString(man.Sum(nil)))
	}
	// Small file (< 64 KB): head only once (no double-read), still consistent.
	small := filepath.Join(dir, "s.bin")
	os.WriteFile(small, []byte("hello"), 0o644)
	h2, err := MovieHash(small)
	if err != nil || len(h2) != 32 {
		t.Errorf("small file: %q %v", h2, err)
	}
	man2 := md5.New()
	man2.Write([]byte("hello"))
	var s2 [8]byte
	for i := 0; i < 8; i++ {
		s2[i] = byte(int64(5) >> (8 * i))
	}
	man2.Write(s2[:])
	if h2 != hex.EncodeToString(man2.Sum(nil)) {
		t.Errorf("small hash: %s vs %s", h2, hex.EncodeToString(man2.Sum(nil)))
	}
}

func TestParseLangsAndLangOf(t *testing.T) {
	if ParseLangs("fr, en;de") != "fr,en,de" {
		t.Errorf("ParseLangs: %q", ParseLangs("fr, en;de"))
	}
	if LangOf("movie.fr.srt") != "fr" || LangOf("movie.en-US.vtt") != "en-US" || LangOf("movie.srt") != "" || LangOf("a.b.c.d.srt") != "" {
		t.Errorf("LangOf wrong: %q %q %q %q", LangOf("movie.fr.srt"), LangOf("movie.en-US.vtt"), LangOf("movie.srt"), LangOf("a.b.c.d.srt"))
	}
}

// fakeOS is a local OpenSubtitles: login, search, download.
func fakeOS(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") != "key-1" {
			w.WriteHeader(401)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["username"] != "bob" || body["password"] != "secret" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-1", "status": "ok"})
	})
	mux.HandleFunc("/subtitles", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jwt-1" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("imdb_id") == "603" {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"id": "1", "attributes": map[string]any{
					"language": "fr", "rating": 4.5, "download_count": 1200, "release": "The.Matrix.1999.1080p",
					"files":           []map[string]any{{"file_id": 111, "file_name": "matrix.fr.srt"}},
					"feature_details": map[string]any{"hearing_impaired": false},
				}},
				{"id": "2", "attributes": map[string]any{
					"language": "en", "rating": 4.0, "download_count": 900,
					"files":           []map[string]any{{"file_id": 222, "file_name": "matrix.en.srt"}},
					"feature_details": map[string]any{"hearing_impaired": true},
				}},
			}})
			return
		}
		if r.URL.Query().Get("moviehash") != "" {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"id": "3", "attributes": map[string]any{
					"language": "en", "rating": 3.0, "download_count": 10,
					"files":           []map[string]any{{"file_id": 333, "file_name": "hash.en.srt"}},
					"feature_details": map[string]any{"hearing_impaired": false},
				}},
			}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jwt-1" {
			w.WriteHeader(401)
			return
		}
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
	old := Base
	Base = srv.URL
	t.Cleanup(func() { Base = old })
	return srv
}

func TestPickBest(t *testing.T) {
	res := []SearchResult{
		{FileID: 1, Lang: "en", Rating: 4.0, Downloads: 900, HearingImpaired: true},
		{FileID: 2, Lang: "fr", Rating: 4.5, Downloads: 1200},
		{FileID: 3, Lang: "en", Rating: 4.0, Downloads: 5000},
		{FileID: 4, Lang: "de", Rating: 5.0, Downloads: 10},
	}
	if PickBest(res, "fr,en") != 1 {
		t.Errorf("fr first: %d", PickBest(res, "fr,en"))
	}
	if PickBest(res, "en,fr") != 2 {
		t.Errorf("en non-HI beats HI despite fewer downloads: %d", PickBest(res, "en,fr"))
	}
	if PickBest(res, "de") != 3 {
		t.Errorf("de: %d", PickBest(res, "de"))
	}
	if PickBest(nil, "en") != -1 {
		t.Errorf("empty: %d", PickBest(nil, "en"))
	}
}

func TestClientSearchDownload(t *testing.T) {
	fakeOS(t)
	c := &Client{APIKey: "key-1", User: "bob", Password: "secret"}
	ctx := context.Background()
	res, err := c.Search(ctx, "tt603", "", "fr,en")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Lang != "fr" || res[0].FileID != 111 || res[1].HearingImpaired != true {
		t.Fatalf("search: %+v", res)
	}
	// By moviehash.
	res2, err := c.Search(ctx, "", "abc123", "en")
	if err != nil || len(res2) != 1 || res2[0].FileID != 333 {
		t.Fatalf("hash search: %+v %v", res2, err)
	}
	// Download.
	b, err := c.Download(ctx, 111)
	if err != nil || !strings.Contains(string(b), "Sous-titre") {
		t.Fatalf("download: %q %v", b, err)
	}
	// Bad credentials.
	bad := &Client{APIKey: "key-1", User: "bob", Password: "wrong"}
	if _, err := bad.Search(ctx, "tt603", "", "en"); err == nil {
		t.Error("bad login should fail")
	}
	// Missing credentials.
	if _, err := (&Client{}).Search(ctx, "tt603", "", "en"); err == nil {
		t.Error("no credentials should fail")
	}
	// No id at all.
	if _, err := c.Search(ctx, "", "", "en"); err == nil {
		t.Error("no id should fail")
	}
}
