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
	// From is the owner, when the album was received (shared with me).
	From string `json:"from,omitempty"`
	// With are the accounts the album is shared with (the owner only).
	With []string `json:"with,omitempty"`
}

func validAlbumName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || utf8.RuneCountInString(n) > 80 || strings.ContainsAny(n, "\n\r") {
		return "", badRequest("invalid album name")
	}
	return n, nil
}

// album returns a copy of an album of the current user, or of one that this
// user received (shared by its owner with other accounts).
func (s *Server) album(r *http.Request, id string) (store.Album, error) {
	var a store.Album
	found := false
	me := userOf(r)
	s.store.Read(func(st *store.State) {
		for _, x := range st.Photos.Albums {
			if x.ID == id && (x.Owner == me || slices.Contains(x.With, me)) {
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
				received := a.Owner != me && slices.Contains(a.With, me)
				if a.Owner != me && !received {
					continue
				}
				v := albumView{ID: a.ID, Name: a.Name, Created: a.Created}
				var newest int64
				if received {
					// Received: it says who shared it. The owner chose who
					// sees it; like a share by link, its photos are shown
					// even when this account does not have the folders
					// they live in.
					v.From = a.Owner
					for _, p := range a.Items {
						if it, ok := photos.Default.Get(p); ok {
							v.Count++
							if it.Date > newest {
								newest, v.Cover = it.Date, p
							}
						}
					}
				} else {
					v.Shared = shared[a.ID] || len(a.With) > 0
					v.With = slices.Clone(a.With)
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

	// Share with other accounts: they see the album, nobody else can change
	// it. Replaces the list; an empty one takes the sharing off.
	s.user("PUT /api/photos/albums/{id}/share", func(r *http.Request) (any, error) {
		var req struct {
			With []string `json:"with"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		id, me := r.PathValue("id"), userOf(r)
		if _, err := s.album(r, id); err != nil {
			return nil, err
		}
		with, err := s.usersExist(req.With)
		if err != nil {
			return nil, err
		}
		// The owner never shares with herself; sharing with nobody takes
		// the sharing off.
		with = slices.DeleteFunc(with, func(u string) bool { return u == me })
		err = s.store.Update(func(st *store.State) error {
			for _, a := range st.Photos.Albums {
				if a.ID != id || a.Owner != me {
					continue
				}
				a.With = slices.Clone(with)
			}
			return nil
		})
		if err == nil {
			s.audit(r, "album share", id+": "+strings.Join(with, ", "))
		}
		return map[string]any{"with": with}, err
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

// usersExist keeps the names that really are accounts, without duplicates.
// A name that does not exist refuses the whole share: a typo must not share
// with nobody, and it must not share with a stranger either.
func (s *Server) usersExist(names []string) ([]string, error) {
	if len(names) > 100 {
		return nil, badRequest("too many users")
	}
	var out []string
	var err error
	s.store.Read(func(st *store.State) {
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" || slices.Contains(out, n) {
				continue
			}
			if st.User(n) == nil {
				err = badRequest("unknown account: " + n)
				return
			}
			out = append(out, n)
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sharedWithMe reports whether path is a photo of an album shared with the
// caller whose owner can still read it now (see sharedTarget).
func (s *Server) sharedWithMe(r *http.Request, path string) bool {
	_, ok := s.sharedTarget(r, path)
	return ok
}

// sharedTarget returns, read-only, the photo path of an album shared with
// the caller, as its owner reaches it: the owner's current access is checked
// on every request (a share closed to the owner closes the album photo too).
func (s *Server) sharedTarget(r *http.Request, path string) (target, bool) {
	me := userOf(r)
	type owner struct {
		name  string
		admin bool
	}
	var owners []owner
	s.store.Read(func(st *store.State) {
		for _, a := range st.Photos.Albums {
			if a.Owner != me && slices.Contains(a.With, me) && slices.Contains(a.Items, path) {
				u := st.User(a.Owner)
				if u != nil {
					owners = append(owners, owner{a.Owner, u.Role == "admin"})
				}
			}
		}
	})
	for _, o := range owners {
		t, err := s.locateFor(o.name, o.admin, path)
		if err != nil || !t.acc.read || t.acc.ancestor || t.rel == "." {
			continue
		}
		t.acc.write, t.acc.e2eWrite = false, false
		return t, true
	}
	return target{}, false
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
