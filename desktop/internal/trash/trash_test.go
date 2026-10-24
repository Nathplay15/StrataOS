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
