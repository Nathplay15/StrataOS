package api

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strataos/internal/jobs"
)

// fileProps is everything the Files app shows in "Properties".
type fileProps struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Dir        bool      `json:"dir"`
	Link       bool      `json:"link"`
	LinkTarget string    `json:"linkTarget,omitempty"`
	Size       int64     `json:"size"`
	Files      int64     `json:"files,omitempty"`
	Dirs       int64     `json:"dirs,omitempty"`
	TotalSize  int64     `json:"totalSize,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
	Mode       string    `json:"mode"`
	Octal      string    `json:"octal"`
	Owner      string    `json:"owner"`
	Group      string    `json:"group"`
	ModTime    time.Time `json:"modTime"`
	Hash       string    `json:"hash,omitempty"`
	HashError  string    `json:"hashError,omitempty"`
}

// maxPropsVisited caps the recursive walk of a folder's properties.
const maxPropsVisited = 200000

// maxHashSize is the largest file hashed on demand (SHA-256 in Properties).
const maxHashSize = 512 << 20

// walkProps counts what a folder holds (recursively, capped).
func walkProps(rt *os.Root, rel string) (files, dirs, total int64, truncated bool) {
	visited := 0
	_ = fs.WalkDir(rt.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		visited++
		if visited > maxPropsVisited {
			truncated = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if p != rel {
				dirs++
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		files++
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return files, dirs, total, truncated
}

func (s *Server) filePropsRoutes() {
	s.user("GET /api/files/properties", func(r *http.Request) (any, error) {
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil {
			return nil, err
		}
		if !t.acc.read || t.acc.ancestor {
			return nil, errForbidden
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		info, err := rt.Lstat(t.rel)
		if err != nil {
			return nil, notFound("not found")
		}
		p := fileProps{
			Name: path.Base(t.rel), Path: t.abs, Dir: info.IsDir(),
			Mode: info.Mode().String(), Octal: fmt.Sprintf("%04o", info.Mode().Perm()),
			ModTime: info.ModTime(),
		}
		if t.acc.e2e == "" && info.Mode()&fs.ModeSymlink != 0 {
			p.Link = true
			if link, err := os.Readlink(t.abs); err == nil {
				p.LinkTarget = link
			}
			if st, err := rt.Stat(t.rel); err == nil {
				p.Dir = st.IsDir()
				p.Size = st.Size()
			}
		} else {
			p.Size = info.Size()
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			p.Owner, p.Group = ownerName(st.Uid, false), ownerName(st.Gid, true)
		}
		switch {
		case p.Dir:
			p.Files, p.Dirs, p.TotalSize, p.Truncated = walkProps(rt, t.rel)
		case r.URL.Query().Get("hash") == "1" && !p.Link:
			if info.Size() > maxHashSize {
				p.HashError = "file too large"
				break
			}
			f, err := rt.Open(t.rel)
			if err != nil {
				p.HashError = err.Error()
				break
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				p.HashError = err.Error()
				break
			}
			p.Hash = hex.EncodeToString(h.Sum(nil))
		}
		return p, nil
	})

	s.user("POST /api/files/chmod", func(r *http.Request) (any, error) {
		var req struct{ Path, Mode string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		perm, err := strconv.ParseUint(req.Mode, 8, 32)
		if err != nil || perm > 0o777 {
			return nil, badRequest("invalid permissions (3 octal digits)")
		}
		t, err := s.locate(r, req.Path)
		if err != nil {
			return nil, err
		}
		if !t.acc.write || t.acc.ancestor || t.protected() {
			return nil, errForbidden
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		if err := rt.Chmod(t.rel, os.FileMode(perm)); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "files", fmt.Sprintf("Permissions of %s changed to %04o", t.abs, perm))
		return nil, nil
	})

	// Compress items into a ZIP archive created in the same folder. Extraction
	// itself is POST /api/files/archive/extract (h_viewers.go), which handles
	// every format the archive package knows.
	s.user("POST /api/files/compress", func(r *http.Request) (any, error) {
		var req struct {
			Paths []string `json:"paths"`
			Name  string   `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Paths) == 0 || len(req.Paths) > 1000 {
			return nil, badRequest("nothing to compress")
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			req.Name = "Archive.zip"
		}
		if !strings.HasSuffix(strings.ToLower(req.Name), ".zip") {
			req.Name += ".zip"
		}
		if req.Name != filepath.Base(req.Name) || strings.HasPrefix(req.Name, ".") {
			return nil, badRequest("invalid archive name")
		}
		parent, err := s.locate(r, path.Dir(req.Paths[0]))
		if err != nil {
			return nil, err
		}
		if why := s.readOnlyReason(parent); why != "" || !parent.acc.write {
			return nil, &Error{Status: http.StatusForbidden, Msg: firstNonEmpty(why, "read-only access")}
		}
		var srcs []target
		for _, p := range req.Paths {
			t, err := s.locate(r, p)
			if err != nil {
				return nil, err
			}
			if !t.acc.read || t.acc.ancestor || t.root != parent.root {
				return nil, &Error{Status: http.StatusForbidden, Msg: "cannot compress " + p}
			}
			if t.abs == parent.abs || strings.HasPrefix(parent.abs, t.abs+"/") {
				return nil, badRequest("a folder cannot be compressed into itself")
			}
			srcs = append(srcs, t)
		}
		s.audit(r, "files", fmt.Sprintf("Compressing %d item(s) into %s", len(srcs), req.Name))
		return s.jobs.Start("files", "Compressing into "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			rt, err := parent.open()
			if err != nil {
				return err
			}
			defer rt.Close()
			name, err := freeName(rt, parent.rel, req.Name)
			if err != nil {
				return err
			}
			out, err := rt.OpenFile(path.Join(parent.rel, name), os.O_CREATE|os.O_WRONLY, 0o664)
			if err != nil {
				return err
			}
			defer out.Close()
			zw := zip.NewWriter(out)
			for i, t := range srcs {
				if err := zipTree(ctx, rt, t, zw); err != nil {
					zw.Close()
					_ = rt.Remove(path.Join(parent.rel, name))
					return fmt.Errorf("%s: %w", path.Base(t.rel), err)
				}
				j.SetProgress(float64(i+1) / float64(len(srcs)) * 100)
			}
			if err := zw.Close(); err != nil {
				_ = rt.Remove(path.Join(parent.rel, name))
				return err
			}
			out.Close()
			s.chownForUser(r, rt, path.Join(parent.rel, name))
			j.Logf("%s created (%d item(s))", name, len(srcs))
			return nil
		}), nil
	})
}

// zipTree adds a file or folder (walked) to a ZIP writer, keeping relative
// paths under its own name. Symlinks and special files are skipped.
func zipTree(ctx context.Context, rt *os.Root, t target, zw *zip.Writer) error {
	base := path.Base(t.rel)
	return fs.WalkDir(rt.FS(), t.rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(t.rel, filepath.FromSlash(p))
		if err != nil {
			return err
		}
		name := path.Join(base, filepath.ToSlash(rel))
		if d.IsDir() {
			_, err := zw.Create(name + "/")
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = name
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := rt.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}
