package api

import (
	"context"
	"io"
	"io/fs"
	"testing"
	"time"

	"strataos/internal/protect"
	"strataos/internal/restore"
)

type fakeSnapSource struct {
	points []restore.Point
	files  map[string]map[string]protect.Entry // point -> abs -> entry
}

func (f fakeSnapSource) Info() restore.Info {
	return restore.Info{ID: "snap:/srv/strata/main", Kind: "snapshot"}
}
func (f fakeSnapSource) Points(context.Context) ([]restore.Point, error) { return f.points, nil }
func (f fakeSnapSource) List(context.Context, string, string, bool) ([]protect.Entry, bool, error) {
	return nil, false, nil
}
func (f fakeSnapSource) Stat(_ context.Context, point, abs string) (protect.Entry, error) {
	if m, ok := f.files[point]; ok {
		if e, ok := m[abs]; ok {
			return e, nil
		}
	}
	return protect.Entry{}, fs.ErrNotExist
}
func (f fakeSnapSource) Stage(context.Context, string, []protect.Entry, string, restore.Progress) (restore.Locator, error) {
	return nil, nil
}
func (f fakeSnapSource) Dump(context.Context, string, string, io.Writer) error { return nil }

func TestVersionsOf(t *testing.T) {
	now := time.Now()
	src := fakeSnapSource{
		points: []restore.Point{
			{ID: "snap-a", Time: now.Add(-72 * time.Hour)},
			{ID: "snap-b", Time: now.Add(-48 * time.Hour)},
			{ID: "snap-c", Time: now.Add(-24 * time.Hour)},
		},
		files: map[string]map[string]protect.Entry{
			"snap-a": {"/srv/strata/main/doc.txt": {Path: "/srv/strata/main/doc.txt", Size: 10, MTime: now.Add(-72 * time.Hour)}},
			// snap-b: the file does not exist yet
			"snap-c": {"/srv/strata/main/doc.txt": {Path: "/srv/strata/main/doc.txt", Size: 42, MTime: now.Add(-24 * time.Hour)}},
		},
	}
	vs := versionsOf(context.Background(), src, src.points, "/srv/strata/main/doc.txt")
	if len(vs) != 2 {
		t.Fatalf("versions = %+v, want 2", vs)
	}
	if vs[0].Snapshot != "snap-c" || vs[0].Size != 42 {
		t.Fatalf("newest first: %+v", vs[0])
	}
	if vs[1].Snapshot != "snap-a" {
		t.Fatalf("oldest last: %+v", vs[1])
	}
	// A folder is not a version.
	src.files["snap-c"]["/srv/strata/main"] = protect.Entry{Path: "/srv/strata/main", Dir: true}
	vs = versionsOf(context.Background(), src, src.points, "/srv/strata/main")
	if len(vs) != 0 {
		t.Fatalf("a folder has no versions: %+v", vs)
	}
	// Cap.
	many := []restore.Point{}
	for i := 0; i < maxFileVersions+10; i++ {
		id := "s" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		many = append(many, restore.Point{ID: id, Time: now.Add(time.Duration(i) * time.Hour)})
		src.files[id] = map[string]protect.Entry{"/f": {Path: "/f", Size: 1}}
	}
	if got := len(versionsOf(context.Background(), src, many, "/f")); got != maxFileVersions {
		t.Fatalf("cap = %d, want %d", got, maxFileVersions)
	}
}
