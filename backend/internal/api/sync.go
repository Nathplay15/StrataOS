package api

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"strataos/internal/store"
	"strataos/internal/syncjournal"
)

// Sync API: two-way folder synchronization for the desktop app (and any
// other client paired with a device token). A client follows a folder F
// (a shared folder or a folder inside one): it lists what changed under F
// since its cursor (long poll), downloads files with Range, uploads them
// with the resumable uploads of the mobile API (conditional on the version
// it last saw, so a concurrent change becomes a conflict instead of being
// overwritten), and moves, deletes (to the recycle bin) and creates folders.
// Every path is relative to F and checked like the file manager does, with
// the user's current rights. Documented in docs/DEVELOPMENT.md (Sync API).

var (
	syncIdxOnce sync.Once
	syncIdx     = &syncjournal.Manager{}
)

func (s *Server) syncIndex() *syncjournal.Manager {
	syncIdxOnce.Do(func() {
		dir := s.dataDir
		if dir == "" {
			dir = os.TempDir()
		}
		syncIdx.Dir = filepath.Join(dir, "sync-index")
		go syncIdx.Loop()
	})
	return syncIdx
}

// syncEntry is a file or folder as clients see it (mtime in Unix milliseconds).
type syncEntry struct {
	Path    string `json:"path"`
	Dir     bool   `json:"dir,omitempty"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"`
	Version string `json:"version,omitempty"`
	Hash    string `json:"hash,omitempty"`
	ID      string `json:"id,omitempty"` // inode: the same file after a move
	Deleted bool   `json:"deleted,omitempty"`
}

func viewEntry(e syncjournal.Entry) syncEntry {
	v := syncEntry{Path: e.Path, Dir: e.Dir, Size: e.Size, MTime: e.MTime / 1e6, Version: e.Version(), Hash: e.Hash, Deleted: e.Deleted != 0}
	if e.Ino != 0 {
		v.ID = strconv.FormatUint(e.Ino, 36)
	}
	if v.Dir {
		v.Size = 0
	}
	return v
}

func statEntry(rel string, fi fs.FileInfo) syncEntry {
	return viewEntry(syncjournal.Entry{Path: rel, Dir: fi.IsDir(), Size: fi.Size(), MTime: fi.ModTime().UnixNano(), Ino: inodeOf(fi)})
}

// syncConflict answers 412 with the current state of the item.
type syncConflict struct {
	msg     string
	current *syncEntry
}

func (e *syncConflict) Error() string { return e.msg }

// syncFolder is a folder followed by a client.
type syncFolder struct {
	abs     string // the folder
	share   store.Share
	rel     string // the folder relative to its share ("" = the share itself)
	write   bool
	recycle bool
}

// syncFolderOf checks the folder a client follows: a shared folder or a
// folder inside one, readable by the user.
func (s *Server) syncFolderOf(r *http.Request, folder string) (syncFolder, error) {
	if folder == "" {
		return syncFolder{}, badRequest("folder required")
	}
	t, err := s.locate(r, folder)
	if err != nil {
		return syncFolder{}, err
	}
	if t.acc.ancestor {
		return syncFolder{}, badRequest("choose a shared folder or a folder inside one")
	}
	var sh *store.Share
	s.store.Read(func(st *store.State) {
		for _, x := range st.Shares {
			if (t.abs == x.Path || strings.HasPrefix(t.abs, x.Path+"/")) && (sh == nil || len(x.Path) > len(sh.Path)) {
				c := *x
				sh = &c
			}
		}
	})
	if sh == nil {
		return syncFolder{}, badRequest("choose a shared folder or a folder inside one")
	}
	if real, err := filepath.EvalSymlinks(sh.Path); err != nil || real != sh.Path {
		return syncFolder{}, &Error{Status: http.StatusServiceUnavailable, Msg: "the shared folder is unavailable (volume missing or encrypted folder locked)"}
	}
	f := syncFolder{abs: t.abs, share: *sh, rel: relTo(sh.Path, t.abs), write: t.acc.write, recycle: sh.RecycleBin}
	if f.rel == "." {
		f.rel = ""
	}
	rt, err := t.open()
	if err != nil {
		return syncFolder{}, err
	}
	defer rt.Close()
	if st, err := rt.Stat(t.rel); err != nil || !st.IsDir() {
		return syncFolder{}, notFound("folder not found")
	}
	return f, nil
}

// validSyncName is validFileName without the refusal of hidden files
// (dotfiles are synchronized), still refusing the names StrataOS uses.
func validSyncName(n string) error {
	switch {
	case n == "" || n == "." || n == "..", strings.ContainsAny(n, "/\\\x00"), len(n) > 255, syncjournal.Ignored("", n):
		return badRequest("invalid file name: " + n)
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return badRequest("invalid file name")
		}
	}
	return nil
}

// item returns the target of rel (a slash path) inside the folder, checked.
func (s *Server) syncItem(r *http.Request, f syncFolder, rel string, write bool) (target, error) {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return target{}, badRequest("path required")
	}
	if len(rel) > 4096 {
		return target{}, badRequest("path too long")
	}
	for _, part := range strings.Split(rel, "/") {
		if err := validSyncName(part); err != nil {
			return target{}, err
		}
	}
	t, err := s.locate(r, f.abs+"/"+rel)
	if err != nil {
		return target{}, err
	}
	if write {
		if err := s.syncWritable(r, f, t); err != nil {
			return target{}, err
		}
	}
	return t, nil
}

func (s *Server) syncWritable(r *http.Request, f syncFolder, t target) error {
	if s.ransomBlocked(userOf(r)) {
		return &Error{Status: http.StatusForbidden, Msg: "your account is blocked by the ransomware protection: ask an administrator"}
	}
	// Frozen or full shares are read-only for administrators too.
	switch {
	case f.share.Frozen:
		return &Error{Status: http.StatusForbidden, Msg: "this shared folder is frozen by the ransomware protection (read-only)"}
	case f.share.QuotaFull:
		return &Error{Status: http.StatusInsufficientStorage, Msg: "this shared folder is full (quota): read-only until space is freed"}
	case !t.acc.write || t.acc.ancestor:
		return &Error{Status: http.StatusForbidden, Msg: "read-only: you cannot change this shared folder"}
	}
	return nil
}

// shareRel is the path of an item relative to its share (the index root).
func (f syncFolder) shareRel(rel string) string {
	if f.rel == "" {
		return rel
	}
	return f.rel + "/" + rel
}

func (s *Server) syncRefresh(f syncFolder, rel string) {
	s.syncIndex().Refresh(f.share.Path, f.shareRel(rel))
}

// syncRaw registers a sync route answering JSON, 412 for conflicts.
func (s *Server) syncRaw(pattern string, h handler) {
	s.userRaw(pattern, func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		v, err := h(r)
		if err != nil {
			writeSyncErr(w, err)
			return
		}
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		writeJSON(w, http.StatusOK, v)
	})
}

func writeSyncErr(w http.ResponseWriter, err error) {
	var c *syncConflict
	if errors.As(err, &c) {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"error": c.msg, "conflict": true, "current": c.current})
		return
	}
	writeErr(w, err)
}

// current returns the state of an item for a conflict answer (nil: none).
func current(rt *os.Root, t target, rel string) *syncEntry {
	fi, err := rt.Lstat(t.rel)
	if err != nil {
		return nil
	}
	e := statEntry(rel, fi)
	return &e
}

// junkFiles are created by file browsers and removed with their folder.
func junkFile(n string) bool {
	return n == ".DS_Store" || n == "Thumbs.db" || n == "desktop.ini" || strings.HasPrefix(n, "._")
}

func (s *Server) syncRoutes() {
	s.user("GET /api/sync/info", func(r *http.Request) (any, error) {
		host, _ := hostname()
		return map[string]any{"hostname": host, "version": s.version, "time": time.Now().UnixMilli(), "api": 1,
			"features": []string{"changes", "longpoll", "hashes", "conditional-upload", "recycle"}, "maxUpload": maxUploadSize}, nil
	})

	// The shared folders the user may follow.
	s.user("GET /api/sync/shares", func(r *http.Request) (any, error) {
		type view struct {
			Name     string `json:"name"`
			Path     string `json:"path"`
			Writable bool   `json:"writable"`
			Recycle  bool   `json:"recycleBin"`
		}
		out := []view{}
		var list []store.Share
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				list = append(list, *sh)
			}
		})
		for _, sh := range list {
			t, err := s.locate(r, sh.Path)
			if err != nil || t.acc.ancestor || sh.Locked {
				continue
			}
			out = append(out, view{sh.Name, sh.Path, t.acc.write, sh.RecycleBin})
		}
		return out, nil
	})

	// What changed under a folder since a cursor (no cursor: everything).
	// wait (seconds, at most 55) holds the request until something changes.
	s.syncRaw("GET /api/sync/changes", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		f, err := s.syncFolderOf(r, q.Get("folder"))
		if err != nil {
			return nil, err
		}
		j, err := s.syncIndex().Get(f.share.Path)
		if err != nil {
			return nil, &Error{Status: http.StatusServiceUnavailable, Msg: "the shared folder is unavailable (volume missing or encrypted folder locked)"}
		}
		limit := min(max(qInt(r, "limit", 1000), 1), 5000)
		wait := min(max(qInt(r, "wait", 0), 0), 55)
		deadline := time.After(time.Duration(wait) * time.Second)
		for {
			ch := j.Wait()
			p, err := j.Changes(f.rel, q.Get("cursor"), limit)
			if err != nil {
				return nil, badRequest(err.Error())
			}
			if len(p.Changes) > 0 || p.Reset || p.More || wait == 0 {
				out := make([]syncEntry, len(p.Changes))
				for i, c := range p.Changes {
					out[i] = viewEntry(c)
				}
				return map[string]any{"cursor": p.Cursor, "reset": p.Reset, "more": p.More, "changes": out}, nil
			}
			select {
			case <-ch:
				time.Sleep(200 * time.Millisecond) // let a burst of changes arrive together
			case <-deadline:
				wait = 0
			case <-r.Context().Done():
				return nil, context.Canceled
			}
		}
	})

	// A file, with Range; ETag and X-Strata-Version carry its version.
	s.userRaw("GET /api/sync/file", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, err := s.syncFolderOf(r, q.Get("folder"))
		if err != nil {
			writeErr(w, err)
			return
		}
		t, err := s.syncItem(r, f, q.Get("path"), false)
		if err != nil {
			writeErr(w, err)
			return
		}
		rt, err := t.open()
		if err != nil {
			writeErr(w, err)
			return
		}
		defer rt.Close()
		fh, err := rt.Open(t.rel)
		if err != nil {
			writeErr(w, notFound("file not found"))
			return
		}
		defer fh.Close()
		fi, err := fh.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			writeErr(w, notFound("file not found"))
			return
		}
		v := syncjournal.StatVersion(fi)
		w.Header().Set("ETag", `"`+v+`"`)
		w.Header().Set("X-Strata-Version", v)
		w.Header().Set("X-Strata-Mtime", strconv.FormatInt(fi.ModTime().UnixMilli(), 10))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "", fi.ModTime(), fh)
	})

	// SHA-256 of files (from the index, else read now within a time budget:
	// the missing ones are left out, ask again).
	s.syncRaw("POST /api/sync/hashes", func(r *http.Request) (any, error) {
		var req struct {
			Folder string   `json:"folder"`
			Paths  []string `json:"paths"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Paths) > 1000 {
			return nil, badRequest("at most 1000 paths per request")
		}
		f, err := s.syncFolderOf(r, req.Folder)
		if err != nil {
			return nil, err
		}
		j, err := s.syncIndex().Get(f.share.Path)
		if err != nil {
			return nil, &Error{Status: http.StatusServiceUnavailable, Msg: "the shared folder is unavailable"}
		}
		out := map[string]string{}
		until := time.Now().Add(20 * time.Second)
		for _, p := range req.Paths {
			if time.Now().After(until) {
				break
			}
			if _, err := s.syncItem(r, f, p, false); err != nil {
				continue
			}
			if h, err := syncjournal.HashFile(j, f.shareRel(strings.Trim(p, "/"))); err == nil {
				out[p] = h
			}
		}
		return map[string]any{"hashes": out}, nil
	})

	// Create a folder and the missing ones above it.
	s.syncRaw("POST /api/sync/mkdir", func(r *http.Request) (any, error) {
		var req struct{ Folder, Path string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		f, err := s.syncFolderOf(r, req.Folder)
		if err != nil {
			return nil, err
		}
		t, err := s.syncItem(r, f, req.Path, true)
		if err != nil {
			return nil, err
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		rel := strings.Trim(req.Path, "/")
		parts := strings.Split(rel, "/")
		base := strings.TrimSuffix(t.rel, rel)
		for i := range parts {
			p := path.Join(base, strings.Join(parts[:i+1], "/"))
			st, err := rt.Lstat(p)
			if err == nil {
				if !st.IsDir() {
					return nil, &syncConflict{msg: "a file has this name", current: current(rt, target{rel: p}, strings.Join(parts[:i+1], "/"))}
				}
				continue
			}
			if err := rt.Mkdir(p, 0o775); err != nil && !errors.Is(err, os.ErrExist) {
				return nil, badRequest(err.Error())
			}
			s.chownForUser(r, rt, p)
		}
		s.syncRefresh(f, rel)
		fi, err := rt.Lstat(t.rel)
		if err != nil {
			return nil, err
		}
		return statEntry(rel, fi), nil
	})

	// Rename or move inside the shared folder. ifMatch: the version the
	// client knows (files), refused with 412 when the file changed.
	s.syncRaw("POST /api/sync/move", func(r *http.Request) (any, error) {
		var req struct {
			Folder  string `json:"folder"`
			From    string `json:"from"`
			To      string `json:"to"`
			IfMatch string `json:"ifMatch"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		f, err := s.syncFolderOf(r, req.Folder)
		if err != nil {
			return nil, err
		}
		from, err := s.syncItem(r, f, req.From, true)
		if err != nil {
			return nil, err
		}
		to, err := s.syncItem(r, f, req.To, true)
		if err != nil {
			return nil, err
		}
		if from.root != to.root || from.protected() || to.protected() {
			return nil, errForbidden
		}
		rt, err := from.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		fi, err := rt.Lstat(from.rel)
		if err != nil {
			return nil, &syncConflict{msg: "the item is gone", current: nil}
		}
		if !fi.IsDir() && req.IfMatch != "" && req.IfMatch != "*" && syncjournal.StatVersion(fi) != req.IfMatch {
			return nil, &syncConflict{msg: "the file changed on the NAS", current: current(rt, from, strings.Trim(req.From, "/"))}
		}
		// Only a change of case may land on "itself" (same inode).
		if ti, err := rt.Lstat(to.rel); err == nil && !os.SameFile(fi, ti) {
			return nil, &syncConflict{msg: "the destination exists", current: current(rt, to, strings.Trim(req.To, "/"))}
		}
		if st, err := rt.Stat(path.Dir(to.rel)); err != nil || !st.IsDir() {
			return nil, notFound("destination folder not found")
		}
		if err := rt.Rename(from.rel, to.rel); err != nil {
			return nil, badRequest(err.Error())
		}
		s.ransomObserve(userOf(r), clientIP(r), f.share.Name, f.shareRel(strings.Trim(req.To, "/")))
		s.syncRefresh(f, strings.Trim(req.From, "/"))
		s.syncRefresh(f, strings.Trim(req.To, "/"))
		ni, err := rt.Lstat(to.rel)
		if err != nil {
			return nil, err
		}
		return statEntry(strings.Trim(req.To, "/"), ni), nil
	})

	// Delete a file (to the recycle bin of the share when it has one) or an
	// empty folder (files of file browsers like .DS_Store do not count).
	s.syncRaw("POST /api/sync/delete", func(r *http.Request) (any, error) {
		var req struct {
			Folder  string `json:"folder"`
			Path    string `json:"path"`
			IfMatch string `json:"ifMatch"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		f, err := s.syncFolderOf(r, req.Folder)
		if err != nil {
			return nil, err
		}
		rel := strings.Trim(req.Path, "/")
		t, err := s.syncItem(r, f, rel, true)
		if err != nil {
			return nil, err
		}
		if t.protected() {
			return nil, errForbidden
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		fi, err := rt.Lstat(t.rel)
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{"gone": true}, nil
		}
		if err != nil {
			return nil, err
		}
		recycled := false
		if fi.IsDir() {
			d, err := rt.Open(t.rel)
			if err != nil {
				return nil, err
			}
			ents, _ := d.ReadDir(-1)
			d.Close()
			for _, e := range ents {
				if !junkFile(e.Name()) || !e.Type().IsRegular() {
					return nil, &syncConflict{msg: "the folder is not empty", current: current(rt, t, rel)}
				}
			}
			for _, e := range ents {
				_ = rt.Remove(path.Join(t.rel, e.Name()))
			}
			if err := rt.Remove(t.rel); err != nil {
				return nil, badRequest(err.Error())
			}
		} else {
			if req.IfMatch != "*" && syncjournal.StatVersion(fi) != req.IfMatch {
				return nil, &syncConflict{msg: "the file changed on the NAS", current: current(rt, t, rel)}
			}
			s.ransomObserve(userOf(r), clientIP(r), f.share.Name, f.shareRel(rel))
			recycled, err = s.trashFile(userOf(r), t.abs)
			if err != nil {
				return nil, err
			}
		}
		s.syncRefresh(f, rel)
		return map[string]any{"recycled": recycled}, nil
	})

	s.syncDeviceRoutes()
}

// inodeOf returns the inode number of a file (0 when unknown).
func inodeOf(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// syncReplace completes an upload made with replace: the file takes the
// place of the one the client knew (ifMatch: its version; "": there must
// be none; "*": whatever is there). A mismatch keeps the upload, complete,
// for another commit (under a conflict name, typically).
func (s *Server) syncReplace(u *mobileUpload, t target, rt *os.Root) (any, error) {
	tmp := path.Join(t.rel, u.temp())
	st, err := rt.Lstat(tmp)
	if err != nil || !st.Mode().IsRegular() || st.Size() != u.Size {
		return nil, &Error{Status: http.StatusConflict, Msg: "upload incomplete"}
	}
	if u.SHA256 != "" {
		sum, err := fileSHA256(rt, tmp)
		if err != nil {
			return nil, err
		}
		if sum != u.SHA256 {
			_ = rt.Remove(tmp)
			s.dropUpload(u.ID)
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: "checksum mismatch: the file was damaged in transit, upload it again"}
		}
	}
	dst := path.Join(t.rel, u.Name)
	cur, err := rt.Lstat(dst)
	var curE *syncEntry
	if err == nil {
		e := statEntry(u.Name, cur)
		curE = &e
	}
	switch {
	case err == nil && !cur.Mode().IsRegular():
		return nil, &syncConflict{msg: "a folder has this name", current: curE}
	case u.IfMatch == "*":
	case err == nil && syncjournal.StatVersion(cur) != u.IfMatch:
		return nil, &syncConflict{msg: "the file changed on the NAS", current: curE}
	case err != nil && u.IfMatch != "":
		return nil, &syncConflict{msg: "the file was deleted on the NAS", current: nil}
	}
	if u.ModTime > 0 {
		mt := time.UnixMilli(u.ModTime)
		_ = rt.Chtimes(tmp, mt, mt)
	}
	if !u.Admin {
		s.chownTo(u.User, rt, tmp)
	}
	if err := rt.Rename(tmp, dst); err != nil {
		return nil, err
	}
	s.dropUpload(u.ID)
	fi, err := rt.Lstat(dst)
	if err != nil {
		return nil, err
	}
	// The index of the share sees the new file now (and its hash, when given).
	if shp := s.shareOf(filepath.Join(t.abs, u.Name)); shp != nil {
		sh := shp.Path
		rel := relTo(sh, filepath.Join(t.abs, u.Name))
		syncIdx := s.syncIndex()
		syncIdx.Refresh(sh, rel)
		if u.SHA256 != "" {
			if j, err := syncIdx.Get(sh); err == nil {
				j.SetHash(rel, inodeOf(fi), fi.Size(), fi.ModTime().UnixNano(), u.SHA256)
			}
		}
	}
	e := statEntry(u.Name, fi)
	if u.SHA256 != "" {
		e.Hash = u.SHA256
	}
	return map[string]any{"id": u.ID, "offset": u.Size, "size": u.Size, "done": true, "name": u.Name, "path": filepath.Join(t.abs, u.Name), "entry": e}, nil
}
