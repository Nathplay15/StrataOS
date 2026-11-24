package rootio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "passwd")
	os.WriteFile(outside, []byte("root:x:0:0"), 0o644)
	share := filepath.Join(dir, "share")
	os.Mkdir(share, 0o755)
	// A planted link where the file goes, and one where a naive temp would go.
	os.Symlink(outside, filepath.Join(share, "doc.txt"))
	os.Symlink(outside, filepath.Join(share, "doc.txt.strata-restore"))
	rt, err := os.OpenRoot(share)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if err := WriteFile(rt, "doc.txt", []byte("new"), 0o640, NoOwner); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "root:x:0:0" {
		t.Fatalf("outside file written: %q", b)
	}
	st, err := os.Lstat(filepath.Join(share, "doc.txt"))
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o640 {
		t.Fatalf("doc.txt should be a regular 0640 file: %v %v", st, err)
	}
	// A link escaping the root as parent folder is refused.
	os.Symlink(filepath.Dir(outside), filepath.Join(share, "out"))
	if err := WriteFile(rt, "out/x", []byte("x"), 0o644, NoOwner); err == nil {
		t.Fatal("write through an escaping folder link should fail")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(outside), "x")); err == nil {
		t.Fatal("file created outside")
	}
}

func TestReadRefusesLinksDevicesAndHugeFiles(t *testing.T) {
	share := t.TempDir()
	os.Symlink("/etc/hostname", filepath.Join(share, "a.vtt"))
	os.Symlink("/dev/zero", filepath.Join(share, "z.vtt"))
	os.WriteFile(filepath.Join(share, "big.vtt"), make([]byte, 2048), 0o644)
	os.WriteFile(filepath.Join(share, "ok.vtt"), []byte("WEBVTT"), 0o644)
	os.Symlink("ok.vtt", filepath.Join(share, "in.vtt"))
	rt, _ := os.OpenRoot(share)
	defer rt.Close()
	if _, err := ReadFile(rt, "a.vtt", 1024); err == nil {
		t.Fatal("escaping link read")
	}
	if _, err := ReadFile(rt, "z.vtt", 1024); err == nil {
		t.Fatal("device read")
	}
	if _, err := ReadFile(rt, "big.vtt", 1024); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big: %v", err)
	}
	if b, err := ReadFile(rt, "in.vtt", 1024); err != nil || string(b) != "WEBVTT" {
		t.Fatalf("link inside the root: %q %v", b, err)
	}
}
