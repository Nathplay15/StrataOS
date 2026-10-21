package storagetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTopN(t *testing.T) {
	tn := newTopN(3)
	for i, sz := range []int64{5, 1, 9, 3, 7, 2, 8} {
		tn.push(FileEntry{Path: fmt.Sprint(i), Size: sz})
	}
	got := tn.sorted()
	if len(got) != 3 || got[0].Size != 9 || got[1].Size != 8 || got[2].Size != 7 {
		t.Fatalf("top 3: %+v", got)
	}
	if len(newTopN(0).sorted()) != 0 {
		t.Fatal("empty top")
	}
}

func TestCategory(t *testing.T) {
	for name, want := range map[string]string{"a.MKV": CatVideo, "b.jpeg": CatPhotos, "c.flac": CatMusic, "d.pdf": CatDocuments, "e.tar": CatArchives, "f.iso": CatImages, "g.qcow2": CatImages, "h": CatOther, "i.xyz": CatOther} {
		if got := Category(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

// The aggregation alone: folders beyond the depth or the node budget are
// counted in their nearest kept ancestor.
func TestAggregatorDepthAndBudget(t *testing.T) {
	a := newAggregator(ScanOpts{Depth: 2, MaxNodes: 4, TopN: 2})
	l1 := a.enter(a.root, "a", 1)
	l2 := a.enter(l1, "b", 2)
	l3 := a.enter(l2, "c", 3) // too deep: counted in b
	if l3 != l2 {
		t.Fatal("depth 3 must fold into depth 2")
	}
	a.file(l3, "a/b/c/x.mp4", 100, 100, time.Now())
	a.file(l1, "a/y.txt", 10, 10, time.Now())
	other := a.enter(a.root, "z", 1) // 4th node
	full := a.enter(other, "w", 2)   // budget reached: folds into z
	if full != other {
		t.Fatal("node budget must fold into parent")
	}
	a.file(full, "z/w/old.iso", 50, 50, time.Now().AddDate(-2, 0, 0))
	r := a.report("/x")
	if r.Tree.Size != 160 || r.Files != 3 {
		t.Fatalf("root: %+v", r.Tree)
	}
	sub := r.Subtree("a", 5, 10)
	if sub.Size != 110 || len(sub.Children) != 1 || sub.Children[0].Size != 100 || sub.Children[0].Children != nil {
		t.Fatalf("a: %+v", sub)
	}
	if r.Subtree("z/w", 1, 1) != nil {
		t.Fatal("folded folder must not exist")
	}
	if r.OldFiles != 1 || r.OldSize != 50 || len(r.Old) != 1 || r.Old[0].Path != "z/w/old.iso" {
		t.Fatalf("old: %+v", r.Old)
	}
	if len(r.Largest) != 2 || r.Largest[0].Size != 100 || r.Largest[1].Size != 50 {
		t.Fatalf("largest: %+v", r.Largest)
	}
	if r.Types[0].Category != CatVideo || r.Types[0].Size != 100 {
		t.Fatalf("types: %+v", r.Types)
	}
	// Trimming: one level and one child per folder.
	tr := r.Subtree("", 1, 1)
	if len(tr.Children) != 1 || !tr.More || tr.Children[0].Children != nil || !tr.Children[0].More {
		t.Fatalf("trim: %+v", tr)
	}
}

func write(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i % 251)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyze(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "Videos/film.mkv"), 300000)
	write(t, filepath.Join(root, "Videos/deep/er/still/clip.mp4"), 100000)
	write(t, filepath.Join(root, "Docs/a.pdf"), 20000)
	write(t, filepath.Join(root, "old.iso"), 50000)
	old := time.Now().AddDate(-3, 0, 0)
	_ = os.Chtimes(filepath.Join(root, "old.iso"), old, old)
	// A hard link is counted once, a symbolic link not at all.
	if err := os.Link(filepath.Join(root, "Docs/a.pdf"), filepath.Join(root, "Docs/b.pdf")); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink("/etc", filepath.Join(root, "etc"))
	// Btrfs snapshots at the root are left out.
	write(t, filepath.Join(root, ".snapshots/s1/film.mkv"), 300000)

	r, err := Analyze(context.Background(), root, ScanOpts{Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 4 || r.Apparent != 470000 {
		t.Fatalf("files %d apparent %d", r.Files, r.Apparent)
	}
	if r.OldFiles != 1 || r.Old[0].Path != "old.iso" {
		t.Fatalf("old: %+v", r.Old)
	}
	if r.Largest[0].Path != "Videos/film.mkv" {
		t.Fatalf("largest: %+v", r.Largest)
	}
	v := r.Subtree("Videos", 3, 10)
	if v == nil || v.Files != 2 || len(v.Children) != 1 || v.Children[0].Name != "deep" || v.Children[0].Children != nil {
		t.Fatalf("Videos: %+v", v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, root, ScanOpts{}); err == nil {
		t.Fatal("cancelled scan must fail")
	}
}
