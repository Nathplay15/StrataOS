package storagetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMeasureSharesHardLinks(t *testing.T) {
	root := t.TempDir()
	mk := func(p string, n int) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, b, nested := filepath.Join(root, "A"), filepath.Join(root, "B"), filepath.Join(root, "A", "Inner")
	mk(filepath.Join(a, "big"), 100000)
	mk(filepath.Join(a, ".recycle", "old"), 5000)
	mk(filepath.Join(a, "x", ".snapshots", "s"), 3000)
	mk(filepath.Join(nested, "n"), 20000)
	mk(filepath.Join(b, "own"), 7000)
	// The same file in both shares, and twice in B.
	if err := os.Link(filepath.Join(a, "big"), filepath.Join(b, "big-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(b, "own"), filepath.Join(b, "own-again")); err != nil {
		t.Fatal(err)
	}
	got, err := MeasureShares(context.Background(), []string{b, nested, a})
	if err != nil {
		t.Fatal(err)
	}
	dirs := func(p string) int64 { // apparent sizes of the folders walked
		var n int64
		_ = filepath.WalkDir(p, func(q string, d os.DirEntry, _ error) error {
			if d.IsDir() && (d.Name() == ".recycle" || d.Name() == ".snapshots" || (q == nested && p != nested)) {
				return filepath.SkipDir
			}
			if d.IsDir() {
				fi, _ := d.Info()
				n += fi.Size()
			}
			return nil
		})
		return n
	}
	if want := 100000 + dirs(a); got[a] != want {
		t.Errorf("A = %d, want %d (big once, inner share, recycle bin and snapshots left out)", got[a], want)
	}
	if want := 7000 + dirs(b); got[b] != want {
		t.Errorf("B = %d, want %d (the link of A's file and the second link not counted)", got[b], want)
	}
	if want := 20000 + dirs(nested); got[nested] != want {
		t.Errorf("Inner = %d, want %d", got[nested], want)
	}
	// Stopped: nothing measured.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, _ := MeasureShares(ctx, []string{a}); len(got) != 0 {
		t.Errorf("measured after the end: %v", got)
	}
}
