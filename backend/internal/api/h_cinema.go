package api

// Cinema library: the videos of the shares, matched with TMDB for their
// poster, synopsis, actors and rating (internal/cinema). The library and
// the posters are cached in the data directory; the TMDB key lives in its
// own file and is never returned by the API.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"strataos/internal/cinema"
	"strataos/internal/storage"
	"strataos/internal/store"
)

func (s *Server) cinemaPaths() (state, key, posters string) {
	return filepath.Join(s.dataDir, "cinema.json"), filepath.Join(s.dataDir, "cinema.key"), filepath.Join(s.dataDir, "cinema-posters")
}

// cinemaShares are the mounted shares scanned for videos, as scan roots
// (absolute path on disk + path as the files API spells it).
func (s *Server) cinemaShares() []cinema.Root {
	var out []cinema.Root
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			if sh.Path != "" && storage.IsMountpoint(sh.Path) {
				out = append(out, cinema.Root{Abs: sh.Path, API: sh.Name})
			}
		}
	})
	return out
}

var cinemaScanMu sync.Mutex

// cinemaScanState loads the state, runs f on it and saves it back.
func (s *Server) cinemaScanState(f func(*cinema.State)) {
	statePath, _, _ := s.cinemaPaths()
	st := cinema.LoadState(statePath)
	f(&st)
	_ = cinema.SaveState(statePath, st)
}

// cinemaScan runs in the background: scan the shares, match TMDB, cache the
// posters, save the library.
func (s *Server) cinemaScan() {
	cinemaScanMu.Lock()
	defer cinemaScanMu.Unlock()
	_, keyPath, posterDir := s.cinemaPaths()
	s.cinemaScanState(func(st *cinema.State) { st.Running = true })
	defer s.cinemaScanState(func(st *cinema.State) { st.Running = false; st.Checked = time.Now() })
	cl := &cinema.Client{Key: cinema.LoadKey(keyPath)}
	items := cinema.BuildLibrary(context.Background(), s.cinemaShares(), cl, posterDir, 6, func(f string, a ...any) {
		s.store.Log("info", "cinema", "", fmt.Sprintf(f, a...))
	})
	s.cinemaScanState(func(st *cinema.State) { st.Scanned = len(items); st.Items = items })
}

func (s *Server) cinemaRoutes() {
	statePath, keyPath, posterDir := s.cinemaPaths()

	s.user("GET /api/cinema", func(r *http.Request) (any, error) {
		st := cinema.LoadState(statePath)
		items := st.Items
		if items == nil {
			items = []cinema.Item{}
		}
		return struct {
			KeySet  bool          `json:"keySet"`
			Running bool          `json:"running"`
			Checked time.Time     `json:"checked,omitzero"`
			Scanned int           `json:"scanned"`
			Items   []cinema.Item `json:"items"`
		}{cinema.LoadKey(keyPath) != "", st.Running, st.Checked, st.Scanned, items}, nil
	})

	s.admin("PUT /api/cinema/settings", func(r *http.Request) (any, error) {
		var req struct {
			TMDBKey string `json:"tmdbKey"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		key := req.TMDBKey
		if key == "********" || key == "" {
			key = cinema.LoadKey(keyPath)
		}
		if len(key) > 64 {
			return nil, badRequest("the TMDB key is too long")
		}
		if err := cinema.SaveKey(keyPath, key); err != nil {
			return nil, err
		}
		s.audit(r, "cinema", "TMDB key saved")
		return map[string]any{"keySet": key != ""}, nil
	})

	s.admin("POST /api/cinema/scan", func(r *http.Request) (any, error) {
		st := cinema.LoadState(statePath)
		if st.Running {
			return map[string]any{"started": false, "running": true}, nil
		}
		go s.cinemaScan()
		return map[string]any{"started": true}, nil
	})

	// Poster of a TMDB id, served from the local cache (filled by the scan).
	s.userRaw("GET /api/cinema/poster/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			writeErr(w, badRequest("invalid id"))
			return
		}
		p := filepath.Join(posterDir, cinema.PosterFile(id))
		if _, err := os.Stat(p); err != nil {
			// Not cached yet: fetch it now when the library knows its path.
			st := cinema.LoadState(statePath)
			var poster string
			for _, it := range st.Items {
				if it.TMDB == id {
					poster = it.Poster
					break
				}
			}
			if poster == "" {
				writeErr(w, notFound("no poster"))
				return
			}
			cl := &cinema.Client{Key: cinema.LoadKey(keyPath)}
			b, err := cl.Poster(r.Context(), poster)
			if err != nil {
				writeErr(w, &Error{Status: http.StatusBadGateway, Msg: err.Error()})
				return
			}
			_ = os.MkdirAll(posterDir, 0o755)
			_ = os.WriteFile(p, b, 0o644)
		}
		f, err := os.Open(p)
		if err != nil {
			writeErr(w, notFound("no poster"))
			return
		}
		defer f.Close()
		info, _ := f.Stat()
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
}
