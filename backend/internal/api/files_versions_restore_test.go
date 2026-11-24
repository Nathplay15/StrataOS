package api

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strataos/internal/rootio"
)

// A planted doc.txt.strata-restore -> /etc/passwd must never be written.
func TestRestoreIntoIgnoresPlantedSymlink(t *testing.T) {
	share := t.TempDir()
	victim := filepath.Join(t.TempDir(), "passwd")
	os.WriteFile(victim, []byte("root:x:0:0"), 0o644)
	os.WriteFile(filepath.Join(share, "doc.txt"), []byte("old"), 0o600)
	os.Symlink(victim, filepath.Join(share, "doc.txt.strata-restore"))
	rt, err := os.OpenRoot(share)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	err = restoreInto(rt, "doc.txt", rootio.NoOwner, func(w io.Writer) error {
		_, err := io.WriteString(w, "restored")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "root:x:0:0" {
		t.Fatalf("symlink target written: %q", b)
	}
	st, _ := os.Lstat(filepath.Join(share, "doc.txt"))
	if b, _ := os.ReadFile(filepath.Join(share, "doc.txt")); string(b) != "restored" || st.Mode().Perm() != 0o600 {
		t.Fatalf("restore: %q %v", b, st.Mode())
	}
	// doc.txt itself a link to the victim: replaced by the restored file, victim untouched.
	os.Remove(filepath.Join(share, "doc.txt"))
	os.Symlink(victim, filepath.Join(share, "doc.txt"))
	if err := restoreInto(rt, "doc.txt", rootio.NoOwner, func(w io.Writer) error { _, err := io.WriteString(w, "x"); return err }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "root:x:0:0" {
		t.Fatalf("victim written: %q", b)
	}
}

func TestUploadKeepNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "IMG_1.jpg"), []byte("first photo"), 0o644)
	rt, _ := os.OpenRoot(dir)
	defer rt.Close()
	// The same photo again: skipped.
	if name, stored, err := uploadKeep(rt, "IMG_1.jpg", strings.NewReader("first photo")); err != nil || stored || name != "IMG_1.jpg" {
		t.Fatalf("same file: %q %v %v", name, stored, err)
	}
	// Another photo with the same name: kept beside it.
	if name, stored, err := uploadKeep(rt, "IMG_1.jpg", strings.NewReader("second photo")); err != nil || !stored || name != "IMG_1 (2).jpg" {
		t.Fatalf("other file: %q %v %v", name, stored, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "IMG_1.jpg")); string(b) != "first photo" {
		t.Fatalf("overwritten: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "IMG_1 (2).jpg")); string(b) != "second photo" {
		t.Fatalf("second: %q", b)
	}
	// A planted link under the name is never written through.
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep"), 0o644)
	os.Symlink(victim, filepath.Join(dir, "IMG_2.jpg"))
	if name, _, err := uploadKeep(rt, "IMG_2.jpg", strings.NewReader("x")); err != nil || name == "IMG_2.jpg" {
		t.Fatalf("link: %q %v", name, err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("victim written")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 4 {
		t.Fatalf("leftover temporary files: %v", ents)
	}
}
