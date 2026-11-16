package storagetools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
