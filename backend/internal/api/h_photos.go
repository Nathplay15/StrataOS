package api

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/jobs"
	"strataos/internal/photos"
	"strataos/internal/storage"
	"strataos/internal/store"
)

// readableRoots returns the folders a user may read (administrators: all).
func (s *Server) readableRoots(r *http.Request) []string {
	if isAdmin(r) {
		return []string{storage.MountRoot}
	}
	me := userOf(r)
	groups := accounts.UserGroups(me)
	var roots []string
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			if p := sharePerm(sh, me, groups); p == "rw" || p == "ro" {
				roots = append(roots, sh.Path)
			}
		}
	})
	return roots
}

func under(roots []string, p string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

func (s *Server) photoFolders() []string {
	var f []string
	s.store.Read(func(st *store.State) { f = append(f, st.Photos.Folders...) })
	return f
}

func (s *Server) runPhotoIndex(user string) (jobs.Snapshot, error) {
	if photos.Default.Running() {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "indexing already running"}
	}
	return s.jobs.Start("photos", "Indexation des photos", user, func(ctx context.Context, j *jobs.Job) error {
		_, err := photos.Default.Scan(ctx, s.photoFolders(), j.Logf)
		return err
	}), nil
}

// photoLoop loads the index and refreshes it every 30 minutes.
func (s *Server) photoLoop() {
	photos.Default.Load()
	time.Sleep(2 * time.Minute)
	for {
		if f := s.photoFolders(); len(f) > 0 && !photos.Default.Running() {
			if _, err := photos.Default.Scan(context.Background(), f, func(string, ...any) {}); err != nil {
				slog.Warn("photo indexing", "err", err)
			}
		}
		time.Sleep(30 * time.Minute)
	}
}

func (s *Server) photoRoutes() {
	s.user("GET /api/photos", func(r *http.Request) (any, error) {
		roots := s.readableRoots(r)
		list := photos.Default.List(func(p string) bool { return under(roots, p) })
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if lim <= 0 || lim > 1000 {
			lim = 300
		}
		total := len(list)
		if off > total {
			off = total
		}
		end := min(off+lim, total)
		out := map[string]any{"installed": photos.Installed(), "total": total, "items": list[off:end], "running": photos.Default.Running(), "updated": photos.Default.Updated}
		if isAdmin(r) {
			f := s.photoFolders()
			if f == nil {
				f = []string{}
			}
			out["folders"] = f
		}
		return out, nil
	})

	s.userRaw("GET /api/photos/thumb", func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Clean(r.URL.Query().Get("path"))
		it, ok := photos.Default.Get(p)
		if !ok || !under(s.readableRoots(r), p) {
			writeErr(w, notFound("photo not found"))
			return
		}
		size := r.URL.Query().Get("size")
		if size == "" {
			size = "s"
		}
		thumb, err := photos.Thumb(r.Context(), it, size)
		if err != nil {
			writeErr(w, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()})
			return
		}
		f, err := os.Open(thumb)
		if err != nil {
			writeErr(w, notFound("thumbnail unavailable"))
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, max-age=604800")
		http.ServeContent(w, r, "thumb.jpg", time.Unix(it.Mtime, 0), f)
	})

	s.admin("PUT /api/photos/settings", func(r *http.Request) (any, error) {
		var req store.Photos
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var clean []string
		for _, f := range req.Folders {
			c := filepath.Clean(f)
			if !strings.HasPrefix(c, storage.MountRoot+"/") {
				return nil, badRequest("photo folders must be inside the NAS storage")
			}
			if fi, err := os.Stat(c); err != nil || !fi.IsDir() {
				return nil, badRequest(c + " does not exist")
			}
			clean = append(clean, c)
		}
		if err := s.store.Update(func(st *store.State) error { st.Photos.Folders = clean; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "photos", "Photo folders: "+strings.Join(clean, ", "))
		return s.runPhotoIndex(userOf(r))
	})

	s.admin("POST /api/photos/reindex", func(r *http.Request) (any, error) { return s.runPhotoIndex(userOf(r)) })
}
