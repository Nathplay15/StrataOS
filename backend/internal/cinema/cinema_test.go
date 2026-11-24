package cinema

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseName(t *testing.T) {
	cases := []struct {
		name                  string
		title                 string
		year, season, episode int
		kind                  string
	}{
		{"The.Matrix.1999.1080p.BluRay.x264.mkv", "The Matrix", 1999, 0, 0, "movie"},
		{"Dune Part Two (2024).mp4", "Dune Part Two", 2024, 0, 0, "movie"},
		{"show.s01e02.720p.mkv", "show", 0, 1, 2, "episode"},
		{"Show Name - S02E10 - Episode Title.mp4", "Show Name Episode Title", 0, 2, 10, "episode"},
		{"movie_without_year.mkv", "movie without year", 0, 0, 0, "movie"},
	}
	for _, c := range cases {
		title, year, season, ep, kind := ParseName(c.name)
		if title != c.title || year != c.year || season != c.season || ep != c.episode || kind != c.kind {
			t.Errorf("ParseName(%q) = %q %d s%de%d %s, want %q %d s%de%d %s", c.name, title, year, season, ep, kind, c.title, c.year, c.season, c.episode, c.kind)
		}
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("Films/The.Matrix.1999.mkv")
	mk("Films/notes.txt") // not a video
	mk("Series/Show.S01E01.mp4")
	mk("Series/Show.S01E02.mp4")
	mk(".recycle/junk.mkv") // skipped
	mk(".hidden/hide.mkv")  // skipped
	items := Scan(root, 6)
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d: %+v", len(items), items)
	}
	var movie Item
	eps := 0
	for _, it := range items {
		if it.Kind == "episode" {
			eps++
			if it.Season != 1 || it.Episode == 0 || it.Title != "Show" {
				t.Errorf("episode parsed wrong: %+v", it)
			}
		} else {
			movie = it
		}
	}
	if movie.Title != "The Matrix" || movie.Year != 1999 {
		t.Errorf("movie parsed wrong: %+v", movie)
	}
	if eps != 2 {
		t.Errorf("want 2 episodes, got %d", eps)
	}
	// Depth limit: a video 8 levels deep is not seen.
	deep := filepath.Join(root, "a/b/c/d/e/f/g/h/deep.mkv")
	mk("a/b/c/d/e/f/g/h/deep.mkv")
	_ = deep
	if got := Scan(root, 6); len(got) != 3 {
		t.Errorf("depth limit broken: %d items", len(got))
	}
	if got := Scan(root, 10); len(got) != 4 {
		t.Errorf("deep file should be found at depth 10: %d items", len(got))
	}
}

func TestStateAndKeyRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st := LoadState(filepath.Join(dir, "cinema.json"))
	if st.Running || len(st.Items) != 0 {
		t.Error("missing file should be an empty state")
	}
	st.Items = []Item{{Path: "/x.mkv", Title: "X", Kind: "movie", TMDB: 42, Poster: "p.jpg"}}
	if err := SaveState(filepath.Join(dir, "cinema.json"), st); err != nil {
		t.Fatal(err)
	}
	st2 := LoadState(filepath.Join(dir, "cinema.json"))
	if len(st2.Items) != 1 || st2.Items[0].TMDB != 42 {
		t.Errorf("state roundtrip: %+v", st2)
	}
	if LoadKey(filepath.Join(dir, "cinema.key")) != "" {
		t.Error("missing key should be empty")
	}
	if err := SaveKey(filepath.Join(dir, "cinema.key"), "secret"); err != nil {
		t.Fatal(err)
	}
	if LoadKey(filepath.Join(dir, "cinema.key")) != "secret" {
		t.Error("key roundtrip")
	}
	fi, _ := os.Stat(filepath.Join(dir, "cinema.key"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode: %v", fi.Mode().Perm())
	}
}

// fakeTMDB serves the search/details/image endpoints used by Client.
func fakeTMDB(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/search/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") == "" {
			w.WriteHeader(401)
			return
		}
		if !strings.Contains(strings.ToLower(r.URL.Query().Get("query")), "matrix") {
			json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"id": 603, "title": "The Matrix", "release_date": "1999-03-31", "vote_average": 8.2, "poster_path": "/poster603.jpg", "overview": "A hacker learns the truth."},
		}})
	})
	mux.HandleFunc("/search/tv", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(strings.ToLower(r.URL.Query().Get("query")), "got") {
			json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"id": 1399, "name": "Game of Thrones", "first_air_date": "2011-04-17", "vote_average": 8.4, "poster_path": "/poster1399.jpg"},
		}})
	})
	mux.HandleFunc("/movie/603", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 603, "title": "The Matrix", "release_date": "1999-03-31", "overview": "A hacker learns the truth.",
			"vote_average": 8.2, "poster_path": "/poster603.jpg", "runtime": 136,
			"belongs_to_collection": map[string]any{"id": 2344, "name": "The Matrix Collection"},
			"external_ids":          map[string]any{"imdb_id": "tt0133093"},
			"genres":                []map[string]any{{"name": "Science Fiction"}},
			"credits":               map[string]any{"cast": []map[string]any{{"name": "Keanu Reeves"}, {"name": "Laurence Fishburne"}}},
		})
	})
	mux.HandleFunc("/tv/1399", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1399, "name": "Game of Thrones", "first_air_date": "2011-04-17",
			"vote_average": 8.4, "poster_path": "/poster1399.jpg", "episode_run_time": []int{60},
		})
	})
	mux.HandleFunc("/movie/603/videos", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"key": "vimeo1", "site": "Vimeo", "type": "Trailer", "name": "ignored (not YouTube)"},
			{"key": "yt-teaser", "site": "YouTube", "type": "Teaser", "name": "Teaser", "official": true},
			{"key": "yt-trailer-unofficial", "site": "YouTube", "type": "Trailer", "name": "Fan trailer"},
			{"key": "yt-trailer", "site": "YouTube", "type": "Trailer", "name": "Official trailer", "official": true},
		}})
	})
	mux.HandleFunc("/tv/1399/videos", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	})
	mux.HandleFunc("/img/poster603.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpeg603"))
	})
	mux.HandleFunc("/img/poster1399.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpeg1399"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oldBase, oldImg := TMDBBase, TMDBImages
	TMDBBase = srv.URL
	TMDBImages = srv.URL + "/img"
	t.Cleanup(func() { TMDBBase, TMDBImages = oldBase, oldImg })
	return srv
}

func TestTrailer(t *testing.T) {
	fakeTMDB(t)
	cl := &Client{Key: "k"}
	ctx := context.Background()
	v, err := cl.Trailer(ctx, "movie", 603)
	if err != nil || v.Key != "yt-trailer" || v.Name != "Official trailer" {
		t.Fatalf("trailer: %+v %v", v, err)
	}
	// No YouTube video: empty key, no error.
	v, err = cl.Trailer(ctx, "tv", 1399)
	if err != nil || v.Key != "" {
		t.Fatalf("no video: %+v %v", v, err)
	}
	// Bad kind, no key.
	if _, err := cl.Trailer(ctx, "book", 1); err == nil {
		t.Error("bad kind should fail")
	}
	if _, err := (&Client{}).Trailer(ctx, "movie", 603); err == nil {
		t.Error("no key should fail")
	}
}

func TestBuildLibrary(t *testing.T) {
	fakeTMDB(t)
	root := t.TempDir()
	posterDir := filepath.Join(t.TempDir(), "posters")
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("The.Matrix.1999.1080p.mkv")
	mk("got/S01E01.mkv")
	mk("got/S01E02.mkv")
	mk("unknown thing.mkv")
	cl := &Client{Key: "k"}
	items := BuildLibrary(context.Background(), []Root{{Abs: root, API: "/srv/strata/videos"}}, cl, posterDir, 6, nil)
	if len(items) != 4 {
		t.Fatalf("want 4 items, got %d", len(items))
	}
	for _, it := range items {
		if !strings.HasPrefix(it.Path, "/srv/strata/videos/") {
			t.Errorf("path not API-relative: %q", it.Path)
		}
	}
	var matrix, eps, unknown int
	for _, it := range items {
		switch {
		case it.TMDB == 603:
			matrix++
			if it.Title != "The Matrix" || it.Year != 1999 || it.Runtime != 136 || it.Rating != 8.2 || len(it.Genres) != 1 || len(it.Cast) != 2 || it.Collection != "The Matrix Collection" || it.CollectionID != 2344 || it.Mod.IsZero() || it.IMDB != "tt0133093" {
				t.Errorf("matrix not filled: %+v", it)
			}
		case it.TMDB == 1399:
			eps++
			if it.Kind != "episode" || it.Title != "Game of Thrones" {
				t.Errorf("episode not filled: %+v", it)
			}
		default:
			unknown++
			if it.TMDB != 0 {
				t.Errorf("unmatched item has TMDB id: %+v", it)
			}
		}
	}
	if matrix != 1 || eps != 2 || unknown != 1 {
		t.Errorf("matrix=%d eps=%d unknown=%d", matrix, eps, unknown)
	}
	for _, id := range []string{"603.jpg", "1399.jpg"} {
		if _, err := os.Stat(filepath.Join(posterDir, id)); err != nil {
			t.Errorf("poster %s not cached: %v", id, err)
		}
	}
	// Without a key: no match, no poster.
	items2 := BuildLibrary(context.Background(), []Root{{Abs: root, API: "/srv/strata/videos"}}, &Client{}, posterDir, 6, nil)
	for _, it := range items2 {
		if it.TMDB != 0 {
			t.Errorf("no key but matched: %+v", it)
		}
	}
}

func TestBuildLibraryNestedShares(t *testing.T) {
	vol := t.TempDir()
	os.MkdirAll(filepath.Join(vol, "Videos", "Films"), 0o755)
	os.WriteFile(filepath.Join(vol, "Videos", "Films", "Heat (1995).mkv"), []byte("x"), 0o644)
	roots := []Root{{Abs: vol, API: vol}, {Abs: filepath.Join(vol, "Videos"), API: filepath.Join(vol, "Videos")}}
	items := BuildLibrary(context.Background(), roots, nil, t.TempDir(), 6, func(string, ...any) {})
	if len(items) != 1 {
		t.Fatalf("items: %+v", items)
	}
}

func TestAgeOf(t *testing.T) {
	for _, c := range []struct {
		certs map[string]string
		age   int
		rated bool
	}{
		{map[string]string{"US": "PG-13"}, 13, true},
		{map[string]string{"FR": "12", "US": "R"}, 12, true},
		{map[string]string{"DE": "16"}, 16, true},
		{map[string]string{"GB": "12A"}, 12, true},
		{map[string]string{"US": "TV-MA"}, 17, true},
		{map[string]string{"FR": "U"}, 0, true},
		{map[string]string{"US": "NR"}, 0, false},
		{map[string]string{}, 0, false},
	} {
		if a, r := AgeOf(c.certs); a != c.age || r != c.rated {
			t.Errorf("%v: %d %v", c.certs, a, r)
		}
	}
}
