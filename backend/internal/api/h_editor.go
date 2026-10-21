package api

// Text editing in the file manager: read a file with its version, write it
// back atomically (temporary file in the same folder, then rename) unless it
// changed meanwhile. OnlyOffice saves go through the same saveFile.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"strataos/internal/accounts"
	"strataos/internal/store"
)

const (
	editMaxBytes = 5 << 20  // editable in the browser
	viewMaxBytes = 20 << 20 // shown read-only up to this size
)

// fileETag identifies a version of a file: modification time and size.
func fileETag(fi fs.FileInfo) string {
	return strconv.FormatInt(fi.ModTime().UnixNano(), 36) + "-" + strconv.FormatInt(fi.Size(), 36)
}

// conflictError: the file is not the version the client loaded.
type conflictError struct {
	msg  string
	etag string // current version ("" when the file is gone)
}

func (e *conflictError) Error() string { return e.msg }

type saveOpts struct {
	user   string
	admin  bool
	etag   string // version the client loaded ("" with create)
	create bool   // new file: refuse if it exists
	force  bool   // overwrite whatever is there now
}

type savedFile struct {
	ETag    string    `json:"etag"`
	ModTime time.Time `json:"modTime"`
	Size    int64     `json:"size"`
}

// Saves of one path are serialized (the version check and the rename).
var saveLocks = struct {
	sync.Mutex
	m map[string]*pathLock
}{m: map[string]*pathLock{}}

type pathLock struct {
	sync.Mutex
	n int
}

func lockPath(p string) func() {
	saveLocks.Lock()
	l := saveLocks.m[p]
	if l == nil {
		l = &pathLock{}
		saveLocks.m[p] = l
	}
	l.n++
	saveLocks.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		saveLocks.Lock()
		if l.n--; l.n == 0 {
			delete(saveLocks.m, p)
		}
		saveLocks.Unlock()
	}
}

// shareOf returns the most specific share holding abs (a copy), or nil.
func (s *Server) shareOf(abs string) *store.Share {
	var best *store.Share
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			if abs == sh.Path || strings.HasPrefix(abs, sh.Path+"/") {
				if best == nil || len(sh.Path) > len(best.Path) {
					c := *sh
					best = &c
				}
			}
		}
	})
	return best
}

// readOnlyReason explains why a writable target cannot be written now: a
// share frozen by the ransomware protection or full, for administrators too.
func (s *Server) readOnlyReason(t target) string {
	if !t.acc.write || t.acc.ancestor {
		return "read-only access"
	}
	if sh := s.shareOf(t.abs); sh != nil {
		switch {
		case sh.Frozen:
			return "shared folder " + sh.Name + " is frozen by the ransomware protection"
		case sh.QuotaFull:
			return "shared folder " + sh.Name + " is over its quota"
		}
	}
	return ""
}

// quotaRoom returns how many more bytes user may store at abs (-1: no limit):
// the share quota when it blocks writes, and the user's quota on the volume.
// The daemon writes as root, which the kernel would let through.
func (s *Server) quotaRoom(user string, admin bool, abs string) int64 {
	if admin {
		return -1
	}
	room := int64(-1)
	lower := func(v int64) {
		if v < 0 {
			v = 0
		}
		if room < 0 || v < room {
			room = v
		}
	}
	if sh := s.shareOf(abs); sh != nil && sh.QuotaGB > 0 && sh.QuotaBlock {
		lower(int64(sh.QuotaGB*1e9) - sh.Used)
	}
	best := ""
	var q struct{ used, limit int64 }
	for _, uq := range s.userQuotas(user) {
		mp := filepath.Clean(uq.MountPoint)
		if uq.Limit > 0 && (abs == mp || strings.HasPrefix(abs, mp+"/")) && len(mp) > len(best) {
			best, q.used, q.limit = mp, uq.Used, uq.Limit
		}
	}
	if best != "" {
		lower(q.limit - q.used)
	}
	return room
}

// copyMeta gives the new file the owner, mode, ACL and extended attributes
// (Samba DOS attributes...) of the file it replaces.
func copyMeta(old *os.File, oldInfo fs.FileInfo, f *os.File) {
	if st, ok := oldInfo.Sys().(*syscall.Stat_t); ok {
		_ = f.Chown(int(st.Uid), int(st.Gid))
	}
	_ = f.Chmod(oldInfo.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky))
	if old == nil {
		return
	}
	buf := make([]byte, 4096)
	n, err := unix.Flistxattr(int(old.Fd()), buf)
	if err != nil {
		return
	}
	for _, name := range strings.Split(string(buf[:n]), "\x00") {
		if name == "" {
			continue
		}
		val := make([]byte, 64<<10)
		m, err := unix.Fgetxattr(int(old.Fd()), name, val)
		if err == nil {
			_ = unix.Fsetxattr(int(f.Fd()), name, val[:m], 0)
		}
	}
}

// saveFile writes src to t atomically. The version check runs again just
// before the rename, so a change made meanwhile (SMB, another tab) is not lost.
func (s *Server) saveFile(t target, src io.Reader, o saveOpts) (savedFile, error) {
	if reason := s.readOnlyReason(t); reason != "" {
		return savedFile{}, &Error{Status: http.StatusForbidden, Msg: reason}
	}
	name := path.Base(t.rel)
	if t.rel == "." || t.protected() || name == "." || name == ".." || name == "/" {
		return savedFile{}, &Error{Status: http.StatusForbidden, Msg: "this path cannot be written"}
	}
	unlock := lockPath(t.abs)
	defer unlock()
	rt, err := t.open()
	if err != nil {
		return savedFile{}, err
	}
	defer rt.Close()

	check := func() (fs.FileInfo, error) {
		fi, err := rt.Lstat(t.rel)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, notFound("folder not found")
		}
		if fi != nil && !fi.Mode().IsRegular() {
			return nil, badRequest("only regular files can be edited")
		}
		switch {
		case o.force:
		case o.create && fi != nil:
			return nil, &conflictError{"a file with this name already exists", fileETag(fi)}
		case !o.create && fi == nil:
			return nil, &conflictError{"the file was deleted since it was opened", ""}
		case !o.create && fileETag(fi) != o.etag:
			return nil, &conflictError{"the file was changed since it was opened", fileETag(fi)}
		}
		return fi, nil
	}
	oldInfo, err := check()
	if err != nil {
		return savedFile{}, err
	}
	if pi, err := rt.Stat(path.Dir(t.rel)); err != nil || !pi.IsDir() {
		return savedFile{}, notFound("folder not found")
	}
	room := s.quotaRoom(o.user, o.admin, t.abs)

	rnd := make([]byte, 6)
	_, _ = rand.Read(rnd)
	tmp := path.Join(path.Dir(t.rel), "."+name+".strata-"+hex.EncodeToString(rnd)+".tmp")
	f, err := rt.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
	if err != nil {
		return savedFile{}, badRequest(err.Error())
	}
	done := false
	defer func() {
		if !done {
			f.Close()
			_ = rt.Remove(tmp)
		}
	}()
	if oldInfo != nil {
		old, _ := rt.Open(t.rel)
		copyMeta(old, oldInfo, f)
		if old != nil {
			old.Close()
		}
	} else if !o.admin {
		// Like uploads: the user's file, group of the folder (the default ACL applies).
		if uid, _, err := accounts.IDs(o.user); err == nil {
			gid := -1
			if pi, err := rt.Stat(path.Dir(t.rel)); err == nil {
				if st, ok := pi.Sys().(*syscall.Stat_t); ok {
					gid = int(st.Gid)
				}
			}
			_ = f.Chown(uid, gid)
		}
	}
	n, err := io.Copy(f, src)
	if err != nil {
		return savedFile{}, err
	}
	var oldSize int64
	if oldInfo != nil {
		oldSize = oldInfo.Size()
	}
	if room >= 0 && n-oldSize > room {
		return savedFile{}, &Error{Status: http.StatusInsufficientStorage, Msg: "quota exceeded"}
	}
	if err := f.Sync(); err != nil {
		return savedFile{}, err
	}
	if err := f.Close(); err != nil {
		_ = rt.Remove(tmp)
		done = true
		return savedFile{}, err
	}
	done = true
	if _, err := check(); err != nil {
		_ = rt.Remove(tmp)
		return savedFile{}, err
	}
	if err := rt.Rename(tmp, t.rel); err != nil {
		_ = rt.Remove(tmp)
		return savedFile{}, err
	}
	fi, err := rt.Lstat(t.rel)
	if err != nil {
		return savedFile{}, err
	}
	return savedFile{ETag: fileETag(fi), ModTime: fi.ModTime(), Size: fi.Size()}, nil
}

func writeSaveErr(w http.ResponseWriter, err error) {
	var c *conflictError
	if errors.As(err, &c) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": c.msg, "conflict": true, "etag": c.etag})
		return
	}
	writeErr(w, err)
}

func (s *Server) editorRoutes() {
	// Content of a text file with its version, for the editor.
	s.user("GET /api/files/content", func(r *http.Request) (any, error) {
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil || !t.acc.read || t.acc.ancestor {
			return nil, firstErr(err, errForbidden)
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		lfi, err := rt.Lstat(t.rel)
		if err != nil {
			return nil, notFound("file not found")
		}
		f, err := rt.Open(t.rel)
		if err != nil {
			return nil, notFound("file not found")
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			return nil, badRequest("not a regular file")
		}
		out := map[string]any{"path": t.abs, "name": filepath.Base(t.abs), "etag": fileETag(lfi), "modTime": fi.ModTime(), "size": fi.Size()}
		reason := s.readOnlyReason(t)
		switch {
		case lfi.Mode()&fs.ModeSymlink != 0:
			reason = "symbolic link"
		case fi.Size() > editMaxBytes && reason == "":
			reason = fmt.Sprintf("file larger than %d MB", editMaxBytes>>20)
		}
		if fi.Size() > viewMaxBytes {
			out["tooLarge"] = true
			reason = fmt.Sprintf("file larger than %d MB", viewMaxBytes>>20)
		} else {
			data, err := io.ReadAll(io.LimitReader(f, viewMaxBytes+1))
			if err != nil {
				return nil, err
			}
			if !utf8.Valid(data) {
				out["binary"] = true
				reason = "not a text file"
			} else {
				out["content"] = string(data)
			}
		}
		out["writable"] = reason == ""
		out["readOnlyReason"] = reason
		return out, nil
	})

	// Raw body = new content. etag: the version loaded; create=1: new file;
	// force=1: overwrite after a conflict.
	s.userRaw("PUT /api/files/content", func(w http.ResponseWriter, r *http.Request) {
		qv := r.URL.Query()
		t, err := s.locate(r, qv.Get("path"))
		if err != nil {
			writeErr(w, err)
			return
		}
		if r.ContentLength > editMaxBytes {
			writeErr(w, &Error{Status: http.StatusRequestEntityTooLarge, Msg: fmt.Sprintf("file larger than %d MB", editMaxBytes>>20)})
			return
		}
		body := http.MaxBytesReader(w, r.Body, editMaxBytes)
		res, err := s.saveFile(t, body, saveOpts{user: userOf(r), admin: isAdmin(r), etag: qv.Get("etag"), create: qv.Get("create") == "1", force: qv.Get("force") == "1"})
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				err = &Error{Status: http.StatusRequestEntityTooLarge, Msg: fmt.Sprintf("file larger than %d MB", editMaxBytes>>20)}
			}
			writeSaveErr(w, err)
			return
		}
		s.audit(r, "files", "Saved file "+t.abs)
		writeJSON(w, http.StatusOK, res)
	})
}
