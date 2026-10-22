package nvr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"strataos/internal/mediaplay"
)

func TestProbeSegmentCache(t *testing.T) {
	calls := 0
	prev := probeSegment
	probeSegment = func(ctx context.Context, f *os.File) (*mediaplay.Info, error) {
		calls++
		return &mediaplay.Info{Duration: 10, Streams: []mediaplay.Stream{{Type: "video", Codec: "hevc"}}}, nil
	}
	t.Cleanup(func() { probeSegment = prev })
	p := filepath.Join(t.TempDir(), "20250101-100000.mp4")
	_ = os.WriteFile(p, []byte("x"), 0o644)
	probe := func() *mediaplay.Info {
		f, _ := os.Open(p)
		defer f.Close()
		info, _ := f.Stat()
		i, err := ProbeSegment(context.Background(), f, info)
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	if i := probe(); i.Streams[0].Codec != "hevc" {
		t.Fatalf("probe %+v", i)
	}
	probe()
	if calls != 1 {
		t.Fatalf("probed %d times, want once", calls)
	}
	// A segment that changed (the one being written) is probed again.
	_ = os.WriteFile(p, []byte("xy"), 0o644)
	_ = os.Chtimes(p, time.Now(), time.Now().Add(time.Minute))
	probe()
	if calls != 2 {
		t.Fatalf("changed file not probed again (%d)", calls)
	}
}
