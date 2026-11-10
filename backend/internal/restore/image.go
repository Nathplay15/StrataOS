package restore

import (
	"context"
	"fmt"
	"io"
	"strings"

	"strataos/internal/backup"
	"strataos/internal/protect"
	"strataos/internal/store"
)

// ---- Disks of VM image backups (datastores) ----

// imageSource reads the disk backups of one VM of a task. A disk must be
// opened first (backup.OpenDisk: read back and mounted read-only); its
// file systems are then the roots ("/vda1", "/vg0-root"). Files restore to
// a folder or download: they have no place on the NAS to go back to.
type imageSource struct {
	t  store.BackupTask
	vm string
}

// ImageSource reads the VM disk backups of a datastore task.
func ImageSource(t store.BackupTask, vm string) Source { return imageSource{t, vm} }

func (s imageSource) Info() Info {
	return Info{ID: "image:" + s.t.ID + "/" + s.vm, Kind: "image", Name: s.vm, Detail: s.t.Name, DownloadDirs: true}
}

// Points are the disks of each complete backup of the VM.
func (s imageSource) Points(ctx context.Context) ([]Point, error) {
	items, err := backup.GuestBackups(ctx, s.t)
	if err != nil {
		return nil, err
	}
	out := []Point{}
	for _, it := range items {
		if it.Kind != "vm" || it.Name != s.vm || !it.Complete {
			continue
		}
		for _, pt := range it.Parts {
			if disk, ok := strings.CutPrefix(pt.Name, "disk-"); ok {
				out = append(out, Point{ID: pt.ID, Time: it.Time, Label: disk, Bytes: pt.Logical, Roots: []string{}})
			}
		}
	}
	return out, nil
}

var errNotOpen = fmt.Errorf("open the disk first")

func (s imageSource) trees(point string) ([]tree, error) {
	sess := backup.DiskSessionFor(s.t.ID, point)
	if sess == nil || sess.State != "open" {
		return nil, errNotOpen
	}
	var out []tree
	for _, r := range sess.Roots {
		out = append(out, tree{Root: r.Dir, Base: r.Path})
	}
	return out, nil
}

// Roots are the file systems of an open disk.
func (s imageSource) Roots(point string) ([]string, error) {
	ts, err := s.trees(point)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range ts {
		out = append(out, t.Base)
	}
	return out, nil
}

func (s imageSource) find(point, abs string) (tree, error) {
	ts, err := s.trees(point)
	if err != nil {
		return tree{}, err
	}
	for _, t := range ts {
		if _, ok := t.rel(abs); ok {
			return t, nil
		}
	}
	return tree{}, errOutside
}

func (s imageSource) List(ctx context.Context, point, dir string, recursive bool) ([]protect.Entry, bool, error) {
	t, err := s.find(point, dir)
	if err != nil {
		return nil, false, err
	}
	return t.list(ctx, dir, recursive, backup.MaxListing)
}

func (s imageSource) Stat(ctx context.Context, point, abs string) (protect.Entry, error) {
	t, err := s.find(point, abs)
	if err != nil {
		return protect.Entry{}, err
	}
	return t.stat(abs)
}

func (s imageSource) Stage(ctx context.Context, point string, sel []protect.Entry, scratch string, p Progress) (Locator, error) {
	if _, err := s.trees(point); err != nil {
		return nil, err
	}
	return func(abs string) (tree, bool) {
		t, err := s.find(point, abs)
		return t, err == nil
	}, nil
}

func (s imageSource) Dump(ctx context.Context, point, abs string, w io.Writer) error {
	t, err := s.find(point, abs)
	if err != nil {
		return err
	}
	return t.dump(ctx, abs, w)
}

// Rooted is a source whose roots are known only once a point is opened.
type Rooted interface {
	Roots(point string) ([]string, error)
}
