package api

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/storage"
	"strataos/internal/store"
)

type fileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
	Link    bool      `json:"link"`
}

// access describes what the current user may do with a path.
type access struct{ read, write, ancestor bool }

// sharePerm returns "rw", "ro", "none" or "" for a user on a share.
func sharePerm(sh *store.Share, me string, groups []string) string {
	if p := sh.Permissions[me]; p != "" {
		return p
	}
	perm := ""
	for _, g := range groups {
		switch sh.Permissions["@"+g] {
		case "rw":
			perm = "rw"
		case "ro":
			if perm == "" {
				perm = "ro"
			}
		}
	}
	if perm == "" && sh.Guest {
		perm = "rw"
	}
	if perm == "rw" && sh.ReadOnly() {
		perm = "ro" // over its quota: read-only until space is freed
	}
	return perm
}

// target is a file-manager path confined to a root directory. Every operation goes
// through os.Root on that directory, so ".." or symlinks (which users can create
// over SMB/NFS/SSH) can never make the root daemon read or write outside it.
type target struct {
	root string // share path for regular users, MountRoot for administrators
	rel  string // slash path relative to root ("." for the root itself)
	abs  string // clean absolute path, for display
	acc  access
}

func relTo(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "."
	}
	return filepath.ToSlash(rel)
}

// locate validates p and returns where and how the caller may operate on it.
func (s *Server) locate(r *http.Request, p string) (target, error) {
	return s.locateFor(userOf(r), isAdmin(r), p)
}

// locateFor is locate for a given account (share links act as their owner).
func (s *Server) locateFor(me string, admin bool, p string) (target, error) {
	if p == "" {
		p = storage.MountRoot
	}
	clean := filepath.Clean("/" + p)
	if clean != storage.MountRoot && !strings.HasPrefix(clean, storage.MountRoot+"/") {
		return target{}, &Error{Status: http.StatusForbidden, Msg: "outside of the NAS storage"}
	}
	if admin {
		return target{root: storage.MountRoot, rel: relTo(storage.MountRoot, clean), abs: clean, acc: access{read: true, write: true}}, nil
	}
	groups := accounts.UserGroups(me)
	var best *store.Share
	var bestPerm string
	ancestor := false
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			perm := sharePerm(sh, me, groups)
			if perm != "rw" && perm != "ro" {
				continue
			}
			if strings.HasPrefix(sh.Path, clean+"/") {
				ancestor = true
				continue
			}
			if clean != sh.Path && !strings.HasPrefix(clean, sh.Path+"/") {
				continue
			}
			// The most specific share wins; on a tie, write access wins.
			if best == nil || len(sh.Path) > len(best.Path) || (len(sh.Path) == len(best.Path) && perm == "rw") {
				c := *sh
				best, bestPerm = &c, perm
			}
		}
	})
	if best != nil {
		// A share root replaced by a symlink would let os.OpenRoot follow it: refuse.
		if real, err := filepath.EvalSymlinks(best.Path); err != nil || real != best.Path {
			return target{}, &Error{Status: http.StatusForbidden, Msg: "share folder is unavailable"}
		}
		return target{root: best.Path, rel: relTo(best.Path, clean), abs: clean, acc: access{read: true, write: bestPerm == "rw"}}, nil
	}
	if clean == storage.MountRoot || ancestor {
		// Folders above a share: listing only, filtered to the way down to shares.
		return target{root: storage.MountRoot, rel: relTo(storage.MountRoot, clean), abs: clean, acc: access{read: true, ancestor: true}}, nil
	}
	return target{}, &Error{Status: http.StatusForbidden, Msg: "access denied"}
}

func (t target) open() (*os.Root, error) {
	rt, err := os.OpenRoot(t.root)
	if err != nil {
		return nil, notFound("storage unavailable")
	}
	return rt, nil
}

// child returns the target of a direct child name (no separators allowed).
func (t target) child(name string) target {
	c := t
	c.rel = path.Join(t.rel, name)
	c.abs = filepath.Join(t.abs, name)
	return c
}

// protected reports whether t is a share root, the storage root or a volume mount point.
func (t target) protected() bool {
	return t.rel == "." || filepath.Dir(t.abs) == storage.MountRoot || storage.IsMountpoint(t.abs)
}

func (s *Server) chownForUser(r *http.Request, rt *os.Root, rel string) {
	if isAdmin(r) {
		return
	}
	s.chownTo(userOf(r), rt, rel)
}

// chownTo gives a new file to an account (group of the parent folder).
func (s *Server) chownTo(user string, rt *os.Root, rel string) {
	uid, _, err := accounts.IDs(user)
	if err != nil {
		return
	}
	gid := -1
	if st, err := rt.Stat(path.Dir(rel)); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			gid = int(sys.Gid)
		}
	}
	_ = rt.Lchown(rel, uid, gid)
}

// inlineTypes are the only file types served inline (previews); anything else,
// notably HTML/SVG/XML that could run script on the UI origin, is an attachment.
var inlineTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp", ".avif": "image/avif", ".ico": "image/x-icon",
	// SVG only ever as a picture: <img> runs no script, and the sandbox CSP below
	// blocks it when the URL is opened directly.
	".svg": "image/svg+xml",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".ogv": "video/ogg",
	".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav", ".m4a": "audio/mp4", ".flac": "audio/flac", ".aac": "audio/aac",
	".pdf": "application/pdf",
	".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8", ".log": "text/plain; charset=utf-8",
	".json": "text/plain; charset=utf-8", ".yml": "text/plain; charset=utf-8", ".yaml": "text/plain; charset=utf-8",
	".csv": "text/plain; charset=utf-8", ".conf": "text/plain; charset=utf-8", ".ini": "text/plain; charset=utf-8",
	".sh": "text/plain; charset=utf-8", ".xml": "text/plain; charset=utf-8",
}

func (s *Server) fileRoutes() {
	s.user("GET /api/files", func(r *http.Request) (any, error) {
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil {
			return nil, err
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		dir, err := rt.Open(t.rel)
		if err != nil {
			return nil, notFound("folder not found")
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			return nil, notFound("not a folder")
		}
		out := []fileEntry{}
		showHidden := r.URL.Query().Get("hidden") == "1"
		for _, e := range entries {
			if !showHidden && strings.HasPrefix(e.Name(), ".") {
				continue
			}
			c := t.child(e.Name())
			if t.acc.ancestor {
				if _, err := s.locate(r, c.abs); err != nil {
					continue
				}
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			fe := fileEntry{Name: e.Name(), Path: c.abs, Dir: e.IsDir(), Size: info.Size(), Mode: info.Mode().String(), ModTime: info.ModTime(), Link: info.Mode()&fs.ModeSymlink != 0}
			if fe.Link {
				// Resolved inside the root only: links pointing outside show as plain entries.
				if st, err := rt.Stat(c.rel); err == nil {
					fe.Dir = st.IsDir()
				}
			}
			out = append(out, fe)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Dir != out[j].Dir {
				return out[i].Dir
			}
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		})
		return map[string]any{"path": t.abs, "entries": out, "writable": t.acc.write}, nil
	})

	s.user("POST /api/files/mkdir", func(r *http.Request) (any, error) {
		var req struct{ Path string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		t, err := s.locate(r, req.Path)
		if err != nil {
			return nil, err
		}
		if !t.acc.write || t.rel == "." {
			return nil, errForbidden
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		if err := rt.Mkdir(t.rel, 0o775); err != nil {
			return nil, badRequest(err.Error())
		}
		s.chownForUser(r, rt, t.rel)
		return nil, nil
	})

	s.user("POST /api/files/rename", func(r *http.Request) (any, error) {
		var req struct{ From, To string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		from, err := s.locate(r, req.From)
		if err != nil {
			return nil, err
		}
		to, err := s.locate(r, req.To)
		if err != nil {
			return nil, err
		}
		if !from.acc.write || !to.acc.write || from.protected() || to.rel == "." {
			return nil, errForbidden
		}
		if from.root != to.root {
			return nil, badRequest("moving files between shares is not supported")
		}
		rt, err := from.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		if _, err := rt.Lstat(to.rel); err == nil {
			return nil, badRequest("destination already exists")
		}
		if err := rt.Rename(from.rel, to.rel); err != nil {
			return nil, badRequest(err.Error())
		}
		return nil, nil
	})

	s.user("POST /api/files/delete", func(r *http.Request) (any, error) {
		var req struct{ Paths []string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		for _, raw := range req.Paths {
			t, err := s.locate(r, raw)
			if err != nil {
				return nil, err
			}
			if !t.acc.write || t.protected() {
				return nil, &Error{Status: http.StatusForbidden, Msg: "cannot delete " + raw}
			}
			rt, err := t.open()
			if err != nil {
				return nil, err
			}
			err = rt.RemoveAll(t.rel)
			rt.Close()
			if err != nil {
				return nil, err
			}
		}
		s.audit(r, "files", fmt.Sprintf("Deleted %d item(s)", len(req.Paths)))
		return nil, nil
	})

	s.userRaw("POST /api/files/upload", func(w http.ResponseWriter, r *http.Request) {
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil {
			writeErr(w, err)
			return
		}
		if !t.acc.write {
			writeErr(w, errForbidden)
			return
		}
		rt, err := t.open()
		if err != nil {
			writeErr(w, err)
			return
		}
		defer rt.Close()
		mr, err := r.MultipartReader()
		if err != nil {
			writeErr(w, badRequest("multipart body expected"))
			return
		}
		n := 0
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				writeErr(w, badRequest(err.Error()))
				return
			}
			// part.FileName() drops directories; folder uploads need the relative path,
			// so read the raw parameter (cleaned below, and confined by os.Root anyway).
			_, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			name := params["filename"]
			if name == "" {
				continue
			}
			// Keep relative folders from directory uploads; os.Root keeps them inside.
			rel := path.Join(t.rel, path.Clean("/" + filepath.ToSlash(name))[1:])
			if rel == t.rel {
				continue
			}
			if err := rt.MkdirAll(path.Dir(rel), 0o775); err != nil {
				writeErr(w, badRequest(err.Error()))
				return
			}
			f, err := rt.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o664)
			if err != nil {
				writeErr(w, badRequest(err.Error()))
				return
			}
			_, err = io.Copy(f, part)
			f.Close()
			if err != nil {
				_ = rt.Remove(rel)
				writeErr(w, err)
				return
			}
			s.chownForUser(r, rt, rel)
			n++
		}
		writeJSON(w, http.StatusOK, map[string]int{"uploaded": n})
	})

	s.userRaw("GET /api/files/download", func(w http.ResponseWriter, r *http.Request) {
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil || !t.acc.read || t.acc.ancestor {
			writeErr(w, firstErr(err, errForbidden))
			return
		}
		s.serveTarget(w, r, t, nil)
	})
}

// serveTarget sends a file (inline previews for safe types when inline=1) or
// a folder as a ZIP. onFile, when set, is called before a file is sent.
func (s *Server) serveTarget(w http.ResponseWriter, r *http.Request, t target, onFile func()) {
	rt, err := t.open()
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rt.Close()
	f, err := rt.Open(t.rel)
	if err != nil {
		writeErr(w, notFound("file not found"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, notFound("file not found"))
		return
	}
	name := filepath.Base(t.abs)
	// User content must never run script on the UI origin.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !st.IsDir() {
		ext := strings.ToLower(filepath.Ext(name))
		ctype, inline := inlineTypes[ext]
		disp := "attachment"
		if inline && r.URL.Query().Get("inline") == "1" {
			disp = "inline"
			w.Header().Set("Content-Type", ctype)
			if ext != ".pdf" { // the browser PDF viewer does not work in a sandbox
				w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'")
			}
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
		if onFile != nil {
			onFile()
		}
		http.ServeContent(w, r, name, st.ModTime(), f)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
	if onFile != nil {
		onFile()
	}
	zw := zip.NewWriter(w)
	defer zw.Close()
	rfs := rt.FS()
	_ = fs.WalkDir(rfs, t.rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil // symlinks and special files are skipped
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr, _ := zip.FileInfoHeader(info)
		hdr.Name = path.Join(name, relTo(t.rel, p))
		hdr.Method = zip.Deflate
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		src, err := rfs.Open(p)
		if err != nil {
			return nil
		}
		_, err = io.Copy(fw, src)
		src.Close()
		return err
	})
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
