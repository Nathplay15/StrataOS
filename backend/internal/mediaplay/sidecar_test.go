package mediaplay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSidecars(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Movie.2024.mkv")
	for _, n := range []string{"Movie.2024.srt", "Movie.2024.vtt", "Movie.2024.fr.srt", "Movie.2024.en-US.vtt", "Other.srt", "Movie.2024.txt", "notes.srt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	subs := Sidecars(video)
	if len(subs) != 4 {
		t.Fatalf("want 4 sidecars, got %d: %+v", len(subs), subs)
	}
	// Stable order by name: Movie.2024.en-US.vtt, Movie.2024.fr.srt, Movie.2024.srt, Movie.2024.vtt
	want := []struct {
		codec, lang string
	}{
		{"webvtt", "en-US"},
		{"subrip", "fr"},
		{"subrip", ""},
		{"webvtt", ""},
	}
	for i, w := range want {
		if subs[i].Codec != w.codec || subs[i].Lang != w.lang || subs[i].Index != SidecarBase+i || subs[i].Type != "subtitle" {
			t.Errorf("subs[%d] = %+v, want codec=%s lang=%s idx=%d", i, subs[i], w.codec, w.lang, SidecarBase+i)
		}
	}
	// Roundtrip: index -> path.
	p, ok := SidecarName(video, SidecarBase+1)
	if !ok || p != "Movie.2024.fr.srt" {
		t.Errorf("SidecarName: %q %v", p, ok)
	}
	if _, ok := SidecarName(video, SidecarBase-1); ok {
		t.Error("container index must not resolve as sidecar")
	}
	if _, ok := SidecarName(video, SidecarBase+99); ok {
		t.Error("out of range index must not resolve")
	}
	// No directory: no sidecars, no panic.
	if subs := Sidecars(filepath.Join(dir, "missing", "x.mkv")); len(subs) != 0 {
		t.Errorf("missing dir: %+v", subs)
	}
}

func TestSRTtoVTT(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,500\nHello, world\n\n2\n00:01:03,250 --> 00:01:04,000\nSecond line\n"
	vtt := string(SRTtoVTT([]byte(srt)))
	if !strings.HasPrefix(vtt, "WEBVTT\n\n") {
		t.Errorf("missing header: %q", vtt[:20])
	}
	if !strings.Contains(vtt, "00:00:01.000 --> 00:00:02.500") || !strings.Contains(vtt, "00:01:03.250 --> 00:01:04.000") {
		t.Errorf("timestamps not converted: %q", vtt)
	}
	if strings.Contains(vtt, ",000 -->") {
		t.Errorf("commas left: %q", vtt)
	}
	if !strings.Contains(vtt, "Hello, world") || !strings.Contains(vtt, "Second line") {
		t.Errorf("text lost: %q", vtt)
	}
	// CRLF input.
	crlf := strings.ReplaceAll(srt, "\n", "\r\n")
	if got := string(SRTtoVTT([]byte(crlf))); !strings.Contains(got, "00:00:01.000 -->") || strings.Contains(got, "\r") {
		t.Errorf("crlf: %q", got)
	}
}
