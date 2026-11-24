// Package stt transcribes the audio of a video or voice note with a local
// speech-to-text model (whisper.cpp) when its binary is installed, like
// ffmpeg for the recorders. It writes the result next to the media as a
// subtitle file (movie.srt) and a text transcript (movie.transcript.txt):
// the player picks the subtitles up as a sidecar track, and the transcript
// is an ordinary text file the file search reads.
package stt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Run executes one command and returns its combined output; replaced in
// tests.
var Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 5 * time.Second
	return cmd.CombinedOutput()
}

// ModelPath is the whisper.cpp model StrataOS uses. Nothing installs it:
// the feature stays hidden until an administrator puts whisper.cpp
// ("whisper-cli") and this model on the NAS (see the user guide).
var ModelPath = "/var/lib/strata/stt/ggml-base.bin"

// Binary is the whisper.cpp command-line program.
var Binary = "whisper-cli"

// Installed reports whether ffmpeg, whisper.cpp and its model are present.
var Installed = func() bool {
	if !sys.Has("ffmpeg") || !sys.Has(Binary) {
		return false
	}
	st, err := os.Stat(ModelPath)
	return err == nil && st.Mode().IsRegular() && st.Size() > 1<<20
}

// ExtractAudioArgs builds the ffmpeg command that turns a media file into a
// 16 kHz mono WAV for the model.
func ExtractAudioArgs(in, out string) []string {
	return []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-i", in, "-vn", "-ac", "1", "-ar", "16000", "-f", "wav", out}
}

// WhisperArgs builds the whisper.cpp command: it writes <prefix>.srt and
// <prefix>.txt next to the audio.
func WhisperArgs(model, audio, prefix string) []string {
	return []string{"-m", model, "-f", audio, "-osrt", "-otxt", "-of", prefix, "-np"}
}

// SidecarNames are the files Transcribe writes next to mediaPath.
func SidecarNames(mediaPath string) (srt, txt string) {
	base := strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath))
	return base + ".srt", base + ".transcript.txt"
}

// Transcribe extracts the audio of mediaPath and transcribes it in a new
// private folder under workDir; it returns the SubRip and text outputs. The
// caller writes them next to the media itself (through the share's root).
func Transcribe(ctx context.Context, mediaPath, workDir, model string) (srt, txt []byte, err error) {
	if model == "" {
		model = ModelPath
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp(workDir, "job-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	audio := filepath.Join(dir, "audio.wav")
	if out, err := Run(ctx, "ffmpeg", ExtractAudioArgs(mediaPath, audio)...); err != nil {
		return nil, nil, &Error{"audio extraction failed: " + tail(out)}
	}
	prefix := filepath.Join(dir, "out")
	if out, err := Run(ctx, Binary, WhisperArgs(model, audio, prefix)...); err != nil {
		return nil, nil, &Error{"transcription failed: " + tail(out)}
	}
	if srt, err = os.ReadFile(prefix + ".srt"); err != nil {
		return nil, nil, &Error{"transcription failed: no subtitles written"}
	}
	txt, _ = os.ReadFile(prefix + ".txt")
	return srt, txt, nil
}

var mediaExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".avi": true, ".webm": true,
	".ts": true, ".m2ts": true, ".wmv": true, ".mpg": true, ".mpeg": true, ".flv": true, ".3gp": true,
	".mp3": true, ".m4a": true, ".aac": true, ".flac": true, ".ogg": true, ".oga": true, ".opus": true, ".wav": true, ".wma": true,
}

// IsMedia reports whether the extension is a video or audio file.
func IsMedia(ext string) bool { return mediaExts[strings.ToLower(ext)] }

// Error is a user-readable transcription failure.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[len(s)-300:]
	}
	return s
}
