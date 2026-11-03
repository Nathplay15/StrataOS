package restore

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"strataos/internal/backup"
	"strataos/internal/cloudsync"
	"strataos/internal/protect"
	"strataos/internal/storage"
	"strataos/internal/store"
)

// Point is a point in time of a source.
type Point struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Label string    `json:"label,omitempty"`
	Files int       `json:"files,omitempty"`
	Bytes int64     `json:"bytes,omitempty"`
	Roots []string  `json:"roots"` // top folders it holds, original paths
}

// Info describes a source for the wizard.
type Info struct {
	ID     string `json:"id"`   // snap:<mount> | lvm:<vg>/<lv> | backup:<id> | cloud:<id>
	Kind   string `json:"kind"` // snapshot | backup | cloud
	Name   string `json:"name"`
	Detail string `json:"detail"`
	// Download: single files can be downloaded; folders too when DownloadDirs.
	DownloadDirs bool `json:"downloadDirs"`
}

// Locator says where an original path can be read locally after Stage.
type Locator func(abs string) (tree, bool)

// Source is something a restore reads from.
type Source interface {
	Info() Info
	Points(ctx context.Context) ([]Point, error)
	// List lists the folder dir of a point (everything below when recursive).
	List(ctx context.Context, point, dir string, recursive bool) ([]protect.Entry, bool, error)
	// Stat describes one path of a point.
	Stat(ctx context.Context, point, abs string) (protect.Entry, error)
	// Stage makes the selected paths readable locally (scratch is an empty
	// folder it may extract into).
	Stage(ctx context.Context, point string, sel []protect.Entry, scratch string, p Progress) (Locator, error)
	// Dump writes a file, or a folder as a zip, to w.
	Dump(ctx context.Context, point, abs string, w io.Writer) error
}

// Progress reports a percentage and log lines.
type Progress struct {
	Set func(float64)
	Log func(string, ...any)
}

// statFromParent finds abs in the listing of its parent folder (sources
// that cannot stat one path).
func statFromParent(ctx context.Context, s Source, point, abs string, roots []string) (protect.Entry, error) {
	if slices.Contains(roots, abs) {
		return protect.Entry{Path: abs, Name: path.Base(abs), Dir: true}, nil
	}
	list, _, err := s.List(ctx, point, path.Dir(abs), false)
	if err != nil {
		return protect.Entry{}, err
	}
	for _, e := range list {
		if e.Path == abs {
			return e, nil
		}
	}
	return protect.Entry{}, fs.ErrNotExist
}

// ---- Btrfs and ZFS snapshots of a volume ----

type snapSource struct{ mp string }

// SnapshotSource reads the snapshots of the Btrfs volume or ZFS dataset
// mounted at mp.
func SnapshotSource(mp string) Source { return snapSource{mp} }

func (s snapSource) Info() Info {
	return Info{ID: "snap:" + s.mp, Kind: "snapshot", Name: path.Base(s.mp), Detail: s.mp, DownloadDirs: true}
}

func (s snapSource) trees() []storage.SnapshotTree {
	var out []storage.SnapshotTree
	for _, t := range storage.SnapshotTrees(s.mp) {
		// Only this volume's (a nested ZFS dataset has its own source).
		if t.Base == s.mp || strings.HasPrefix(t.Base, s.mp+"/") {
			out = append(out, t)
		}
	}
	return out
}

func (s snapSource) Points(ctx context.Context) ([]Point, error) {
	out := []Point{}
	for _, t := range s.trees() {
		out = append(out, Point{ID: t.Name, Time: t.Time, Label: t.Name, Roots: []string{t.Base}})
	}
	return out, nil
}

func (s snapSource) tree(point string) (tree, error) {
	for _, t := range s.trees() {
		if t.Name == point {
			return tree{Root: t.Root, Base: t.Base}, nil
		}
	}
	return tree{}, fmt.Errorf("snapshot %s not found", point)
}

func (s snapSource) List(ctx context.Context, point, dir string, recursive bool) ([]protect.Entry, bool, error) {
	t, err := s.tree(point)
	if err != nil {
		return nil, false, err
	}
	return t.list(ctx, dir, recursive, backup.MaxListing)
}

func (s snapSource) Stat(ctx context.Context, point, abs string) (protect.Entry, error) {
	t, err := s.tree(point)
	if err != nil {
		return protect.Entry{}, err
	}
	return t.stat(abs)
}

func (s snapSource) Stage(ctx context.Context, point string, sel []protect.Entry, scratch string, p Progress) (Locator, error) {
	t, err := s.tree(point)
	if err != nil {
		return nil, err
	}
	return func(string) (tree, bool) { return t, true }, nil
}

func (s snapSource) Dump(ctx context.Context, point, abs string, w io.Writer) error {
	t, err := s.tree(point)
	if err != nil {
		return err
	}
	return t.dump(ctx, abs, w)
}

// ---- LVM thin snapshots ----

type thinSource struct{ vg, lv, mp string }

// ThinSource reads the thin snapshots of vg/lv, mounted at mp.
func ThinSource(vg, lv, mp string) Source { return thinSource{vg, lv, mp} }

func (s thinSource) Info() Info {
	return Info{ID: "lvm:" + s.vg + "/" + s.lv, Kind: "snapshot", Name: s.lv, Detail: s.mp + " (LVM " + s.vg + "/" + s.lv + ")", DownloadDirs: true}
}

func (s thinSource) Points(ctx context.Context) ([]Point, error) {
	names, err := storage.ThinSnapshotNames(s.vg, s.lv)
	if err != nil {
		return nil, err
	}
	out := []Point{}
	for _, n := range names {
		at, err := time.ParseInLocation("20060102-150405", n[max(0, len(n)-15):], time.Local)
		if err != nil {
			at = time.Time{}
		}
		out = append(out, Point{ID: n, Time: at, Label: n, Roots: []string{s.mp}})
	}
	slices.SortFunc(out, func(a, b Point) int { return b.Time.Compare(a.Time) })
	return out, nil
}

func (s thinSource) tree(point string) (tree, error) {
	names, err := storage.ThinSnapshotNames(s.vg, s.lv)
	if err != nil || !slices.Contains(names, point) {
		return tree{}, fmt.Errorf("snapshot %s not found", point)
	}
	dir, err := storage.MountThinSnapshot(s.vg, point)
	if err != nil {
		return tree{}, err
	}
	return tree{Root: dir, Base: s.mp}, nil
}

func (s thinSource) List(ctx context.Context, point, dir string, recursive bool) ([]protect.Entry, bool, error) {
	t, err := s.tree(point)
	if err != nil {
		return nil, false, err
	}
	return t.list(ctx, dir, recursive, backup.MaxListing)
}

func (s thinSource) Stat(ctx context.Context, point, abs string) (protect.Entry, error) {
	t, err := s.tree(point)
	if err != nil {
		return protect.Entry{}, err
	}
	return t.stat(abs)
}

func (s thinSource) Stage(ctx context.Context, point string, sel []protect.Entry, scratch string, p Progress) (Locator, error) {
	t, err := s.tree(point)
	if err != nil {
		return nil, err
	}
	return func(string) (tree, bool) { return t, true }, nil
}

func (s thinSource) Dump(ctx context.Context, point, abs string, w io.Writer) error {
	t, err := s.tree(point)
	if err != nil {
		return err
	}
	return t.dump(ctx, abs, w)
}

// ---- File copy (rsync) to a local folder ----

type rsyncSource struct {
	t      store.BackupTask
	lastOK time.Time
}

func (s rsyncSource) Info() Info {
	return Info{ID: "backup:" + s.t.ID, Kind: "backup", Name: s.t.Name, Detail: s.t.DestPath, DownloadDirs: true}
}

// trees maps each source folder to its copy (DestPath/<name of the folder>).
func (s rsyncSource) trees() []tree {
	var out []tree
	for _, src := range s.t.Sources {
		out = append(out, tree{Root: filepath.Join(s.t.DestPath, filepath.Base(src)), Base: src})
	}
	return out
}

func (s rsyncSource) find(abs string) (tree, bool) {
	for _, t := range s.trees() {
		if _, ok := t.rel(abs); ok {
			return t, true
		}
	}
	return tree{}, false
}

func (s rsyncSource) Points(ctx context.Context) ([]Point, error) {
	if s.lastOK.IsZero() {
		return []Point{}, nil
	}
	return []Point{{ID: "latest", Time: s.lastOK, Label: "Last copy", Roots: slices.Clone(s.t.Sources)}}, nil
}

func (s rsyncSource) List(ctx context.Context, point, dir string, recursive bool) ([]protect.Entry, bool, error) {
	t, ok := s.find(dir)
	if !ok {
		return nil, false, errOutside
	}
	return t.list(ctx, dir, recursive, backup.MaxListing)
}

func (s rsyncSource) Stat(ctx context.Context, point, abs string) (protect.Entry, error) {
	t, ok := s.find(abs)
	if !ok {
		return protect.Entry{}, errOutside
	}
	return t.stat(abs)
}

func (s rsyncSource) Stage(ctx context.Context, point string, sel []protect.Entry, scratch string, p Progress) (Locator, error) {
	return s.find, nil
}

func (s rsyncSource) Dump(ctx context.Context, point, abs string, w io.Writer) error {
	t, ok := s.find(abs)
	if !ok {
		return errOutside
	}
	return t.dump(ctx, abs, w)
}

// ---- Versioned backup (restic) ----

type resticSource struct{ t store.BackupTask }

func (s resticSource) Info() Info {
	return Info{ID: "backup:" + s.t.ID, Kind: "backup", Name: s.t.Name, Detail: s.t.DestType, DownloadDirs: true}
}

func (s resticSource) Points(ctx context.Context) ([]Point, error) {
	snaps, err := backup.Snapshots(ctx, s.t)
	if err != nil {
		return nil, err
	}
	out := []Point{}
	for _, sn := range snaps {
		p := Point{ID: sn.ID, Time: sn.Time, Label: sn.ShortID, Roots: sn.Paths}
		if sn.Summary != nil {
			p.Files, p.Bytes = sn.Summary.TotalFilesProcessed, sn.Summary.TotalBytesProcessed
		}
		out = append(out, p)
	}
	return out, nil
}

func (s resticSource) List(ctx context.Context, point, dir string, recursive bool) ([]protect.Entry, bool, error) {
	return backup.ResticList(ctx, s.t, point, dir, recursive)
}

func (s resticSource) Stat(ctx context.Context, point, abs string) (protect.Entry, error) {
	pts, err := s.Points(ctx)
	if err != nil {
		return protect.Entry{}, err
	}
	var roots []string
	for _, p := range pts {
		if p.ID == point {
			roots = p.Roots
		}
	}
	return statFromParent(ctx, s, point, abs, roots)
}

func (s resticSource) Stage(ctx context.Context, point string, sel []protect.Entry, scratch string, p Progress) (Locator, error) {
	var paths []string
	for _, e := range sel {
		paths = append(paths, e.Path)
	}
	p.Log("Extracting %d item(s) from the repository", len(paths))
	if err := backup.ResticExtract(ctx, s.t, point, paths, scratch, true, backup.Progress{Set: p.Set, Log: p.Log}); err != nil {
		return nil, err
	}
	// restic recreates the absolute paths under the target.
	t := tree{Root: scratch, Base: ""}
	return func(abs string) (tree, bool) { return t, true }, nil
}

func (s resticSource) Dump(ctx context.Context, point, abs string, w io.Writer) error {
	e, err := s.Stat(ctx, point, abs)
	if err != nil {
		return err
	}
	return backup.ResticDump(ctx, s.t, point, abs, e.Dir, w)
}

// ---- Cloud copy (rclone upload) ----

type cloudSource struct {
	t      store.CloudSync
	lastOK time.Time
}

func (s cloudSource) Info() Info {
	return Info{ID: "cloud:" + s.t.ID, Kind: "cloud", Name: s.t.Name, Detail: s.t.Provider + ":" + s.t.RemotePath}
}

func (s cloudSource) Points(ctx context.Context) ([]Point, error) {
	if s.lastOK.IsZero() {
		return []Point{}, nil
	}
	return []Point{{ID: "latest", Time: s.lastOK, Label: "Cloud copy", Roots: []string{s.t.LocalPath}}}, nil
}

func (s cloudSource) rel(abs string) (string, bool) {
	return tree{Base: s.t.LocalPath}.rel(abs)
}

func (s cloudSource) List(ctx context.Context, point, dir string, recursive bool) ([]protect.Entry, bool, error) {
	rel, ok := s.rel(dir)
	if !ok {
		return nil, false, errOutside
	}
	items, err := cloudsync.List(ctx, s.t, rel, recursive)
	if err != nil {
		return nil, false, err
	}
	var out []protect.Entry
	truncated := false
	for _, it := range items {
		if len(out) >= backup.MaxListing {
			truncated = true
			break
		}
		out = append(out, protect.Entry{Path: path.Join(dir, it.Path), Name: it.Name, Dir: it.IsDir, Size: it.Size, MTime: it.ModTime})
	}
	return out, truncated, nil
}

func (s cloudSource) Stat(ctx context.Context, point, abs string) (protect.Entry, error) {
	return statFromParent(ctx, s, point, abs, []string{s.t.LocalPath})
}

func (s cloudSource) Stage(ctx context.Context, point string, sel []protect.Entry, scratch string, p Progress) (Locator, error) {
	var rels []string
	var dirs []bool
	for _, e := range sel {
		rel, ok := s.rel(e.Path)
		if !ok {
			return nil, errOutside
		}
		rels, dirs = append(rels, rel), append(dirs, e.Dir)
	}
	p.Log("Downloading %d item(s) from the cloud", len(rels))
	if err := cloudsync.Fetch(ctx, s.t, rels, dirs, scratch, cloudsync.Progress{Set: p.Set, Log: p.Log}); err != nil {
		return nil, err
	}
	t := tree{Root: scratch, Base: s.t.LocalPath}
	return func(string) (tree, bool) { return t, true }, nil
}

func (s cloudSource) Dump(ctx context.Context, point, abs string, w io.Writer) error {
	rel, ok := s.rel(abs)
	if !ok || rel == "." {
		return errOutside
	}
	e, err := s.Stat(ctx, point, abs)
	if err != nil {
		return err
	}
	if e.Dir {
		return fmt.Errorf("folders of a cloud copy cannot be downloaded: restore them to a folder")
	}
	return cloudsync.Cat(ctx, s.t, rel, w)
}

// ---- Choosing a source ----

// lastSuccess is the start of the newest successful run.
func lastSuccess(h []store.ScheduleRun) time.Time {
	for _, r := range h {
		if r.Status == "success" {
			return r.Started
		}
	}
	return time.Time{}
}

// BackupSource reads a backup task, when it can be browsed: versioned
// backups (restic, any destination) and file copies to a local folder.
func BackupSource(t store.BackupTask) (Source, error) {
	switch {
	case t.Kind == "restic":
		return resticSource{t}, nil
	case t.Kind == "rsync" && t.SourceType == "" && t.DestType == "local":
		return rsyncSource{t, lastSuccess(t.History)}, nil
	}
	return nil, fmt.Errorf("this kind of backup cannot be browsed here")
}

// Browsable tells whether BackupSource accepts a task.
func Browsable(t store.BackupTask) bool {
	_, err := BackupSource(t)
	return err == nil
}

// CloudSource reads the cloud copy of an upload task.
func CloudSource(t store.CloudSync) (Source, error) {
	if t.Direction != "upload" {
		return nil, fmt.Errorf("only uploads to the cloud are copies of the NAS")
	}
	return cloudSource{t, lastSuccess(t.History)}, nil
}

// exists is true when p can be stat'ed.
func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
