package nvr

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"strataos/internal/sys"
)

// RunDir holds the ffconcat files (credentials) and live pictures: tmpfs, root only.
var RunDir = "/run/strata/nvr"

// Installed reports whether ffmpeg is present.
func Installed() bool { return sys.Has("ffmpeg") }

// Recording is what the recorder of one camera needs.
type Recording struct {
	ID             string
	Name           string
	Stream         string // full URL, with the escaped credentials
	Dir            string
	SegmentSeconds int
	Audio          bool
}

// Status of a camera recorder.
type Status struct {
	State    string    `json:"state"` // connecting | recording | offline | stopped
	Since    time.Time `json:"since,omitzero"`
	Error    string    `json:"error,omitempty"` // last ffmpeg message, credentials removed
	Restarts int       `json:"restarts"`
}

// Manager supervises one ffmpeg recorder per camera and the live views.
type Manager struct {
	// OnDown is called once a camera has been unreachable for the alert
	// delay (SetAlertAfter); OnUp when it records again after OnDown.
	OnDown func(id, name string, since time.Time, reason string)
	OnUp   func(id, name string, down time.Duration)

	alertAfter atomic.Int64
	mu         sync.Mutex
	recs       map[string]*recorder
	lives      map[string]*live
}

func NewManager() *Manager {
	m := &Manager{recs: map[string]*recorder{}, lives: map[string]*live{}}
	m.SetAlertAfter(5 * time.Minute)
	return m
}

// SetAlertAfter sets how long a camera must be unreachable to be notified.
func (m *Manager) SetAlertAfter(d time.Duration) { m.alertAfter.Store(int64(d)) }

// camDir is the private runtime folder of a camera.
func camDir(id string) (string, error) {
	d := filepath.Join(RunDir, id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(RunDir, 0o700)
	return d, nil
}

// WriteInput writes the ffconcat file of a stream (root only) and returns its path.
func WriteInput(id, name, stream string) (string, error) {
	d, err := camDir(id)
	if err != nil {
		return "", err
	}
	p := filepath.Join(d, name+".ffconcat")
	return p, sys.WriteFileAtomic(p, []byte(ConcatList(stream)), 0o600)
}

// Sync starts, restarts and stops recorders so that exactly recs run.
func (m *Manager) Sync(recs []Recording) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]Recording{}
	for _, r := range recs {
		want[r.ID] = r
	}
	for id, r := range m.recs {
		if w, ok := want[id]; !ok || w != r.cfg {
			r.stop()
			delete(m.recs, id)
		}
	}
	for id, w := range want {
		if _, ok := m.recs[id]; !ok {
			r := &recorder{m: m, cfg: w, done: make(chan struct{})}
			var ctx context.Context
			ctx, r.cancel = context.WithCancel(context.Background())
			r.status = Status{State: "connecting", Since: time.Now()}
			r.downSince = time.Now()
			m.recs[id] = r
			go r.run(ctx)
		}
	}
}

// Status returns the recorder status of a camera (stopped when none runs).
func (m *Manager) Status(id string) Status {
	m.mu.Lock()
	r := m.recs[id]
	m.mu.Unlock()
	if r == nil {
		return Status{State: "stopped"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Recording reports whether a recorder runs for the camera.
func (m *Manager) Recording(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recs[id] != nil
}

// Forget stops everything about a camera and removes its runtime folder.
func (m *Manager) Forget(id string) {
	m.mu.Lock()
	if r := m.recs[id]; r != nil {
		r.stop()
		delete(m.recs, id)
	}
	if l := m.lives[id]; l != nil {
		l.cancel()
		delete(m.lives, id)
	}
	m.mu.Unlock()
	if id != "" && !strings.ContainsAny(id, "/.") {
		_ = os.RemoveAll(filepath.Join(RunDir, id))
	}
}

type recorder struct {
	m      *Manager
	cfg    Recording
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	status    Status
	proc      *os.Process
	started   time.Time
	lastSeen  string // newest segment name and size
	lastGrow  time.Time
	downSince time.Time
	alerted   bool
}

// stop ends the recorder and waits for ffmpeg to close its segment.
func (r *recorder) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(15 * time.Second):
	}
}

func (r *recorder) set(f func(s *Status)) {
	r.mu.Lock()
	f(&r.status)
	r.mu.Unlock()
}

func (r *recorder) run(ctx context.Context) {
	defer close(r.done)
	go r.watch(ctx)
	backoff := 2 * time.Second
	for {
		start := time.Now()
		err := r.once(ctx)
		if ctx.Err() != nil {
			r.set(func(s *Status) { *s = Status{State: "stopped", Since: time.Now()} })
			return
		}
		if time.Since(start) > 2*time.Minute {
			backoff = 2 * time.Second
		}
		r.markDown(err)
		select {
		case <-ctx.Done():
			r.set(func(s *Status) { *s = Status{State: "stopped", Since: time.Now()} })
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
		r.set(func(s *Status) { s.Restarts++ })
	}
}

// once runs ffmpeg until it exits (stream lost, camera off, stall).
func (r *recorder) once(ctx context.Context) error {
	if err := os.MkdirAll(r.cfg.Dir, 0o755); err != nil {
		return err
	}
	input, err := WriteInput(r.cfg.ID, "main", r.cfg.Stream)
	if err != nil {
		return err
	}
	args := RecordArgs(RecordOpts{Input: input, Dir: r.cfg.Dir, Ext: Ext(r.cfg.Stream), SegmentSeconds: r.cfg.SegmentSeconds, Audio: r.cfg.Audio})
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	// SIGINT lets ffmpeg finish the current segment (an MP4 needs its index).
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.mu.Lock()
	// Growth is measured from what is on disk now (files of a previous run).
	r.proc, r.started, r.lastGrow, r.lastSeen = cmd.Process, time.Now(), time.Now(), newest(r.cfg.Dir)
	r.mu.Unlock()
	last := tail(stderr)
	err = cmd.Wait()
	r.mu.Lock()
	r.proc = nil
	r.mu.Unlock()
	if msg := last(); msg != "" {
		return errors.New(msg)
	}
	if err != nil {
		return err
	}
	return errors.New("the stream ended")
}

// tail keeps the last line ffmpeg wrote, without credentials.
func tail(rd io.Reader) func() string {
	var mu sync.Mutex
	var last string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(rd)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				mu.Lock()
				last = Redact(l)
				mu.Unlock()
			}
		}
	}()
	return func() string {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		mu.Lock()
		defer mu.Unlock()
		if len(last) > 300 {
			return last[:300]
		}
		return last
	}
}

// stallAfter: a recorder whose files stop growing is restarted.
const stallAfter = 90 * time.Second

// watch checks every few seconds that the newest segment grows: that tells a
// working camera from a connected but silent one, which it restarts.
func (r *recorder) watch(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		seen := newest(r.cfg.Dir)
		r.mu.Lock()
		running := r.proc != nil
		grew := running && seen != "" && seen != r.lastSeen
		r.lastSeen = seen
		if grew {
			r.lastGrow = time.Now()
		}
		stalled := running && !grew && time.Since(r.lastGrow) > stallAfter
		proc := r.proc
		r.mu.Unlock()
		if grew {
			r.markUp()
		}
		if stalled {
			slog.Warn("camera stream stalled, restarting", "camera", r.cfg.Name)
			_ = proc.Signal(os.Interrupt)
			go func() {
				time.Sleep(10 * time.Second)
				_ = proc.Kill()
			}()
		}
		r.checkAlert()
	}
}

func newest(dir string) string {
	segs, err := ListSegments(dir, false)
	if err != nil || len(segs) == 0 {
		return ""
	}
	s := segs[len(segs)-1]
	return fmt.Sprintf("%s:%d", s.Name, s.Size)
}

func (r *recorder) markUp() {
	r.mu.Lock()
	wasDown, alerted, since := !r.downSince.IsZero(), r.alerted, r.downSince
	if r.status.State != "recording" {
		r.status = Status{State: "recording", Since: time.Now(), Restarts: r.status.Restarts}
	}
	r.downSince, r.alerted = time.Time{}, false
	r.mu.Unlock()
	if wasDown && alerted && r.m.OnUp != nil {
		r.m.OnUp(r.cfg.ID, r.cfg.Name, time.Since(since))
	}
}

func (r *recorder) markDown(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.downSince.IsZero() {
		r.downSince = time.Now()
	}
	state := "offline"
	if r.status.State == "connecting" {
		state = "connecting"
	}
	if r.status.State != state {
		r.status.Since = time.Now()
	}
	r.status.State = state
	if err != nil {
		r.status.Error = Redact(err.Error())
	}
}

func (r *recorder) checkAlert() {
	r.mu.Lock()
	fire := !r.downSince.IsZero() && !r.alerted && time.Since(r.downSince) >= time.Duration(r.m.alertAfter.Load())
	since, reason := r.downSince, r.status.Error
	if fire {
		r.alerted = true
	}
	r.mu.Unlock()
	if fire && r.m.OnDown != nil {
		r.m.OnDown(r.cfg.ID, r.cfg.Name, since, reason)
	}
}

// ---- Live view ----

// liveIdle: a live view nobody looks at stops after this.
const liveIdle = 30 * time.Second

type live struct {
	cancel context.CancelFunc
	out    string
	mu     sync.Mutex
	seen   time.Time // last request
	ended  time.Time // ffmpeg exited (zero while running)
	err    string
}

// Live returns the live picture of a camera, starting its ffmpeg if needed,
// and waits up to wait for a first picture. stream is the full URL of the
// sub-stream (or of the main stream when sub is false).
func (m *Manager) Live(id, stream string, sub bool, wait time.Duration) (string, error) {
	m.mu.Lock()
	l := m.lives[id]
	if l != nil {
		l.mu.Lock()
		// A failed view is retried, at most every 5 s.
		if !l.ended.IsZero() && time.Since(l.ended) > 5*time.Second {
			l.mu.Unlock()
			delete(m.lives, id)
			l = nil
		} else {
			l.mu.Unlock()
		}
	}
	if l == nil {
		d, err := camDir(id)
		if err != nil {
			m.mu.Unlock()
			return "", err
		}
		input, err := WriteInput(id, "live", stream)
		if err != nil {
			m.mu.Unlock()
			return "", err
		}
		out := filepath.Join(d, "live.jpg")
		_ = os.Remove(out)
		ctx, cancel := context.WithCancel(context.Background())
		l = &live{cancel: cancel, out: out, seen: time.Now()}
		m.lives[id] = l
		go m.runLive(ctx, id, l, LiveArgs(input, out, sub))
	}
	m.mu.Unlock()
	l.mu.Lock()
	l.seen = time.Now()
	l.mu.Unlock()
	deadline := time.Now().Add(wait)
	for {
		if info, err := os.Stat(l.out); err == nil && time.Since(info.ModTime()) < 15*time.Second {
			return l.out, nil
		}
		l.mu.Lock()
		ended, msg := l.ended, l.err
		l.mu.Unlock()
		if !ended.IsZero() {
			if msg == "" {
				msg = "the camera stream could not be read"
			}
			return "", errors.New(msg)
		}
		if time.Now().After(deadline) {
			return "", errors.New("no picture from the camera yet")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (m *Manager) runLive(ctx context.Context, id string, l *live, args []string) {
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				l.mu.Lock()
				idle := time.Since(l.seen) > liveIdle
				l.mu.Unlock()
				if idle {
					m.mu.Lock()
					if m.lives[id] == l {
						delete(m.lives, id)
					}
					m.mu.Unlock()
					l.cancel()
					return
				}
			}
		}
	}()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.WaitDelay = 5 * time.Second
	var msg func() string
	if stderr, err := cmd.StderrPipe(); err == nil {
		msg = tail(stderr)
	}
	err := cmd.Start()
	if err == nil {
		err = cmd.Wait()
	}
	l.mu.Lock()
	l.ended = time.Now()
	if msg != nil {
		l.err = msg()
	}
	if l.err == "" && err != nil && ctx.Err() == nil {
		l.err = Redact(err.Error())
	}
	l.mu.Unlock()
	_ = os.Remove(l.out)
}

// LiveCount is the number of running live views (tests, status).
func (m *Manager) LiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.lives)
}

// Snapshot grabs one picture of a stream (connection test).
func Snapshot(ctx context.Context, stream string) ([]byte, error) {
	if err := os.MkdirAll(RunDir, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(RunDir, "test-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "test.ffconcat")
	if err := sys.WriteFileAtomic(input, []byte(ConcatList(stream)), 0o600); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, "test.jpg")
	_, err = sys.RunOpts(ctx, sys.Opts{Timeout: 20 * time.Second}, "ffmpeg", SnapshotArgs(input, out)...)
	if err != nil {
		var ce *sys.CmdError
		if errors.As(err, &ce) {
			msg := strings.TrimSpace(ce.Stderr)
			if i := strings.LastIndex(msg, "\n"); i >= 0 {
				msg = msg[i+1:]
			}
			return nil, errors.New(Redact(msg))
		}
		return nil, errors.New(Redact(err.Error()))
	}
	return os.ReadFile(out)
}
