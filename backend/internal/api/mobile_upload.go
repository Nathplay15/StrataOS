package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// Resumable uploads (the app's photo backup and large files), in the spirit
// of tus: create the upload (folder, name, size, date), send the bytes in
// chunks with PATCH and the current offset, and the file appears in the
// folder once complete, all at once, owned by the user and with its date.
// The bytes go to a hidden file in the destination folder itself (same
// filesystem, so the final step is a link/rename, and the user's quota sees
// them), named .strata-upload-<id>. What a dropped connection had written
// stays: the app asks for the offset and continues from there, also after a
// restart of either side. Uploads idle for two days are removed.

const (
	uploadPrefix     = ".strata-upload-"
	maxUploadSize    = 1 << 40 // 1 TiB
	maxChunk         = 256 << 20
	maxUploadsByUser = 64
	uploadIdle       = 48 * time.Hour
)

type mobileUpload struct {
	ID      string `json:"id"`
	User    string `json:"user"`
	Admin   bool   `json:"admin,omitempty"` // created by an administrator: files keep root as owner, like web uploads
	Folder  string `json:"folder"`          // absolute destination folder
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime,omitempty"` // Unix milliseconds
	SHA256  string `json:"sha256,omitempty"`
	// Sync clients (sync.go): dotfiles allowed, and with Replace the file
	// takes the place of the version IfMatch ("" = none, "*" = any).
	Sync    bool      `json:"sync,omitempty"`
	Replace bool      `json:"replace,omitempty"`
	IfMatch string    `json:"ifMatch,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

func (u *mobileUpload) temp() string { return uploadPrefix + u.ID }

type uploadSet struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]*mobileUpload
	locks  map[string]*sync.Mutex
}

var uploads = &uploadSet{m: map[string]*mobileUpload{}, locks: map[string]*sync.Mutex{}}

func (s *Server) uploadsPath() string { return filepath.Join(s.dataDir, "mobile-uploads.json") }

func (s *Server) uploadsLoadLocked() {
	if uploads.loaded {
		return
	}
	uploads.loaded = true
	if data, err := os.ReadFile(s.uploadsPath()); err == nil {
		_ = json.Unmarshal(data, &uploads.m)
	}
	if uploads.m == nil {
		uploads.m = map[string]*mobileUpload{}
	}
}

func (s *Server) uploadsSaveLocked() {
	if s.dataDir == "" {
		return
	}
	data, err := json.Marshal(uploads.m)
	if err == nil {
		err = sys.WriteFileAtomic(s.uploadsPath(), data, 0o600)
	}
	if err != nil {
		slog.Warn("save uploads", "err", err)
	}
}

// getUpload returns a copy of the upload of user and its lock (held by the
// caller while it writes).
func (s *Server) getUpload(user, id string) (*mobileUpload, *sync.Mutex) {
	uploads.mu.Lock()
	defer uploads.mu.Unlock()
	s.uploadsLoadLocked()
	u := uploads.m[id]
	if u == nil || u.User != user {
		return nil, nil
	}
	l := uploads.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		uploads.locks[id] = l
	}
	c := *u
	return &c, l
}

func (s *Server) dropUpload(id string) {
	uploads.mu.Lock()
	delete(uploads.m, id)
	delete(uploads.locks, id)
	s.uploadsSaveLocked()
	uploads.mu.Unlock()
}

func (s *Server) touchUpload(id string) {
	uploads.mu.Lock()
	if u := uploads.m[id]; u != nil && time.Since(u.Updated) > time.Minute {
		u.Updated = time.Now()
		s.uploadsSaveLocked()
	}
	uploads.mu.Unlock()
}

// validFileName refuses separators, "." and "..", control characters and
// overlong names; leading dots are refused too (hidden, and our temp files).
func validFileName(n string) error {
	switch {
	case n == "" || n == "." || n == "..", strings.ContainsAny(n, "/\\\x00"), strings.HasPrefix(n, "."), len(n) > 255:
		return badRequest("invalid file name")
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return badRequest("invalid file name")
		}
	}
	return nil
}

// uploadTarget locates the destination folder for the user (rights are
// checked again at every step: they may change during a long upload).
func (s *Server) uploadTarget(user string, admin bool, folder string) (target, error) {
	t, err := s.locateFor(user, admin, folder)
	if err != nil {
		return target{}, err
	}
	if !t.acc.write || t.acc.ancestor {
		return target{}, &Error{Status: http.StatusForbidden, Msg: "no write access to this folder"}
	}
	return t, nil
}

// spaceFor checks that size more bytes fit: free space, the share's quota
// (when it blocks writes) and the user's quota on the volume.
func (s *Server) spaceFor(r *http.Request, t target, size int64) error {
	if u, err := storage.StatFS(t.abs); err == nil && u.Free < size {
		return &Error{Status: http.StatusInsufficientStorage, Msg: "not enough free space on the volume"}
	}
	var sh store.Share
	s.store.Read(func(st *store.State) {
		for _, x := range st.Shares {
			if x.Path == t.root {
				sh = *x
			}
		}
	})
	if sh.QuotaBlock && sh.QuotaGB > 0 && float64(sh.Used+size) > sh.QuotaGB*1e9 {
		return &Error{Status: http.StatusInsufficientStorage, Msg: "the shared folder's quota would be exceeded"}
	}
	if isAdmin(r) {
		return nil
	}
	var best storage.UserQuota
	for _, q := range s.userQuotas(userOf(r)) {
		if q.Limit > 0 && (t.abs == q.MountPoint || strings.HasPrefix(t.abs, q.MountPoint+"/")) && len(q.MountPoint) > len(best.MountPoint) {
			best = q
		}
	}
	if best.Limit > 0 && best.Used+size > best.Limit {
		return &Error{Status: http.StatusInsufficientStorage, Msg: "your quota on this volume would be exceeded"}
	}
	return nil
}

// freeName returns name, or "name (2).ext"… when name is taken in dir.
func freeName(rt *os.Root, dir, name string) (string, error) {
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		if _, err := rt.Lstat(path.Join(dir, n)); errors.Is(err, os.ErrNotExist) {
			return n, nil
		}
	}
	return "", badRequest("too many files with this name")
}

// sameFile tells whether rel is a regular file of that size (and date, when given).
func sameFile(rt *os.Root, rel string, size, modMillis int64) bool {
	st, err := rt.Lstat(rel)
	if err != nil || !st.Mode().IsRegular() || st.Size() != size {
		return false
	}
	return modMillis == 0 || st.ModTime().Truncate(time.Second).Equal(time.UnixMilli(modMillis).Truncate(time.Second))
}

func fileSHA256(rt *os.Root, rel string) (string, error) {
	f, err := rt.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// finishUpload moves a complete temp file to its final name in dir, never
// replacing an existing file: the same file already there (name, size and
// date) is kept and the copy dropped; another file of that name makes it
// "name (2).ext". It returns the final name and whether it was a duplicate.
func finishUpload(rt *os.Root, dir string, u *mobileUpload, owner func(rel string)) (string, bool, error) {
	tmp := path.Join(dir, u.temp())
	st, err := rt.Lstat(tmp)
	if err != nil || !st.Mode().IsRegular() || st.Size() != u.Size {
		return "", false, &Error{Status: http.StatusConflict, Msg: "upload incomplete"}
	}
	if u.SHA256 != "" {
		sum, err := fileSHA256(rt, tmp)
		if err != nil {
			return "", false, err
		}
		if sum != u.SHA256 {
			_ = rt.Remove(tmp)
			return "", false, &Error{Status: http.StatusUnprocessableEntity, Msg: "checksum mismatch: the file was damaged in transit, upload it again"}
		}
	}
	if sameFile(rt, path.Join(dir, u.Name), u.Size, u.ModTime) {
		_ = rt.Remove(tmp)
		return u.Name, true, nil
	}
	if u.ModTime > 0 {
		mt := time.UnixMilli(u.ModTime)
		_ = rt.Chtimes(tmp, mt, mt)
	}
	if owner != nil {
		owner(tmp)
	}
	for range 5 {
		name, err := freeName(rt, dir, u.Name)
		if err != nil {
			return "", false, err
		}
		// link fails if the name was taken meanwhile: never overwrite.
		err = rt.Link(tmp, path.Join(dir, name))
		if err == nil {
			_ = rt.Remove(tmp)
			return name, false, nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		// No hard links here (some FUSE filesystems): rename after the check.
		if err := rt.Rename(tmp, path.Join(dir, name)); err != nil {
			return "", false, err
		}
		return name, false, nil
	}
	return "", false, &Error{Status: http.StatusConflict, Msg: "destination busy, try again"}
}

func (s *Server) uploadView(u *mobileUpload, offset int64) map[string]any {
	return map[string]any{"id": u.ID, "offset": offset, "size": u.Size, "name": u.Name, "folder": u.Folder}
}

// uploadOffset is the size of the temp file (what was really written).
func uploadOffset(rt *os.Root, t target, u *mobileUpload) (int64, error) {
	st, err := rt.Lstat(path.Join(t.rel, u.temp()))
	if err != nil {
		return 0, &Error{Status: http.StatusGone, Msg: "upload expired or cancelled: start again"}
	}
	if !st.Mode().IsRegular() {
		return 0, badRequest("invalid upload")
	}
	return st.Size(), nil
}

func (s *Server) mobileUploadRoutes() {
	// Create an upload. The app may give the SHA-256 of the file: it is
	// checked once complete.
	s.syncRaw("POST /api/mobile/uploads", func(r *http.Request) (any, error) {
		var req struct {
			Folder  string `json:"folder"`
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			ModTime int64  `json:"modTime"`
			SHA256  string `json:"sha256"`
			MkDirs  bool   `json:"mkdirs"` // create missing folders under the share (backup albums, years)
			Sync    bool   `json:"sync"`
			Replace bool   `json:"replace"`
			IfMatch string `json:"ifMatch"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		nameOK := validFileName
		if req.Sync {
			nameOK = validSyncName
			if s.ransomBlocked(userOf(r)) {
				return nil, &Error{Status: http.StatusForbidden, Msg: "your account is blocked by the ransomware protection: ask an administrator"}
			}
		}
		if err := nameOK(req.Name); err != nil {
			return nil, err
		}
		if len(req.IfMatch) > 64 {
			return nil, badRequest("invalid ifMatch")
		}
		if req.Size < 0 || req.Size > maxUploadSize {
			return nil, badRequest("invalid size")
		}
		req.SHA256 = strings.ToLower(req.SHA256)
		if req.SHA256 != "" {
			if b, err := hex.DecodeString(req.SHA256); err != nil || len(b) != 32 {
				return nil, badRequest("invalid sha256")
			}
		}
		if req.MkDirs {
			if err := s.mkdirsFor(r, req.Folder); err != nil {
				return nil, err
			}
		}
		t, err := s.uploadTarget(userOf(r), isAdmin(r), req.Folder)
		if err != nil {
			return nil, err
		}
		if reason := s.readOnlyReason(t); req.Sync && reason != "" {
			return nil, &Error{Status: http.StatusForbidden, Msg: reason}
		}
		if err := s.spaceFor(r, t, req.Size); err != nil {
			return nil, err
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		if st, err := rt.Stat(t.rel); err != nil || !st.IsDir() {
			return nil, notFound("folder not found")
		}
		u := &mobileUpload{ID: token()[:24], User: userOf(r), Admin: isAdmin(r), Folder: t.abs, Name: req.Name, Size: req.Size,
			ModTime: req.ModTime, SHA256: req.SHA256, Sync: req.Sync, Replace: req.Replace, IfMatch: req.IfMatch, Created: time.Now(), Updated: time.Now()}
		uploads.mu.Lock()
		s.uploadsLoadLocked()
		n := 0
		for _, x := range uploads.m {
			if x.User == u.User {
				n++
			}
		}
		if n >= maxUploadsByUser {
			uploads.mu.Unlock()
			return nil, &Error{Status: http.StatusTooManyRequests, Msg: "too many uploads in progress"}
		}
		f, err := rt.OpenFile(path.Join(t.rel, u.temp()), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o664)
		if err != nil {
			uploads.mu.Unlock()
			return nil, badRequest(err.Error())
		}
		f.Close()
		if !u.Admin {
			s.chownTo(u.User, rt, path.Join(t.rel, u.temp()))
		}
		uploads.m[u.ID] = u
		s.uploadsSaveLocked()
		uploads.mu.Unlock()
		if req.Size == 0 {
			return s.completeUpload(u, t, rt)
		}
		return s.uploadView(u, 0), nil
	})

	s.user("GET /api/mobile/uploads/{id}", func(r *http.Request) (any, error) {
		u, l := s.getUpload(userOf(r), r.PathValue("id"))
		if u == nil {
			return nil, &Error{Status: http.StatusGone, Msg: "upload expired or cancelled: start again"}
		}
		l.Lock()
		defer l.Unlock()
		t, err := s.uploadTarget(u.User, isAdmin(r), u.Folder)
		if err != nil {
			return nil, err
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		off, err := uploadOffset(rt, t, u)
		if err != nil {
			s.dropUpload(u.ID)
			return nil, err
		}
		return s.uploadView(u, off), nil
	})

	// Append a chunk at Upload-Offset (the current size, else 409 with the
	// right offset). The last chunk completes the file.
	s.userRaw("PATCH /api/mobile/uploads/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.patchUpload(w, r)
		if err != nil {
			var c *syncConflict
			if errors.As(err, &c) {
				writeSyncErr(w, err)
				return
			}
			var e *Error
			if errors.As(err, &e) && e.Status == http.StatusConflict {
				if off, ok := v.(int64); ok {
					w.Header().Set("Upload-Offset", strconv.FormatInt(off, 10))
					writeJSON(w, http.StatusConflict, map[string]any{"error": e.Msg, "offset": off})
					return
				}
			}
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})

	// Complete a finished upload again, possibly under another name or
	// condition (after a 412: the file changed on the NAS meanwhile).
	s.syncRaw("POST /api/mobile/uploads/{id}/commit", func(r *http.Request) (any, error) {
		var req struct {
			Name    *string `json:"name"`
			IfMatch *string `json:"ifMatch"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		u, l := s.getUpload(userOf(r), r.PathValue("id"))
		if u == nil {
			return nil, &Error{Status: http.StatusGone, Msg: "upload expired or cancelled: start again"}
		}
		l.Lock()
		defer l.Unlock()
		if req.Name != nil {
			check := validFileName
			if u.Sync {
				check = validSyncName
			}
			if err := check(*req.Name); err != nil {
				return nil, err
			}
			u.Name = *req.Name
		}
		if req.IfMatch != nil {
			u.IfMatch = *req.IfMatch
		}
		uploads.mu.Lock()
		if x := uploads.m[u.ID]; x != nil {
			x.Name, x.IfMatch = u.Name, u.IfMatch
			s.uploadsSaveLocked()
		}
		uploads.mu.Unlock()
		t, err := s.uploadTarget(u.User, isAdmin(r), u.Folder)
		if err != nil {
			return nil, err
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		return s.completeUpload(u, t, rt)
	})

	s.user("DELETE /api/mobile/uploads/{id}", func(r *http.Request) (any, error) {
		u, l := s.getUpload(userOf(r), r.PathValue("id"))
		if u == nil {
			return nil, nil
		}
		l.Lock()
		defer l.Unlock()
		s.removeUploadFile(u)
		s.dropUpload(u.ID)
		return nil, nil
	})

	// Which of these files the folder already has (backup: skip them).
	// Same name and size (and date when given) is a match; with a SHA-256,
	// a file of the same size under another name ("IMG_1 (2).jpg") too.
	s.user("POST /api/mobile/exists", func(r *http.Request) (any, error) {
		var req struct {
			Folder string `json:"folder"`
			Files  []struct {
				Name    string `json:"name"`
				Size    int64  `json:"size"`
				ModTime int64  `json:"modTime"`
				SHA256  string `json:"sha256"`
			} `json:"files"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Files) > 1000 {
			return nil, badRequest("at most 1000 files per request")
		}
		type result struct {
			Name   string `json:"name"`
			Exists bool   `json:"exists"`
			As     string `json:"as,omitempty"` // the name found, when it differs
		}
		out := make([]result, len(req.Files))
		for i, f := range req.Files {
			out[i].Name = f.Name
		}
		t, err := s.locate(r, req.Folder)
		if err != nil || t.acc.ancestor {
			return map[string]any{"files": out}, nil // no such folder for this user: nothing there
		}
		rt, err := t.open()
		if err != nil {
			return map[string]any{"files": out}, nil
		}
		defer rt.Close()
		var bySize map[int64][]string // lazily: regular files of the folder by size
		hashes := map[string]string{}
		budget := int64(4 << 30) // bytes hashed per request at most
		for i, f := range req.Files {
			if validFileName(f.Name) != nil {
				continue
			}
			if sameFile(rt, path.Join(t.rel, f.Name), f.Size, 0) {
				out[i].Exists = true
				continue
			}
			if len(f.SHA256) != 64 {
				continue
			}
			if bySize == nil {
				bySize = map[int64][]string{}
				if d, err := rt.Open(t.rel); err == nil {
					ents, _ := d.ReadDir(-1)
					d.Close()
					for _, e := range ents {
						if info, err := e.Info(); err == nil && info.Mode().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
							bySize[info.Size()] = append(bySize[info.Size()], e.Name())
						}
					}
				}
			}
			for _, cand := range bySize[f.Size] {
				h, ok := hashes[cand]
				if !ok {
					if budget < f.Size {
						break
					}
					budget -= f.Size
					h, _ = fileSHA256(rt, path.Join(t.rel, cand))
					hashes[cand] = h
				}
				if h == strings.ToLower(f.SHA256) {
					out[i].Exists, out[i].As = true, cand
					break
				}
			}
		}
		return map[string]any{"files": out}, nil
	})
}

// mkdirsFor creates the missing folders of p below the deepest existing
// folder the user may write to (the share or a folder inside it).
func (s *Server) mkdirsFor(r *http.Request, p string) error {
	clean := filepath.Clean("/" + p)
	t, err := s.locate(r, clean)
	if err != nil {
		return err
	}
	if t.acc.ancestor || t.rel == "." {
		return nil // nothing to create (or not inside a share)
	}
	if !t.acc.write {
		return errForbidden
	}
	rt, err := t.open()
	if err != nil {
		return err
	}
	defer rt.Close()
	if st, err := rt.Stat(t.rel); err == nil && st.IsDir() {
		return nil
	}
	// Create one level at a time so each new folder gets the user as owner.
	parts := strings.Split(t.rel, "/")
	for i := range parts {
		rel := strings.Join(parts[:i+1], "/")
		if st, err := rt.Stat(rel); err == nil {
			if !st.IsDir() {
				return badRequest("not a folder: " + rel)
			}
			continue
		}
		if err := rt.Mkdir(rel, 0o775); err != nil && !errors.Is(err, os.ErrExist) {
			return badRequest(err.Error())
		}
		s.chownForUser(r, rt, rel)
	}
	return nil
}

func (s *Server) patchUpload(w http.ResponseWriter, r *http.Request) (any, error) {
	u, l := s.getUpload(userOf(r), r.PathValue("id"))
	if u == nil {
		return nil, &Error{Status: http.StatusGone, Msg: "upload expired or cancelled: start again"}
	}
	off, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || off < 0 {
		return nil, badRequest("Upload-Offset header required")
	}
	l.Lock()
	defer l.Unlock()
	t, err := s.uploadTarget(u.User, isAdmin(r), u.Folder)
	if err != nil {
		return nil, err
	}
	rt, err := t.open()
	if err != nil {
		return nil, err
	}
	defer rt.Close()
	cur, err := uploadOffset(rt, t, u)
	if err != nil {
		s.dropUpload(u.ID)
		return nil, err
	}
	if off != cur {
		return cur, &Error{Status: http.StatusConflict, Msg: "offset mismatch"}
	}
	if cur < u.Size {
		f, err := rt.OpenFile(path.Join(t.rel, u.temp()), os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return nil, err
		}
		// Whatever arrives is kept, even when the connection drops midway.
		r.Body = http.MaxBytesReader(w, r.Body, maxChunk)
		n, cerr := io.Copy(f, io.LimitReader(r.Body, u.Size-cur))
		serr := f.Sync()
		f.Close()
		cur += n
		s.touchUpload(u.ID)
		if cerr != nil {
			var se syscall.Errno
			if errors.As(cerr, &se) && se == syscall.ENOSPC || errors.As(cerr, &se) && se == syscall.EDQUOT {
				return nil, &Error{Status: http.StatusInsufficientStorage, Msg: "no space left (volume or quota)"}
			}
			return nil, fmt.Errorf("upload interrupted at %d: %w", cur, cerr)
		}
		if serr != nil {
			return nil, serr
		}
		// More bytes than announced: refuse rather than truncate silently.
		var one [1]byte
		if k, _ := r.Body.Read(one[:]); k > 0 {
			return nil, badRequest("more data than the announced size")
		}
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(cur, 10))
	if cur < u.Size {
		return s.uploadView(u, cur), nil
	}
	return s.completeUpload(u, t, rt)
}

// completeUpload moves the finished file in place and forgets the upload.
func (s *Server) completeUpload(u *mobileUpload, t target, rt *os.Root) (any, error) {
	if u.Replace {
		return s.syncReplace(u, t, rt)
	}
	var owner func(string)
	if !u.Admin {
		owner = func(rel string) { s.chownTo(u.User, rt, rel) }
	}
	name, dup, err := finishUpload(rt, t.rel, u, owner)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Status == http.StatusUnprocessableEntity {
			s.dropUpload(u.ID)
		}
		return nil, err
	}
	s.dropUpload(u.ID)
	return map[string]any{"id": u.ID, "offset": u.Size, "size": u.Size, "done": true, "duplicate": dup, "name": name, "path": filepath.Join(t.abs, name)}, nil
}

func (s *Server) removeUploadFile(u *mobileUpload) {
	if !strings.HasPrefix(u.Folder, storage.MountRoot+"/") {
		return
	}
	// Only ever our own random temp name inside that folder.
	rt, err := os.OpenRoot(u.Folder)
	if err != nil {
		return
	}
	defer rt.Close()
	_ = rt.Remove(u.temp())
}

// mobileUploadsLoop removes uploads left idle (phone gone, app uninstalled).
func (s *Server) mobileUploadsLoop() {
	for {
		time.Sleep(time.Hour)
		var stale []*mobileUpload
		uploads.mu.Lock()
		s.uploadsLoadLocked()
		for id, u := range uploads.m {
			if time.Since(u.Updated) > uploadIdle {
				stale = append(stale, u)
				delete(uploads.m, id)
				delete(uploads.locks, id)
			}
		}
		if len(stale) > 0 {
			s.uploadsSaveLocked()
		}
		uploads.mu.Unlock()
		for _, u := range stale {
			s.removeUploadFile(u)
		}
	}
}
