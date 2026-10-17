package ha

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func collect(t *testing.T, w *Watcher, want func(Change) bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case c := <-w.Changes:
			if want(c) {
				return
			}
		case <-deadline:
			t.Fatal("expected change not seen")
		}
	}
}

func TestWatcher(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "a/b"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".recycle"), 0o755)
	w, err := NewWatcher([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	_ = os.WriteFile(filepath.Join(root, "a/b/f.txt"), []byte("x"), 0o644)
	collect(t, w, func(c Change) bool { return c.Dir == filepath.Join(root, "a/b") && !c.Recursive })

	// A new folder is followed and copied whole.
	_ = os.MkdirAll(filepath.Join(root, "new/deep"), 0o755)
	collect(t, w, func(c Change) bool { return c.Dir == filepath.Join(root, "new") && c.Recursive })
	time.Sleep(100 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(root, "new/deep/g.txt"), []byte("y"), 0o644)
	collect(t, w, func(c Change) bool { return c.Dir == filepath.Join(root, "new/deep") })

	// Deletions are reported on the parent folder.
	_ = os.Remove(filepath.Join(root, "a/b/f.txt"))
	collect(t, w, func(c Change) bool { return c.Dir == filepath.Join(root, "a/b") })

	// The network recycle bin is ignored.
	_ = os.WriteFile(filepath.Join(root, ".recycle/z"), []byte("z"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "marker"), []byte("m"), 0o644)
	collect(t, w, func(c Change) bool {
		if c.Dir == filepath.Join(root, ".recycle") {
			t.Fatal("recycle bin followed")
		}
		return c.Dir == root
	})
}
