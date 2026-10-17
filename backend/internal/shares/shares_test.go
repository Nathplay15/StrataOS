package shares

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPurgeRecycle(t *testing.T) {
	share := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	mk := func(rel string, mtime time.Time) string {
		p := filepath.Join(share, ".recycle", rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
		_ = os.Chtimes(p, mtime, mtime)
		return p
	}
	oldFile := mk("alice/docs/old.txt", old)
	newFile := mk("alice/new.txt", time.Now())
	// A symlink planted by a user must never be followed into the outside folder.
	link := filepath.Join(share, ".recycle", "alice", "evil")
	_ = os.Symlink(outside, link)

	n, _ := PurgeRecycle(share, 30)
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("old file was not purged")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Errorf("recent file was purged")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("purge followed a symlink outside the share")
	}
	if _, err := os.Stat(filepath.Join(share, ".recycle", "alice", "docs")); !os.IsNotExist(err) {
		t.Errorf("empty folder was not removed")
	}
	if n != 1 {
		t.Errorf("removed %d entries, want 1", n)
	}
}

func TestValidateNFS(t *testing.T) {
	for _, c := range [][2]string{{"", ""}, {"10.0.2.0/24", ""}, {"*", "rw,sync,no_subtree_check,root_squash"},
		{"host.lan, 192.168.1.0/24 fe80::/64 @netgroup", "ro,sec=krb5:krb5i,anonuid=1000,fsid=1"}} {
		if err := ValidateNFS(c[0], c[1]); err != nil {
			t.Errorf("%q / %q rejected: %v", c[0], c[1], err)
		}
	}
	for _, c := range [][2]string{{"-rw,no_root_squash", ""}, {"a(rw)", ""}, {"*", "rw) *(rw,no_root_squash"},
		{"*", "rw\n/ *(rw"}, {"*", "rw\r/ *(rw"}, {"*", "rw no_root_squash"}, {"*", `rw"`}, {"\"x\"", ""}} {
		if ValidateNFS(c[0], c[1]) == nil {
			t.Errorf("%q / %q accepted", c[0], c[1])
		}
	}
}

func TestSmbConfValues(t *testing.T) {
	for _, bad := range []string{"/srv/strata/v/%H", `/srv/strata/v/x\`, "/srv/strata/v/\"x", "/srv/strata/v/a\nb", "/etc"} {
		if _, err := ValidatePath(bad); err == nil {
			t.Errorf("path %q accepted", bad)
		}
	}
	if p, err := ValidatePath("/srv/strata/main/Photos d'été"); err != nil || p != "/srv/strata/main/Photos d'été" {
		t.Errorf("valid path rejected: %q %v", p, err)
	}
	if got := clean("desc\\\n[global]"); got != "desc/ (global)" {
		t.Errorf("clean = %q", got)
	}
}
