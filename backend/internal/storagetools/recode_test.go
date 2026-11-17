package storagetools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestVideoExt(t *testing.T) {
	for _, ext := range []string{".mp4", ".m4v", ".mov", ".mkv"} {
		if !VideoExt[ext] {
			t.Fatalf("%s must be compressed", ext)
		}
	}
	for _, ext := range []string{".avi", ".ts", ".webm", ".txt"} {
		if VideoExt[ext] {
			t.Fatalf("%s must be left out", ext)
		}
	}
}

func TestPlanRecode(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err == nil {
		t.Skip("ffprobe installed: the plan would run it on the fixtures")
	}
	dir := t.TempDir()
	write := func(name string, mb int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, mb<<20), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("big.mp4", 1)
	write("small.mp4", 0) // below minSize: not a candidate
	write("notes.txt", 1) // not a video
	sub := filepath.Join(dir, ".recycle")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "old.mov"), make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := PlanRecode(context.Background(), dir, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	// ffprobe is missing: the codec is "", so the file stays a candidate.
	if len(files) != 1 || filepath.Base(files[0].Path) != "big.mp4" {
		t.Fatalf("PlanRecode = %v", files)
	}
}

func TestRecodeKeepRatio(t *testing.T) {
	if RecodeKeepRatio >= 1 || RecodeKeepRatio <= 0.5 {
		t.Fatal("the keep ratio must make the replacement clearly worth it")
	}
}

// ffmpegFixture writes a short H.264 video at a high bitrate (so that H.265
// at CRF 28 is much smaller), or skips when ffmpeg or its encoders are missing.
func ffmpegFixture(t *testing.T, path string) {
	t.Helper()
	if !FFmpegAvailable() {
		t.Skip("ffmpeg not installed")
	}
	out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:v", "libx264", "-qp", "0", "-c:a", "aac", "-shortest", path).CombinedOutput()
	if err != nil {
		t.Skipf("cannot make a fixture video: %v %s", err, out)
	}
}

func TestRecodeVideos(t *testing.T) {
	dir := t.TempDir()
	vid := filepath.Join(dir, "clip.mp4")
	ffmpegFixture(t, vid)
	if err := os.Chmod(vid, 0o640); err != nil {
		t.Fatal(err)
	}
	xattr := unix.Setxattr(vid, "user.strata-test", []byte("kept"), 0) == nil
	old := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(vid, old, old); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(vid)
	before, _ := os.Stat(vid)

	// No recycle bin and no permission to discard: nothing is touched.
	rep := RecodeVideos(context.Background(), []RecodeFile{{Path: vid}}, RecodeOptions{}, nil)
	if rep.Files[0].Status != "kept" || !strings.Contains(rep.Files[0].Detail, "recycle bin") {
		t.Fatalf("without a bin: %+v", rep.Files[0])
	}

	// A hard-linked video is left alone.
	link := filepath.Join(dir, "other-name.mp4")
	if err := os.Link(vid, link); err != nil {
		t.Fatal(err)
	}
	rep = RecodeVideos(context.Background(), []RecodeFile{{Path: vid}}, RecodeOptions{Discard: true}, nil)
	if rep.Files[0].Status != "kept" || !strings.Contains(rep.Files[0].Detail, "hard-linked") {
		t.Fatalf("hard link: %+v", rep.Files[0])
	}
	_ = os.Remove(link)

	// With a recycle bin: the original is put there, the new file takes its
	// place with the same mode, dates and attributes.
	binDir := filepath.Join(dir, ".recycle")
	_ = os.Mkdir(binDir, 0o750)
	kept := ""
	opts := RecodeOptions{
		HasBin: func(string) bool { return true },
		Keep: func(p string) error {
			kept = filepath.Join(binDir, filepath.Base(p))
			return os.Rename(p, kept)
		},
	}
	rep = RecodeVideos(context.Background(), []RecodeFile{{Path: vid}}, opts, nil)
	f := rep.Files[0]
	if f.Status != "recoded" || f.New <= 0 || f.New >= before.Size() {
		t.Fatalf("recode: %+v", f)
	}
	if b, _ := os.ReadFile(kept); string(b) != string(orig) {
		t.Fatal("the original was not kept intact in the bin")
	}
	after, err := os.Stat(vid)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != 0o640 || !after.ModTime().Equal(old) {
		t.Errorf("mode %v, mtime %v (want 0640, %v)", after.Mode().Perm(), after.ModTime(), old)
	}
	if videoCodec(vid) != "hevc" || !sameDuration(duration(vid), 3) {
		t.Errorf("result: codec %q, %.2f s", videoCodec(vid), duration(vid))
	}
	if xattr {
		buf := make([]byte, 16)
		n, err := unix.Getxattr(vid, "user.strata-test", buf)
		if err != nil || string(buf[:n]) != "kept" {
			t.Errorf("extended attribute lost: %v", err)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".*.strata-recode.tmp")); len(m) != 0 {
		t.Errorf("temporary file left: %v", m)
	}
}

func TestSameDuration(t *testing.T) {
	for _, c := range []struct {
		a, b float64
		ok   bool
	}{{3, 3.04, true}, {3, 3.9, true}, {3, 4.1, false}, {600, 605, true}, {600, 610, false}, {0, 0, false}, {3, 0, false}} {
		if sameDuration(c.a, c.b) != c.ok {
			t.Errorf("sameDuration(%v, %v) != %v", c.a, c.b, c.ok)
		}
	}
}
