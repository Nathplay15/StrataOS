package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"strataos-sync/internal/api"
	"strataos-sync/internal/engine"
	"strataos-sync/internal/i18n"
	"strataos-sync/internal/icon"
	"strataos-sync/internal/mdns"
	"strataos-sync/internal/platform"
)

// The settings page is served on 127.0.0.1 with a random token per launch:
// the page opened by the app carries it once (?t=…) and gets it back as a
// cookie. Other local programs and web pages cannot use the API: no token,
// a custom header required on every call (no cross-site form posts), and
// only the 127.0.0.1 host name (no DNS rebinding).

//go:embed web
var webFS embed.FS

// UI is the settings server.
type UI struct {
	app   *App
	token string
	addr  string
}

// ServeUI starts the settings server on a free local port.
func (a *App) ServeUI(ctx context.Context) (*UI, error) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	u := &UI{app: a, token: hex.EncodeToString(b)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	u.addr = ln.Addr().String()
	srv := &http.Server{Handler: u.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return u, nil
}

// URL opens the page (with the token).
func (u *UI) URL() string { return "http://" + u.addr + "/?t=" + u.token }

func (u *UI) handler() http.Handler {
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("t"); t != "" {
			if subtle.ConstantTimeCompare([]byte(t), []byte(u.token)) != 1 {
				http.Error(w, "invalid link: open the settings from the app", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "st", Value: u.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if !u.authed(r) {
			http.Error(w, "open the settings from the StrataOS Sync menu", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/icon.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(icon.PNG(icon.Idle, 64))
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'")
		files.ServeHTTP(w, r)
	})
	api := func(pattern string, h func(r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !u.authed(r) || r.Header.Get("X-Strata-Sync") != "1" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			v, err := h(r)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if err != nil {
				status := http.StatusBadRequest
				out := map[string]any{"error": u.message(err)}
				if errors.Is(err, api.ErrTwoFactor) {
					out["twoFactor"] = true
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(out)
				return
			}
			if v == nil {
				v = map[string]bool{"ok": true}
			}
			_ = json.NewEncoder(w).Encode(v)
		})
	}
	u.routes(api)
	return mux
}

func (u *UI) authed(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.Host)
	if host != "127.0.0.1" {
		return false
	}
	c, err := r.Cookie("st")
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(u.token)) == 1
}

// message explains an error in the user's language when it is a known one.
func (u *UI) message(err error) string {
	var ce *api.CertError
	switch {
	case errors.As(err, &ce) && ce.Pinned != "":
		return u.app.T("err.certchanged", ce.Presented)
	case errors.As(err, &ce):
		return u.app.T("err.certuntrusted", ce.Presented)
	case errors.Is(err, api.ErrTwoFactor):
		return u.app.T("err.twofactor")
	case api.StatusOf(err) == http.StatusUnauthorized:
		return u.app.T("err.unauthorized") + " (" + err.Error() + ")"
	}
	return err.Error()
}

func decodeBody(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errors.New("invalid request")
	}
	return nil
}

func (u *UI) routes(handle func(string, func(*http.Request) (any, error))) {
	a := u.app
	handle("GET /api/state", func(r *http.Request) (any, error) {
		home, _ := os.UserHomeDir()
		c := a.Config()
		return map[string]any{"paired": a.Paired(), "config": c, "status": a.Status(), "lang": a.Lang(),
			"version": a.Version, "dir": a.Store.Dir, "home": home, "sep": string(filepath.Separator),
			"defaultIgnore": engine.DefaultIgnore, "autostart": platform.Autostart()}, nil
	})
	handle("GET /api/i18n", func(r *http.Request) (any, error) {
		return i18n.Catalog(i18n.Pick(r.URL.Query().Get("lang"))), nil
	})
	handle("POST /api/discover", func(r *http.Request) (any, error) {
		l, err := mdns.Discover(r.Context(), 2500*time.Millisecond)
		if l == nil {
			l = []mdns.NAS{}
		}
		return l, err
	})
	handle("POST /api/probe", func(r *http.Request) (any, error) {
		var req struct{ URL, Link string }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		expected := ""
		if req.Link != "" {
			base, fp, _, _, err := ParseLink(req.Link)
			if err != nil {
				return nil, errors.New(a.T("err.link"))
			}
			req.URL, expected = base, fp
		}
		base, err := NormalizeURL(req.URL)
		if err != nil {
			return nil, err
		}
		fp, trusted, err := api.ProbeCertificate(r.Context(), base)
		if err != nil {
			return nil, errors.New(a.T("err.unreachable", base, err.Error()))
		}
		return map[string]any{"fingerprint": fp, "trusted": trusted, "expected": expected, "url": base}, nil
	})
	handle("POST /api/pair", func(r *http.Request) (any, error) {
		var in PairInput
		if err := decodeBody(r, &in); err != nil {
			return nil, err
		}
		return nil, a.Pair(r.Context(), in)
	})
	handle("POST /api/signout", func(r *http.Request) (any, error) { return nil, a.SignOut(r.Context()) })
	handle("GET /api/remote/shares", func(r *http.Request) (any, error) {
		c := a.Client()
		if c == nil {
			return nil, errors.New(a.T("err.notpaired"))
		}
		return c.Shares(r.Context())
	})
	handle("GET /api/remote/list", func(r *http.Request) (any, error) {
		c := a.Client()
		if c == nil {
			return nil, errors.New(a.T("err.notpaired"))
		}
		p := r.URL.Query().Get("path")
		ents, err := c.List(r.Context(), p)
		if err != nil {
			return nil, err
		}
		type dir struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		dirs := []dir{}
		for _, e := range ents {
			if e.Dir {
				dirs = append(dirs, dir{e.Name, e.Path})
			}
		}
		sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
		isShare := false
		if shares, err := c.Shares(r.Context()); err == nil {
			for _, s := range shares {
				isShare = isShare || s.Path == path.Clean(p)
			}
		}
		return map[string]any{"dirs": dirs, "isShare": isShare}, nil
	})
	handle("POST /api/pairs", func(r *http.Request) (any, error) {
		var req struct{ Local, Remote string }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		return a.AddPair(req.Local, req.Remote)
	})
	handle("PUT /api/pairs/{id}", func(r *http.Request) (any, error) {
		var req struct {
			Paused  *bool    `json:"paused"`
			Exclude []string `json:"exclude"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		if req.Exclude != nil && len(req.Exclude) == 0 {
			req.Exclude = []string{}
		}
		return nil, a.UpdatePair(r.PathValue("id"), req.Paused, req.Exclude)
	})
	handle("DELETE /api/pairs/{id}", func(r *http.Request) (any, error) { return nil, a.RemovePair(r.PathValue("id")) })
	handle("POST /api/pick-folder", func(r *http.Request) (any, error) {
		p, err := platform.PickFolder(a.T("folders.local"))
		return map[string]string{"path": p}, err
	})
	handle("POST /api/settings", func(r *http.Request) (any, error) {
		var s Settings
		if err := decodeBody(r, &s); err != nil {
			return nil, err
		}
		return nil, a.UpdateSettings(s)
	})
	handle("POST /api/pause", func(r *http.Request) (any, error) {
		var req struct{ On bool }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		return nil, a.SetPaused(req.On)
	})
	handle("POST /api/sync-now", func(r *http.Request) (any, error) { a.SyncNow(); return nil, nil })
	handle("POST /api/brake", func(r *http.Request) (any, error) {
		var req struct {
			Pair  string `json:"pair"`
			Apply bool   `json:"apply"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		a.ResolveBrake(req.Pair, req.Apply)
		return nil, nil
	})
	handle("POST /api/open", func(r *http.Request) (any, error) {
		var req struct{ What, ID string }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		return nil, a.Open(req.What, req.ID)
	})
}

// Open opens a synchronized folder ("folder", by pair ID; the first one
// without an ID) or the NAS web interface ("web").
func (a *App) Open(what, id string) error {
	if what == "web" {
		if u := a.WebURL(); u != "" {
			return platform.Open(u)
		}
		return errors.New(a.T("err.notpaired"))
	}
	for _, p := range a.Config().Pairs {
		if id == "" || p.ID == id {
			return platform.Open(p.Local)
		}
	}
	return errors.New("folder not found")
}
