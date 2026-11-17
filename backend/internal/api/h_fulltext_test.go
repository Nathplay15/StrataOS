package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/ftsearch"
	"strataos/internal/store"
)

// fulltextServer builds a server whose index holds two documents: one in the
// share "main" (bob may read it), one outside (only administrators).
func fulltextServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	s.fulltextRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users,
			&store.User{Username: "admin", Role: "admin", PasswordHash: hash},
			&store.User{Username: "bob", PasswordHash: hash})
		st.Shares = append(st.Shares, &store.Share{Name: "main", Path: "/srv/strata/main", Permissions: map[string]string{"bob": "rw"}})
		return nil
	})
	ftsearch.Seed(map[string]ftsearch.SeedDoc{
		"/srv/strata/main/notes.md":   {Name: "notes.md", Text: "Le quota btrfs est activé sur le volume principal."},
		"/srv/strata/private/plan.md": {Name: "plan.md", Text: "Le plan secret du projet mentionne le quota btrfs."},
	})
	return s
}

func TestFullTextSearchPermissionFilter(t *testing.T) {
	s := fulltextServer(t)
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}
	bob := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("bob", "", "192.0.2.9", time.Hour)}}

	w := admin.req("GET", "/api/files/fulltext?q=quota+btrfs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("admin search: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Hits []fulltextHit `json:"hits"`
		Docs int           `json:"docs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 2 || out.Docs != 2 {
		t.Fatalf("admin hits: %+v", out)
	}

	// Bob only sees the share he may read; snippets are empty here because
	// the indexed files do not exist on disk (the next test covers them).
	w = bob.req("GET", "/api/files/fulltext?q=quota+btrfs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("bob search: %d %s", w.Code, w.Body.String())
	}
	out = struct {
		Hits []fulltextHit `json:"hits"`
		Docs int           `json:"docs"`
	}{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 1 || out.Hits[0].Path != "/srv/strata/main/notes.md" {
		t.Fatalf("bob hits: %+v", out)
	}

	// Too short a query is refused.
	if w = bob.req("GET", "/api/files/fulltext?q=q", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("short query: %d", w.Code)
	}
	// Reindex is for administrators.
	if w = bob.req("POST", "/api/files/fulltext/reindex", nil); w.Code != http.StatusForbidden {
		t.Fatalf("bob reindex: %d", w.Code)
	}
	if w = admin.req("POST", "/api/files/fulltext/reindex", nil); w.Code != http.StatusOK {
		t.Fatalf("admin reindex: %d %s", w.Code, w.Body.String())
	}
}

func TestFullTextSearchRealFiles(t *testing.T) {
	s := newTestServer(t)
	s.fulltextRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin", PasswordHash: hash})
		st.Shares = append(st.Shares, &store.Share{Name: "docs", Path: "/srv/strata/docs", Permissions: map[string]string{}})
		return nil
	})
	root := "/srv/strata/docs"
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Skip("cannot create /srv/strata in the test environment")
	}
	defer os.Remove(root)
	if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte("Pour installer l'application, ouvrez le centre de paquets."), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ftsearch.Default.Scan(t.Context(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}
	w := admin.req("GET", "/api/files/fulltext?q=centre+paquets", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("search: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Hits []fulltextHit `json:"hits"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 1 || out.Hits[0].Name != "guide.md" {
		t.Fatalf("hits: %+v", out)
	}
	if !strings.Contains(out.Hits[0].Snippet, "centre de paquets") {
		t.Fatalf("snippet: %q", out.Hits[0].Snippet)
	}
}

// A folder above the user's shares only gives traversal access: searching
// from there must not widen the results to every share (review 0.12.0 #1).
func TestFullTextSearchAncestorPath(t *testing.T) {
	s := fulltextServer(t)
	bob := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("bob", "", "192.0.2.9", time.Hour)}}
	// (The share folder itself does not exist in tests, so locate refuses it:
	// narrowRoots is checked directly below.)
	for _, p := range []string{"/srv/strata", "/srv/strata/"} {
		w := bob.req("GET", "/api/files/fulltext?q=quota+btrfs&path="+p, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body.String())
		}
		var out struct {
			Hits []fulltextHit `json:"hits"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, h := range out.Hits {
			if !strings.HasPrefix(h.Path, "/srv/strata/main/") {
				t.Fatalf("path=%s leaks %s", p, h.Path)
			}
		}
		if len(out.Hits) != 1 {
			t.Fatalf("path=%s: %d hits", p, len(out.Hits))
		}
	}
	roots := []string{"/srv/strata/main", "/srv/strata/media"}
	for p, want := range map[string]string{
		"/srv/strata":             "/srv/strata/main|/srv/strata/media",
		"/srv/strata/main":        "/srv/strata/main",
		"/srv/strata/main/sub":    "/srv/strata/main/sub",
		"/srv/strata/mainly":      "",
		"/srv/strata/private/doc": "",
	} {
		if got := strings.Join(narrowRoots(roots, p), "|"); got != want {
			t.Fatalf("narrowRoots(%s) = %q, want %q", p, got, want)
		}
	}
	// A folder bob cannot reach at all stays refused.
	if w := bob.req("GET", "/api/files/fulltext?q=quota+btrfs&path=/srv/strata/private", nil); w.Code != http.StatusForbidden {
		t.Fatalf("private: %d", w.Code)
	}
}
