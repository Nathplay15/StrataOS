package nvr

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seg(name string, start, end time.Time, open bool) Segment {
	s := Segment{Name: name, Start: start, End: end, Size: 100, Open: open}
	return s
}

func TestParseClipName(t *testing.T) {
	at, ok := ParseClipName("2026-11-23_14h05m09s.mp4")
	if !ok || at.Format(ClipLayout) != "2026-11-23_14h05m09s" {
		t.Fatalf("ParseClipName = %v %v", at, ok)
	}
	if _, ok := ParseClipName("20261123-140509.mp4"); ok {
		t.Error("a segment name must not parse as a clip")
	}
	if _, ok := ParseClipName("random.mp4"); ok {
		t.Error("a random name must not parse as a clip")
	}
	if _, ok := ParseClipName("2026-11-23_14h05m09s.mkv"); !ok {
		t.Error("mkv clips must parse")
	}
}

func TestClipWindow(t *testing.T) {
	base := time.Date(2026, 11, 23, 14, 0, 0, 0, time.Local)
	segs := []Segment{
		seg("20261123-135000.mp4", base.Add(-15*time.Minute), base.Add(-5*time.Minute), false),
		seg("20261123-135500.mp4", base.Add(-5*time.Minute), base.Add(5*time.Minute), false),
		seg("20261123-140500.mp4", base.Add(5*time.Minute), base.Add(15*time.Minute), true), // being written
	}
	at := base.Add(2 * time.Minute) // event inside the second closed segment

	// The window fits in one closed segment (14:01:50–14:02:10 in 13:55–14:05).
	files, from, dur, ok := ClipWindow("/cams/c1", segs, at, 10*time.Second, 10*time.Second)
	if !ok || len(files) != 1 || files[0] != "/cams/c1/20261123-135500.mp4" {
		t.Fatalf("one segment: %v %v", files, ok)
	}
	if from != 6*time.Minute+50*time.Second || dur != 20*time.Second {
		t.Errorf("from=%v dur=%v, want 6m50s and 20s", from, dur)
	}

	// A wider window spans two closed segments; the open one is skipped.
	files, from, dur, ok = ClipWindow("/cams/c1", segs, at, 10*time.Minute, 10*time.Second)
	if !ok || len(files) != 2 {
		t.Fatalf("two segments: %v %v", files, ok)
	}
	if from != 7*time.Minute || dur != 10*time.Minute+10*time.Second {
		t.Errorf("from=%v dur=%v, want 7m and 10m10s", from, dur)
	}

	// The end of the window is clamped to the last closed segment (14:05).
	files, _, dur, ok = ClipWindow("/cams/c1", segs, at, 10*time.Second, 30*time.Minute)
	if !ok || len(files) != 1 || dur != 3*time.Minute+10*time.Second {
		t.Errorf("clamped: %v %v %v", files, dur, ok)
	}

	// No closed segment covers the event.
	if _, _, _, ok := ClipWindow("/cams/c1", segs, base.Add(10*time.Minute), 10*time.Second, 10*time.Second); ok {
		t.Error("the open segment must not be selected")
	}
	if _, _, _, ok := ClipWindow("/cams/c1", nil, at, time.Second, time.Second); ok {
		t.Error("no segments at all must fail")
	}
}

func TestPruneClips(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	mk := func(name string, mod time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, mod, mod)
	}
	mk("2026-10-01_10h00m00s.mp4", now.AddDate(0, 0, -40)) // too old (keep 30)
	mk("2026-11-20_10h00m00s.mp4", now.AddDate(0, 0, -3))
	mk("2026-11-23_10h00m00s.mkv", now.Add(-time.Hour))
	mk("not-a-clip.txt", now) // ignored
	if n := PruneClips(dir, 0, 0, now); n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-01_10h00m00s.mp4")); !os.IsNotExist(err) {
		t.Error("the old clip must be gone")
	}
	for _, keep := range []string{"2026-11-20_10h00m00s.mp4", "2026-11-23_10h00m00s.mkv"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s must be kept: %v", keep, err)
		}
	}
	// Cap: 3 clips, keep at most 2 -> the oldest goes.
	mk("2026-11-21_10h00m00s.mp4", now.Add(-2*time.Hour))
	if n := PruneClips(dir, 0, 2, now); n != 1 {
		t.Fatalf("capped prune deleted %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-11-20_10h00m00s.mp4")); !os.IsNotExist(err) {
		t.Error("the oldest of the cap must be gone")
	}
}
