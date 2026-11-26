package trash

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFallback(t *testing.T) {
	dir := t.TempDir()
	fb := filepath.Join(t.TempDir(), "trash")
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("x"), 0o644)
	if err := (Trasher{Fallback: fb}).fallback(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("still there")
	}
	os.WriteFile(p, []byte("y"), 0o644)
	(Trasher{Fallback: fb}).fallback(p)
	m, _ := filepath.Glob(filepath.Join(fb, "*", "a.txt*"))
	if len(m) != 2 {
		t.Fatalf("kept: %v", m)
	}
}

func TestSystemTrash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)
	p := filepath.Join(home, "doc.txt")
	os.WriteFile(p, []byte("x"), 0o644)
	if err := systemTrash(p); err != nil {
		t.Skip("no system trash here:", err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("not moved")
	}
}

// A file left in the trash without its info file is never replaced.
func TestSystemTrashKeepsStaleFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("HOME", home)
	files := filepath.Join(home, "Trash", "files")
	os.MkdirAll(files, 0o700)
	os.WriteFile(filepath.Join(files, "doc.txt"), []byte("old"), 0o644)
	p := filepath.Join(home, "doc.txt")
	os.WriteFile(p, []byte("new"), 0o644)
	if err := systemTrash(p); err != nil {
		t.Skip("no system trash here:", err)
	}
	if b, _ := os.ReadFile(filepath.Join(files, "doc.txt")); string(b) != "old" {
		t.Fatalf("stale trash file replaced: %q", b)
	}
}
