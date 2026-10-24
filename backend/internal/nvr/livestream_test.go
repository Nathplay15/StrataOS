package nvr

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"
	"testing"
	"time"
)

func box(typ string, parts ...[]byte) []byte {
	p := bytes.Join(parts, nil)
	b := make([]byte, 8, 8+len(p))
	binary.BigEndian.PutUint32(b, uint32(8+len(p)))
	copy(b[4:], typ)
	return append(b, p...)
}

func u32(vs ...uint32) []byte {
	b := make([]byte, 4*len(vs))
	for i, v := range vs {
		binary.BigEndian.PutUint32(b[4*i:], v)
	}
	return b
}

// testInit: an audio track (1) then a video track (2), whose samples are
// non-sync by default (trex).
func testInit() []byte {
	trak := func(id uint32, handler string) []byte {
		tkhd := box("tkhd", u32(0, 0, 0, id, 0))
		hdlr := box("hdlr", u32(0, 0), []byte(handler), u32(0, 0, 0))
		return box("trak", tkhd, box("mdia", hdlr))
	}
	mvex := box("mvex", box("trex", u32(0, 1, 1, 0, 0, 0)), box("trex", u32(0, 2, 1, 0, 0, 0x00010000)))
	return append(box("ftyp", []byte("isom"), u32(0x200)), box("moov", trak(1, "soun"), trak(2, "vide"), mvex)...)
}

// testFragment: audio traf first, then the video traf, flagged key or not.
func testFragment(key bool, n byte) []byte {
	audio := box("traf", box("tfhd", u32(0, 1)), box("trun", u32(0x4, 1, 0)))
	var video []byte
	if key {
		video = box("traf", box("tfhd", u32(0, 2)), box("trun", u32(0x4, 3, 0x02000000)))
	} else {
		video = box("traf", box("tfhd", u32(0, 2)), box("trun", u32(0x100, 3, 40, 40, 40)))
	}
	return append(box("moof", box("mfhd", u32(0, uint32(n))), audio, video), box("mdat", []byte{n, n, n})...)
}

func TestFMP4Reader(t *testing.T) {
	var in bytes.Buffer
	in.Write(testInit())
	in.Write(testFragment(true, 1))
	in.Write(box("styp", []byte("msdh")))
	in.Write(testFragment(false, 2))
	// Video flags given per sample (0x400) and by tfhd (0x20).
	in.Write(append(box("moof", box("traf", box("tfhd", u32(0, 2)), box("trun", u32(0x400, 2, 0x02000000, 0x00010000)))), box("mdat")...))
	in.Write(append(box("moof", box("traf", box("tfhd", u32(0x20, 2, 0x00010000)), box("trun", u32(0, 1)))), box("mdat")...))
	r := newFMP4Reader(&in)
	init, err := r.Init()
	if err != nil || !bytes.Equal(init, testInit()) {
		t.Fatalf("init: %v", err)
	}
	if r.video != 2 {
		t.Errorf("video track %d", r.video)
	}
	var keys []bool
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		keys = append(keys, f.Key)
	}
	if want := []bool{true, false, true, false}; !equalBools(keys, want) {
		t.Errorf("keys %v, want %v", keys, want)
	}
}

func TestFMP4ReaderRejects(t *testing.T) {
	if _, err := newFMP4Reader(bytes.NewReader(testFragment(true, 1))).Init(); err == nil {
		t.Error("fragment without init accepted")
	}
	huge := u32(maxBox+1, 0x6d646174)
	if _, err := newFMP4Reader(bytes.NewReader(append(testInit(), huge...))).Init(); err == nil {
		t.Error("oversized box accepted")
	}
}

func equalBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLiveStreamShared(t *testing.T) {
	RunDir = t.TempDir()
	streamLinger = 50 * time.Millisecond
	pr, pw := io.Pipe()
	starts := 0
	m := NewManager()
	m.startStream = func(ctx context.Context, args []string) (io.ReadCloser, func() string, error) {
		starts++
		go func() { <-ctx.Done(); pw.CloseWithError(io.EOF) }()
		return pr, func() string { return "" }, nil
	}
	go func() {
		pw.Write(testInit())
		pw.Write(testFragment(true, 1))
		pw.Write(testFragment(false, 2))
	}()
	v1, err := m.LiveStream("cam1", "sub", "rtsp://h/x", false, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v1.Init, testInit()) {
		t.Error("wrong init")
	}
	if f := <-v1.C; !f.Key || f.Data[len(f.Data)-1] != 1 {
		t.Errorf("first fragment %v", f.Key)
	}
	if f := <-v1.C; f.Key {
		t.Error("second fragment should not be a key frame")
	}
	// A second viewer shares ffmpeg and starts at the latest key frame.
	v2, err := m.LiveStream("cam1", "sub", "rtsp://h/x", false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 || m.StreamCount() != 1 {
		t.Errorf("starts %d, streams %d", starts, m.StreamCount())
	}
	if f := <-v2.C; !f.Key {
		t.Error("joined on a non-key fragment")
	}
	if f := <-v2.C; f.Key {
		t.Error("backlog order")
	}
	pw.Write(testFragment(true, 3))
	for _, v := range []*Viewer{v1, v2} {
		if f := <-v.C; f.Data[len(f.Data)-1] != 3 {
			t.Error("live fragment not shared")
		}
	}
	m.Leave(v1)
	m.Leave(v2)
	m.Leave(v2) // twice is harmless
	deadline := time.Now().Add(2 * time.Second)
	for m.StreamCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.StreamCount() != 0 {
		t.Error("stream still running without viewers")
	}
}

func TestLiveStreamFailure(t *testing.T) {
	RunDir = t.TempDir()
	m := NewManager()
	m.startStream = func(ctx context.Context, args []string) (io.ReadCloser, func() string, error) {
		return io.NopCloser(bytes.NewReader(nil)), func() string { return "Connection refused" }, nil
	}
	if _, err := m.LiveStream("cam1", "main", "rtsp://h/x", false, time.Second); err == nil || err.Error() != "Connection refused" {
		t.Errorf("error %v", err)
	}
}

func TestLiveStreamArgs(t *testing.T) {
	j := strings.Join(LiveStreamArgs("/run/x.ffconcat", false), " ")
	for _, want := range []string{"-c:v copy", "-an", "frag_keyframe", "pipe:1", "-protocol_whitelist file"} {
		if !strings.Contains(j, want) {
			t.Errorf("missing %q in %s", want, j)
		}
	}
	if !strings.Contains(strings.Join(LiveStreamArgs("x", true), " "), "-c:a aac") {
		t.Error("audio not re-encoded")
	}
}
