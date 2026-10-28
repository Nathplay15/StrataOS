package api

// File manager additions for the mobile app (and any API client): search,
// copy and move between folders and shares (as a job), and deleting through
// the recycle bin of the share. Every file operation goes through os.Root,
// like the rest of the file manager.

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
	"strings"
	"syscall"

	"strataos/internal/jobs"
	"strataos/internal/storage"
)

const (
	maxSearchResults = 500
	maxSearchVisited = 200000
)

// searchRoots returns where a search from t may look: t itself, or for a
// folder above shares (regular users), the readable shares below it.
func (s *Server) searchRoots(r *http.Request, t target) []target {
	if !t.acc.ancestor {
		return []target{t}
	}
	var out []target
	for _, root := range s.readableRoots(r) {
		if root == t.abs || strings.HasPrefix(root, strings.TrimSuffix(t.abs, "/")+"/") {
			if c, err := s.locate(r, root); err == nil && c.acc.read && !c.acc.ancestor {
				out = append(out, c)
			}
		}
	}
	return out
}

// searchTarget walks t for names containing q (lower case), skipping hidden
// entries and recycle bins. visited counts entries across roots.
func searchTarget(ctx context.Context, t target, q string, out *[]fileEntry, visited *int) error {
	rt, err := t.open()
	if err != nil {
		return err
	}
	defer rt.Close()
	return fs.WalkDir(rt.FS(), t.rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable folder: skip it
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		*visited++
		if *visited > maxSearchVisited || len(*out) >= maxSearchResults {
			return fs.SkipAll
		}
		if p == t.rel {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "#recycle" || name == "@eaDir" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.Contains(strings.ToLower(name), q) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		abs := filepath.Join(t.root, filepath.FromSlash(p))
		*out = append(*out, fileEntry{Name: name, Path: abs, Dir: d.IsDir(), Size: info.Size(), Mode: info.Mode().String(), ModTime: info.ModTime(), Link: info.Mode()&fs.ModeSymlink != 0})
		return nil
	})
}

// copyTree copies rel from src into dstRel of dst (files, folders, no
// symlinks), keeping modification times; owner gives new files to a user.
func copyTree(ctx context.Context, src, dst *os.Root, rel, dstRel string, owner func(rel string)) error {
	return fs.WalkDir(src.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		to := path.Join(dstRel, strings.TrimPrefix(strings.TrimPrefix(p, rel), "/"))
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := dst.Mkdir(to, 0o775); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
		case d.Type().IsRegular():
			in, err := src.Open(p)
			if err != nil {
				return err
			}
			out, err := dst.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o664)
			if err != nil {
				in.Close()
				return err
			}
			_, err = io.Copy(out, ctxReader{ctx, in})
			in.Close()
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				_ = dst.Remove(to)
				return err
			}
		default:
			return nil // symlinks, devices, sockets: not copied
		}
		_ = dst.Chtimes(to, info.ModTime(), info.ModTime())
		owner(to)
		return nil
	})
}

func (s *Server) fileMoreRoutes() {
	s.user("GET /api/files/search", func(r *http.Request) (any, error) {
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		if len(q) < 2 || len(q) > 200 {
			return nil, badRequest("search for at least 2 characters")
		}
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil {
			return nil, err
		}
		out := []fileEntry{}
		visited := 0
		for _, root := range s.searchRoots(r, t) {
			if err := searchTarget(r.Context(), root, q, &out, &visited); err != nil && !errors.Is(err, fs.SkipAll) {
				return nil, err
			}
		}
		return map[string]any{"path": t.abs, "entries": out, "truncated": len(out) >= maxSearchResults || visited > maxSearchVisited}, nil
	})

	// Copy or move items into a folder, possibly on another share or volume.
	// Names already taken get a " (2)" suffix: nothing is ever overwritten.
	s.user("POST /api/files/copy", func(r *http.Request) (any, error) {
		var req struct {
			Paths []string `json:"paths"`
			To    string   `json:"to"`
			Move  bool     `json:"move"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Paths) == 0 || len(req.Paths) > 1000 {
			return nil, badRequest("nothing to copy")
		}
		dst, err := s.locate(r, req.To)
		if err != nil {
			return nil, err
		}
		if why := s.readOnlyReason(dst); why != "" || dst.abs == storage.MountRoot {
			return nil, &Error{Status: http.StatusForbidden, Msg: firstNonEmpty(why, "access denied")}
		}
		var srcs []target
		for _, p := range req.Paths {
			t, err := s.locate(r, p)
			if err != nil {
				return nil, err
			}
			if !t.acc.read || t.acc.ancestor || (req.Move && (!t.acc.write || t.protected())) {
				return nil, &Error{Status: http.StatusForbidden, Msg: "cannot move " + p}
			}
			if dst.abs == t.abs || strings.HasPrefix(dst.abs, t.abs+"/") {
				return nil, badRequest("a folder cannot be copied into itself")
			}
			srcs = append(srcs, t)
		}
		user, admin := userOf(r), isAdmin(r)
		title := fmt.Sprintf("Copy %d item(s)", len(srcs))
		if req.Move {
			title = fmt.Sprintf("Move %d item(s)", len(srcs))
		}
		s.audit(r, "files", fmt.Sprintf("%s to %s", title, dst.abs))
		return s.jobs.Start("files", title, user, func(ctx context.Context, j *jobs.Job) error {
			drt, err := dst.open()
			if err != nil {
				return err
			}
			defer drt.Close()
			owner := func(rel string) {
				if !admin {
					s.chownTo(user, drt, rel)
				}
			}
			for i, t := range srcs {
				srt, err := t.open()
				if err != nil {
					return err
				}
				name, err := freeName(drt, dst.rel, path.Base(t.rel))
				if err != nil {
					srt.Close()
					return err
				}
				to := path.Join(dst.rel, name)
				moved := false
				if req.Move && t.root == dst.root {
					// Same root: a rename, unless it crosses file systems.
					err = srt.Rename(t.rel, to)
					if err == nil {
						moved = true
					} else if !errors.Is(err, syscall.EXDEV) {
						srt.Close()
						return fmt.Errorf("%s: %w", path.Base(t.rel), err)
					}
				}
				if !moved {
					j.Logf("%s → %s", t.abs, filepath.Join(dst.abs, name))
					if err := copyTree(ctx, srt, drt, t.rel, to, owner); err != nil {
						srt.Close()
						_ = drt.RemoveAll(to)
						return fmt.Errorf("%s: %w", path.Base(t.rel), err)
					}
					if req.Move {
						if err := srt.RemoveAll(t.rel); err != nil {
							srt.Close()
							return err
						}
					}
				}
				srt.Close()
				j.SetProgress(float64(i+1) / float64(len(srcs)) * 100)
			}
			return nil
		}), nil
	})

	// Delete through the recycle bin of the share when it has one (where SMB
	// and WebDAV put deleted files); items already in a bin are deleted.
	s.user("POST /api/files/trash", func(r *http.Request) (any, error) {
		var req struct {
			Paths []string `json:"paths"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var targets []target
		for _, p := range req.Paths {
			t, err := s.locate(r, p)
			if err != nil {
				return nil, err
			}
			if !t.acc.write || t.protected() {
				return nil, &Error{Status: http.StatusForbidden, Msg: "cannot delete " + p}
			}
			targets = append(targets, t)
		}
		recycled, deleted := 0, 0
		for _, t := range targets {
			if sp := s.recycleShare(t.abs); sp != "" && !inRecycleBin(sp, t.abs) {
				if _, err := s.trashFile(userOf(r), t.abs); err != nil {
					return nil, err
				}
				recycled++
				continue
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
			deleted++
		}
		s.audit(r, "files", fmt.Sprintf("Deleted %d item(s), %d to the recycle bin", deleted+recycled, recycled))
		return map[string]int{"recycled": recycled, "deleted": deleted}, nil
	})
}

// inRecycleBin reports whether p lies in the recycle bin of share sp.
func inRecycleBin(sp, p string) bool {
	return p == sp+"/.recycle" || strings.HasPrefix(p, sp+"/.recycle/")
}
