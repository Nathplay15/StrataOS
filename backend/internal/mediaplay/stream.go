package mediaplay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"strataos/internal/sys"
)

// Installed reports whether ffmpeg and ffprobe are present.
func Installed() bool { return sys.Has("ffmpeg") && sys.Has("ffprobe") }

// ErrBusy: every transcoding slot is taken.
var ErrBusy = errors.New("too many videos are being converted at the same time, try again later")

// command runs a sandboxed ffmpeg/ffprobe reading f as its stdin (the fd:
// protocol keeps it seekable): the unprivileged process never opens the
// path, so it reads exactly the file the caller checked.
func command(ctx context.Context, f *os.File, name string, args ...string) (*exec.Cmd, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = f
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.SysProcAttr = sys.Sandboxed()
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd, nil
}

// Probe describes the tracks of f.
func Probe(ctx context.Context, f *os.File) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd, err := command(ctx, f, "ffprobe", "-v", "error", "-print_format", "json", "-show_format", "-show_streams", "-i", "fd:")
	if err != nil {
		return nil, err
	}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("not a playable media file (%s)", firstLine(msg))
	}
	return ParseProbe(out.Bytes())
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Player runs the ffmpeg processes of on-the-fly playback within limits.
type Player struct {
	transcodes chan struct{} // re-encoding slots (CPU/GPU heavy)
	remuxes    chan struct{} // stream copy slots (disk bound)
	hwOnce     sync.Once
	hw         HW
	hwBroken   atomic.Bool // VAAPI failed once: software from now on
}

// NewPlayer allows maxTranscodes re-encodings and 3× as many stream copies.
func NewPlayer(maxTranscodes int) *Player {
	if maxTranscodes < 1 {
		maxTranscodes = 1
	}
	return &Player{transcodes: make(chan struct{}, maxTranscodes), remuxes: make(chan struct{}, 3*maxTranscodes)}
}

// Default is the player shared by the file manager and the recordings.
var Default = NewPlayer(2)

// HW returns the hardware encoder found on this machine (detected once).
func (p *Player) HW() HW {
	p.hwOnce.Do(func() {
		dev := "/dev/dri/renderD128"
		if _, err := os.Stat(dev); err != nil {
			return
		}
		out, err := sys.Run("ffmpeg", "-hide_banner", "-encoders")
		if err == nil && strings.Contains(out, "h264_vaapi") {
			p.hw = HW{VAAPI: true, Device: dev}
		}
	})
	if p.hwBroken.Load() {
		return HW{}
	}
	return p.hw
}

// countingWriter remembers whether anything reached the client.
type countingWriter struct {
	w io.Writer
	n atomic.Int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n.Add(int64(n))
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

// Stream writes plan as fragmented MP4 from start seconds until the end of the
// file or until the client goes away (the request context kills ffmpeg).
func (p *Player) Stream(w http.ResponseWriter, r *http.Request, f *os.File, plan Plan, start float64) error {
	if plan.Mode == Native {
		return errors.New("native files are served as they are")
	}
	slots := p.remuxes
	if plan.Mode == Transcode {
		slots = p.transcodes
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-time.After(3 * time.Second):
		return ErrBusy
	case <-r.Context().Done():
		return r.Context().Err()
	}
	w.Header().Set("Content-Type", plan.Type)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	cw := &countingWriter{w: w}
	hw := p.HW()
	err := p.run(r.Context(), f, Args(plan, start, hw), cw)
	if err != nil && hw.VAAPI && cw.n.Load() == 0 && r.Context().Err() == nil && !plan.CopyVideo && !plan.AudioOnly {
		// The GPU refused this file (or the driver is missing): software from now on.
		slog.Warn("VAAPI transcoding failed, using the CPU", "err", err)
		p.hwBroken.Store(true)
		err = p.run(r.Context(), f, Args(plan, start, HW{}), cw)
	}
	if err != nil && r.Context().Err() != nil {
		return nil // the player moved on (seek, closed tab)
	}
	if err != nil && cw.n.Load() > 0 {
		slog.Warn("media stream ended with an error", "err", err)
		return nil // headers are gone: nothing else to tell the client
	}
	return err
}

func (p *Player) run(ctx context.Context, f *os.File, args []string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	cmd, err := command(ctx, f, "ffmpeg", args...)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &limited{b: &stderr, max: 4096}
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("ffmpeg: %s", firstLine(msg))
		}
		return err
	}
	return nil
}

type limited struct {
	b   *bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

// Subtitles converts subtitle track index of f to WebVTT.
func Subtitles(ctx context.Context, f *os.File, index int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd, err := command(ctx, f, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-i", "fd:", "-map", "0:"+strconv.Itoa(index), "-c:s", "webvtt", "-f", "webvtt", "pipe:1")
	if err != nil {
		return nil, err
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &limited{b: &stderr, max: 4096}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("subtitles: %s", firstLine(strings.TrimSpace(stderr.String()+" "+err.Error())))
	}
	return out.Bytes(), nil
}

var vttTime = regexp.MustCompile(`^((?:\d+:)?\d{2}:\d{2}\.\d{3}) --> ((?:\d+:)?\d{2}:\d{2}\.\d{3})(.*)$`)

func parseVTTTime(s string) float64 {
	parts := strings.Split(s, ":")
	t := 0.0
	for _, p := range parts {
		v, _ := strconv.ParseFloat(p, 64)
		t = t*60 + v
	}
	return t
}

func formatVTTTime(t float64) string {
	ms := int64(t*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// ShiftVTT moves every cue offset seconds earlier, for a stream started at
// offset; cues that ended before it are dropped.
func ShiftVTT(vtt []byte, offset float64) []byte {
	if offset <= 0 {
		return vtt
	}
	blocks := strings.Split(strings.ReplaceAll(string(vtt), "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(blocks))
	for i, b := range blocks {
		lines := strings.Split(b, "\n")
		keep := true
		for j, l := range lines {
			m := vttTime.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			from, to := parseVTTTime(m[1])-offset, parseVTTTime(m[2])-offset
			if to <= 0 {
				keep = false
				break
			}
			lines[j] = formatVTTTime(max(from, 0)) + " --> " + formatVTTTime(to) + m[3]
			break
		}
		if keep || i == 0 {
			out = append(out, strings.Join(lines, "\n"))
		}
	}
	return []byte(strings.Join(out, "\n\n"))
}

// CapsFrom reads the codecs the browser announced (hevc=1, av1=1).
func CapsFrom(r *http.Request) Caps {
	q := r.URL.Query()
	return Caps{HEVC: q.Get("hevc") == "1", AV1: q.Get("av1") == "1"}
}

// HeightFrom reads the picture height asked for (height=720…, 0 when none).
func HeightFrom(r *http.Request) int {
	h, _ := strconv.Atoi(r.URL.Query().Get("height"))
	return h
}

// StartFrom reads the start time in seconds (start=…).
func StartFrom(r *http.Request) float64 {
	v, err := strconv.ParseFloat(r.URL.Query().Get("start"), 64)
	if err != nil || v < 0 || v > 1e6 {
		return 0
	}
	return v
}

// ServeFile plays f in the browser: the file itself when it can (Range
// requests, seeking by the browser), otherwise a remux or a transcode from
// start= (seeking restarts the request). Query: start, audio (track index),
// hevc, av1, height (a lower transcoded quality). name gives the extension;
// the caller checked access to f.
func ServeFile(w http.ResponseWriter, r *http.Request, f *os.File, name string) error {
	info, err := Probe(r.Context(), f)
	if err != nil {
		return err
	}
	audio := -1
	if v := r.URL.Query().Get("audio"); v != "" {
		if audio, err = strconv.Atoi(v); err != nil {
			return errors.New("invalid audio track")
		}
	}
	plan, err := Decide(info, name, CapsFrom(r), audio)
	if err != nil {
		return err
	}
	plan = WithHeight(plan, HeightFrom(r))
	if plan.Mode == Native {
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		w.Header().Set("Content-Type", plan.Type)
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		http.ServeContent(w, r, filepath.Base(name), st.ModTime(), f)
		return nil
	}
	return Default.Stream(w, r, f, plan, StartFrom(r))
}

// ServePath is ServeFile for a path the caller trusts (recordings).
func ServePath(w http.ResponseWriter, r *http.Request, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return ServeFile(w, r, f, path)
}
