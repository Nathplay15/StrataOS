package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/storage"
)

// The self-service side of the previous versions (h_versions.go): browsing the
// snapshots of a folder and putting deleted or overwritten files and folders
// back, without an administrator. Everything stays confined: the snapshot is
// read through an os.Root opened at the caller's share folder inside it
// (scopeIn, which also refuses symbolic links on the way), the writes go
// through the share root that locate validated.

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

// snapScope is where a request may read inside a snapshot: root is the
// caller's share folder as the snapshot holds it (the whole snapshot for
// administrators, or when the snapshotted sub-volume lies inside the share)
// and rel the path asked for, inside root.
type snapScope struct {
	tree storage.SnapshotTree
	root string
	rel  string
}

// errSnapLink: a snapshot path going through a symbolic link.
var errSnapLink = &Error{Status: http.StatusForbidden, Msg: "symbolic links are not followed in snapshots"}

// scopeIn confines t to its share inside tree. A snapshot holds every share
// of the volume, and a relative link kept in it (A/link -> ../B) would lead
// from share A into share B: os.Root is opened at the share folder, and no
// component of the share folder or of the path may be a link. Snapshots are
// read-only, so what is checked here cannot change before it is opened.
func scopeIn(tree storage.SnapshotTree, t target) (snapScope, error) {
	full := tree.RelIn(t.abs)
	if full == "" {
		return snapScope{}, notFound("version not found")
	}
	if err := noSnapLinks(tree.Root, full); err != nil {
		return snapScope{}, err
	}
	sc := snapScope{tree: tree, root: tree.Root, rel: full}
	if tree.Base != t.root && !strings.HasPrefix(tree.Base, t.root+"/") {
		share := tree.RelIn(t.root)
		if share == "" {
			return snapScope{}, notFound("version not found")
		}
		sc.root, sc.rel = filepath.Join(tree.Root, filepath.FromSlash(share)), t.rel
	}
	return sc, nil
}

// noSnapLinks refuses a path of a snapshot that goes through a symbolic link.
// Components that do not exist end the check (opening them fails anyway).
func noSnapLinks(root, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	rt, err := os.OpenRoot(root)
	if err != nil {
		return notFound("version not found")
	}
	defer rt.Close()
	cur := ""
	for _, c := range strings.Split(rel, "/") {
		cur = path.Join(cur, c)
		fi, err := rt.Lstat(cur)
		if err != nil {
			return nil
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return errSnapLink
		}
	}
	return nil
}

// child is the scope of a direct child of the folder, which must not be a link.
func (sc snapScope) child(name string) (snapScope, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return snapScope{}, badRequest("invalid name")
	}
	c := sc
	c.rel = path.Join(sc.rel, name)
	if err := noSnapLinks(sc.root, c.rel); err != nil {
		return snapScope{}, err
	}
	return c, nil
}

// snapTarget validates p and a snapshot name for the current user: the
// folder p and where it may be read inside the snapshot.
func (s *Server) snapTarget(r *http.Request, p, snap string) (target, snapScope, error) {
	t, err := s.locate(r, p)
	if err != nil || !t.acc.read || t.acc.ancestor {
		return target{}, snapScope{}, firstErr(err, errForbidden)
	}
	for _, tr := range snapTrees(t.abs) {
		if tr.Name == snap {
			sc, err := scopeIn(tr, t)
			return t, sc, err
		}
	}
	return target{}, snapScope{}, notFound("version not found")
}

// snapRestoreRoom is how many bytes a restore may write at abs (-1: no
// limit): the quotas, like an extraction or a compression, and the free space
// minus a margin. A variable for the tests.
var snapRestoreRoom = func(s *Server, user string, admin bool, abs string) int64 {
	room := s.quotaRoom(user, admin, abs)
	var sfs syscall.Statfs_t
	if syscall.Statfs(abs, &sfs) == nil {
		free := max(int64(sfs.Bavail)*int64(sfs.Bsize)-512<<20, 0)
		if room < 0 || free < room {
			room = free
		}
	}
	return room
}

var errSnapNoRoom = &Error{Status: http.StatusForbidden, Msg: "not enough room left (quota or free space)"}

// snapInlineMax: restores of files up to this size run in the request; any
// folder, or more, runs as a job.
const snapInlineMax = 256 << 20

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
		t, sc, err := s.snapTarget(r, q.Get("path"), q.Get("snapshot"))
		if err != nil {
			return nil, err
		}
		rt, err := os.OpenRoot(sc.root)
		if err != nil {
			return nil, notFound("version not found")
		}
		defer rt.Close()
		f, err := rt.Open(sc.rel)
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
		t, sc, err := s.snapTarget(r, q.Get("path"), q.Get("snapshot"))
		if err == nil {
			sc, err = sc.child(q.Get("name"))
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		v := target{root: sc.root, rel: sc.rel, abs: filepath.Join(t.abs, path.Base(sc.rel)), acc: access{read: true}}
		s.serveTarget(w, r, v, nil)
	})

	// Puts files and folders of a snapshot back into the folder, as a copy
	// next to what exists ("(version of …)" suffix) or in place for files.
	// Folders, or files over snapInlineMax, are restored by a job.
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
		t, sc, err := s.snapTarget(r, req.Path, req.Snapshot)
		if err != nil {
			return nil, err
		}
		if why := s.readOnlyReason(t); why != "" {
			return nil, &Error{Status: http.StatusForbidden, Msg: why}
		}
		src, err := os.OpenRoot(sc.root)
		if err != nil {
			return nil, notFound("version not found")
		}
		var items []snapItem
		var size int64
		big := false
		for _, name := range req.Names {
			c, err := sc.child(name)
			if err != nil {
				src.Close()
				return nil, err
			}
			fi, err := src.Lstat(c.rel)
			if err != nil {
				src.Close()
				return nil, notFound(name + " is not in this version")
			}
			if req.Replace && fi.IsDir() {
				src.Close()
				return nil, badRequest(name + " is a folder: folders are restored as a copy")
			}
			items = append(items, snapItem{name: name, rel: c.rel, fi: fi})
			if fi.IsDir() {
				big = true
			} else if fi.Mode().IsRegular() {
				size += fi.Size()
			}
		}
		user, admin := userOf(r), isAdmin(r)
		rs := &snapRestore{src: src, room: snapRestoreRoom(s, user, admin, t.abs)}
		if !admin {
			rs.own = func(dst *os.Root, rel string) { s.chownTo(user, dst, rel) }
		}
		run := func(ctx context.Context, j *jobs.Job) (map[string]any, error) {
			defer src.Close()
			dst, err := t.open()
			if err != nil {
				return nil, err
			}
			defer dst.Close()
			rs.ctx, rs.dst, rs.job = ctx, dst, j
			restored, err := rs.restore(items, t.rel, sc.tree.Time.Format("2006-01-02 15h04"), req.Replace)
			if len(restored) > 0 {
				what := strings.Join(restored, ", ")
				if len(restored) > 3 {
					what = fmt.Sprintf("%d items", len(restored))
				}
				s.store.Log("info", "files", user, fmt.Sprintf("Restored %s from snapshot %s into %s", what, req.Snapshot, t.abs))
			}
			return map[string]any{"restored": restored, "skipped": rs.skipped}, err
		}
		if !big && size <= snapInlineMax {
			return run(r.Context(), nil)
		}
		return s.jobs.Start("files", "Restoring from a snapshot into "+path.Base(t.abs), user, func(ctx context.Context, j *jobs.Job) error {
			out, err := run(ctx, j)
			if sk, _ := out["skipped"].([]string); len(sk) > 0 {
				j.Logf("Skipped (links or special files): %s", strings.Join(sk, ", "))
			}
			return err
		}), nil
	})
}

// snapItem is one name chosen in a restore.
type snapItem struct {
	name string
	rel  string // in the snapshot scope
	fi   os.FileInfo
}

// snapRestore copies items of a snapshot into a share folder: regular files
// and folders only, every created item given to the user (own), within the
// room the quotas and the free space leave.
type snapRestore struct {
	ctx      context.Context
	src, dst *os.Root
	own      func(dst *os.Root, rel string) // nil: administrators keep root
	room     int64                          // bytes left (-1: no limit)
	job      *jobs.Job
	total    int64 // bytes to copy (jobs: progress)
	done     int64
	skipped  []string
}

// restore puts items back into dir. A name taken in dir gets a free
// "(version of …)" name ("… (2)" for the next restore of the same version),
// unless replace (files only) swaps the current file.
func (rs *snapRestore) restore(items []snapItem, dir, stamp string, replace bool) (restored []string, err error) {
	if rs.ctx == nil {
		rs.ctx = context.Background()
	}
	// The size is measured before writing anything: a restore that cannot
	// fit is refused whole rather than left half-way.
	for _, it := range items {
		n, err := rs.measure(it.rel, it.fi)
		if err != nil {
			return nil, err
		}
		rs.total += n
	}
	if rs.room >= 0 && rs.total > rs.room {
		return nil, errSnapNoRoom
	}
	for _, it := range items {
		if !it.fi.Mode().IsRegular() && !it.fi.IsDir() {
			rs.skipped = append(rs.skipped, it.name)
			continue
		}
		out := it.name
		taken := false
		if _, err := rs.dst.Lstat(path.Join(dir, out)); err == nil {
			taken = true
		}
		if taken && !replace {
			if out, err = freeName(rs.dst, dir, versionName(it.name, stamp)); err != nil {
				return restored, err
			}
		}
		drel := path.Join(dir, out)
		if it.fi.IsDir() {
			// A folder created meanwhile under that name is never merged into.
			for range 20 {
				if err = rs.dst.Mkdir(drel, it.fi.Mode().Perm()); !errors.Is(err, os.ErrExist) {
					break
				}
				if out, err = freeName(rs.dst, dir, versionName(it.name, stamp)); err != nil {
					return restored, err
				}
				drel = path.Join(dir, out)
			}
			if err != nil {
				return restored, err
			}
			rs.chown(drel)
			err = rs.dir(it.rel, drel, it.fi)
		} else {
			err = rs.file(it.rel, drel, it.fi, replace && taken)
		}
		if err != nil {
			return restored, fmt.Errorf("%s: %w", it.name, err)
		}
		restored = append(restored, out)
	}
	return restored, nil
}

// measure adds up the bytes of the regular files under rel (links not followed).
func (rs *snapRestore) measure(rel string, fi os.FileInfo) (int64, error) {
	if fi.Mode().IsRegular() {
		return fi.Size(), nil
	}
	if !fi.IsDir() {
		return 0, nil
	}
	var n int64
	err := fs.WalkDir(rs.src.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rs.ctx.Err() != nil {
			return rs.ctx.Err()
		}
		if d.Type().IsRegular() {
			if i, err := d.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n, err
}

func (rs *snapRestore) chown(rel string) {
	if rs.own != nil {
		rs.own(rs.dst, rel)
	}
}

// file copies one regular file of the snapshot. With replace, it is written
// to a temporary name then renamed over the current file, so an interrupted
// copy never destroys it.
func (rs *snapRestore) file(srel, drel string, fi os.FileInfo, replace bool) error {
	in, err := rs.src.Open(srel)
	if err != nil {
		return err
	}
	defer in.Close()
	out := drel
	if replace {
		out = path.Join(path.Dir(drel), fmt.Sprintf(".%s.strata-restore-%d", path.Base(drel), time.Now().UnixNano()))
	}
	f, err := rs.dst.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(f, &snapReader{rs: rs, r: in})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_ = rs.dst.Chtimes(out, fi.ModTime(), fi.ModTime())
		rs.chown(out)
	}
	if err != nil {
		_ = rs.dst.Remove(out)
		return err
	}
	if replace {
		if err := rs.dst.Rename(out, drel); err != nil {
			_ = rs.dst.Remove(out)
			return err
		}
	}
	return nil
}

// dir copies the content of a folder of the snapshot into the new folder
// drel; links and special files are skipped and reported.
func (rs *snapRestore) dir(srel, drel string, fi os.FileInfo) error {
	d, err := rs.src.Open(srel)
	if err != nil {
		return err
	}
	ents, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := rs.ctx.Err(); err != nil {
			return err
		}
		child, dchild := path.Join(srel, e.Name()), path.Join(drel, e.Name())
		cfi, err := rs.src.Lstat(child)
		if err != nil {
			continue
		}
		switch {
		case cfi.IsDir():
			if err := rs.dst.Mkdir(dchild, cfi.Mode().Perm()); err != nil {
				return err
			}
			rs.chown(dchild)
			if err := rs.dir(child, dchild, cfi); err != nil {
				return err
			}
		case cfi.Mode().IsRegular():
			if err := rs.file(child, dchild, cfi, false); err != nil {
				return err
			}
		default:
			rs.skipped = append(rs.skipped, child)
		}
	}
	_ = rs.dst.Chtimes(drel, fi.ModTime(), fi.ModTime())
	return nil
}

// snapReader counts the bytes copied: it stops the restore when the room is
// used up (the quota changed meanwhile) or the job is stopped, and moves the
// progress of the job.
type snapReader struct {
	rs *snapRestore
	r  io.Reader
}

func (sr *snapReader) Read(b []byte) (int, error) {
	if err := sr.rs.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := sr.r.Read(b)
	rs := sr.rs
	rs.done += int64(n)
	if rs.room >= 0 && rs.done > rs.room {
		return n, errSnapNoRoom
	}
	if rs.job != nil && rs.total > 0 {
		rs.job.SetProgress(float64(rs.done) / float64(rs.total) * 100)
	}
	return n, err
}
