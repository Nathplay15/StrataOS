package restore

import (
	"context"
	"io"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"strataos/internal/protect"
)

// fakeSource is a Source holding two points whose contents are given as
// "path size mtime" per point (dirs end with /).
type fakeSource struct {
	points    []Point
	roots     []string
	files     map[string]map[string]protect.Entry // point → path → entry
	truncated bool
}

func (f *fakeSource) Info() Info { return Info{ID: "fake", Kind: "snapshot"} }
func (f *fakeSource) Points(context.Context) ([]Point, error) {
	return f.points, nil
}
func (f *fakeSource) List(_ context.Context, point, dir string, recursive bool) ([]protect.Entry, bool, error) {
	out := []protect.Entry{}
	for p, e := range f.files[point] {
		if p == dir || (recursive && strings.HasPrefix(p, dir+"/")) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, f.truncated, nil
}
func (f *fakeSource) Stat(context.Context, string, string) (protect.Entry, error) {
	return protect.Entry{}, nil
}
func (f *fakeSource) Stage(context.Context, string, []protect.Entry, string, Progress) (Locator, error) {
	return nil, nil
}
func (f *fakeSource) Dump(context.Context, string, string, io.Writer) error { return nil }

func mkEntry(p string, size int64, mtime time.Time) protect.Entry {
	return protect.Entry{Path: p, Name: path.Base(p), Dir: strings.HasSuffix(p, "/"), Size: size, MTime: mtime}
}

func TestDiff(t *testing.T) {
	t1 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	f := &fakeSource{
		points: []Point{{ID: "a", Time: t1, Roots: []string{"/srv/strata/data"}}, {ID: "b", Time: t2, Roots: []string{"/srv/strata/data"}}},
		files: map[string]map[string]protect.Entry{
			"a": {"/srv/strata/data": mkEntry("/srv/strata/data/", 0, t1),
				"/srv/strata/data/kept.txt":  mkEntry("/srv/strata/data/kept.txt", 10, t1),
				"/srv/strata/data/gone.txt":  mkEntry("/srv/strata/data/gone.txt", 20, t1),
				"/srv/strata/data/changed":   mkEntry("/srv/strata/data/changed/", 0, t1),
				"/srv/strata/data/changed/f": mkEntry("/srv/strata/data/changed/f", 5, t1)},
			"b": {"/srv/strata/data": mkEntry("/srv/strata/data/", 0, t2),
				"/srv/strata/data/kept.txt":  mkEntry("/srv/strata/data/kept.txt", 10, t1),
				"/srv/strata/data/changed":   mkEntry("/srv/strata/data/changed/", 0, t2),
				"/srv/strata/data/changed/f": mkEntry("/srv/strata/data/changed/f", 7, t2),
				"/srv/strata/data/new.txt":   mkEntry("/srv/strata/data/new.txt", 3, t2)},
		},
	}
	changes, truncated, err := Diff(context.Background(), f, "a", "b", 100)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("nothing was truncated")
	}
	want := map[string]string{
		"new.txt":   "added",
		"gone.txt":  "deleted",
		"changed/f": "modified",
	}
	if len(changes) != len(want) {
		t.Fatalf("changes: %v", changes)
	}
	for _, c := range changes {
		if want[c.Path] != c.Change {
			t.Fatalf("%s: got %s, want %s", c.Path, c.Change, want[c.Path])
		}
		if c.Name != path.Base(c.Path) {
			t.Fatalf("name of %s: %s", c.Path, c.Name)
		}
	}
	// The deleted file reports the size it had.
	for _, c := range changes {
		if c.Change == "deleted" && c.Size != 20 {
			t.Fatalf("deleted size: %v", c)
		}
		if c.Change == "added" && c.Size != 3 {
			t.Fatalf("added size: %v", c)
		}
	}
	// A directory that only saw its date move is not a change.
	for _, c := range changes {
		if c.Dir {
			t.Fatalf("directory reported as a change: %v", c)
		}
	}
}

func TestDiffUnknownPointAndCap(t *testing.T) {
	t1 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	files := map[string]protect.Entry{}
	for i := 0; i < 10; i++ {
		p := "/srv/x/f" + string(rune('a'+i))
		files[p] = mkEntry(p, int64(i), t1)
	}
	f := &fakeSource{
		points: []Point{{ID: "a", Roots: []string{"/srv/x"}}, {ID: "b", Roots: []string{"/srv/x"}}},
		files:  map[string]map[string]protect.Entry{"a": files, "b": {}},
	}
	if _, _, err := Diff(context.Background(), f, "a", "nope", 100); err == nil {
		t.Fatal("unknown point accepted")
	}
	changes, truncated, err := Diff(context.Background(), f, "a", "b", 4)
	if err != nil || !truncated || len(changes) != 4 {
		t.Fatalf("cap: %d %v %v", len(changes), truncated, err)
	}
}
