package ondemand

import (
	"errors"
	"reflect"
	"testing"

	"strataos-sync/internal/engine"
)

func TestRanges(t *testing.T) {
	var r Ranges
	r = r.Add(10, 20).Add(30, 40).Add(20, 25) // contact merges
	if want := (Ranges{{10, 25}, {30, 40}}); !reflect.DeepEqual(r, want) {
		t.Fatalf("add: %v", r)
	}
	r = r.Add(0, 5).Add(24, 31) // bridges the two
	if want := (Ranges{{0, 5}, {10, 40}}); !reflect.DeepEqual(r, want) {
		t.Fatalf("bridge: %v", r)
	}
	if !r.Has(12, 39) || r.Has(4, 11) || !r.Has(7, 7) {
		t.Fatal("has")
	}
	if r.Total() != 35 || r.Complete(40) || !(Ranges{{0, 40}}).Complete(40) || !(Ranges(nil)).Complete(0) {
		t.Fatal("total/complete")
	}
	if got := r.Truncate(12); !reflect.DeepEqual(got, Ranges{{0, 5}, {10, 12}}) {
		t.Fatalf("truncate: %v", got)
	}
	if got := r.Add(5, 5); !reflect.DeepEqual(got, r) {
		t.Fatal("empty add")
	}
}

func TestMissing(t *testing.T) {
	r := Ranges{{4096, 8192}}
	// Holes widened to 4 KiB blocks and clipped at the end of the file.
	if got := r.Missing(100, 9000, 10000, 4096, 0); !reflect.DeepEqual(got, Ranges{{0, 4096}, {8192, 10000}}) {
		t.Fatalf("aligned: %v", got)
	}
	// Close holes join into one request.
	if got := r.Missing(0, 10000, 10000, 4096, 4096); !reflect.DeepEqual(got, Ranges{{0, 10000}}) {
		t.Fatalf("joined: %v", got)
	}
	if got := r.Missing(4096, 8192, 10000, 4096, 0); got != nil {
		t.Fatalf("present: %v", got)
	}
	if got := r.Missing(20000, 30000, 10000, 4096, 0); got != nil {
		t.Fatalf("past the end: %v", got)
	}
	if got := (Ranges(nil)).Missing(5000, 5001, 1<<20, 256<<10, 0); !reflect.DeepEqual(got, Ranges{{0, 256 << 10}}) {
		t.Fatalf("one block: %v", got)
	}
	// Windows: every range a multiple of 4 KiB except the one ending the file.
	size := int64(3*4096 + 100)
	for _, m := range (Ranges{{4096, 5000}}).Missing(0, size, size, 4096, 0) {
		if m.Off%4096 != 0 || (m.End != size && m.End%4096 != 0) {
			t.Fatalf("unaligned %v", m)
		}
	}
	if got := (Range{0, 10}).Split(4); !reflect.DeepEqual(got, []Range{{0, 4}, {4, 8}, {8, 10}}) {
		t.Fatalf("split: %v", got)
	}
}

func TestStateMachine(t *testing.T) {
	f := &FileState{Version: "v1", Size: 100}
	if f.Status() != StCloud {
		t.Fatal(f.Status())
	}
	f.Apply(Event{Kind: EvFilled, Off: 0, End: 50})
	if f.Status() != StPartial {
		t.Fatal(f.Status())
	}
	if act, _ := f.Apply(Event{Kind: EvPin}); act != ActHydrate || f.Status() != StPinning {
		t.Fatal("pin", act, f.Status())
	}
	f.Apply(Event{Kind: EvFilled, Off: 50, End: 200}) // clipped to the size
	if f.Status() != StPinned || f.Ranges.Total() != 100 {
		t.Fatal(f.Status(), f.Ranges)
	}
	if _, err := f.Apply(Event{Kind: EvEvict}); !errors.Is(err, ErrKeptHere) {
		t.Fatal("evicted a pinned file")
	}
	// The NAS changes a pinned file: drop and download the new version.
	if act, _ := f.Apply(Event{Kind: EvRemoteChanged, Version: "v2", Size: 120}); act != ActDrop|ActHydrate || f.Ranges != nil || f.Size != 120 {
		t.Fatal("remote change of a pinned file", act)
	}
	if act, _ := f.Apply(Event{Kind: EvRemoteChanged, Version: "v2", Size: 120}); act != 0 {
		t.Fatal("same version again", act)
	}
	f.Apply(Event{Kind: EvFilled, Off: 0, End: 120})
	if act, _ := f.Apply(Event{Kind: EvUnpin}); act != ActDrop || f.Status() != StCloud {
		t.Fatal("unpin", act, f.Status())
	}

	// Writing: open, write, close sends; an upload of an older write keeps it dirty.
	f.Apply(Event{Kind: EvFilled, Off: 0, End: 120})
	f.Apply(Event{Kind: EvOpen})
	f.Apply(Event{Kind: EvWrite, Off: 100, End: 150})
	if f.Status() != StDirty || f.Size != 150 {
		t.Fatal("write", f.Status(), f.Size)
	}
	if _, err := f.Apply(Event{Kind: EvEvict}); !errors.Is(err, ErrNotSent) {
		t.Fatal("evicted a changed file")
	}
	if _, err := f.Apply(Event{Kind: EvUnpin}); !errors.Is(err, ErrNotSent) {
		t.Fatal("freed a changed file")
	}
	if act, _ := f.Apply(Event{Kind: EvClose}); act != ActUpload {
		t.Fatal("close", act)
	}
	gen := f.Gen
	f.Apply(Event{Kind: EvOpen})
	f.Apply(Event{Kind: EvTruncate, Size: 10})
	f.Apply(Event{Kind: EvClose})
	if act, _ := f.Apply(Event{Kind: EvUploaded, Version: "v3", Gen: gen}); act != ActUpload || !f.Dirty {
		t.Fatal("older upload", act)
	}
	if act, _ := f.Apply(Event{Kind: EvUploaded, Version: "v4", Gen: f.Gen}); act != 0 || f.Dirty || f.Status() != StLocal || f.Size != 10 {
		t.Fatal("upload", act, f.Status())
	}

	// Both sides changed: a conflict copy, then the NAS version.
	f.Apply(Event{Kind: EvWrite, Off: 0, End: 5})
	if act, _ := f.Apply(Event{Kind: EvRemoteChanged, Version: "v5", Size: 7}); act != ActConflict|ActDrop || f.Dirty || f.Version != "v5" {
		t.Fatal("conflict", act)
	}
	// Changed here, deleted there: sent again as a new file.
	f.Apply(Event{Kind: EvWrite, Off: 0, End: 5})
	if act, _ := f.Apply(Event{Kind: EvRemoteDeleted}); act != ActUpload || f.Version != "" {
		t.Fatal("deleted there", act)
	}
	g := &FileState{Version: "a", Size: 3, Ranges: Ranges{{0, 3}}}
	if act, _ := g.Apply(Event{Kind: EvRemoteDeleted}); act != ActDrop|ActForget {
		t.Fatal("deleted", act)
	}
	g = &FileState{Version: "a", Size: 3, Ranges: Ranges{{0, 3}}, Open: 1}
	if _, err := g.Apply(Event{Kind: EvEvict}); !errors.Is(err, ErrBusy) || g.Ranges == nil {
		t.Fatal("evicted an open file")
	}
}

func TestPickEvictions(t *testing.T) {
	files := map[string]*FileState{
		"old":    {Size: 100, Ranges: Ranges{{0, 100}}, Used: 1},
		"older":  {Size: 50, Ranges: Ranges{{0, 50}}, Used: 0},
		"new":    {Size: 300, Ranges: Ranges{{0, 300}}, Used: 9},
		"pinned": {Size: 500, Ranges: Ranges{{0, 500}}, Used: 0, Pinned: true},
		"dirty":  {Size: 500, Ranges: Ranges{{0, 500}}, Used: 0, Dirty: true},
		"open":   {Size: 500, Ranges: Ranges{{0, 500}}, Used: 0, Open: 1},
		"cloud":  {Size: 500, Used: 0},
	}
	if got := PickEvictions(files, 1950, 2000, 0.8); got != nil {
		t.Fatalf("under budget: %v", got)
	}
	// 1950 used, budget 1800, low water 1440: frees "older" (50) then "old" (100) … then "new".
	if got := PickEvictions(files, 1950, 1800, 0.8); !reflect.DeepEqual(got, []string{"older", "old", "new"}) {
		t.Fatalf("lru: %v", got)
	}
	if got := PickEvictions(files, 1950, 1900, 1); !reflect.DeepEqual(got, []string{"older"}) {
		t.Fatalf("just enough: %v", got)
	}
	if b := DefaultBudget(1000<<30, 900<<30); b != 50<<30 {
		t.Fatal(b)
	}
	if b := DefaultBudget(100<<30, 3<<30); b != 1536<<20 {
		t.Fatal(b)
	}
}

func TestTree(t *testing.T) {
	tr := NewTree()
	e := func(p string, dir bool, v string) engine.RemoteEntry {
		return engine.RemoteEntry{Path: p, Dir: dir, Version: v, Size: 1}
	}
	ch := tr.Apply(engine.Changes{Cursor: "c1", Entries: []engine.RemoteEntry{
		e("a/b/c.txt", false, "1"), e("a", true, ""), e("a/b", true, ""), e("x.txt", false, "1"),
		{Path: "vault", Dir: true, E2E: true}, e("vault/QUJD", false, "1"),
	}}, true)
	if len(ch) != 4 || tr.Cursor() != "c1" {
		t.Fatalf("first listing: %v", ch)
	}
	l, _ := tr.List("")
	if len(l) != 2 || l[0].Path != "a" || l[1].Path != "x.txt" {
		t.Fatalf("root: %v", l)
	}
	if !reflect.DeepEqual(tr.Hidden(), []string{"vault"}) {
		t.Fatal("encrypted folder shown")
	}
	if l, ok := tr.List("a/b"); !ok || len(l) != 1 || l[0].Path != "a/b/c.txt" {
		t.Fatalf("a/b: %v", l)
	}
	// A change, a deletion of a folder (and what is below), a local file kept.
	tr.Put(Node{RemoteEntry: engine.RemoteEntry{Path: "new.txt"}, Local: true})
	ch = tr.Apply(engine.Changes{Cursor: "c2", Entries: []engine.RemoteEntry{e("x.txt", false, "2")}, Deleted: []string{"a"}}, false)
	kinds := map[string]ChangeKind{}
	for _, c := range ch {
		kinds[c.Path] = c.Kind
	}
	if kinds["x.txt"] != Modified || kinds["a/b/c.txt"] != Removed || kinds["a"] != Removed || len(ch) != 4 {
		t.Fatalf("changes: %v", ch)
	}
	// A reset listing drops what it does not list, except local files.
	ch = tr.Apply(engine.Changes{Cursor: "c3", Entries: []engine.RemoteEntry{e("y", true, "")}}, true)
	if _, ok := tr.Get("x.txt"); ok {
		t.Fatal("x.txt kept after reset")
	}
	if n, ok := tr.Get("new.txt"); !ok || !n.Local {
		t.Fatal("local file dropped")
	}
	if len(tr.Hidden()) != 0 {
		t.Fatal("hidden kept after reset")
	}
	tr.Apply(engine.Changes{Entries: []engine.RemoteEntry{e("y/z", false, "1")}}, false)
	tr.Move("y", "w")
	if _, ok := tr.Get("w/z"); !ok {
		t.Fatal("move")
	}
	if _, ok := tr.Get("y"); ok {
		t.Fatal("moved folder still there")
	}
	// Saved and restored.
	nodes, hidden, cur := tr.Snapshot()
	t2 := NewTree()
	t2.Restore(nodes, hidden, cur)
	a, _ := tr.List("w")
	b, _ := t2.List("w")
	if !reflect.DeepEqual(a, b) || t2.Cursor() != "c3" {
		t.Fatal("restore")
	}
	if p, err := CleanPath(`Share\a/b`); err != nil || p != "Share/a/b" {
		t.Fatal(p, err)
	}
	if _, err := CleanPath("Share/../etc"); err == nil {
		t.Fatal("dot dot")
	}
}
