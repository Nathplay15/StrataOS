package stt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	a := ExtractAudioArgs("/v/movie.mkv", "/w/audio.wav")
	joined := strings.Join(a, " ")
	for _, want := range []string{"-i /v/movie.mkv", "-vn", "-ac 1", "-ar 16000", "-f wav /w/audio.wav"} {
		if !strings.Contains(joined, want) {
			t.Errorf("extract args missing %q: %s", want, joined)
		}
	}
	w := WhisperArgs("/m/ggml-base.bin", "/w/audio.wav", "/v/movie")
	joined = strings.Join(w, " ")
	for _, want := range []string{"-m /m/ggml-base.bin", "-f /w/audio.wav", "-osrt", "-otxt", "-of /v/movie"} {
		if !strings.Contains(joined, want) {
			t.Errorf("whisper args missing %q: %s", want, joined)
		}
	}
}

func TestIsMedia(t *testing.T) {
	for _, ok := range []string{".mkv", ".MP4", ".mp3", ".WAV", ".flac"} {
		if !IsMedia(ok) {
			t.Errorf("%s should be media", ok)
		}
	}
	for _, bad := range []string{".txt", ".jpg", "", ".srt"} {
		if IsMedia(bad) {
			t.Errorf("%s should not be media", bad)
		}
	}
}

func TestSidecarNames(t *testing.T) {
	srt, txt := SidecarNames("/share/videos/Movie.2024.mkv")
	if srt != "/share/videos/Movie.2024.srt" || txt != "/share/videos/Movie.2024.transcript.txt" {
		t.Errorf("sidecar names: %q %q", srt, txt)
	}
}

func TestTranscribe(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "voice.m4a")
	if err := os.WriteFile(media, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	old := Run
	defer func() { Run = old }()
	var calls [][]string
	Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "ffmpeg" {
			// Find the output path (last arg) and fake the WAV.
			out := args[len(args)-1]
			if err := os.WriteFile(out, []byte("wav"), 0o644); err != nil {
				return nil, err
			}
			return []byte("ok"), nil
		}
		// whisper: -of prefix -> write prefix.srt and prefix.txt
		for i, a := range args {
			if a == "-of" && i+1 < len(args) {
				prefix := args[i+1]
				if err := os.WriteFile(prefix+".srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(prefix+".txt", []byte("hi"), 0o644); err != nil {
					return nil, err
				}
			}
		}
		return []byte("done"), nil
	}
	// A planted link where whisper wrote before (next to the media) is never touched.
	victim := filepath.Join(dir, "victim")
	os.WriteFile(victim, []byte("keep"), 0o644)
	os.Symlink(victim, filepath.Join(dir, "voice.srt"))
	os.Symlink(victim, filepath.Join(dir, "voice.txt"))
	srt, txt, err := Transcribe(context.Background(), media, work, "")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if len(calls) != 2 || calls[0][0] != "ffmpeg" || calls[1][0] != Binary || calls[1][2] != ModelPath {
		t.Fatalf("calls: %+v", calls)
	}
	if !strings.Contains(string(srt), "00:00:01,000") || string(txt) != "hi" {
		t.Errorf("outputs: %q %q", srt, txt)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("victim written: %q", b)
	}
	if ents, _ := os.ReadDir(work); len(ents) != 0 {
		t.Errorf("work folder not cleaned: %v", ents)
	}
}

func TestTranscribeFailure(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "v.mp4")
	os.WriteFile(media, []byte("x"), 0o644)
	old := Run
	defer func() { Run = old }()
	Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("boom: no model"), &Error{"exit 1"}
	}
	_, _, err := Transcribe(context.Background(), media, filepath.Join(dir, "w"), "")
	if err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("error should carry the tail: %v", err)
	}
}
