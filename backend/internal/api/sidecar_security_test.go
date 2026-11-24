package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strataos/internal/mediaplay"
)

func TestSidecarReadRefusesLinksOut(t *testing.T) {
	share := t.TempDir()
	secret := filepath.Join(t.TempDir(), "shadow")
	os.WriteFile(secret, []byte("root:$6$secret"), 0o600)
	os.MkdirAll(filepath.Join(share, "films"), 0o755)
	os.WriteFile(filepath.Join(share, "films", "m.mkv"), []byte("v"), 0o644)
	os.Symlink(secret, filepath.Join(share, "films", "m.en.vtt"))
	os.Symlink("/dev/zero", filepath.Join(share, "films", "m.fr.vtt"))
	os.WriteFile(filepath.Join(share, "films", "m.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644)
	tg := target{root: share, rel: "films/m.mkv", abs: filepath.Join(share, "films", "m.mkv"), acc: access{read: true}}
	subs := mediaplay.Sidecars(tg.abs)
	if len(subs) != 3 {
		t.Fatalf("sidecars: %+v", subs)
	}
	for i, sub := range subs {
		b, err := readSidecar(tg, mediaplay.SidecarBase+i)
		if sub.Title == "m.srt" {
			if err != nil || !strings.Contains(string(b), "00:00:01.000") {
				t.Errorf("m.srt: %q %v", b, err)
			}
			continue
		}
		if err == nil || strings.Contains(string(b), "secret") {
			t.Errorf("%s read through a link out of the share: %q", sub.Title, b)
		}
	}
}

func TestSidecarWriteRefusesPlantedLink(t *testing.T) {
	s := newTestServer(t)
	share := t.TempDir()
	victim := filepath.Join(t.TempDir(), "passwd")
	os.WriteFile(victim, []byte("root:x:0:0"), 0o644)
	os.Symlink(victim, filepath.Join(share, "m.en.srt"))
	tg := target{root: share, rel: "m.mkv", abs: filepath.Join(share, "m.mkv"), acc: access{read: true, write: true}}
	r := httptest.NewRequest("POST", "/api/files/subtitles/fetch", nil)
	if err := s.writeSidecar(r, tg, "m.en.srt", []byte("subs")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "root:x:0:0" {
		t.Fatalf("victim written: %q", b)
	}
	if st, _ := os.Lstat(filepath.Join(share, "m.en.srt")); !st.Mode().IsRegular() {
		t.Fatal("sidecar should now be a regular file")
	}
	os.Symlink(filepath.Dir(victim), filepath.Join(share, "out"))
	if err := s.writeSidecar(r, tg, "out/x.srt", []byte("x")); err == nil {
		t.Fatal("write through an escaping folder accepted")
	}
}
