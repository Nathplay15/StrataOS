package cloudsync

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeRclone puts a minimal rclone in PATH for the writeConfig tests: only
// "obscure" is implemented, with a scrambler a human can reverse, so the
// configuration file never keeps the plaintext (CI has no real rclone).
func fakeRclone(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rclone"), []byte("#!/bin/sh\nif [ \"$1\" != obscure ]; then exit 1; fi\n"+`while IFS= read -r line; do printf '%s' "$line" | tr 'a-zA-Z0-9' 'nopqrstuvwxyzabcdefghijklmNOPQRSTUVWXYZABCDEFGHIJKLM5678901234'; echo; done`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
