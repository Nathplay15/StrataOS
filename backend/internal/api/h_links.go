package api

// Share links: a file, a folder or a photo album reachable without an account
// at /s/<id>, optionally protected by a password, with an expiry date and a
// download limit. Folders can also receive files: a file request when the
// content stays hidden (upload only), with an optional name and email of the
// sender, size limits and a notification to the owner. A link acts with the
// rights of its owner, checked again on every request: it stops working when
// the owner loses access or is disabled. Visitors are rate limited per
// address (linkReads, linkUploads); views, downloads and uploads are counted.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"strataos/internal/photos"
	"strataos/internal/store"
)

var errLinkGone = &Error{Status: http.StatusGone, Msg: "this link has expired or was removed"}

func newLinkID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// The key signing the cookies of unlocked links, kept across restarts.
var (
	linkKeyMu sync.Mutex
	linkKey   []byte
)

func (s *Server) linkSecret() []byte {
	linkKeyMu.Lock()
	defer linkKeyMu.Unlock()
	if linkKey != nil {
		return linkKey
	}
	p := filepath.Join(s.dataDir, "links.key")
	if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
		linkKey = b
		return b
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	_ = os.WriteFile(p, b, 0o600)
	linkKey = b
	return b
}

func (s *Server) linkToken(l *store.ShareLink) string {
	m := hmac.New(sha256.New, s.linkSecret())
	m.Write([]byte(l.ID + "|" + l.PasswordHash))
	return hex.EncodeToString(m.Sum(nil))
}

func linkCookie(id string) string { return "strata_link_" + id }

// linkView is what the owner sees (never the password hash).
type linkView struct {
	*store.ShareLink
	Password bool   `json:"password"`
	Name     string `json:"name"`
	State    string `json:"state"` // gate.Link*
}

func (s *Server) viewLink(l *store.ShareLink, albums []*store.Album) linkView {
	c := *l
	c.PasswordHash = ""
	v := linkView{ShareLink: &c, Password: l.PasswordHash != "", Name: filepath.Base(l.Path), State: linkLimits(l).State(time.Now())}
	for _, a := range albums {
		if a.ID == l.Album {
			v.Name = a.Name
		}
	}
	return v
}

// openLink returns the link if it is usable, with the owner's role.
func (s *Server) openLink(id string) (store.ShareLink, bool, error) {
	var l store.ShareLink
	var found, admin, disabled, owner bool
	s.store.Read(func(st *store.State) {
		for _, x := range st.Links {
			if x.ID == id {
				l, found = *x, true
			}
		}
		for _, u := range st.Users {
			if found && u.Username == l.Owner {
				admin, disabled, owner = u.Role == "admin", u.Disabled, true
			}
		}
	})
	if !found || !owner || disabled || (!l.Expires.IsZero() && time.Now().After(l.Expires)) {
		return l, false, errLinkGone
	}
	return l, admin, nil
}

func (s *Server) linkUnlocked(r *http.Request, l *store.ShareLink) bool {
	if l.PasswordHash == "" {
		return true
	}
	c, err := r.Cookie(linkCookie(l.ID))
	return err == nil && hmac.Equal([]byte(c.Value), []byte(s.linkToken(l)))
}

// linkTarget resolves a path inside a folder link (sub is relative to it).
func (s *Server) linkTarget(l *store.ShareLink, admin bool, sub string) (target, error) {
	p := l.Path
	if sub != "" && l.Dir {
		p = filepath.Join(l.Path, filepath.Clean("/"+sub))
	}
	t, err := s.locateFor(l.Owner, admin, p)
	if err != nil || !t.acc.read || t.acc.ancestor {
		return target{}, errLinkGone
	}
	return t, nil
}

// albumItems returns the photos of an album the owner can still read.
func (s *Server) albumItems(l *store.ShareLink, admin bool) (store.Album, []photos.Item, error) {
	var a store.Album
	found := false
	s.store.Read(func(st *store.State) {
		for _, x := range st.Photos.Albums {
			if x.ID == l.Album && x.Owner == l.Owner {
				a, found = *x, true
				a.Items = slices.Clone(x.Items)
			}
		}
	})
	if !found {
		return a, nil, errLinkGone
	}
	var out []photos.Item
	for _, p := range a.Items {
		it, ok := photos.Default.Get(p)
		if !ok {
			continue
		}
		if t, err := s.locateFor(l.Owner, admin, p); err != nil || !t.acc.read || t.acc.ancestor {
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return a, out, nil
}

func (s *Server) touchLink(id string, f func(l *store.ShareLink)) {
	_ = s.store.Update(func(st *store.State) error {
		for _, x := range st.Links {
			if x.ID == id {
				f(x)
				x.LastUsed = time.Now()
			}
		}
		return nil
	})
}

// uniqueName returns name, or "name (2).ext"… when it already exists.
func uniqueName(exists func(string) bool, name string) string {
	if !exists(name) {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i < 10000; i++ {
		n := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !exists(n) {
			return n
		}
	}
	return fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext)
}

func (s *Server) linkRoutes() {
	// ---- Owner side ----
	s.user("GET /api/links", func(r *http.Request) (any, error) {
		me, admin := userOf(r), isAdmin(r)
		out := []linkView{}
		s.store.Read(func(st *store.State) {
			for _, l := range st.Links {
				if l.Owner == me || admin {
					out = append(out, s.viewLink(l, st.Photos.Albums))
				}
			}
		})
		sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
		return out, nil
	})

	s.user("POST /api/links", func(r *http.Request) (any, error) {
		var req struct {
			Path     string `json:"path"`
			Album    string `json:"album"`
			Download bool   `json:"download"`
			Upload   bool   `json:"upload"`
			linkOptions
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		me := userOf(r)
		l := &store.ShareLink{ID: newLinkID(), Owner: me, Download: req.Download, Created: time.Now()}
		switch {
		case req.Album != "":
			ok := false
			s.store.Read(func(st *store.State) {
				for _, a := range st.Photos.Albums {
					ok = ok || (a.ID == req.Album && a.Owner == me)
				}
			})
			if !ok {
				return nil, notFound("album not found")
			}
			l.Album, l.Download = req.Album, true
		default:
			t, err := s.locate(r, req.Path)
			if err != nil {
				return nil, err
			}
			if !t.acc.read || t.acc.ancestor || t.abs == "/srv/strata" {
				return nil, &Error{Status: http.StatusForbidden, Msg: "access denied"}
			}
			rt, err := t.open()
			if err != nil {
				return nil, err
			}
			st, err := rt.Stat(t.rel)
			rt.Close()
			if err != nil {
				return nil, notFound("file not found")
			}
			l.Path, l.Dir = t.abs, st.IsDir()
			if req.Upload {
				if !l.Dir {
					return nil, badRequest("only a folder can receive files")
				}
				if !t.acc.write {
					return nil, &Error{Status: http.StatusForbidden, Msg: "you cannot write to this folder"}
				}
				l.Upload = true
			}
		}
		if !l.Download && !l.Upload {
			return nil, badRequest("the link must allow downloading or uploading")
		}
		if err := req.linkOptions.apply(l, time.Now()); err != nil {
			return nil, err
		}
		var albums []*store.Album
		err := s.store.Update(func(st *store.State) error {
			if len(st.Links) >= 5000 {
				return badRequest("too many links")
			}
			st.Links = append(st.Links, l)
			albums = st.Photos.Albums
			return nil
		})
		if err != nil {
			return nil, err
		}
		if l.Upload && !l.Download {
			s.audit(r, "links", "File request created for "+l.Path)
		} else {
			s.audit(r, "links", "Share link created for "+firstNonEmpty(l.Path, "album "+l.Album))
		}
		return s.viewLink(l, albums), nil
	})

	// Changes the options of a link (expiry, password, limits, file request).
	s.user("PUT /api/links/{id}", func(r *http.Request) (any, error) {
		var req linkOptions
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		id, me, admin := r.PathValue("id"), userOf(r), isAdmin(r)
		// Hash a new password outside the state lock.
		if req.Password != nil && *req.Password != "" {
			var tmp store.ShareLink
			if err := (linkOptions{Password: req.Password}).apply(&tmp, time.Now()); err != nil {
				return nil, err
			}
			req.Password, req.ClearPassword = nil, false
			req.hashed = tmp.PasswordHash
		}
		var view linkView
		err := s.store.Update(func(st *store.State) error {
			for _, l := range st.Links {
				if l.ID != id || (l.Owner != me && !admin) {
					continue
				}
				c := *l
				if err := req.apply(&c, time.Now()); err != nil {
					return err
				}
				if req.hashed != "" {
					c.PasswordHash = req.hashed
				}
				*l = c
				view = s.viewLink(l, st.Photos.Albums)
				return nil
			}
			return notFound("link not found")
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "links", "Share link changed: "+view.Name)
		return view, nil
	})

	s.user("DELETE /api/links/{id}", func(r *http.Request) (any, error) {
		id, me, admin := r.PathValue("id"), userOf(r), isAdmin(r)
		found := false
		err := s.store.Update(func(st *store.State) error {
			st.Links = slices.DeleteFunc(st.Links, func(l *store.ShareLink) bool {
				if l.ID == id && (l.Owner == me || admin) {
					found = true
					return true
				}
				return false
			})
			return nil
		})
		if err == nil && !found {
			return nil, notFound("link not found")
		}
		s.audit(r, "links", "Share link removed")
		return nil, err
	})

	// ---- Visitor side (no account) ----
	s.mux.Handle("GET /api/s/{id}", linkRate(linkReads, s.wrap(func(r *http.Request) (any, error) {
		l, admin, err := s.openLink(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		unlocked := s.linkUnlocked(r, &l)
		if now := time.Now(); countView(l.ID, clientIP(r), now) {
			s.touchLink(l.ID, func(x *store.ShareLink) { x.Views++; x.LastView = now })
		}
		info := map[string]any{"password": l.PasswordHash != "", "unlocked": unlocked, "dir": l.Dir, "download": l.Download, "upload": l.Upload, "owner": l.Owner, "title": l.Title}
		if !l.Expires.IsZero() {
			info["expires"] = l.Expires
		}
		if l.MaxDownloads > 0 {
			info["downloadsLeft"] = max(0, l.MaxDownloads-l.Downloads)
		}
		if l.Upload && unlocked {
			info["message"], info["askGuest"] = l.Message, l.AskGuest
			if l.MaxFileBytes > 0 {
				info["maxFileBytes"] = l.MaxFileBytes
			}
			if l.MaxTotal > 0 {
				info["uploadLeft"] = max(0, l.MaxTotal-l.Received)
			}
		}
		if l.Album != "" {
			a, _, err := s.albumItems(&l, admin)
			if err != nil {
				return nil, err
			}
			info["album"], info["name"] = true, a.Name
			return info, nil
		}
		t, err := s.linkTarget(&l, admin, "")
		if err != nil {
			return nil, err
		}
		info["name"] = filepath.Base(t.abs)
		if !l.Download && l.Title != "" {
			info["name"] = l.Title // a file request: the folder name is not needed
		}
		if !l.Dir {
			if rt, err := t.open(); err == nil {
				if st, err := rt.Stat(t.rel); err == nil {
					info["size"] = st.Size()
				}
				rt.Close()
			}
		}
		return info, nil
	})))

	s.mux.HandleFunc("POST /api/s/{id}/unlock", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if !sameOrigin(r) {
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "cross-site request refused"})
			return
		}
		l, _, err := s.openLink(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		var req struct {
			Password string `json:"password"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many attempts, try again later"})
			return
		}
		if l.PasswordHash == "" || !checkPassword(l.PasswordHash, req.Password) {
			s.guardFail(ip, "share link", "")
			writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "wrong password"})
			return
		}
		s.limiter.reset(ip)
		http.SetCookie(w, &http.Cookie{Name: linkCookie(l.ID), Value: s.linkToken(&l), Path: "/api/s/" + l.ID, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 86400})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	// Folder content or album photos.
	s.mux.Handle("GET /api/s/{id}/files", linkRate(linkReads, s.wrap(func(r *http.Request) (any, error) {
		l, admin, err := s.openLink(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		if !s.linkUnlocked(r, &l) {
			return nil, &Error{Status: http.StatusUnauthorized, Msg: "password required"}
		}
		if !l.Download {
			return map[string]any{"entries": []fileEntry{}}, nil // drop box: content stays hidden
		}
		if l.Album != "" {
			_, items, err := s.albumItems(&l, admin)
			if err != nil {
				return nil, err
			}
			out := []map[string]any{}
			for _, it := range items {
				out = append(out, map[string]any{"path": it.Path, "name": filepath.Base(it.Path), "date": it.Date, "size": it.Size, "video": it.Video})
			}
			return map[string]any{"photos": out}, nil
		}
		if !l.Dir {
			return nil, badRequest("not a folder")
		}
		sub := r.URL.Query().Get("path")
		t, err := s.linkTarget(&l, admin, sub)
		if err != nil {
			return nil, err
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		dir, err := rt.Open(t.rel)
		if err != nil {
			return nil, notFound("folder not found")
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			return nil, notFound("not a folder")
		}
		out := []fileEntry{}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || e.Name() == "#recycle" || e.Name() == "@eaDir" {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			rel := path.Join(filepath.ToSlash(filepath.Clean("/" + sub))[1:], e.Name())
			out = append(out, fileEntry{Name: e.Name(), Path: rel, Dir: e.IsDir(), Size: info.Size(), ModTime: info.ModTime()})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Dir != out[j].Dir {
				return out[i].Dir
			}
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		})
		return map[string]any{"path": filepath.ToSlash(filepath.Clean("/" + sub)), "entries": out}, nil
	})))

	s.mux.Handle("GET /api/s/{id}/download", linkRate(linkReads, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l, admin, err := s.openLink(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		if !s.linkUnlocked(r, &l) {
			writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "password required"})
			return
		}
		if !l.Download {
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "downloads are not allowed"})
			return
		}
		if l.MaxDownloads > 0 && l.Downloads >= l.MaxDownloads {
			writeErr(w, &Error{Status: http.StatusGone, Msg: "the download limit of this link is reached"})
			return
		}
		var t target
		if l.Album != "" {
			_, items, err := s.albumItems(&l, admin)
			p := r.URL.Query().Get("path")
			if err != nil || !slices.ContainsFunc(items, func(it photos.Item) bool { return it.Path == p }) {
				writeErr(w, notFound("photo not found"))
				return
			}
			if t, err = s.locateFor(l.Owner, admin, p); err != nil {
				writeErr(w, errLinkGone)
				return
			}
		} else if t, err = s.linkTarget(&l, admin, r.URL.Query().Get("path")); err != nil {
			writeErr(w, err)
			return
		}
		// Previews and resumed parts are not downloads; a download is
		// counted before it starts so the limit holds.
		if r.URL.Query().Get("inline") != "1" && firstRange(r) {
			if err := s.reserveDownload(l.ID); err != nil {
				writeErr(w, err)
				return
			}
		}
		s.serveTarget(w, r, t, nil)
	})))

	// Thumbnails of the photos of an album or a folder link.
	s.mux.Handle("GET /api/s/{id}/thumb", linkRate(linkReads, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l, admin, err := s.openLink(r.PathValue("id"))
		if err != nil || !s.linkUnlocked(r, &l) || !l.Download {
			writeErr(w, firstErr(err, &Error{Status: http.StatusUnauthorized, Msg: "password required"}))
			return
		}
		p := r.URL.Query().Get("path")
		if l.Album != "" {
			_, items, err := s.albumItems(&l, admin)
			if err != nil || !slices.ContainsFunc(items, func(it photos.Item) bool { return it.Path == p }) {
				writeErr(w, notFound("photo not found"))
				return
			}
		} else {
			t, err := s.linkTarget(&l, admin, p)
			if err != nil {
				writeErr(w, err)
				return
			}
			p = t.abs
		}
		it, ok := photos.Default.Get(p)
		if !ok {
			writeErr(w, notFound("no thumbnail"))
			return
		}
		f, err := photos.Thumb(r.Context(), it, r.URL.Query().Get("size"))
		if err != nil {
			writeErr(w, notFound("no thumbnail"))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeFile(w, r, f)
	})))

	// Drop box / file request: visitors add files (never overwriting).
	s.mux.Handle("POST /api/s/{id}/upload", linkRate(linkUploads, http.HandlerFunc(s.linkUpload)))
}
