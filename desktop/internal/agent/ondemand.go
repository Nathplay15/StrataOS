package agent

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"strataos-sync/internal/ondemand"
)

// Files on demand: the window's routes (/v1/accounts/{id}/ondemand…), the
// API of the macOS File Provider extension (/v1/fp/…, its own token), and
// the NAS path of a file of the on-demand folder (menus).

func (a *Agent) onDemandRoutes(acc func(string, func(*http.Request, *account) (any, error))) {
	acc("GET /v1/accounts/{id}/ondemand", func(r *http.Request, x *account) (any, error) {
		return x.app.OnDemandStatus(), nil
	})
	acc("PUT /v1/accounts/{id}/ondemand", func(r *http.Request, x *account) (any, error) {
		var req struct {
			Enabled *bool   `json:"enabled"`
			Path    *string `json:"path"`
			CacheMB *int64  `json:"cacheMB"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		if req.Path != nil && *req.Path != "" {
			if err := a.checkLocal(*req.Path); err != nil {
				return nil, err
			}
		}
		if err := x.app.SetOnDemand(req.Enabled, req.Path, req.CacheMB); err != nil {
			return nil, err
		}
		return x.app.OnDemandStatus(), nil
	})
	// Keep on this device / free up space: a path of the on-demand folder
	// (absolute) or "Share/…".
	acc("POST /v1/accounts/{id}/ondemand/pin", func(r *http.Request, x *account) (any, error) {
		var req struct {
			Path   string `json:"path"`
			Pinned bool   `json:"pinned"`
		}
		if err := decodeBody(r, &req); err != nil {
			return nil, err
		}
		_, prov := x.app.OnDemand()
		if prov == nil {
			return nil, &httpError{http.StatusConflict, "files on demand is off"}
		}
		p, err := onDemandRel(prov, req.Path)
		if err != nil {
			return nil, err
		}
		return nil, prov.Pin(p, req.Pinned)
	})
	acc("POST /v1/accounts/{id}/ondemand/free", func(r *http.Request, x *account) (any, error) {
		s, _ := x.app.OnDemand()
		if s == nil {
			return nil, &httpError{http.StatusConflict, "files on demand is off"}
		}
		return map[string]int64{"freed": s.FreeUp()}, nil
	})
	// Windows: the shell registered the sync root with Explorer.
	acc("POST /v1/accounts/{id}/ondemand/registered", func(r *http.Request, x *account) (any, error) {
		_, prov := x.app.OnDemand()
		if rp, ok := prov.(interface{ Registered() error }); ok {
			return nil, rp.Registered()
		}
		return nil, nil
	})
}

// onDemandRel turns a path of the on-demand folder into "Share/…".
func onDemandRel(prov ondemand.Provider, p string) (string, error) {
	if root := prov.Info().Root; root != "" && filepath.IsAbs(p) {
		rel, err := filepath.Rel(root, filepath.Clean(p))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", &httpError{http.StatusBadRequest, "not in the on-demand folder"}
		}
		if rel == "." {
			rel = ""
		}
		p = rel
	}
	return ondemand.CleanPath(p)
}

// resolveOnDemand finds a path of an on-demand folder (menus).
func (a *Agent) resolveOnDemand(p string, dir bool) (Resolved, bool) {
	for _, x := range a.sorted() {
		s, prov := x.app.OnDemand()
		if s == nil || prov == nil || prov.Info().Root == "" {
			continue
		}
		rel, err := onDemandRel(prov, p)
		if err != nil || rel == "" {
			continue
		}
		remote, err := s.RemotePath(rel)
		if err != nil {
			continue
		}
		return Resolved{Account: x.id, Pair: "ondemand", Remote: remote, Dir: dir}, true
	}
	return Resolved{}, false
}

// pinLocal pins or frees a path of an account's on-demand folder.
func (a *Agent) pinLocal(p string, on bool) error {
	if !filepath.IsAbs(p) {
		return &httpError{http.StatusBadRequest, "absolute path required"}
	}
	for _, x := range a.sorted() {
		_, prov := x.app.OnDemand()
		if prov == nil || prov.Info().Root == "" {
			continue
		}
		if rel, err := onDemandRel(prov, p); err == nil && rel != "" {
			return prov.Pin(rel, on)
		}
	}
	return &httpError{http.StatusNotFound, a.T("err.resolve.outside")}
}

// publishFP gives the macOS extensions the agent's address, their token
// and the synchronized folders (Finder Sync menus), again when they change.
func (a *Agent) publishFP(port int) {
	a.mu.Lock()
	if port == 0 {
		port = a.fpPort
	}
	a.fpPort = port
	a.mu.Unlock()
	if port == 0 {
		return
	}
	var folders []string
	for _, x := range a.sorted() {
		for _, pr := range x.app.Config().Pairs {
			folders = append(folders, pr.Local)
		}
	}
	key := strings.Join(folders, "\x00")
	a.mu.Lock()
	same := a.fpFolders == key && a.fpDone
	a.fpFolders, a.fpDone = key, true
	a.mu.Unlock()
	if same {
		return
	}
	if err := ondemand.PublishAgent(port, a.FPToken, folders); err != nil {
		log.Printf("files on demand: %v", err)
	}
}

// ---- API of the macOS File Provider extension ----

// fpItem is an item as the extension sees it ("" is the root container).
type fpItem struct {
	ondemand.Item
	Parent string `json:"parent"`
}

func fpView(it ondemand.Item) fpItem {
	par := ""
	if i := strings.LastIndexByte(it.Path, '/'); i >= 0 {
		par = it.Path[:i]
	}
	return fpItem{Item: it, Parent: par}
}

func (a *Agent) fpRoutes(mux *http.ServeMux) {
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, err error) {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, ondemand.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ondemand.ErrReadOnly):
			status = http.StatusForbidden
		case errors.Is(err, ondemand.ErrExists), errors.Is(err, ondemand.ErrNotEmpty), errors.Is(err, ondemand.ErrStale):
			status = http.StatusConflict
		case errors.Is(err, ondemand.ErrCrossShare), errors.Is(err, ondemand.ErrIsDir):
			status = http.StatusBadRequest
		}
		var he *httpError
		if errors.As(err, &he) {
			status = he.status
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
	}
	// h wraps a route: the account's service and a checked path.
	h := func(pattern string, f func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			x, err := a.account(r.PathValue("id"))
			if err != nil {
				fail(w, err)
				return
			}
			s, _ := x.app.OnDemand()
			if s == nil {
				fail(w, &httpError{http.StatusServiceUnavailable, "files on demand is off"})
				return
			}
			p, err := ondemand.CleanPath(r.URL.Query().Get("path"))
			if err != nil {
				fail(w, &httpError{http.StatusBadRequest, err.Error()})
				return
			}
			f(w, r, s, p)
		})
	}

	h("GET /v1/fp/{id}/item", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		it, err := s.Stat(p)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, fpView(it))
	})
	h("GET /v1/fp/{id}/list", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		anchor := s.Anchor()
		l, err := s.List(p)
		if err != nil {
			fail(w, err)
			return
		}
		out := make([]fpItem, 0, len(l))
		for _, it := range l {
			out = append(out, fpView(it))
		}
		writeJSON(w, 200, map[string]any{"items": out, "anchor": anchor})
	})
	h("GET /v1/fp/{id}/anchor", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		writeJSON(w, 200, map[string]string{"anchor": s.Anchor()})
	})
	// Changes after an anchor; expired: enumerate everything again.
	h("GET /v1/fp/{id}/changes", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		ch, next, ok := s.ChangesSince(r.URL.Query().Get("anchor"))
		if !ok {
			writeJSON(w, 200, map[string]any{"anchor": next, "expired": true})
			return
		}
		type change struct {
			Path    string  `json:"path"`
			Removed bool    `json:"removed,omitempty"`
			Item    *fpItem `json:"item,omitempty"`
		}
		seen := map[string]bool{}
		out := []change{}
		for i := len(ch) - 1; i >= 0; i-- { // the last change of each path wins
			c := ch[i]
			if seen[c.Path] {
				continue
			}
			seen[c.Path] = true
			if it, err := s.Stat(c.Path); err == nil {
				v := fpView(it)
				out = append(out, change{Path: c.Path, Item: &v})
			} else {
				out = append(out, change{Path: c.Path, Removed: true})
			}
		}
		writeJSON(w, 200, map[string]any{"anchor": next, "changes": out})
	})
	// The contents of a file, from the NAS (version: the one the system knows).
	h("GET /v1/fp/{id}/content", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		it, err := s.Stat(p)
		if err != nil {
			fail(w, err)
			return
		}
		if it.Dir {
			fail(w, ondemand.ErrIsDir)
			return
		}
		v := r.URL.Query().Get("version")
		if v != "" && v != it.Version {
			fail(w, ondemand.ErrStale)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(it.Size, 10))
		w.Header().Set("X-Strata-Version", it.Version)
		if err := s.Fetch(r.Context(), p, it.Version, 0, it.Size, w); err != nil {
			log.Printf("files on demand: send %s to the File Provider: %v", p, err)
			panic(http.ErrAbortHandler) // the extension sees a cut answer, not a short file
		}
	})
	// A file changed or created in Finder (ifMatch: the version replaced,
	// "" a new file). The answer is the item: under a conflict name when
	// the NAS had another version.
	h("PUT /v1/fp/{id}/content", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		tmp, err := os.CreateTemp(filepath.Join(a.Root), ".fp-upload-*")
		if err != nil {
			fail(w, err)
			return
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		n, err := io.Copy(tmp, r.Body)
		if err != nil {
			fail(w, err)
			return
		}
		mtime, _ := strconv.ParseInt(r.URL.Query().Get("mtime"), 10, 64)
		if mtime <= 0 {
			mtime = time.Now().UnixMilli()
		}
		it, err := s.Upload(r.Context(), p, tmp, n, mtime, r.URL.Query().Get("ifMatch"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, fpView(it))
	})
	h("POST /v1/fp/{id}/mkdir", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		it, err := s.Mkdir(r.Context(), p)
		if err != nil && !errors.Is(err, ondemand.ErrExists) || err != nil && !it.Dir {
			fail(w, err)
			return
		}
		writeJSON(w, 200, fpView(it))
	})
	h("POST /v1/fp/{id}/move", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		to, err := ondemand.CleanPath(r.URL.Query().Get("to"))
		if err != nil || to == "" {
			fail(w, &httpError{http.StatusBadRequest, "destination required"})
			return
		}
		if err := s.Move(r.Context(), p, to); err != nil {
			fail(w, err)
			return
		}
		it, err := s.Stat(to)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, fpView(it))
	})
	h("POST /v1/fp/{id}/delete", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		if r.URL.Query().Get("recursive") == "1" {
			// Finder deletes a folder as a whole: its files first.
			var items []ondemand.Item
			s.Walk(p, func(it ondemand.Item) { items = append(items, it) })
			for i := len(items) - 1; i >= 0; i-- {
				if err := s.Delete(r.Context(), items[i].Path); err != nil && !errors.Is(err, ondemand.ErrNotFound) {
					fail(w, err)
					return
				}
			}
		}
		if err := s.Delete(r.Context(), p); err != nil && !errors.Is(err, ondemand.ErrNotFound) {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// The NAS path of an item (Finder Sync menu: share link, open on the NAS).
	h("GET /v1/fp/{id}/remote", func(w http.ResponseWriter, r *http.Request, s *ondemand.Service, p string) {
		rp, err := s.RemotePath(p)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"remote": rp})
	})
}
