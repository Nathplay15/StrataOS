package api

import (
	"io"
	"os"
	"path/filepath"
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
