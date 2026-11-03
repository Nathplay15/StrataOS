package api

import (
	"fmt"
	"io"
	"net/http"
	"regexp"

	"strataos/internal/store"
)

// Desktop mode preferences of the signed-in user: the positions of the
// desktop icons, kept on the NAS so they follow the user between browsers.

const (
	maxDesktopIcons = 500
	maxDesktopCell  = 200 // columns and rows
)

var desktopIconRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$`)

type desktopPrefs struct {
	Icons map[string][2]int `json:"icons"`
}

// validDesktopIcons checks the icon layout sent by the browser: known shape,
// bounded size, and no two icons in the same cell.
func validDesktopIcons(icons map[string][2]int) error {
	if len(icons) > maxDesktopIcons {
		return fmt.Errorf("too many desktop icons")
	}
	used := map[[2]int]string{}
	for id, c := range icons {
		if !desktopIconRe.MatchString(id) {
			return fmt.Errorf("invalid icon id %q", id)
		}
		if c[0] < 0 || c[1] < 0 || c[0] >= maxDesktopCell || c[1] >= maxDesktopCell {
			return fmt.Errorf("icon %s outside the grid", id)
		}
		if other, ok := used[c]; ok {
			return fmt.Errorf("icons %s and %s share a cell", other, id)
		}
		used[c] = id
	}
	return nil
}

func (s *Server) desktopRoutes() {
	s.user("GET /api/me/desktop", func(r *http.Request) (any, error) {
		p := desktopPrefs{Icons: map[string][2]int{}}
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				for k, v := range u.Desktop {
					p.Icons[k] = v
				}
			}
		})
		return p, nil
	})
	s.user("PUT /api/me/desktop", func(r *http.Request) (any, error) {
		r.Body = io.NopCloser(io.LimitReader(r.Body, 64<<10))
		var req desktopPrefs
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := validDesktopIcons(req.Icons); err != nil {
			return nil, badRequest(err.Error())
		}
		return nil, s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil {
				return notFound("user not found")
			}
			u.Desktop = req.Icons
			if len(u.Desktop) == 0 {
				u.Desktop = nil
			}
			return nil
		})
	})
}
