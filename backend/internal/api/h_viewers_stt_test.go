package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
	"strataos/internal/stt"
)

// sttTestServer builds a server with one share at /srv/strata/main holding a
// video and its subtitle sidecars (skipped when /srv cannot be created).
func sttTestServer(t *testing.T) (*Server, *jarClient) {
	t.Helper()
	s := newTestServer(t)
	s.viewerRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		st.Shares = append(st.Shares, &store.Share{Name: "main", Path: "/srv/strata/main", Permissions: map[string]string{}})
		return nil
	})
	root := "/srv/strata/main"
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Skip("cannot create /srv/strata in the test environment")
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	os.WriteFile(filepath.Join(root, "movie.mkv"), []byte("video"), 0o644)
	os.WriteFile(filepath.Join(root, "movie.fr.srt"), []byte("1\n00:00:01,000 --> 00:00:02,500\nBonjour\n"), 0o644)
	os.WriteFile(filepath.Join(root, "movie.vtt"), []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.500\nHello\n"), 0o644)
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}
	return s, admin
}

func TestSubtitleSidecarServed(t *testing.T) {
	_, admin := sttTestServer(t)
	// Sidecars are sorted by name: movie.fr.srt (1000), movie.vtt (1001).
	w := admin.req("GET", "/api/files/subtitle?path=/srv/strata/main/movie.mkv&index=1000", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/vtt; charset=utf-8" {
		t.Fatalf("sidecar srt: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "WEBVTT") || !strings.Contains(body, "00:00:01.000 --> 00:00:02.500") || !strings.Contains(body, "Bonjour") {
		t.Fatalf("srt not converted to vtt: %q", body)
	}
	// .vtt sidecar served as-is.
	w = admin.req("GET", "/api/files/subtitle?path=/srv/strata/main/movie.mkv&index=1001", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Hello") {
		t.Fatalf("sidecar vtt: %d %s", w.Code, w.Body.String())
	}
	// Out-of-range sidecar index: 404. Container index without ffmpeg: 400.
	if w := admin.req("GET", "/api/files/subtitle?path=/srv/strata/main/movie.mkv&index=1099", nil); w.Code != 404 {
		t.Errorf("bad sidecar index: %d", w.Code)
	}
	if w := admin.req("GET", "/api/files/subtitle?path=/srv/strata/main/movie.mkv&index=0", nil); w.Code == 200 {
		t.Errorf("container track without ffmpeg should not be 200: %d", w.Code)
	}
	// Negative index: 400.
	if w := admin.req("GET", "/api/files/subtitle?path=/srv/strata/main/movie.mkv&index=-1", nil); w.Code != 400 {
		t.Errorf("negative index: %d", w.Code)
	}
}

func TestTranscribeRoute(t *testing.T) {
	s, admin := sttTestServer(t)
	// Without the binaries: 409.
	if w := admin.req("POST", "/api/files/transcribe", map[string]string{"path": "/srv/strata/main/movie.mkv"}); w.Code != 409 {
		t.Fatalf("no whisper: %d %s", w.Code, w.Body.String())
	}
	// Fake the binaries.
	oldInst, oldRun := stt.Installed, stt.Run
	t.Cleanup(func() { stt.Installed, stt.Run = oldInst, oldRun })
	stt.Installed = func() bool { return true }
	// A planted link where the transcript goes is replaced, never followed.
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep"), 0o644)
	os.Symlink(victim, "/srv/strata/main/movie.transcript.txt")
	stt.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == stt.Binary {
			for i, a := range args {
				if a == "-of" && i+1 < len(args) {
					prefix := args[i+1]
					os.WriteFile(prefix+".srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644)
					os.WriteFile(prefix+".txt", []byte("hi there"), 0o644)
				}
			}
		}
		return []byte("ok"), nil
	}
	w := admin.req("POST", "/api/files/transcribe", map[string]string{"path": "/srv/strata/main/movie.mkv"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"text":"hi there"`) {
		t.Fatalf("transcribe: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat("/srv/strata/main/movie.srt"); err != nil {
		t.Errorf("srt sidecar not written: %v", err)
	}
	if st, err := os.Lstat("/srv/strata/main/movie.transcript.txt"); err != nil || !st.Mode().IsRegular() {
		t.Errorf("transcript not written as a regular file: %v", err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("transcript written through a planted link")
	}
	// Not a media file: 400.
	if w := admin.req("POST", "/api/files/transcribe", map[string]string{"path": "/srv/strata/main/movie.fr.srt"}); w.Code != 400 {
		t.Errorf("srt file: %d", w.Code)
	}
	// Unknown path: 404/403.
	if w := admin.req("POST", "/api/files/transcribe", map[string]string{"path": "/srv/strata/main/nope.mkv"}); w.Code == 200 {
		t.Errorf("missing file: %d", w.Code)
	}
	_ = s
}
