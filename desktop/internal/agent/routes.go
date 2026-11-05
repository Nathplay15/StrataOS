package agent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"strataos-sync/internal/app"
	"strataos-sync/internal/engine"
	"strataos-sync/internal/i18n"
	"strataos-sync/internal/mdns"
	"strataos-sync/internal/platform"
)

// AccountView is an account as the window sees it (no token, no keys).
type AccountView struct {
	ID     string        `json:"id"`
	Paired bool          `json:"paired"`
	Config any           `json:"config"`
	Status engine.Status `json:"status"`
}

func (a *Agent) view(x *account) AccountView {
	return AccountView{ID: x.id, Paired: x.app.Paired(), Config: app.RedactKeys(x.app.Config()), Status: x.app.Status()}
}

func (a *Agent) routes(handle func(string, func(*http.Request) (any, error))) {
	handle("GET /v1/state", func(r *http.Request) (any, error) {
		home, _ := os.UserHomeDir()
		var accs []AccountView
		for _, x := range a.sorted() {
			accs = append(accs, a.view(x))
		}
		a.mu.Lock()
		lang := a.lang
		a.mu.Unlock()
		return map[string]any{"version": a.Version, "dir": a.Root, "home": home, "sep": string(filepath.Separator),
			"defaultIgnore": engine.DefaultIgnore, "language": lang, "systemLanguage": i18n.Pick(""), "accounts": accs,
			"seq": a.events.last()}, nil
	})

	// Long poll of what the window shows as notifications or refreshes.
	handle("GET /v1/events", func(r *http.Request) (any, error) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
		wait = min(max(wait, 0), 50)
		seq, evs := a.events.wait(r.Context(), after, time.Duration(wait)*time.Second)
		return map[string]any{"seq": seq, "events": evs}, nil
	})

	handle("POST /v1/discover", func(r *http.Request) (any, error) {
		l, err := mdns.Discover(r.Context(), 2500*time.Millisecond)
		if l == nil {
			l = []mdns.NAS{}
		}
		return l, err
	})

	handle("POST /v1/probe", func(r *http.Request) (any, error) {
		var req struct{ URL, Link string }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		return a.probe(r.Context(), req.URL, req.Link)
	})

	// Pairs this computer with a NAS: into the given account (signing in
	// again), else the main one when free, else a new account.
	handle("POST /v1/accounts", func(r *http.Request) (any, error) {
		var req struct {
			Account string `json:"account"`
			app.PairInput
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		id, err := a.pair(r.Context(), req.Account, req.PairInput)
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	})

	handle("POST /v1/accounts/{id}/signout", func(r *http.Request) (any, error) {
		return nil, a.signOut(r.Context(), r.PathValue("id"))
	})

	acc := func(pattern string, h func(r *http.Request, x *account) (any, error)) {
		handle(pattern, func(r *http.Request) (any, error) {
			x, err := a.account(r.PathValue("id"))
			if err != nil {
				return nil, err
			}
			return h(r, x)
		})
	}

	acc("GET /v1/accounts/{id}/remote/shares", func(r *http.Request, x *account) (any, error) {
		c := x.app.Client()
		if c == nil {
			return nil, errors.New(a.T("err.notpaired"))
		}
		return c.Shares(r.Context())
	})

	// Folders of the NAS, to choose the one a pair follows.
	acc("GET /v1/accounts/{id}/remote/list", func(r *http.Request, x *account) (any, error) {
		c := x.app.Client()
		if c == nil {
			return nil, errors.New(a.T("err.notpaired"))
		}
		p := r.URL.Query().Get("path")
		ents, err := c.List(r.Context(), p)
		if err != nil {
			return nil, err
		}
		type dir struct {
			Name      string `json:"name"`
			Path      string `json:"path"`
			Encrypted bool   `json:"encrypted,omitempty"`
		}
		dirs := []dir{}
		for _, e := range ents {
			if e.Dir {
				dirs = append(dirs, dir{e.Name, e.Path, e.Encrypted})
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

	acc("POST /v1/accounts/{id}/pairs", func(r *http.Request, x *account) (any, error) {
		var req struct {
			Local, Remote string
			app.PairOptions
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		if err := a.checkLocal(req.Local); err != nil {
			return nil, err
		}
		p, err := x.app.AddPair(req.Local, req.Remote, req.PairOptions)
		if app.IsE2ELocked(err) {
			// The NAS folder is encrypted: the window asks for its passphrase.
			return map[string]any{"locked": true, "message": err.Error()}, nil
		}
		if err != nil {
			return nil, err
		}
		out := map[string]any{"pair": p}
		if p.Encrypted && req.Encrypt {
			out["recoveryKey"], _ = x.app.RecoveryKey(p.ID)
		}
		return out, nil
	})
	acc("GET /v1/accounts/{id}/pairs/{pair}/recovery", func(r *http.Request, x *account) (any, error) {
		k, err := x.app.RecoveryKey(r.PathValue("pair"))
		return map[string]string{"recoveryKey": k}, err
	})
	acc("POST /v1/accounts/{id}/pairs/{pair}/passphrase", func(r *http.Request, x *account) (any, error) {
		var req struct{ Passphrase string }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		return nil, x.app.ChangePassphrase(r.Context(), r.PathValue("pair"), req.Passphrase)
	})
	acc("PUT /v1/accounts/{id}/pairs/{pair}", func(r *http.Request, x *account) (any, error) {
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
		return nil, x.app.UpdatePair(r.PathValue("pair"), req.Paused, req.Exclude)
	})
	acc("DELETE /v1/accounts/{id}/pairs/{pair}", func(r *http.Request, x *account) (any, error) {
		return nil, x.app.RemovePair(r.PathValue("pair"))
	})
	acc("POST /v1/accounts/{id}/settings", func(r *http.Request, x *account) (any, error) {
		var s app.Settings
		if err := decodeBody(r, &s); err != nil {
			return nil, err
		}
		s.Autostart = nil // start at login belongs to the desktop app (its autostart plugin)
		s.Language = nil  // the same for every account: POST /v1/settings
		return nil, x.app.UpdateSettings(s)
	})
	acc("POST /v1/accounts/{id}/pause", func(r *http.Request, x *account) (any, error) {
		var req struct{ On bool }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		return nil, x.app.SetPaused(req.On)
	})
	acc("POST /v1/accounts/{id}/sync-now", func(r *http.Request, x *account) (any, error) { x.app.SyncNow(); return nil, nil })
	acc("POST /v1/accounts/{id}/brake", func(r *http.Request, x *account) (any, error) {
		var req struct {
			Pair  string `json:"pair"`
			Apply bool   `json:"apply"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		x.app.ResolveBrake(req.Pair, req.Apply)
		return nil, nil
	})
	acc("POST /v1/accounts/{id}/web", func(r *http.Request, x *account) (any, error) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		u, err := a.openWeb(x, req.Path)
		return map[string]string{"url": u}, err
	})
	acc("POST /v1/accounts/{id}/download", func(r *http.Request, x *account) (any, error) {
		var req struct {
			Path string `json:"path"` // on the NAS
			Dest string `json:"dest"` // file on this computer
			Size int64  `json:"size"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		return a.xfers.download(x, req.Path, req.Dest, req.Size)
	})
	acc("POST /v1/accounts/{id}/upload", func(r *http.Request, x *account) (any, error) {
		var req struct {
			Paths     []string `json:"paths"`  // files or folders of this computer
			Folder    string   `json:"folder"` // NAS folder
			Overwrite bool     `json:"overwrite"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		return a.xfers.upload(x, req.Paths, req.Folder, req.Overwrite)
	})

	// Every pause and resume at once (tray menu).
	handle("POST /v1/pause", func(r *http.Request) (any, error) {
		var req struct{ On bool }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		for _, x := range a.sorted() {
			if x.app.Paired() {
				if err := x.app.SetPaused(req.On); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	})
	handle("POST /v1/sync-now", func(r *http.Request) (any, error) {
		for _, x := range a.sorted() {
			x.app.SyncNow()
		}
		return nil, nil
	})
	handle("POST /v1/settings", func(r *http.Request) (any, error) {
		var req struct {
			Language *string `json:"language"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		if req.Language != nil {
			if *req.Language != "" && !i18n.Has(*req.Language) {
				return nil, errors.New("unknown language")
			}
			a.mu.Lock()
			a.lang = *req.Language
			a.mu.Unlock()
			for _, x := range a.sorted() {
				if err := x.app.UpdateSettings(app.Settings{Language: req.Language}); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	})

	handle("GET /v1/transfers", func(r *http.Request) (any, error) { return a.xfers.snapshot(), nil })
	handle("DELETE /v1/transfers/{tid}", func(r *http.Request) (any, error) { return nil, a.xfers.cancel(r.PathValue("tid")) })
	handle("POST /v1/transfers/clear", func(r *http.Request) (any, error) { a.xfers.clear(); return nil, nil })

	// Which NAS file a file of this computer is (Explorer and Finder menus).
	handle("POST /v1/resolve", func(r *http.Request) (any, error) {
		var req struct{ Path string }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		return a.resolve(req.Path)
	})

	handle("POST /v1/open", func(r *http.Request) (any, error) {
		var req struct{ Account, Pair string }
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		x, err := a.account(req.Account)
		if err != nil {
			return nil, err
		}
		for _, p := range x.app.Config().Pairs {
			if req.Pair == "" || p.ID == req.Pair {
				return nil, platform.Open(p.Local)
			}
		}
		return nil, errors.New("folder not found")
	})
}

// probe returns the certificate the NAS presents.
func (a *Agent) probe(ctx context.Context, rawURL, link string) (any, error) {
	expected := ""
	if link != "" {
		base, fp, _, _, err := app.ParseLink(link)
		if err != nil {
			return nil, errors.New(a.T("err.link"))
		}
		rawURL, expected = base, fp
	}
	base, err := app.NormalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	fp, trusted, err := probeCertificate(ctx, base)
	if err != nil {
		return nil, errors.New(a.T("err.unreachable", base, err.Error()))
	}
	return map[string]any{"fingerprint": fp, "trusted": trusted, "expected": expected, "url": base}, nil
}

// pair signs in to a NAS and returns the account used.
func (a *Agent) pair(ctx context.Context, id string, in app.PairInput) (string, error) {
	if in.Link != "" {
		base, fp, code, _, err := app.ParseLink(in.Link)
		if err != nil {
			return "", errors.New(a.T("err.link"))
		}
		in.URL, in.Code, in.Link = base, code, ""
		if fp != "" {
			in.Fingerprint = fp
		}
	}
	base, err := app.NormalizeURL(in.URL)
	if err != nil {
		return "", err
	}
	in.URL = base
	for _, x := range a.sorted() {
		if x.id != id && x.app.Paired() && x.app.Config().Server == base {
			return "", &httpError{http.StatusConflict, a.T("err.alreadypaired")}
		}
	}
	if id != "" {
		x, err := a.account(id)
		if err != nil {
			return "", err
		}
		return id, x.app.Pair(ctx, in)
	}
	if m, _ := a.account(MainAccount); m != nil && !m.app.Paired() && len(m.app.Config().Pairs) == 0 {
		return MainAccount, m.app.Pair(ctx, in)
	}
	nid, dir, err := a.newAccountDir()
	if err != nil {
		return "", err
	}
	if err := a.load(nid, dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	x, _ := a.account(nid)
	a.mu.Lock()
	lang := a.lang
	a.mu.Unlock()
	_ = x.app.UpdateSettings(app.Settings{Language: &lang})
	if err := x.app.Pair(ctx, in); err != nil {
		a.drop(x)
		return "", err
	}
	return nid, nil
}

// signOut revokes this computer on a NAS; an account other than the main
// one is then forgotten.
func (a *Agent) signOut(ctx context.Context, id string) error {
	x, err := a.account(id)
	if err != nil {
		return err
	}
	if err := x.app.SignOut(ctx); err != nil {
		return err
	}
	if id != MainAccount {
		a.drop(x)
	}
	a.events.add(Event{Kind: "changed", Account: id})
	return nil
}

func (a *Agent) drop(x *account) {
	x.app.Stop()
	a.mu.Lock()
	delete(a.accounts, x.id)
	a.mu.Unlock()
	if x.web != nil {
		x.web.close()
	}
	_ = os.RemoveAll(x.app.Store.Dir)
}

// checkLocal refuses a local folder inside the folder of the agent (the
// app checks its own account folder only).
func (a *Agent) checkLocal(local string) error {
	l := strings.TrimSpace(local)
	if !filepath.IsAbs(l) {
		return nil // the app explains
	}
	rel, err := filepath.Rel(filepath.Clean(a.Root), filepath.Clean(l))
	if err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
		return errors.New(a.T("err.local.appdir"))
	}
	return nil
}

// Resolved is the NAS file of a local file.
type Resolved struct {
	Account string `json:"account"`
	Pair    string `json:"pair"`
	Remote  string `json:"remote"` // absolute NAS path
	Dir     bool   `json:"dir"`
}

// resolve finds the synchronized folder holding a local path.
func (a *Agent) resolve(p string) (Resolved, error) {
	p = filepath.Clean(strings.TrimSpace(p))
	if !filepath.IsAbs(p) {
		return Resolved{}, errors.New("absolute path required")
	}
	st, err := os.Stat(p)
	if err != nil {
		return Resolved{}, err
	}
	for _, x := range a.sorted() {
		for _, pr := range x.app.Config().Pairs {
			rel, err := filepath.Rel(pr.Local, p)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if pr.Encrypted {
				return Resolved{}, &httpError{http.StatusConflict, a.T("err.resolve.encrypted")}
			}
			remote := pr.Remote
			if rel != "." {
				remote = path.Join(pr.Remote, filepath.ToSlash(rel))
			}
			return Resolved{Account: x.id, Pair: pr.ID, Remote: remote, Dir: st.IsDir()}, nil
		}
	}
	return Resolved{}, &httpError{http.StatusNotFound, a.T("err.resolve.outside")}
}
