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
	"slices"
	"strconv"
	"strings"
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
				// The files API spells paths as absolute under the mount root.
				out = append(out, cinema.Root{Abs: sh.Path, API: sh.Path})
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
		pos := map[string]store.PlayPos{}
		s.store.Read(func(st *store.State) {
			for p, v := range st.Media.Positions[userOf(r)] {
				pos[p] = v
			}
		})
		return struct {
			KeySet    bool                     `json:"keySet"`
			Running   bool                     `json:"running"`
			Checked   time.Time                `json:"checked,omitzero"`
			Scanned   int                      `json:"scanned"`
			Items     []cinema.Item            `json:"items"`
			Positions map[string]store.PlayPos `json:"positions"`
		}{cinema.LoadKey(keyPath) != "", st.Running, st.Checked, st.Scanned, items, pos}, nil
	})

	// PUT /api/cinema/position records where the signed-in account stopped
	// a video; a video seen past 95% counts as finished and loses its entry.
	s.user("PUT /api/cinema/position", func(r *http.Request) (any, error) {
		var req struct {
			Path     string  `json:"path"`
			At       float64 `json:"at"`
			Duration float64 `json:"duration"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Path == "" || len(req.Path) > 4096 || req.Duration <= 0 || req.At < 0 || req.At > req.Duration*1.5 {
			return nil, badRequest("invalid position")
		}
		u := userOf(r)
		return nil, s.store.Update(func(st *store.State) error {
			if st.Media.Positions == nil {
				st.Media.Positions = map[string]map[string]store.PlayPos{}
			}
			m := st.Media.Positions[u]
			if m == nil {
				m = map[string]store.PlayPos{}
				st.Media.Positions[u] = m
			}
			if req.At >= req.Duration*0.95 {
				delete(m, req.Path) // finished
			} else {
				m[req.Path] = store.PlayPos{At: req.At, Duration: req.Duration, Updated: time.Now()}
			}
			if len(m) == 0 {
				delete(st.Media.Positions, u)
			}
			return nil
		})
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

	// ---- Family profiles (kid-only shelves) ----
	s.user("GET /api/cinema/profiles", func(r *http.Request) (any, error) {
		var out []store.MediaProfile
		s.store.Read(func(st *store.State) { out = slices.Clone(st.Media.Profiles) })
		if out == nil {
			out = []store.MediaProfile{}
		}
		return out, nil
	})
	profileReq := func(r *http.Request) (store.MediaProfile, error) {
		var req struct {
			Name      string   `json:"name"`
			MaxRating *float64 `json:"maxRating"`
		}
		if err := decode(r, &req); err != nil {
			return store.MediaProfile{}, err
		}
		name := strings.TrimSpace(req.Name)
		if name == "" || len(name) > 64 {
			return store.MediaProfile{}, badRequest("the profile name must have 1 to 64 characters")
		}
		rating := 0.0
		if req.MaxRating != nil {
			rating = *req.MaxRating
		}
		if rating < 0 || rating > 10 {
			return store.MediaProfile{}, badRequest("the maximum rating must be between 0 and 10")
		}
		return store.MediaProfile{Name: name, MaxRating: rating}, nil
	}
	s.admin("POST /api/cinema/profiles", func(r *http.Request) (any, error) {
		p, err := profileReq(r)
		if err != nil {
			return nil, err
		}
		p.ID = newLinkID()[:12]
		return nil, s.store.Update(func(st *store.State) error {
			if len(st.Media.Profiles) >= 20 {
				return badRequest("too many profiles")
			}
			st.Media.Profiles = append(st.Media.Profiles, p)
			return nil
		})
	})
	s.admin("DELETE /api/cinema/profiles/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		return nil, s.store.Update(func(st *store.State) error {
			st.Media.Profiles = slices.DeleteFunc(st.Media.Profiles, func(p store.MediaProfile) bool { return p.ID == id })
			return nil
		})
	})

	// ---- Watch-together lists (shared between accounts) ----
	type listItemView struct {
		Path string `json:"path"`
		cinema.Item
	}
	type listView struct {
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		Owner   string         `json:"owner"`
		Mine    bool           `json:"mine"`
		With    []string       `json:"with,omitempty"`
		Created time.Time      `json:"created"`
		Items   []listItemView `json:"items"`
	}
	// listsOf returns the lists the account may see, with their items
	// resolved against the library.
	listsOf := func(u string) []listView {
		st := cinema.LoadState(statePath)
		byPath := map[string]cinema.Item{}
		for _, it := range st.Items {
			byPath[it.Path] = it
		}
		out := []listView{}
		s.store.Read(func(st *store.State) {
			for _, l := range st.Media.WatchLists {
				if l.Owner != u && !slices.Contains(l.With, u) {
					continue
				}
				v := listView{ID: l.ID, Name: l.Name, Owner: l.Owner, Mine: l.Owner == u, With: l.With, Created: l.Created, Items: []listItemView{}}
				for _, p := range l.Items {
					if it, ok := byPath[p]; ok {
						v.Items = append(v.Items, listItemView{Path: p, Item: it})
					}
				}
				out = append(out, v)
			}
		})
		return out
	}
	// knownVideo checks a path against the library (anti-garbage entries).
	knownVideo := func(p string) bool {
		st := cinema.LoadState(statePath)
		for _, it := range st.Items {
			if it.Path == p {
				return true
			}
		}
		return false
	}

	s.user("GET /api/cinema/lists", func(r *http.Request) (any, error) {
		return listsOf(userOf(r)), nil
	})
	s.user("POST /api/cinema/lists", func(r *http.Request) (any, error) {
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := strings.TrimSpace(req.Name)
		if name == "" || len(name) > 64 {
			return nil, badRequest("the list name must have 1 to 64 characters")
		}
		u := userOf(r)
		l := store.WatchList{ID: newLinkID()[:12], Name: name, Owner: u, Created: time.Now()}
		return nil, s.store.Update(func(st *store.State) error {
			if len(st.Media.WatchLists) >= 100 {
				return badRequest("too many lists")
			}
			st.Media.WatchLists = append(st.Media.WatchLists, l)
			return nil
		})
	})
	s.user("PUT /api/cinema/lists/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var req struct {
			Name  *string  `json:"name"`
			Items []string `json:"items"`
			With  []string `json:"with"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		u := userOf(r)
		with, err := s.usersExist(req.With)
		if err != nil {
			return nil, err
		}
		return nil, s.store.Update(func(st *store.State) error {
			for i := range st.Media.WatchLists {
				l := &st.Media.WatchLists[i]
				if l.ID != id {
					continue
				}
				owner := l.Owner == u
				member := slices.Contains(l.With, u)
				if !owner && !member {
					return forbidden("not in this list")
				}
				if req.Name != nil {
					if !owner {
						return forbidden("only the owner renames the list")
					}
					name := strings.TrimSpace(*req.Name)
					if name == "" || len(name) > 64 {
						return badRequest("the list name must have 1 to 64 characters")
					}
					l.Name = name
				}
				if req.Items != nil {
					if len(req.Items) > 500 {
						return badRequest("at most 500 videos per list")
					}
					seen := map[string]bool{}
					items := make([]string, 0, len(req.Items))
					for _, p := range req.Items {
						if p == "" || seen[p] || !knownVideo(p) {
							continue
						}
						seen[p] = true
						items = append(items, p)
					}
					l.Items = items
				}
				if req.With != nil {
					if !owner {
						return forbidden("only the owner shares the list")
					}
					l.With = with
				}
				return nil
			}
			return notFound("list not found")
		})
	})
	s.user("DELETE /api/cinema/lists/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		u := userOf(r)
		return nil, s.store.Update(func(st *store.State) error {
			for i := range st.Media.WatchLists {
				if st.Media.WatchLists[i].ID == id {
					if st.Media.WatchLists[i].Owner != u {
						return forbidden("only the owner deletes the list")
					}
					st.Media.WatchLists = slices.Delete(st.Media.WatchLists, i, i+1)
					return nil
				}
			}
			return notFound("list not found")
		})
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
