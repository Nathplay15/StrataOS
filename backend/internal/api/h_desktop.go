package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// desktopPrefs is the body of GET/PUT /api/me/desktop. In a PUT, absent
// fields keep their saved value.
type desktopPrefs struct {
	Icons *map[string][2]int `json:"icons,omitempty"`
	// Widgets: in a PUT, null goes back to the default widgets.
	Widgets   json.RawMessage `json:"widgets,omitempty"`
	Wallpaper *string         `json:"wallpaper,omitempty"`
	Accent    *string         `json:"accent,omitempty"`
	// Read only: an uploaded wallpaper exists, and its version (for caches).
	CustomWallpaper int64 `json:"customWallpaper,omitempty"`
}

// desktopWidgets are the widgets the desktop mode knows (frontend/src/desktop/widgets.tsx).
var desktopWidgets = map[string]bool{
	"status": true, "resources": true, "storage": true, "apps": true, "network": true, "docker": true,
	"vms": true, "ups": true, "backups": true, "temps": true, "notifications": true,
}

// builtinWallpapers are the wallpapers drawn by the interface (frontend/src/desktop/wallpapers.ts).
var builtinWallpapers = map[string]bool{"aurora": true, "dusk": true, "ocean": true, "forest": true, "graphite": true, "strata": true}

var accentRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// validDesktopWidgets checks a widget list: known ids, each at most once.
func validDesktopWidgets(w []string) error {
	seen := map[string]bool{}
	for _, id := range w {
		if !desktopWidgets[id] {
			return fmt.Errorf("unknown widget %q", id)
		}
		if seen[id] {
			return fmt.Errorf("widget %s listed twice", id)
		}
		seen[id] = true
	}
	return nil
}

// validAppearance checks a wallpaper id and an accent colour ("" = default).
func validAppearance(wallpaper, accent string, hasCustom bool) error {
	if wallpaper != "" && !builtinWallpapers[wallpaper] && (wallpaper != "custom" || !hasCustom) {
		return fmt.Errorf("unknown wallpaper %q", wallpaper)
	}
	if accent != "" && !accentRe.MatchString(accent) {
		return fmt.Errorf("invalid accent colour %q", accent)
	}
	return nil
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
		icons := map[string][2]int{}
		p := desktopPrefs{Icons: &icons}
		wall, accent := "", ""
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				for k, v := range u.Desktop {
					icons[k] = v
				}
				p.Widgets = json.RawMessage("null")
				if u.DesktopWidgets != nil {
					p.Widgets, _ = json.Marshal(append([]string{}, (*u.DesktopWidgets)...))
				}
				wall, accent = u.Wallpaper, u.Accent
			}
		})
		p.Wallpaper, p.Accent = &wall, &accent
		if fi, err := os.Stat(s.wallpaperPath(userOf(r))); err == nil {
			p.CustomWallpaper = fi.ModTime().Unix()
		}
		return p, nil
	})
	s.user("PUT /api/me/desktop", func(r *http.Request) (any, error) {
		r.Body = io.NopCloser(io.LimitReader(r.Body, 64<<10))
		var req desktopPrefs
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Icons != nil {
			if err := validDesktopIcons(*req.Icons); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		var widgets *[]string
		if len(req.Widgets) > 0 {
			if err := json.Unmarshal(req.Widgets, &widgets); err != nil {
				return nil, badRequest("invalid widget list")
			}
			if widgets != nil {
				if err := validDesktopWidgets(*widgets); err != nil {
					return nil, badRequest(err.Error())
				}
			}
		}
		_, err := os.Stat(s.wallpaperPath(userOf(r)))
		if err := validAppearance(deref(req.Wallpaper), deref(req.Accent), err == nil); err != nil {
			return nil, badRequest(err.Error())
		}
		return nil, s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil {
				return notFound("user not found")
			}
			if req.Icons != nil {
				u.Desktop = *req.Icons
				if len(u.Desktop) == 0 {
					u.Desktop = nil
				}
			}
			if len(req.Widgets) > 0 {
				u.DesktopWidgets = widgets
				if widgets != nil && *widgets == nil {
					u.DesktopWidgets = &[]string{}
				}
			}
			if req.Wallpaper != nil {
				u.Wallpaper = *req.Wallpaper
			}
			if req.Accent != nil {
				u.Accent = *req.Accent
			}
			return nil
		})
	})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
