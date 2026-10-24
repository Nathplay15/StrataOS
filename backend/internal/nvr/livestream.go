package nvr

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// ---- Live video stream ----
//
// One ffmpeg per camera and stream (main or sub) remuxes the camera's video
// (stream copy, no transcoding) into fragmented MP4 cut at every key frame and
// at least every half second. Every viewer of that camera shares it: each one
// gets the init segment, then the fragments from the latest key frame on.
// ffmpeg stops a few seconds after the last viewer leaves.

// streamLinger: how long a stream outlives its last viewer (a viewer
// switching from the grid to full screen finds it running).
var streamLinger = 5 * time.Second

const (
	streamMaxViewers = 16
	// streamBacklog bounds the fragments kept since the last key frame (a
	// camera sending a key frame every few minutes).
	streamBacklog = 32 << 20
)

// LiveStreamArgs builds the ffmpeg command line of a live stream.
func LiveStreamArgs(input string, audio bool) []string {
	a := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-fflags", "nobuffer", "-protocol_whitelist", "file",
		"-f", "concat", "-safe", "0", "-i", input, "-map", "0:v:0"}
	if audio {
		a = append(a, "-map", "0:a:0?", "-c:v", "copy", "-c:a", "aac", "-b:a", "64k")
	} else {
		a = append(a, "-an", "-c:v", "copy")
	}
	return append(a, "-f", "mp4", "-movflags", "empty_moov+default_base_moof+frag_keyframe",
		"-frag_duration", strconv.Itoa(500_000), "-flush_packets", "1", "pipe:1")
}

// Viewer receives the fragments of a live stream.
type Viewer struct {
	Init []byte
	C    <-chan Fragment // closed when the stream ends or the viewer lags too far
	ch   chan Fragment
	s    *liveStream
	skip bool // lagging: wait for the next key frame
}

type liveStream struct {
	key     string
	cancel  context.CancelFunc
	ready   chan struct{} // closed once init is known, or on failure
	mu      sync.Mutex
	init    []byte
	backlog []Fragment // since the latest key frame
	size    int
	viewers map[*Viewer]bool
	err     error
	done    bool
	idle    *time.Timer
}

// streamStarter runs ffmpeg and returns its output (tests replace it).
type streamStarter func(ctx context.Context, args []string) (io.ReadCloser, func() string, error)

func startFFmpeg(ctx context.Context, args []string) (io.ReadCloser, func() string, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	msg := tail(stderr)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return &cmdReader{ReadCloser: stdout, cmd: cmd}, msg, nil
}

type cmdReader struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (c *cmdReader) Close() error {
	_ = c.ReadCloser.Close()
	return c.cmd.Wait()
}

// LiveStream joins (or starts) the live stream of a camera. kind is "main"
// or "sub"; stream is the full URL of that stream. It waits up to wait for
// the stream to start; Leave must be called once done.
func (m *Manager) LiveStream(id, kind, stream string, audio bool, wait time.Duration) (*Viewer, error) {
	key := id + "/" + kind
	m.mu.Lock()
	if m.streams == nil {
		m.streams = map[string]*liveStream{}
	}
	s := m.streams[key]
	if s == nil {
		input, err := WriteInput(id, "stream-"+kind, stream)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		s = &liveStream{key: key, cancel: cancel, ready: make(chan struct{}), viewers: map[*Viewer]bool{}}
		m.streams[key] = s
		start := m.startStream
		if start == nil {
			start = startFFmpeg
		}
		go m.runStream(ctx, s, start, LiveStreamArgs(input, audio))
	}
	m.mu.Unlock()

	select {
	case <-s.ready:
	case <-time.After(wait):
		m.releaseIfUnused(s)
		return nil, errors.New("no video from the camera yet")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		err := s.err
		if err == nil {
			err = errors.New("the camera stream ended")
		}
		return nil, err
	}
	if len(s.viewers) >= streamMaxViewers {
		return nil, errors.New("too many viewers of this camera")
	}
	ch := make(chan Fragment, 64)
	v := &Viewer{Init: s.init, C: ch, ch: ch, s: s}
	for _, f := range s.backlog {
		ch <- f
		if len(ch) == cap(ch) {
			break
		}
	}
	s.viewers[v] = true
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	return v, nil
}

// releaseIfUnused stops a stream nobody joined.
func (m *Manager) releaseIfUnused(s *liveStream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.viewers) == 0 && s.idle == nil && !s.done {
		s.idle = time.AfterFunc(streamLinger, func() { m.stopIfIdle(s) })
	}
}

// Leave ends a viewer; the stream stops a little after its last viewer.
func (m *Manager) Leave(v *Viewer) {
	s := v.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.viewers[v] {
		return
	}
	delete(s.viewers, v)
	close(v.ch)
	if len(s.viewers) == 0 && !s.done && s.idle == nil {
		s.idle = time.AfterFunc(streamLinger, func() { m.stopIfIdle(s) })
	}
}

func (m *Manager) stopIfIdle(s *liveStream) {
	m.mu.Lock()
	s.mu.Lock()
	idle := len(s.viewers) == 0 && !s.done
	if idle {
		// Done at once: a viewer arriving now starts a new stream rather
		// than joining this one while ffmpeg exits.
		s.idle, s.done = nil, true
		if m.streams[s.key] == s {
			delete(m.streams, s.key)
		}
		select {
		case <-s.ready:
		default:
			close(s.ready)
		}
	}
	s.mu.Unlock()
	m.mu.Unlock()
	if idle {
		s.cancel()
	}
}

func (m *Manager) runStream(ctx context.Context, s *liveStream, start streamStarter, args []string) {
	defer func() {
		m.mu.Lock()
		if m.streams[s.key] == s {
			delete(m.streams, s.key)
		}
		m.mu.Unlock()
	}()
	finish := func(err error) {
		s.mu.Lock()
		if !s.done {
			s.done, s.err = true, err
			for v := range s.viewers {
				close(v.ch)
				delete(s.viewers, v)
			}
			select {
			case <-s.ready:
			default:
				close(s.ready)
			}
		}
		s.mu.Unlock()
		s.cancel()
	}
	out, msg, err := start(ctx, args)
	if err != nil {
		finish(err)
		return
	}
	failure := func(err error) error {
		if ctx.Err() != nil {
			return nil
		}
		_ = out.Close()
		if m := msg(); m != "" {
			return errors.New(m)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return errors.New("the camera stream could not be read")
		}
		return errors.New(Redact(err.Error()))
	}
	rd := newFMP4Reader(out)
	init, err := rd.Init()
	if err != nil {
		finish(failure(err))
		_ = out.Close()
		return
	}
	// The first fragment comes with the init segment, so a viewer always
	// has a picture to start with.
	f, err := rd.Next()
	if err != nil {
		finish(failure(err))
		_ = out.Close()
		return
	}
	s.mu.Lock()
	s.init = init
	s.add(f)
	close(s.ready)
	s.mu.Unlock()
	for {
		f, err := rd.Next()
		if err != nil {
			finish(failure(err))
			_ = out.Close()
			return
		}
		s.mu.Lock()
		s.add(f)
		s.mu.Unlock()
	}
}

// add keeps a fragment and hands it to the viewers (s.mu held).
func (s *liveStream) add(f Fragment) {
	if f.Key || s.size+len(f.Data) > streamBacklog {
		s.backlog, s.size = s.backlog[:0], 0
	}
	if f.Key || len(s.backlog) > 0 {
		s.backlog = append(s.backlog, f)
		s.size += len(f.Data)
	}
	for v := range s.viewers {
		if v.skip && !f.Key {
			continue
		}
		select {
		case v.ch <- f:
			v.skip = false
		default:
			v.skip = true // too slow: drop until the next key frame
		}
	}
}

// StreamCount is the number of running live streams (tests, status).
func (m *Manager) StreamCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.streams)
}
