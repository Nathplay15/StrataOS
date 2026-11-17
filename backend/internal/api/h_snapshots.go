package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"strataos/internal/storage"
)

// The self-service side of the previous versions (h_versions.go): browsing the
// snapshots of a folder and putting deleted or overwritten files and folders
// back, without an administrator. Everything stays confined: the snapshot is
// read through os.Root, the writes go through the share root that locate
// validated.

// snapshotTreesAt lists the snapshots of a path; a variable for the tests.
var snapshotTreesAt = storage.SnapshotTrees

// snapTrees lists the snapshot trees that hold p, even when p no longer
// exists: the volume is then found on the nearest existing ancestor.
func snapTrees(p string) []storage.SnapshotTree {
	for base := p; ; base = filepath.Dir(base) {
		var out []storage.SnapshotTree
		for _, t := range snapshotTreesAt(base) {
			if t.RelIn(p) != "" {
				out = append(out, t)
			}
		}
		if len(out) > 0 || base == storage.MountRoot || filepath.Dir(base) == base {
			return out
		}
	}
}

// versionName is the name a restored item takes next to an existing one.
func versionName(name, stamp string) string {
	ext := filepath.Ext(name)
	if ext == name { // a dotfile has no extension
		ext = ""
	}
	return fmt.Sprintf("%s (version of %s)%s", strings.TrimSuffix(name, ext), stamp, ext)
}

// snapPoint is a snapshot of a folder's timeline.
type snapPoint struct {
	Snapshot string    `json:"snapshot"`
	Time     time.Time `json:"time"`
}

// snapEntry is a child of a folder as a snapshot holds it.
type snapEntry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"modTime"`
}

// snapTarget validates p and a snapshot name for the current user: the tree
// of the snapshot and the path of p inside it.
func (s *Server) snapTarget(r *http.Request, p, snap string) (t target, tree storage.SnapshotTree, rel string, err error) {
	t, err = s.locate(r, p)
	if err != nil || !t.acc.read || t.acc.ancestor {
		return target{}, storage.SnapshotTree{}, "", firstErr(err, errForbidden)
	}
	for _, tr := range snapTrees(t.abs) {
		if tr.Name != snap {
			continue
		}
		rel = tr.RelIn(t.abs)
		if rel == "" {
			break
		}
		return t, tr, rel, nil
	}
	return target{}, storage.SnapshotTree{}, "", notFound("version not found")
}

func (s *Server) snapshotRoutes() {
	// Timeline of a folder: the snapshots that hold it (a deleted folder too).
	s.user("GET /api/files/snapshots", func(r *http.Request) (any, error) {
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil || !t.acc.read || t.acc.ancestor {
			return nil, firstErr(err, errForbidden)
		}
		out := []snapPoint{}
		for _, tr := range snapTrees(t.abs) {
			out = append(out, snapPoint{Snapshot: tr.Name, Time: tr.Time})
		}
		return map[string]any{"points": out, "writable": t.acc.write}, nil
	})

	// One folder of a snapshot: its direct children. exists tells whether the
	// folder still exists in the share: restoring into a deleted folder is
	// refused, the folder itself is restored instead from one level above.
	s.user("GET /api/files/snapshots/browse", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		t, tree, rel, err := s.snapTarget(r, q.Get("path"), q.Get("snapshot"))
		if err != nil {
			return nil, err
		}
		rt, err := os.OpenRoot(tree.Root)
		if err != nil {
			return nil, notFound("version not found")
		}
		defer rt.Close()
		f, err := rt.Open(rel)
		if err != nil {
			return nil, notFound("version not found")
		}
		defer f.Close()
		ents, err := f.ReadDir(-1)
		if err != nil {
			return nil, notFound("version not found")
		}
		out := []snapEntry{}
		for _, e := range ents {
			fi, err := e.Info()
			if err != nil {
				continue
			}
			se := snapEntry{Name: e.Name(), Dir: e.IsDir(), ModTime: fi.ModTime()}
			if !e.IsDir() && fi.Mode().IsRegular() {
				se.Size = fi.Size()
			}
			out = append(out, se)
		}
		slices.SortFunc(out, func(a, b snapEntry) int {
			if a.Dir != b.Dir {
				if a.Dir {
					return -1
				}
				return 1
			}
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		_, statErr := os.Stat(t.abs)
		return map[string]any{"entries": out, "exists": statErr == nil}, nil
	})

	// A file of a snapshot, or a folder as a zip.
	s.userRaw("GET /api/files/snapshots/download", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		t, tree, rel, err := s.snapTarget(r, q.Get("path"), q.Get("snapshot"))
		if err != nil {
			writeErr(w, err)
			return
		}
		name := q.Get("name")
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
			writeErr(w, badRequest("invalid name"))
			return
		}
		v := target{root: tree.Root, rel: path.Join(rel, name), abs: filepath.Join(t.abs, name), acc: access{read: true}}
		s.serveTarget(w, r, v, nil)
	})

	// Puts files and folders of a snapshot back into the folder, as a copy
	// next to what exists ("(version of …)" suffix) or in place for files.
	s.user("POST /api/files/snapshots/restore", func(r *http.Request) (any, error) {
		var req struct {
			Path     string   `json:"path"`
			Snapshot string   `json:"snapshot"`
			Names    []string `json:"names"`
			Replace  bool     `json:"replace"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Names) == 0 || len(req.Names) > 200 {
			return nil, badRequest("choose what to restore")
		}
		for _, name := range req.Names {
			if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
				return nil, badRequest("invalid name")
			}
		}
		t, tree, rel, err := s.snapTarget(r, req.Path, req.Snapshot)
		if err != nil {
			return nil, err
		}
		if !t.acc.write {
			return nil, &Error{Status: http.StatusForbidden, Msg: "you cannot write to this folder"}
		}
		src, err := os.OpenRoot(tree.Root)
		if err != nil {
			return nil, notFound("version not found")
		}
		defer src.Close()
		dst, err := t.open()
		if err != nil {
			return nil, err
		}
		defer dst.Close()
		stamp := tree.Time.Format("2006-01-02 15h04")
		var restored, skipped []string
		for _, name := range req.Names {
			srel := path.Join(rel, name)
			fi, err := src.Lstat(srel)
			if err != nil {
				return nil, notFound(name + " is not in this version")
			}
			if !fi.Mode().IsRegular() && !fi.IsDir() {
				skipped = append(skipped, name)
				continue
			}
			out := name
			if req.Replace && fi.IsDir() {
				return nil, badRequest(name + " is a folder: folders are restored as a copy")
			}
			if _, err := dst.Lstat(path.Join(t.rel, name)); err == nil && !req.Replace {
				out = versionName(name, stamp)
			}
			drel := path.Join(t.rel, out)
			if fi.IsDir() {
				n, err := copySnapshotDir(src, srel, dst, drel, fi)
				if err != nil {
					return nil, err
				}
				skipped = append(skipped, n...)
			} else if err := copySnapshotFile(src, srel, dst, drel, fi, req.Replace); err != nil {
				return nil, err
			}
			s.chownForUser(r, dst, drel)
			restored = append(restored, out)
		}
		what := strings.Join(restored, ", ")
		if len(restored) > 3 {
			what = fmt.Sprintf("%d items", len(restored))
		}
		s.audit(r, "files", fmt.Sprintf("Restored %s from snapshot %s into %s", what, req.Snapshot, t.abs))
		return map[string]any{"restored": restored, "skipped": skipped}, nil
	})
}

// copySnapshotFile copies one regular file of a snapshot into the share. With
// replace, the file is written to a temporary name then renamed over the
// original, so an interrupted copy never destroys it.
func copySnapshotFile(src *os.Root, srel string, dst *os.Root, drel string, fi os.FileInfo, replace bool) error {
	in, err := src.Open(srel)
	if err != nil {
		return err
	}
	defer in.Close()
	out := drel
	if replace {
		out = path.Join(path.Dir(drel), fmt.Sprintf(".%s.strata-restore-%d", path.Base(drel), time.Now().UnixNano()))
	}
	f, err := dst.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(f, in)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_ = dst.Chtimes(out, fi.ModTime(), fi.ModTime())
	}
	if err != nil {
		_ = dst.Remove(out)
		return err
	}
	if replace {
		if err := dst.Rename(out, drel); err != nil {
			_ = dst.Remove(out)
			return err
		}
	}
	return nil
}

// copySnapshotDir copies a folder of a snapshot recursively: regular files
// and folders only, symlinks and special files are skipped and reported.
func copySnapshotDir(src *os.Root, srel string, dst *os.Root, drel string, fi os.FileInfo) (skipped []string, err error) {
	if err := dst.Mkdir(drel, fi.Mode().Perm()); err != nil && !os.IsExist(err) {
		return nil, err
	}
	d, err := src.Open(srel)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	ents, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		child := path.Join(srel, e.Name())
		cfi, err := src.Lstat(child)
		if err != nil {
			continue
		}
		switch {
		case cfi.IsDir():
			sk, err := copySnapshotDir(src, child, dst, path.Join(drel, e.Name()), cfi)
			skipped = append(skipped, sk...)
			if err != nil {
				return skipped, err
			}
		case cfi.Mode().IsRegular():
			if err := copySnapshotFile(src, child, dst, path.Join(drel, e.Name()), cfi, false); err != nil {
				return skipped, err
			}
		default:
			skipped = append(skipped, e.Name())
		}
	}
	_ = dst.Chtimes(drel, fi.ModTime(), fi.ModTime())
	return skipped, nil
}
