package backup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestSSHCmdKeyOrPassword(t *testing.T) {
	dir := t.TempDir()
	old := SecretDir
	SecretDir = dir
	defer func() { SecretDir = old }()

	key := sshCmd(&store.BackupTask{ID: "k1", Port: 2222})
	if key[0] != "ssh" || !slices.Contains(key, "BatchMode=yes") || !slices.Contains(key, keyPath()) || !slices.Contains(key, "2222") {
		t.Fatalf("key auth: %v", key)
	}

	pw := sshCmd(&store.BackupTask{ID: "p1", SSHPassword: "s3cret pass"})
	if pw[0] != "sshpass" || pw[1] != "-f" || pw[3] != "ssh" {
		t.Fatalf("password auth: %v", pw)
	}
	if slices.Contains(pw, keyPath()) || slices.Contains(pw, "BatchMode=yes") || !slices.Contains(pw, "PubkeyAuthentication=no") {
		t.Fatalf("password auth options: %v", pw)
	}
	if strings.Contains(strings.Join(pw, " "), "s3cret") {
		t.Fatal("password on the command line")
	}
	f := filepath.Join(dir, "ssh-p1")
	if pw[2] != f {
		t.Fatalf("password file %q", pw[2])
	}
	b, err := os.ReadFile(f)
	if err != nil || string(b) != "s3cret pass" {
		t.Fatalf("password file content: %q %v", b, err)
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
		t.Fatalf("password file mode %v", st.Mode())
	}
	RemovePassFile("p1")
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("password file kept after RemovePassFile")
	}
	// An id that could escape the folder is never used as a file name.
	if p := passFile(&store.BackupTask{ID: "../x", SSHPassword: "a"}); filepath.Dir(p) != dir || filepath.Base(p) != "ssh-test" {
		t.Fatalf("unsafe id: %s", p)
	}
}

func TestRcloneConfigPassword(t *testing.T) {
	c := rcloneConfig(&store.BackupTask{Host: "h", User: "u"}, "", "OBSCURED")
	if !strings.Contains(c, "pass = OBSCURED") || strings.Contains(c, "key_file") {
		t.Fatalf("password config: %s", c)
	}
	if c := rcloneConfig(&store.BackupTask{Host: "h", User: "u"}, "", ""); !strings.Contains(c, "key_file = ") || strings.Contains(c, "pass =") {
		t.Fatalf("key config: %s", c)
	}
}
