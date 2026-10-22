package nvr

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseProbe(t *testing.T) {
	out := `{"programs":[],"streams":[{"codec_name":"hevc","codec_type":"video"},{"codec_name":"pcm_alaw","codec_type":"audio"},{"codec_name":"h264","codec_type":"video"}],"format":{"duration":"10.040000"}}`
	p, err := ParseProbe([]byte(out))
	if err != nil || p.Video != "hevc" || p.Audio != "pcm_alaw" || p.Duration != 10.04 {
		t.Fatalf("got %+v %v", p, err)
	}
	if _, err := ParseProbe([]byte(`{"streams":[{"codec_name":"aac","codec_type":"audio"}]}`)); err == nil {
		t.Error("a file without video accepted")
	}
	if _, err := ParseProbe([]byte(`garbage`)); err == nil {
		t.Error("garbage accepted")
	}
}

func TestDirect(t *testing.T) {
	for _, c := range []struct {
		name string
		p    Probe
		want bool
	}{
		{"a.mp4", Probe{Video: "h264"}, true},
		{"a.mp4", Probe{Video: "h264", Audio: "aac"}, true},
		{"a.mp4", Probe{Video: "h264", Audio: "pcm_mulaw"}, false},
		{"a.mp4", Probe{Video: "hevc"}, false},
		{"a.mkv", Probe{Video: "h264"}, false}, // Matroska: remuxed
		{"a.mkv", Probe{Video: "mjpeg"}, false},
	} {
		if got := c.p.Direct(c.name); got != c.want {
			t.Errorf("Direct(%s, %+v) = %v", c.name, c.p, got)
		}
	}
}

func TestPlaybackArgs(t *testing.T) {
	has := func(a []string, seq ...string) bool {
		for i := range a {
			if i+len(seq) <= len(a) && slices.Equal(a[i:i+len(seq)], seq) {
				return true
			}
		}
		return false
	}
	// H.264 with G.711 sound: video copied, sound to AAC, seek before the input.
	a := PlaybackArgs("/srv/x/20250101-100000.mp4", Probe{Video: "h264", Audio: "pcm_alaw"}, PlayOpts{Start: 90, VAAPI: "/dev/dri/renderD128"})
	for _, seq := range [][]string{{"-ss", "90.000", "-i", "/srv/x/20250101-100000.mp4"}, {"-c:v", "copy"}, {"-c:a", "aac"},
		{"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1"}} {
		if !has(a, seq...) {
			t.Errorf("h264: %v missing in %v", seq, a)
		}
	}
	if slices.Contains(a, "-vaapi_device") {
		t.Errorf("VA-API set up for a copy: %v", a)
	}
	// HEVC: libx264, or VA-API when there is a render node.
	a = PlaybackArgs("/r.mp4", Probe{Video: "hevc"}, PlayOpts{})
	if !has(a, "-c:v", "libx264") || !has(a, "-c:a", "copy") || slices.Contains(a, "-ss") {
		t.Errorf("hevc: %v", a)
	}
	a = PlaybackArgs("/r.mp4", Probe{Video: "hevc"}, PlayOpts{VAAPI: "/dev/dri/renderD128"})
	if !has(a, "-vaapi_device", "/dev/dri/renderD128") || !has(a, "-c:v", "h264_vaapi") || a[len(a)-1] != "pipe:1" {
		t.Errorf("hevc vaapi: %v", a)
	}
	if !has(a, "-protocol_whitelist", "file") {
		t.Errorf("input protocols not limited: %v", a)
	}
}

// TestPlayHEVC runs ffmpeg on a short H.265 file when the encoders exist.
func TestPlayHEVC(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("no ffmpeg")
	}
	if out, _ := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output(); !bytes.Contains(out, []byte("libx265")) || !bytes.Contains(out, []byte("libx264")) {
		t.Skip("no libx265/libx264")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "20250101-100000.mp4")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=10:duration=3",
		"-c:v", "libx265", "-x265-params", "log-level=error", "-pix_fmt", "yuv420p", src).CombinedOutput(); err != nil {
		t.Fatalf("test file: %v %s", err, out)
	}
	info, _ := os.Stat(src)
	ctx := context.Background()
	p, err := ProbeFile(ctx, src, info)
	if err != nil || p.Video != "hevc" || p.Duration < 2.9 || p.Direct(src) {
		t.Fatalf("probe %+v %v", p, err)
	}
	var buf bytes.Buffer
	if err := Play(ctx, &buf, src, p, 1); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if len(b) < 1000 || !bytes.Contains(b[:64], []byte("ftyp")) || !bytes.Contains(b, []byte("moof")) {
		t.Fatalf("not fragmented MP4 (%d bytes)", len(b))
	}
	out := filepath.Join(dir, "out.mp4")
	_ = os.WriteFile(out, b, 0o644)
	info, _ = os.Stat(out)
	q, err := ProbeFile(ctx, out, info)
	if err != nil || q.Video != "h264" || q.Duration > 2.5 {
		t.Fatalf("output %+v %v (want H.264, about 2 s after the seek)", q, err)
	}
}
