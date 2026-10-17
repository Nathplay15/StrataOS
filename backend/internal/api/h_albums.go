package api

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"strataos/internal/photos"
	"strataos/internal/store"
)

// albumView is an album as listed in Photos.
type albumView struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Count   int       `json:"count"`
	Cover   string    `json:"cover,omitempty"`
	Created time.Time `json:"created"`
	Shared  bool      `json:"shared"`
}

func validAlbumName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || utf8.RuneCountInString(n) > 80 || strings.ContainsAny(n, "\n\r") {
		return "", badRequest("invalid album name")
	}
	return n, nil
}

// album returns a copy of an album of the current user.
func (s *Server) album(r *http.Request, id string) (store.Album, error) {
	var a store.Album
	found := false
	me := userOf(r)
	s.store.Read(func(st *store.State) {
		for _, x := range st.Photos.Albums {
			if x.ID == id && x.Owner == me {
				a, found = *x, true
				a.Items = slices.Clone(x.Items)
			}
		}
	})
	if !found {
		return a, notFound("album not found")
	}
	return a, nil
}

func (s *Server) albumRoutes() {
	s.user("GET /api/photos/albums", func(r *http.Request) (any, error) {
		me, roots := userOf(r), s.readableRoots(r)
		out := []albumView{}
		s.store.Read(func(st *store.State) {
			shared := map[string]bool{}
			for _, l := range st.Links {
				if l.Album != "" && (l.Expires.IsZero() || time.Now().Before(l.Expires)) {
					shared[l.Album] = true
				}
			}
			for _, a := range st.Photos.Albums {
				if a.Owner != me {
					continue
				}
				v := albumView{ID: a.ID, Name: a.Name, Created: a.Created, Shared: shared[a.ID]}
				var newest int64
				for _, p := range a.Items {
					it, ok := photos.Default.Get(p)
					if !ok || !under(roots, p) {
						continue
					}
					v.Count++
					if it.Date > newest {
						newest, v.Cover = it.Date, p
					}
				}
				out = append(out, v)
			}
		})
		slices.SortFunc(out, func(a, b albumView) int { return b.Created.Compare(a.Created) })
		return out, nil
	})

	s.user("POST /api/photos/albums", func(r *http.Request) (any, error) {
		var req struct {
			Name  string   `json:"name"`
			Items []string `json:"items"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name, err := validAlbumName(req.Name)
		if err != nil {
			return nil, err
		}
		items, err := s.checkAlbumItems(r, req.Items)
		if err != nil {
			return nil, err
		}
		a := &store.Album{ID: newLinkID()[:12], Name: name, Owner: userOf(r), Items: items, Created: time.Now()}
		err = s.store.Update(func(st *store.State) error {
			if len(st.Photos.Albums) >= 10000 {
				return badRequest("too many albums")
			}
			st.Photos.Albums = append(st.Photos.Albums, a)
			return nil
		})
		return map[string]string{"id": a.ID}, err
	})

	// Rename and/or add or remove photos.
	s.user("PUT /api/photos/albums/{id}", func(r *http.Request) (any, error) {
		var req struct {
			Name   string   `json:"name"`
			Add    []string `json:"add"`
			Remove []string `json:"remove"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if _, err := s.album(r, r.PathValue("id")); err != nil {
			return nil, err
		}
		var name string
		if req.Name != "" {
			n, err := validAlbumName(req.Name)
			if err != nil {
				return nil, err
			}
			name = n
		}
		add, err := s.checkAlbumItems(r, req.Add)
		if err != nil {
			return nil, err
		}
		me := userOf(r)
		return nil, s.store.Update(func(st *store.State) error {
			for _, a := range st.Photos.Albums {
				if a.ID != r.PathValue("id") || a.Owner != me {
					continue
				}
				if name != "" {
					a.Name = name
				}
				for _, p := range add {
					if !slices.Contains(a.Items, p) {
						a.Items = append(a.Items, p)
					}
				}
				a.Items = slices.DeleteFunc(a.Items, func(p string) bool { return slices.Contains(req.Remove, p) })
				if len(a.Items) > 50000 {
					return badRequest("too many photos in the album")
				}
			}
			return nil
		})
	})

	s.user("DELETE /api/photos/albums/{id}", func(r *http.Request) (any, error) {
		id, me := r.PathValue("id"), userOf(r)
		if _, err := s.album(r, id); err != nil {
			return nil, err
		}
		return nil, s.store.Update(func(st *store.State) error {
			st.Photos.Albums = slices.DeleteFunc(st.Photos.Albums, func(a *store.Album) bool { return a.ID == id && a.Owner == me })
			// Its share links go with it.
			st.Links = slices.DeleteFunc(st.Links, func(l *store.ShareLink) bool { return l.Album == id && l.Owner == me })
			return nil
		})
	})
}

// checkAlbumItems keeps indexed photos the user can read.
func (s *Server) checkAlbumItems(r *http.Request, items []string) ([]string, error) {
	if len(items) > 50000 {
		return nil, badRequest("too many photos")
	}
	roots := s.readableRoots(r)
	out := []string{}
	for _, p := range items {
		p = filepath.Clean(p)
		if _, ok := photos.Default.Get(p); !ok || !under(roots, p) {
			return nil, notFound("photo not found: " + filepath.Base(p))
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, nil
}
