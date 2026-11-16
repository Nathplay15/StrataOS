package restore

import (
	"context"
	"fmt"
	"path"
	"sort"
	"time"

	"strataos/internal/protect"
)

// DiffEntry is one path that differs between two points in time of a source.
type DiffEntry struct {
	Path string `json:"path"` // original path (inside its root)
	Name string `json:"name"`
	Dir  bool   `json:"dir,omitempty"`
	// Change: added (only in the newer point), deleted (only in the older
	// one), modified (present in both, size or date changed).
	Change string    `json:"change"`
	Size   int64     `json:"size,omitempty"`
	MTime  time.Time `json:"mtime,omitempty"`
}

// Diff compares two points in time of a source: what was added, removed and
// modified between from and to, sorted by path. At most max changes come
// back; truncated says the listing did not see everything (listing caps).
func Diff(ctx context.Context, s Source, from, to string, max int) (out []DiffEntry, truncated bool, err error) {
	old, trunc1, err := entriesOf(ctx, s, from)
	if err != nil {
		return nil, false, err
	}
	new, trunc2, err := entriesOf(ctx, s, to)
	if err != nil {
		return nil, false, err
	}
	truncated = trunc1 || trunc2
	for rel, e := range new {
		o, was := old[rel]
		change := "added"
		if was {
			// A folder present in both is never a change: its size and date
			// move whenever something inside does, which the files below
			// already report.
			if e.Dir || (o.Dir == e.Dir && o.Size == e.Size && o.MTime.Equal(e.MTime)) {
				continue
			}
			change = "modified"
		}
		out = append(out, entry(rel, e, change))
	}
	for rel, e := range old {
		if _, is := new[rel]; !is {
			out = append(out, entry(rel, e, "deleted"))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > max {
		out, truncated = out[:max], true
	}
	if out == nil {
		out = []DiffEntry{}
	}
	return out, truncated, nil
}

func entry(rel string, e protect.Entry, change string) DiffEntry {
	return DiffEntry{Path: rel, Name: path.Base(rel), Dir: e.Dir, Change: change, Size: e.Size, MTime: e.MTime}
}

// entriesOf lists a point recursively, every root of it merged by relative
// path (a point may hold several source folders).
func entriesOf(ctx context.Context, s Source, point string) (map[string]protect.Entry, bool, error) {
	pts, err := s.Points(ctx)
	if err != nil {
		return nil, false, err
	}
	var roots []string
	for _, p := range pts {
		if p.ID == point {
			roots = p.Roots
		}
	}
	if roots == nil {
		return nil, false, fmt.Errorf("snapshot %s not found", point)
	}
	m := map[string]protect.Entry{}
	truncated := false
	for _, root := range roots {
		list, trunc, err := s.List(ctx, point, root, true)
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || trunc
		for _, e := range list {
			rel, ok := tree{Base: root}.rel(e.Path)
			if !ok || rel == "." {
				continue
			}
			m[rel] = e
		}
	}
	return m, truncated, nil
}
