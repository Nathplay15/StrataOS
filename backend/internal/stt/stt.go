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

// Installed reports whether local transcription is possible (a variable so
// tests can fake the binaries).
var Installed = func() bool { return sys.Has("ffmpeg") && sys.Has("whisper") }

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

// Transcribe extracts the audio of mediaPath and runs the model on it,
// writing <base>.srt and <base>.transcript.txt next to the media. workDir
// holds the temporary WAV. model is the whisper.cpp model name or path
// ("base" by default). One call at a time is expected (the caller serializes).
func Transcribe(ctx context.Context, mediaPath, workDir, model string) error {
	if model == "" {
		model = "base"
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	audio := filepath.Join(workDir, "audio.wav")
	if out, err := Run(ctx, "ffmpeg", ExtractAudioArgs(mediaPath, audio)...); err != nil {
		return &Error{"audio extraction failed: " + tail(out)}
	}
	prefix := strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath))
	if out, err := Run(ctx, "whisper", WhisperArgs(model, audio, prefix)...); err != nil {
		return &Error{"transcription failed: " + tail(out)}
	}
	// whisper writes <prefix>.txt; the transcript sidecar has a distinct name.
	_, txt := SidecarNames(mediaPath)
	if err := os.Rename(prefix+".txt", txt); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
