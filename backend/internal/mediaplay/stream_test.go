package mediaplay

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
)

// fakeFFmpeg fails every GPU conversion with msg and writes a few bytes
// otherwise; it records the encoders it was asked for.
func fakeFFmpeg(msg string, calls *[]string) func(context.Context, *os.File, []string, io.Writer) error {
	return func(_ context.Context, _ *os.File, args []string, out io.Writer) error {
		enc := args[slices.Index(args, "-c:v")+1]
		*calls = append(*calls, enc)
		if enc == "h264_vaapi" {
			return errors.New("ffmpeg: " + msg)
		}
		_, err := out.Write([]byte("fragment"))
		return err
	}
}

func TestStreamFallback(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "v")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	plan := Plan{Mode: Transcode, Video: &Stream{Index: 0, Codec: "mpeg2video"}, Type: "video/mp4"}
	gpu := func() HW { return HW{VAAPI: true, Device: "/dev/dri/renderD128"} }

	// A file the GPU refuses: the CPU converts it, the GPU stays on.
	var calls []string
	var told []string
	p := NewPlayer(1)
	p.Configure(gpu)
	p.OnFallback = func(r string, off bool) { told = append(told, r) }
	p.exec = fakeFFmpeg("No usable encoding entrypoint found for profile VAProfileH264High", &calls)
	w := httptest.NewRecorder()
	if err := p.Stream(w, httptest.NewRequest("GET", "/", nil), f, plan, 0); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"h264_vaapi", "libx264"}) || w.Body.String() != "fragment" {
		t.Errorf("calls %v body %q", calls, w.Body.String())
	}
	st := p.Status()
	if st.Disabled || st.Fallbacks != 1 || !strings.Contains(st.LastReason, "entrypoint") || st.Encoder != "vaapi" || len(told) != 1 {
		t.Errorf("status %+v told %v", st, told)
	}

	// A broken driver: the GPU is turned off until Configure.
	calls = nil
	p.exec = fakeFFmpeg("Failed to initialise VAAPI connection: -1 (unknown libva error).", &calls)
	if err := p.Stream(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), f, plan, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.Stream(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), f, plan, 0); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"h264_vaapi", "libx264", "libx264"}) {
		t.Errorf("calls after the driver failure: %v", calls)
	}
	st = p.Status()
	if !st.Disabled || st.Fallbacks != 2 || st.CPUStreams != 3 || st.GPUStreams != 2 || len(told) != 2 {
		t.Errorf("status %+v told %d", st, len(told))
	}
	p.Configure(gpu)
	if !p.HW().On() || p.Status().Disabled {
		t.Error("Configure did not try the GPU again")
	}

	// A stream copy never touches the GPU.
	calls = nil
	cp := Plan{Mode: Remux, Video: &Stream{Index: 0, Codec: "h264"}, CopyVideo: true, Type: "video/mp4"}
	if err := p.Stream(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), f, cp, 0); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"copy"}) {
		t.Errorf("remux calls %v", calls)
	}
}
