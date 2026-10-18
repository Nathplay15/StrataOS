package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
)

// versionTarget is the file p as kept in a snapshot (read-only, confined).
func (s *Server) versionTarget(r *http.Request, p, snap string) (target, target, error) {
	t, err := s.locate(r, p)
	if err != nil || !t.acc.read || t.acc.ancestor {
		return target{}, target{}, firstErr(err, errForbidden)
	}
	tree, ok := storage.FindSnapshot(t.abs, snap)
	if !ok {
		return target{}, target{}, notFound("version not found")
	}
	rel := tree.RelIn(t.abs)
	if rel == "" {
		return target{}, target{}, notFound("version not found")
	}
	return t, target{root: tree.Root, rel: rel, abs: t.abs, acc: access{read: true}}, nil
}

func (s *Server) versionRoutes() {
	s.user("GET /api/files/versions", func(r *http.Request) (any, error) {
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil || !t.acc.read || t.acc.ancestor {
			return nil, firstErr(err, errForbidden)
		}
		v := storage.Versions(t.abs)
		if v == nil {
			v = []storage.Version{}
		}
		return map[string]any{"versions": v, "writable": t.acc.write}, nil
	})

	s.userRaw("GET /api/files/versions/download", func(w http.ResponseWriter, r *http.Request) {
		_, v, err := s.versionTarget(r, r.URL.Query().Get("path"), r.URL.Query().Get("snapshot"))
		if err != nil {
			writeErr(w, err)
			return
		}
		s.serveTarget(w, r, v, nil)
	})

	// Puts a version back, as a copy next to the file or in its place.
	s.user("POST /api/files/versions/restore", func(r *http.Request) (any, error) {
		var req struct {
			Path     string `json:"path"`
			Snapshot string `json:"snapshot"`
			Replace  bool   `json:"replace"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		t, v, err := s.versionTarget(r, req.Path, req.Snapshot)
		if err != nil {
			return nil, err
		}
		if !t.acc.write {
			return nil, &Error{Status: http.StatusForbidden, Msg: "you cannot write to this folder"}
		}
		src, err := os.OpenRoot(v.root)
		if err != nil {
			return nil, err
		}
		defer src.Close()
		in, err := src.Open(v.rel)
		if err != nil {
			return nil, notFound("version not found")
		}
		defer in.Close()
		if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() {
			return nil, badRequest("only files can be restored")
		}
		dst, err := t.open()
		if err != nil {
			return nil, err
		}
		defer dst.Close()
		dir := path.Dir(t.rel)
		name := path.Base(t.rel)
		ext := filepath.Ext(name)
		tree, _ := storage.FindSnapshot(t.abs, req.Snapshot)
		out := path.Join(dir, fmt.Sprintf("%s (version of %s)%s", strings.TrimSuffix(name, ext), tree.Time.Format("2006-01-02 15h04"), ext))
		if req.Replace {
			out = t.rel
		}
		tmp := path.Join(dir, fmt.Sprintf(".%s.strata-restore-%d", name, time.Now().UnixNano()))
		f, err := dst.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o664)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(f, in)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = dst.Rename(tmp, out)
		}
		if err != nil {
			_ = dst.Remove(tmp)
			return nil, err
		}
		if fi, err := src.Stat(v.rel); err == nil {
			_ = dst.Chtimes(out, fi.ModTime(), fi.ModTime())
		}
		s.chownForUser(r, dst, out)
		s.audit(r, "files", fmt.Sprintf("Restored %s from snapshot %s", t.abs, req.Snapshot))
		return map[string]string{"path": filepath.Join(filepath.Dir(t.abs), path.Base(out))}, nil
	})
}

// versionLinksLoop keeps the Windows "Previous Versions" links in step with
// the snapshots (created and deleted by schedules or by hand).
func (s *Server) versionLinksLoop() {
	for {
		time.Sleep(2 * time.Minute)
		seen := map[string]bool{}
		var paths []string
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				if sh.SMB {
					paths = append(paths, sh.Path)
				}
			}
		})
		for _, p := range paths {
			if mp := storage.ShadowMount(p); mp != "" && !seen[mp] {
				seen[mp] = true
				_ = storage.SyncVersionLinks(mp)
			}
		}
	}
}
