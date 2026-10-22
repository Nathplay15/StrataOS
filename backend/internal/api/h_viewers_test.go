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

// viewerRequest is a request as the built-in PDF viewer (pdf.js) sends it:
// fetch() from the page, session cookie, X-Strata header, Range chunks. The
// browser's own PDF viewer in an iframe sent requests without the
// SameSite=Strict cookie and showed {"error":"authentication required"}.
func viewerRequest(url, cookie string) *http.Request {
	r := httptest.NewRequest("GET", url, nil)
	r.Host = "nas.local:7070"
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	r.Header.Set("X-Strata", "1")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	r.Header.Set("Range", "bytes=0-1023")
	return r
}

func TestPDFViewerRequestAuthenticated(t *testing.T) {
	s := newTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "alice", Role: "user"})
		return nil
	})
	root := t.TempDir()
	pdf := append([]byte("%PDF-1.7\n"), make([]byte, 4096)...)
	if err := os.WriteFile(filepath.Join(root, "doc.pdf"), pdf, 0o600); err != nil {
		t.Fatal(err)
	}
	// The download handler, on a temporary root instead of /srv/strata.
	s.userRaw("GET /api/files/download", func(w http.ResponseWriter, r *http.Request) {
		s.serveTarget(w, r, target{root: root, rel: "doc.pdf", abs: "/srv/strata/main/doc.pdf", acc: access{read: true}}, nil)
	})
	cookie := s.sessions.create("alice", "user", "127.0.0.1", time.Hour)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, viewerRequest("/api/files/download?path=/srv/strata/main/doc.pdf&inline=1", cookie))
	if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(rec.Body.String(), "%PDF") {
		t.Fatalf("viewer request: %d %q %.80s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") != `inline; filename=doc.pdf` {
		t.Errorf("disposition %q", rec.Header().Get("Content-Disposition"))
	}
	// Without the cookie: the error the user saw.
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, viewerRequest("/api/files/download?path=/srv/strata/main/doc.pdf&inline=1", ""))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "authentication required") {
		t.Fatalf("cookieless request: %d %s", rec.Code, rec.Body.String())
	}
}

// The viewer endpoints use the same session: never 401 with a valid cookie.
func TestViewerRoutesUseTheSession(t *testing.T) {
	s := newTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "alice", Role: "user"})
		return nil
	})
	s.viewerRoutes()
	cookie := s.sessions.create("alice", "user", "127.0.0.1", time.Hour)
	for _, u := range []string{"/api/files/docpreview", "/api/files/play", "/api/files/image", "/api/files/info", "/api/files/archive", "/api/files/probe", "/api/files/subtitle"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, viewerRequest(u+"?path=/srv/strata/nowhere/x", cookie))
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s: 401 with a valid session", u)
		}
		rec = httptest.NewRecorder()
		s.ServeHTTP(rec, viewerRequest(u+"?path=/srv/strata/nowhere/x", ""))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d without a session", u, rec.Code)
		}
	}
}

func TestArchiveStemAndText(t *testing.T) {
	for in, want := range map[string]string{"photos.tar.gz": "photos", "a.ZIP": "a", "b.7z": "b", "c.txt": "c.txt"} {
		if got := archiveStem(in); got != want {
			t.Errorf("archiveStem(%q) = %q", in, got)
		}
	}
	if !looksText([]byte("hello\nwörld")) || !looksText([]byte("caf\xc3")) || looksText([]byte("a\x00b")) || looksText([]byte{0xff, 0xfe, 0x41, 0x42, 0x43, 0x44}) {
		t.Error("looksText")
	}
}
