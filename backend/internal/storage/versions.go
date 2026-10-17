package storage

// Previous versions of a file, found in the snapshots of its volume: Btrfs
// (.snapshots of the volume or of a sub-volume) and ZFS (.zfs/snapshot).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"strataos/internal/sys"
)

// SnapshotTree is a snapshot seen as a folder: Root holds what Base held at Time.
type SnapshotTree struct {
	Name string
	Time time.Time
	Root string
	Base string
}

// Version is a file as it was in a snapshot.
type Version struct {
	Snapshot string    `json:"snapshot"`
	Time     time.Time `json:"time"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"modTime"`
}

var stampRes = []struct {
	re     *regexp.Regexp
	layout string
}{
	{regexp.MustCompile(`(\d{8}-\d{6})$`), "20060102-150405"},
	{regexp.MustCompile(`(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})$`), "2006-01-02_15-04-05"},
}

func snapshotTime(name, dir string) time.Time {
	for _, s := range stampRes {
		if m := s.re.FindStringSubmatch(name); m != nil {
			if t, err := time.ParseInLocation(s.layout, m[1], time.Local); err == nil {
				return t
			}
		}
	}
	// The root of a new snapshot is a new inode: its change time is the creation.
	if fi, err := os.Stat(dir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			return time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
		}
		return fi.ModTime()
	}
	return time.Time{}
}

// mountOf returns the mount point and file system type holding p.
func mountOf(p string) (string, string) {
	out, err := sys.Run("findmnt", "-n", "-o", "TARGET,FSTYPE", "-T", p)
	if err != nil {
		return "", ""
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) < 2 {
		return "", ""
	}
	return strings.ReplaceAll(f[0], `\x20`, " "), f[1]
}

// SnapshotTrees lists the snapshots that contain path p, newest first.
func SnapshotTrees(p string) []SnapshotTree {
	mp, fstype := mountOf(p)
	if mp == "" || !strings.HasPrefix(mp, MountRoot+"/") {
		return nil
	}
	var out []SnapshotTree
	switch fstype {
	case "btrfs":
		dir := filepath.Join(mp, ".snapshots")
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			base, _, ok := strings.Cut(e.Name(), "@")
			if !ok {
				continue
			}
			b := mp
			if base != "root" {
				// Snapshot of a sub-volume: "a_b@…" holds mp/a/b.
				b = filepath.Join(mp, strings.ReplaceAll(base, "_", "/"))
			}
			if p != b && !strings.HasPrefix(p, b+"/") {
				continue
			}
			root := filepath.Join(dir, e.Name())
			out = append(out, SnapshotTree{Name: e.Name(), Time: snapshotTime(e.Name(), root), Root: root, Base: b})
		}
	case "zfs":
		dir := filepath.Join(mp, ".zfs", "snapshot")
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			root := filepath.Join(dir, e.Name())
			out = append(out, SnapshotTree{Name: e.Name(), Time: snapshotTime(e.Name(), root), Root: root, Base: mp})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// RelIn returns the path of p inside a snapshot tree ("" if outside).
func (t SnapshotTree) RelIn(p string) string {
	rel, err := filepath.Rel(t.Base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// Versions lists the distinct versions of the file p kept in snapshots.
func Versions(p string) []Version {
	var out []Version
	for _, t := range SnapshotTrees(p) {
		rel := t.RelIn(p)
		if rel == "" {
			continue
		}
		rt, err := os.OpenRoot(t.Root)
		if err != nil {
			continue
		}
		fi, err := rt.Lstat(rel)
		rt.Close()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		v := Version{Snapshot: t.Name, Time: t.Time, Size: fi.Size(), ModTime: fi.ModTime()}
		// Only changes: skip a snapshot holding the same file as the newer one.
		if n := len(out); n > 0 && out[n-1].Size == v.Size && out[n-1].ModTime.Equal(v.ModTime) {
			out[n-1] = v // keep the oldest snapshot of an unchanged stretch
			continue
		}
		out = append(out, v)
	}
	return out
}

// FindSnapshot returns the tree of a snapshot name that holds p.
func FindSnapshot(p, name string) (SnapshotTree, bool) {
	for _, t := range SnapshotTrees(p) {
		if t.Name == name {
			return t, true
		}
	}
	return SnapshotTree{}, false
}

// VersionsDirName holds, at the root of a volume, one link per snapshot of the
// whole volume named the way Windows expects (@GMT-…), for Samba's
// "Previous Versions" (vfs shadow_copy2).
const VersionsDirName = ".strata-versions"

// ShadowMount returns the Btrfs/ZFS volume of a share folder ("" otherwise).
func ShadowMount(p string) string {
	mp, fstype := mountOf(p)
	if (fstype == "btrfs" || fstype == "zfs") && strings.HasPrefix(mp, MountRoot+"/") {
		return mp
	}
	return ""
}

// SyncVersionLinks updates the @GMT links of a volume.
func SyncVersionLinks(mp string) error {
	dir := filepath.Join(mp, VersionsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	want := map[string]string{}
	for _, t := range SnapshotTrees(mp) {
		if t.Base != mp || t.Time.IsZero() {
			continue
		}
		rel, err := filepath.Rel(dir, t.Root)
		if err != nil {
			continue
		}
		want["@GMT-"+t.Time.UTC().Format("2006.01.02-15.04.05")] = rel
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if cur, err := os.Readlink(p); err != nil || want[e.Name()] != cur {
			_ = os.Remove(p) // stale, or not one of ours
		} else {
			delete(want, e.Name())
		}
	}
	for name, target := range want {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// MountOf returns the mount point holding p (or its nearest existing parent).
func MountOf(p string) string {
	for q := filepath.Clean(p); q != "/" && q != "."; q = filepath.Dir(q) {
		if _, err := os.Stat(q); err == nil {
			mp, _ := mountOf(q)
			return mp
		}
	}
	return ""
}
