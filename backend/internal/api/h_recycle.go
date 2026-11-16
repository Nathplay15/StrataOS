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
	"time"

	"strataos/internal/store"
)

// The recycle bin of a share (shares.go) keeps what SMB and the web delete:
// .recycle/<user>/<path>, never overwritten, purged by age of deletion. These
// endpoints expose it in the Files page: every account lists, restores and
// empties its own bin; nothing from another user is reachable.

type recycleItem struct {
	Path    string    `json:"path"`   // where the item lies in the bin
	Origin  string    `json:"origin"` // where it was before deletion
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	Deleted time.Time `json:"deleted"` // deletion time (mtime set on trash)
	Files   int       `json:"files"`   // files inside, for folders
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

// binItem validates that p is a top-level item of the caller's own bin in sp
// and returns its path relative to the share.
func binItem(sp, user, p string) (string, error) {
	rel := relTo(sp, filepath.Clean("/"+p))
	if rel == "." || strings.HasPrefix(rel, "../") {
		return "", badRequest("invalid recycle bin path")
	}
	prefix := path.Join(".recycle", user) + "/"
	if !strings.HasPrefix(rel, prefix) || strings.Contains(strings.TrimPrefix(rel, prefix), "/") {
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
		var back []string
		for _, x := range rs {
			to, err := restoreRecycle(x.sp, userOf(r), x.rel)
			if err != nil {
				return nil, err
			}
			back = append(back, to)
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
				removed++
			}
		}
		s.audit(r, "files", fmt.Sprintf("Deleted %d item(s) from the recycle bin", removed))
		return map[string]int{"deleted": removed}, nil
	})
}

// listRecycle reads the top-level entries of the bin of user in share sp.
func listRecycle(sp, user string) ([]recycleItem, error) {
	rt, err := os.OpenRoot(sp)
	if err != nil {
		return nil, notFound("storage unavailable")
	}
	defer rt.Close()
	base := path.Join(".recycle", user)
	d, err := rt.Open(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []recycleItem{}, nil
		}
		return nil, err
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return nil, err
	}
	items := []recycleItem{}
	for _, e := range entries {
		it := recycleItem{Name: e.Name(), Dir: e.IsDir(), Path: sp + "/" + base + "/" + e.Name(), Origin: sp + "/" + e.Name()}
		if info, err := e.Info(); err == nil {
			it.Size, it.Deleted = info.Size(), info.ModTime()
		}
		if e.IsDir() {
			it.Size = 0
			it.Files = countRecycleFiles(rt, base+"/"+e.Name())
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Deleted.After(items[j].Deleted) })
	return items, nil
}

func countRecycleFiles(rt *os.Root, dir string) int {
	n := 0
	_ = fs.WalkDir(rt.FS(), dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			n++
		}
		return nil
	})
	return n
}

// restoreRecycle moves rel (inside the bin) back to its original place in the
// share. The ".<unixtime>" suffix trashFile adds on collision is undone when
// the plain name is free; a name that is taken again gets a fresh suffix
// instead of overwriting.
func restoreRecycle(sp, user, rel string) (string, error) {
	rt, err := os.OpenRoot(sp)
	if err != nil {
		return "", notFound("storage unavailable")
	}
	defer rt.Close()
	orig := strings.TrimPrefix(rel, path.Join(".recycle", user)+"/")
	dst := orig
	if base := trashSuffixBase(orig); base != "" {
		if _, err := rt.Lstat(base); err != nil {
			dst = base
		}
	}
	if _, err := rt.Lstat(dst); err == nil {
		dst += "." + strconv.FormatInt(time.Now().Unix(), 10)
	}
	if err := rt.MkdirAll(path.Dir(dst), 0o770); err != nil {
		return "", err
	}
	if err := rt.Rename(rel, dst); err != nil {
		return "", err
	}
	return sp + "/" + dst, nil
}
