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

	mu     sync.Mutex
	detect func() HW // the daemon's choice of GPU (nil: probe renderD128)
	hwSet  bool
	hw     HW
	st     HWStatus
	// OnFallback is told when a GPU conversion failed and the CPU took
	// over; disabled when the GPU is off until Configure is called again.
	OnFallback func(reason string, disabled bool)
	// exec runs ffmpeg (tests replace it).
	exec func(ctx context.Context, f *os.File, args []string, out io.Writer) error
}

// HWStatus says how videos were converted since the daemon started.
type HWStatus struct {
	Encoder string `json:"encoder"` // vaapi, nvenc, "" (CPU)
	Device  string `json:"device,omitempty"`
	// Disabled: the GPU failed in a way that will not get better by itself
	// (driver, device, permissions): the CPU converts until Configure.
	Disabled   bool      `json:"disabled,omitempty"`
	GPUStreams int       `json:"gpuStreams"`
	CPUStreams int       `json:"cpuStreams"`
	Fallbacks  int       `json:"fallbacks"`
	LastReason string    `json:"lastReason,omitempty"`
	LastAt     time.Time `json:"lastAt,omitzero"`
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

// Configure sets how the GPU is chosen (called again after a change of
// settings or drivers): the choice is made again on the next conversion
// and a GPU turned off by a failure is tried again.
func (p *Player) Configure(detect func() HW) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.detect, p.hwSet, p.st.Disabled = detect, false, false
}

// HW returns the hardware encoder in use (detected once).
func (p *Player) HW() HW {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.hwSet {
		p.hwSet = true
		if p.detect != nil {
			p.hw = p.detect()
		} else {
			p.hw = probeVAAPI()
		}
	}
	if p.st.Disabled {
		return HW{}
	}
	return p.hw
}

// Status reports the encoder chosen and the fallbacks to the CPU.
func (p *Player) Status() HWStatus {
	hw := p.HW()
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.st
	st.Encoder, st.Device = hw.Encoder(), hw.Device
	if p.st.Disabled {
		st.Encoder, st.Device = p.hw.Encoder(), p.hw.Device
	}
	return st
}

// probeVAAPI is the detection without the daemon: the first render node
// when ffmpeg has the VA-API encoder.
func probeVAAPI() HW {
	dev := "/dev/dri/renderD128"
	if _, err := os.Stat(dev); err != nil {
		return HW{}
	}
	out, err := sys.Run("ffmpeg", "-hide_banner", "-encoders")
	if err == nil && strings.Contains(out, "h264_vaapi") {
		return HW{VAAPI: true, Device: dev}
	}
	return HW{}
}

// DeviceFailure tells whether ffmpeg's error means the GPU cannot work at
// all (no driver, no device, no access) rather than refusing one file.
func DeviceFailure(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{"failed to initialise vaapi", "no va display", "device creation failed", "vainitialize failed",
		"failed to open the drm device", "permission denied", "cannot load libcuda", "cannot load libnvidia-encode",
		"no capable devices found", "driver does not support the required nvenc api", "openencodesessionex failed", "unknown encoder"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// fellBack records a GPU conversion that failed.
func (p *Player) fellBack(reason string) {
	p.mu.Lock()
	p.st.Fallbacks++
	p.st.LastReason, p.st.LastAt = reason, time.Now()
	off := DeviceFailure(reason) && !p.st.Disabled
	if off {
		p.st.Disabled = true
	}
	first := p.st.Fallbacks == 1
	cb := p.OnFallback
	p.mu.Unlock()
	slog.Warn("GPU video conversion failed, using the CPU", "err", reason, "gpuOff", off)
	if cb != nil && (first || off) {
		cb(reason, off)
	}
}

func (p *Player) counted(gpu bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gpu {
		p.st.GPUStreams++
	} else {
		p.st.CPUStreams++
	}
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
	gpu := hw.On() && !plan.CopyVideo && !plan.AudioOnly
	if plan.Mode == Transcode {
		p.counted(gpu)
	}
	err := p.run(r.Context(), f, Args(plan, start, hw), cw)
	if err != nil && gpu && r.Context().Err() == nil {
		// The GPU refused this file, or cannot work at all (then it stays
		// off): the CPU converts it, when nothing reached the client yet.
		p.fellBack(err.Error())
		if cw.n.Load() == 0 {
			p.counted(false)
			err = p.run(r.Context(), f, Args(plan, start, HW{}), cw)
		}
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
	if p.exec != nil {
		return p.exec(ctx, f, args, out)
	}
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

// Active is the number of videos being converted or remuxed now.
func (p *Player) Active() int { return len(p.transcodes) + len(p.remuxes) }
