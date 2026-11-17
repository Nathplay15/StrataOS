package api

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strataos/internal/store"
)

// The recycle bin of a share (shares.go) keeps what SMB, WebDAV and the web
// delete: .recycle/<user>/<path below the share>, the folders of the path
// kept (Samba's keeptree), never overwritten, purged by age of deletion.
// These endpoints expose it in the Files page item by item: each deleted
// file (or empty folder) at its original place, restored there, into the
// folders that exist again or were never deleted. Every account lists,
// restores and empties its own bin; nothing from another user is reachable.

// maxRecycleItems bounds a listing (a bin can hold a whole deleted tree).
const maxRecycleItems = 5000

type recycleItem struct {
	Path    string    `json:"path"`   // where the item lies in the bin
	Origin  string    `json:"origin"` // where it was before deletion
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"` // an empty folder
	Size    int64     `json:"size"`
	Deleted time.Time `json:"deleted"`
	Files   int       `json:"files,omitempty"` // kept for older clients (always 0)
}

// binShare validates that share is the root of a share with a recycle bin the
// caller may reach, and returns the share path with its access rights.
func (s *Server) binShare(r *http.Request, share string) (sp string, t target, err error) {
	clean := filepath.Clean("/" + share)
	t, err = s.locate(r, clean)
	if err != nil {
		return "", target{}, err
	}
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			if sh.RecycleBin && sh.Path == clean {
				sp = sh.Path
			}
		}
	})
	if sp == "" {
		return "", target{}, badRequest("this share has no recycle bin")
	}
	return sp, t, nil
}

// binItem validates that p is inside the caller's own bin in sp (any depth)
// and returns its path relative to the share.
func binItem(sp, user, p string) (string, error) {
	rel := relTo(sp, filepath.Clean("/"+p))
	if rel == "." || strings.HasPrefix(rel, "../") {
		return "", badRequest("invalid recycle bin path")
	}
	prefix := path.Join(".recycle", user) + "/"
	if !strings.HasPrefix(rel, prefix) || len(rel) == len(prefix) {
		return "", badRequest("invalid recycle bin path")
	}
	return rel, nil
}

// trashSuffixBase undoes the ".<unixtime>" suffix trashFile adds on collision.
func trashSuffixBase(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 && len(name)-i-1 >= 10 && allDigits(name[i+1:]) {
		return name[:i]
	}
	return ""
}

// sambaCopyBase undoes the "Copy #N of " prefix Samba's recycle:versions
// gives a file deleted again under a name the bin already holds.
func sambaCopyBase(name string) string {
	rest, ok := strings.CutPrefix(name, "Copy #")
	if !ok {
		return ""
	}
	n, base, ok := strings.Cut(rest, " of ")
	if !ok || !allDigits(n) || base == "" {
		return ""
	}
	return base
}

// restoredName is the n-th free name tried for a file restored over one
// that exists again: "report (restored).pdf", "report (restored 2).pdf".
func restoredName(p string, n int) string {
	dir, base := path.Split(p)
	ext := path.Ext(base)
	if ext == base {
		ext = ""
	}
	stem := strings.TrimSuffix(base, ext)
	if n <= 1 {
		return dir + stem + " (restored)" + ext
	}
	return dir + stem + " (restored " + strconv.Itoa(n) + ")" + ext
}

// deletedAt is when a file entered the bin: Samba (recycle:touch) and
// StrataOS set its access time then, and Samba its modification time too;
// the later of both (files moved inside a deleted folder keep their own
// modification time).
func deletedAt(info fs.FileInfo) time.Time {
	t := info.ModTime()
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if a := time.Unix(st.Atim.Sec, st.Atim.Nsec); a.After(t) {
			t = a
		}
	}
	return t
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func (s *Server) recycleRoutes() {
	// The deleted items of my bin in a share: top-level entries only, a
	// deleted folder counts as one item and comes back whole.
	s.user("GET /api/files/recycle", func(r *http.Request) (any, error) {
		sp, t, err := s.binShare(r, r.URL.Query().Get("share"))
		if err != nil {
			return nil, err
		}
		if !t.acc.read {
			return nil, &Error{Status: http.StatusForbidden, Msg: "no read access to this share"}
		}
		items, err := listRecycle(sp, userOf(r))
		if err != nil {
			return nil, err
		}
		return map[string]any{"share": sp, "items": items}, nil
	})

	// Put deleted items back where they were.
	s.user("POST /api/files/recycle/restore", func(r *http.Request) (any, error) {
		var req struct {
			Paths []string `json:"paths"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		type restore struct {
			sp, rel string
		}
		var rs []restore
		for _, p := range req.Paths {
			sp := s.recycleShare(p)
			if sp == "" {
				return nil, badRequest("this share has no recycle bin")
			}
			t, err := s.locate(r, sp)
			if err != nil {
				return nil, err
			}
			if !t.acc.write {
				return nil, &Error{Status: http.StatusForbidden, Msg: "no write access to this share"}
			}
			rel, err := binItem(sp, userOf(r), p)
			if err != nil {
				return nil, err
			}
			rs = append(rs, restore{sp, rel})
		}
		if len(rs) == 0 {
			return nil, badRequest("no item to restore")
		}
		back := []string{}
		for _, x := range rs {
			to, err := restoreRecycle(x.sp, userOf(r), x.rel)
			back = append(back, to...)
			// Already restored with a folder selected too: nothing left to do.
			if err != nil && !isNotFound(err) {
				return nil, err
			}
		}
		s.audit(r, "files", fmt.Sprintf("Restored %d item(s) from the recycle bin", len(back)))
		return map[string]any{"restored": back}, nil
	})

	// Empty my bin, or only the selected items when paths are given.
	s.user("POST /api/files/recycle/empty", func(r *http.Request) (any, error) {
		var req struct {
			Share string   `json:"share"`
			Paths []string `json:"paths"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		sp, t, err := s.binShare(r, req.Share)
		if err != nil {
			return nil, err
		}
		if !t.acc.write {
			return nil, &Error{Status: http.StatusForbidden, Msg: "no write access to this share"}
		}
		rt, err := os.OpenRoot(sp)
		if err != nil {
			return nil, notFound("storage unavailable")
		}
		defer rt.Close()
		base := path.Join(".recycle", userOf(r))
		removed := 0
		if len(req.Paths) == 0 {
			if err := rt.RemoveAll(base); err != nil {
				return nil, err
			}
			removed = 1
		} else {
			for _, p := range req.Paths {
				rel, err := binItem(sp, userOf(r), p)
				if err != nil {
					return nil, err
				}
				if err := rt.RemoveAll(rel); err != nil {
					return nil, err
				}
				for d := path.Dir(rel); d != base && strings.HasPrefix(d, base+"/"); d = path.Dir(d) {
					if rt.Remove(d) != nil {
						break
					}
				}
				removed++
			}
		}
		s.audit(r, "files", fmt.Sprintf("Deleted %d item(s) from the recycle bin", removed))
		return map[string]int{"deleted": removed}, nil
	})
}

// listRecycle lists the deleted items of the bin of user in share sp: its
// files, links and empty folders at any depth, most recent deletion first.
// The walk never follows links.
func listRecycle(sp, user string) ([]recycleItem, error) {
	rt, err := os.OpenRoot(sp)
	if err != nil {
		return nil, notFound("storage unavailable")
	}
	defer rt.Close()
	base := path.Join(".recycle", user)
	if fi, err := rt.Lstat(base); err != nil || !fi.IsDir() {
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return []recycleItem{}, nil
		}
		return nil, err
	}
	items := []recycleItem{}
	seen := 0
	err = fs.WalkDir(rt.FS(), base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == base {
			return nil
		}
		if seen++; seen > 20*maxRecycleItems {
			return fs.SkipAll
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if d.IsDir() {
			// Only an empty folder is an item of its own.
			if f, err := rt.Open(p); err == nil {
				names, _ := f.Readdirnames(1)
				f.Close()
				if len(names) > 0 {
					return nil
				}
			}
		}
		orig := strings.TrimPrefix(p, base+"/")
		it := recycleItem{Name: d.Name(), Dir: d.IsDir(), Path: sp + "/" + p, Origin: sp + "/" + orig, Deleted: deletedAt(info)}
		if d.Type().IsRegular() {
			it.Size = info.Size()
		}
		items = append(items, it)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].Deleted.Equal(items[j].Deleted) {
			return items[i].Deleted.After(items[j].Deleted)
		}
		return items[i].Origin < items[j].Origin
	})
	if len(items) > maxRecycleItems {
		items = items[:maxRecycleItems]
	}
	return items, nil
}

// restoreRecycle puts rel (inside the bin of user) back at its original
// place in the share: a file into its folder (created again when missing,
// merged when it exists), a folder item by item. The ".<unixtime>" suffix
// trashFile adds and Samba's "Copy #N of " prefix are undone when the plain
// name is free; a file whose name is taken again gets a new name
// ("… (restored).ext") instead of overwriting. Returns the restored paths.
func restoreRecycle(sp, user, rel string) ([]string, error) {
	rt, err := os.OpenRoot(sp)
	if err != nil {
		return nil, notFound("storage unavailable")
	}
	defer rt.Close()
	bin := path.Join(".recycle", user)
	fi, err := rt.Lstat(rel)
	if err != nil {
		return nil, notFound("not in the recycle bin")
	}
	var leaves, empty, dirs []string
	if fi.IsDir() {
		err = fs.WalkDir(rt.FS(), rel, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				leaves = append(leaves, p)
				return nil
			}
			dirs = append(dirs, p)
			if f, err := rt.Open(p); err == nil {
				names, _ := f.Readdirnames(1)
				f.Close()
				if len(names) == 0 {
					empty = append(empty, p)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		leaves = []string{rel}
	}
	var out []string
	for _, p := range leaves {
		to, err := placeRestored(rt, p, strings.TrimPrefix(p, bin+"/"))
		if err != nil {
			return out, err
		}
		out = append(out, sp+"/"+to)
	}
	// Empty folders come back as folders (merged with one that exists).
	for _, d := range empty {
		orig := strings.TrimPrefix(d, bin+"/")
		if err := rt.MkdirAll(orig, 0o770); err != nil {
			return out, err
		}
		out = append(out, sp+"/"+orig)
	}
	// The bin's copies of the folders, deepest first.
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = rt.Remove(dirs[i])
	}
	// The bin's folders left empty above it go too.
	for d := path.Dir(rel); d != bin && strings.HasPrefix(d, bin+"/"); d = path.Dir(d) {
		if rt.Remove(d) != nil {
			break
		}
	}
	return out, nil
}

// placeRestored moves the file p of the bin to orig (or the name chosen as
// restoreRecycle says) and returns where it went.
func placeRestored(rt *os.Root, p, orig string) (string, error) {
	dst := orig
	name := path.Base(orig)
	for _, b := range []string{trashSuffixBase(name), sambaCopyBase(name)} {
		if b == "" {
			continue
		}
		if _, err := rt.Lstat(path.Join(path.Dir(orig), b)); err != nil {
			dst = path.Join(path.Dir(orig), b)
			break
		}
	}
	if dir := path.Dir(dst); dir != "." {
		if err := rt.MkdirAll(dir, 0o770); err != nil {
			return "", fmt.Errorf("cannot recreate the folder %s: %v", dir, err)
		}
	}
	if _, err := rt.Lstat(dst); err == nil {
		base := dst
		for n := 1; ; n++ {
			dst = restoredName(base, n)
			if _, err := rt.Lstat(dst); err != nil {
				break
			}
			if n > 999 {
				return "", fmt.Errorf("no free name to restore %s", orig)
			}
		}
	}
	if err := rt.Rename(p, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func isNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// stampDeleted marks dst, just moved into a bin, as deleted now: its times
// (the bin is purged by age of deletion) and, for a folder, the access time
// of everything inside (their modification times are kept for a restore).
func stampDeleted(rt *os.Root, dst string) {
	now := time.Now()
	_ = rt.Chtimes(dst, now, now)
	if fi, err := rt.Lstat(dst); err != nil || !fi.IsDir() {
		return
	}
	_ = fs.WalkDir(rt.FS(), dst, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dst || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, err := d.Info(); err == nil {
			_ = rt.Chtimes(p, now, info.ModTime())
		}
		return nil
	})
}
