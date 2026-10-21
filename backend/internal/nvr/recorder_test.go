package nvr

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// publish serves a test pattern over HTTP (MPEG-TS, H.264) to one client.
func publish(t *testing.T, ctx context.Context) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	u := "http://" + addr + "/cam"
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-re", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=10",
		"-c:v", "libx264", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-g", "10", "-f", "mpegts", "-listen", "1", u)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	time.Sleep(time.Second)
	return u
}

// TestRecorderWithFFmpeg records a local HTTP stream: segments appear, the
// status turns to recording, the stream URL is not on the command line, and
// Forget stops ffmpeg.
func TestRecorderWithFFmpeg(t *testing.T) {
	if testing.Short() || !Installed() {
		t.Skip("needs ffmpeg")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	RunDir = t.TempDir()
	dir := t.TempDir()
	src := publish(t, ctx)
	img, err := Snapshot(ctx, src)
	if err != nil || len(img) < 100 || img[0] != 0xff || img[1] != 0xd8 {
		t.Fatalf("snapshot: %v (%d bytes)", err, len(img))
	}
	src = publish(t, ctx)
	m := NewManager()
	m.Sync([]Recording{{ID: "cam1", Name: "Test", Stream: src, Dir: dir, SegmentSeconds: 2}})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && m.Status("cam1").State != "recording" {
		time.Sleep(time.Second)
	}
	if st := m.Status("cam1"); st.State != "recording" {
		t.Fatalf("status: %+v", st)
	}
	// (The test publisher has the URL on its command line, the recorder must not.)
	out, _ := exec.Command("ps", "-eo", "args").Output()
	recorders := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "-f concat") && strings.Contains(l, RunDir) {
			recorders++
			if strings.Contains(l, "http://") {
				t.Errorf("stream URL visible in the process list: %s", l)
			}
		}
	}
	if recorders != 1 {
		t.Errorf("%d recorder processes", recorders)
	}
	time.Sleep(3 * time.Second)
	m.Forget("cam1")
	segs, _ := ListSegments(dir, false)
	if len(segs) < 2 || !strings.HasSuffix(segs[0].Name, ".mkv") {
		t.Fatalf("segments: %+v", segs)
	}
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", dir+"/"+segs[0].Name).Output()
	if err != nil || strings.TrimSpace(string(probe)) != "h264" {
		t.Errorf("first segment: %q %v", probe, err)
	}
	if m.Recording("cam1") {
		t.Error("recorder still running")
	}
	if _, err := os.Stat(RunDir + "/cam1"); !os.IsNotExist(err) {
		t.Error("runtime folder (credentials) left behind")
	}
	// Unreachable camera: the snapshot fails with a readable message.
	if _, err := Snapshot(ctx, "http://127.0.0.1:9/none"); err == nil {
		t.Error("snapshot of a closed port succeeded")
	} else {
		fmt.Println("expected failure:", err)
	}
}

func TestLiveWithFFmpeg(t *testing.T) {
	if testing.Short() || !Installed() {
		t.Skip("needs ffmpeg")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	RunDir = t.TempDir()
	src := publish(t, ctx)
	m := NewManager()
	p, err := m.Live("cam2", src, false, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if len(b) < 100 || b[0] != 0xff || b[1] != 0xd8 {
		t.Fatalf("live picture: %d bytes", len(b))
	}
	if m.LiveCount() != 1 {
		t.Error("live view not tracked")
	}
	m.Forget("cam2")
	if m.LiveCount() != 0 {
		t.Error("live view not stopped")
	}
}
