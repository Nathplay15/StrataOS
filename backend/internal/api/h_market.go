package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/docker"
	"strataos/internal/jobs"
	"strataos/internal/market"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// Community app catalogs ("sources") of the App Center. A source is an
// index fetched over HTTPS or from a Git repository (docs/CATALOG-FORMAT.md),
// trusted through an Ed25519 key or a SHA-256 the administrator accepted.
// Its apps are compose templates checked twice before an install: by
// market.Analyze on the template, then on what `docker compose config`
// makes of it; anything risky (paths outside /srv/strata, privileged mode,
// host network, devices, capabilities…) needs an explicit approval.

const maxSources = 20

var marketState struct {
	mu    sync.Mutex // sources and their cached files
	cache map[string]cachedCatalog
}

type cachedCatalog struct {
	sha     string
	cat     *market.Catalog
	invalid []market.Invalid
}

// communityMeta is kept next to an installed community app
// (.strata-community.json in its stack folder).
type communityMeta struct {
	Source     string          `json:"source"`
	SourceName string          `json:"sourceName"`
	App        string          `json:"app"`
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	Icon       string          `json:"icon,omitempty"`
	WebPort    string          `json:"webPort,omitempty"` // variable key
	Approved   market.Approval `json:"approved"`
	Installed  time.Time       `json:"installed"`
}

const communityFile = ".strata-community.json"

func readCommunity(stack string) *communityMeta {
	if docker.ValidStack(stack) != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(docker.StacksDir, stack, communityFile))
	if err != nil {
		return nil
	}
	var m communityMeta
	if json.Unmarshal(b, &m) != nil || m.App == "" {
		return nil
	}
	return &m
}

func writeCommunity(stack string, m communityMeta) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return sys.WriteFileAtomic(filepath.Join(docker.StacksDir, stack, communityFile), b, 0o640)
}

func (s *Server) catalogDir() string { return filepath.Join(s.dataDir, "catalogs") }

func (s *Server) catalogFile(id string, pending bool) string {
	if pending {
		return filepath.Join(s.catalogDir(), id+".pending.json")
	}
	return filepath.Join(s.catalogDir(), id+".json")
}

// loadCatalog returns the accepted index of a source (parsed once per checksum).
func (s *Server) loadCatalog(src store.CatalogSource) (*market.Catalog, []market.Invalid, error) {
	marketState.mu.Lock()
	defer marketState.mu.Unlock()
	if c, ok := marketState.cache[src.ID]; ok && c.sha == src.SHA256 {
		return c.cat, c.invalid, nil
	}
	b, err := os.ReadFile(s.catalogFile(src.ID, false))
	if err != nil {
		return nil, nil, errors.New("catalog not downloaded yet: refresh it")
	}
	cat, invalid, err := market.Parse(b)
	if err != nil {
		return nil, nil, err
	}
	if marketState.cache == nil {
		marketState.cache = map[string]cachedCatalog{}
	}
	marketState.cache[src.ID] = cachedCatalog{sha: src.SHA256, cat: cat, invalid: invalid}
	return cat, invalid, nil
}

func (s *Server) sources() []store.CatalogSource {
	var out []store.CatalogSource
	s.store.Read(func(st *store.State) {
		for _, x := range st.CatalogSources {
			out = append(out, *x)
		}
	})
	return out
}

func (s *Server) source(id string) (store.CatalogSource, error) {
	for _, x := range s.sources() {
		if x.ID == id {
			return x, nil
		}
	}
	return store.CatalogSource{}, notFound("unknown catalog source")
}

type sourceView struct {
	store.CatalogSource
	Trust   string           `json:"trust"` // signed | pinned
	Apps    int              `json:"apps"`
	Invalid []market.Invalid `json:"invalid"`
}

func (s *Server) viewSource(src store.CatalogSource) sourceView {
	v := sourceView{CatalogSource: src, Trust: "pinned", Invalid: []market.Invalid{}}
	if src.PublicKey != "" {
		v.Trust = "signed"
	}
	if cat, inv, err := s.loadCatalog(src); err == nil {
		v.Apps = len(cat.Apps)
		if inv != nil {
			v.Invalid = inv
		}
	}
	return v
}

// fetchVerified downloads a source and checks its signature (signed
// sources) and format.
func fetchVerified(ctx context.Context, loc market.Location, key string) (*market.Fetched, *market.Catalog, []market.Invalid, error) {
	f, err := market.Fetch(ctx, loc)
	if err != nil {
		return nil, nil, nil, err
	}
	if key != "" {
		pk, err := market.ParsePublicKey(key)
		if err != nil {
			return nil, nil, nil, err
		}
		if err := market.Verify(pk, f.Index, f.Sig); err != nil {
			return nil, nil, nil, err
		}
	}
	cat, invalid, err := market.Parse(f.Index)
	if err != nil {
		return nil, nil, nil, err
	}
	return f, cat, invalid, nil
}

// refreshSource downloads a source again. A signed one is replaced when its
// signature checks; a pinned one whose checksum changed waits for approval.
func (s *Server) refreshSource(ctx context.Context, id string) (sourceView, error) {
	src, err := s.source(id)
	if err != nil {
		return sourceView{}, err
	}
	loc, err := market.ParseLocation(src.URL)
	if err != nil {
		return sourceView{}, badRequest(err.Error())
	}
	f, cat, _, ferr := fetchVerified(ctx, loc, src.PublicKey)
	marketState.mu.Lock()
	switch {
	case ferr != nil:
	case f.SHA256 == src.SHA256:
		_ = os.Remove(s.catalogFile(id, true))
	case src.PublicKey != "":
		ferr = sys.WriteFileAtomic(s.catalogFile(id, false), f.Index, 0o640)
	default:
		ferr = sys.WriteFileAtomic(s.catalogFile(id, true), f.Index, 0o640)
	}
	marketState.mu.Unlock()
	err = s.store.Update(func(st *store.State) error {
		for _, x := range st.CatalogSources {
			if x.ID != id {
				continue
			}
			x.Refreshed = time.Now()
			if ferr != nil {
				x.LastError = ferr.Error()
				return nil
			}
			x.LastError = ""
			switch {
			case f.SHA256 == x.SHA256:
				x.Pending = ""
			case x.PublicKey != "":
				x.SHA256, x.Commit, x.Name, x.Pending = f.SHA256, f.Commit, cat.Name, ""
			default:
				if x.Pending != f.SHA256 {
					s.store.LogLocked(st, "warning", "apps", "", "The community catalog "+x.Name+" changed: review and accept its new checksum")
				}
				x.Pending = f.SHA256
			}
		}
		return nil
	})
	if err != nil {
		return sourceView{}, err
	}
	src, err = s.source(id)
	if err != nil {
		return sourceView{}, err
	}
	if ferr != nil {
		return s.viewSource(src), badRequest(ferr.Error())
	}
	return s.viewSource(src), nil
}

// communityApp finds an app of a source.
func (s *Server) communityApp(sourceID, appID string) (store.CatalogSource, *market.App, error) {
	src, err := s.source(sourceID)
	if err != nil {
		return src, nil, err
	}
	cat, _, err := s.loadCatalog(src)
	if err != nil {
		return src, nil, badRequest(err.Error())
	}
	a := cat.Find(appID)
	if a == nil {
		return src, nil, notFound("unknown app in this catalog")
	}
	return src, a, nil
}

// appPassword generates a password of n letters and digits.
func appPassword(n int) string {
	const alpha = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return string(b)
}

// communityEnv fills in the values of an app (defaults, generated
// passwords) and renders its .env.
func communityEnv(a *market.App, values map[string]string) (string, map[string]string, []string, error) {
	final := map[string]string{}
	for _, v := range a.Vars {
		val := strings.TrimSpace(values[v.Key])
		if val == "" {
			val = v.Default
		}
		if val == "" && v.Type == "password" {
			n := v.Length
			if n == 0 {
				n = 20
			}
			val = appPassword(n)
		}
		final[v.Key] = val
	}
	env, dirs, err := a.DockerApp().RenderEnv(final)
	return env, final, dirs, err
}

// hostCheck moves to HostPaths the folders that only look allowed: an
// existing path under /srv/strata that resolves (symlinks) elsewhere.
func hostCheck(g *market.Grants, stackDir string) {
	var keep []market.Mount
	for _, m := range g.Folders {
		if real, err := filepath.EvalSymlinks(m.Source); err == nil && !market.Inside(real, stackDir) {
			m.Source = real
			g.HostPaths = append(g.HostPaths, m)
			continue
		}
		keep = append(keep, m)
	}
	if keep == nil {
		keep = []market.Mount{}
	}
	g.Folders = keep
}

type communityCheck struct {
	Grants   *market.Grants   `json:"grants"`
	Required market.Approval  `json:"required"`
	Previous *market.Approval `json:"previous,omitempty"`
	Version  string           `json:"version"`
	From     string           `json:"from,omitempty"`
}

func analyzeCommunity(a *market.App, values map[string]string, stack string) (*communityCheck, string, []string, error) {
	env, final, dirs, err := communityEnv(a, values)
	if err != nil {
		return nil, "", nil, badRequest(err.Error())
	}
	stackDir := filepath.Join(docker.StacksDir, stack)
	g, err := market.Analyze(a.Compose, final, stackDir, true)
	if err != nil {
		return nil, "", nil, badRequest(err.Error())
	}
	hostCheck(g, stackDir)
	return &communityCheck{Grants: g, Required: g.Required(), Version: a.Version}, env, dirs, nil
}

// deployCommunity writes the stack, checks what compose itself reads in it
// against the approval, then starts it. On a failed check the previous
// files come back (update) or the stack goes (install).
func deployCommunity(ctx context.Context, j *jobs.Job, stack, compose, env string, dirs []string, approve market.Approval, update bool) error {
	stackDir := filepath.Join(docker.StacksDir, stack)
	prevYml, _ := os.ReadFile(filepath.Join(stackDir, "compose.yaml"))
	prevEnv, _ := os.ReadFile(filepath.Join(stackDir, ".env"))
	undo := func() {
		if update {
			_ = docker.SaveStack(ctx, stack, string(prevYml), string(prevEnv), "")
		} else {
			_ = docker.DeleteStack(ctx, stack, false)
		}
	}
	for _, d := range dirs {
		// Only data folders are created (and given to the usual app user);
		// an approved path elsewhere must already exist.
		d = filepath.Clean(d)
		if !market.Inside(d, stackDir) {
			continue
		}
		if _, err := os.Lstat(d); err == nil {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		_ = os.Chown(d, 1000, 1000)
	}
	if err := docker.SaveStack(ctx, stack, compose, env, ""); err != nil {
		if !update {
			_ = docker.DeleteStack(ctx, stack, false)
		}
		return err
	}
	out, err := docker.StackConfigJSON(ctx, stack)
	if err != nil {
		undo()
		return fmt.Errorf("compose config: %v", err)
	}
	g, err := market.Analyze(out, nil, stackDir, false)
	if err == nil {
		hostCheck(g, stackDir)
		err = approve.Covers(g.Required())
	}
	if err != nil {
		undo()
		return fmt.Errorf("refused after the compose check: %v", err)
	}
	if update {
		j.Logf("Updating %s...", stack)
		return docker.StackAction(ctx, stack, "update", j)
	}
	j.Logf("Deploying %s...", stack)
	return docker.StackAction(ctx, stack, "up", j)
}

type communityInstalled struct {
	Stack string `json:"stack"`
	communityMeta
	Status   string `json:"status"`
	Latest   string `json:"latest,omitempty"`
	Update   bool   `json:"update"`
	Port     string `json:"port,omitempty"`
	Orphaned bool   `json:"orphaned,omitempty"` // its source was removed or no longer lists it
}

func (s *Server) marketRoutes() {
	s.admin("GET /api/apps/sources", func(r *http.Request) (any, error) {
		out := []sourceView{}
		for _, src := range s.sources() {
			out = append(out, s.viewSource(src))
		}
		return out, nil
	})

	// Adds a source. Without a key nor a checksum, answers what would be
	// pinned ({confirm: …}) and saves nothing: the administrator accepts it
	// by posting again with that checksum.
	s.admin("POST /api/apps/sources", func(r *http.Request) (any, error) {
		var req struct {
			URL       string `json:"url"`
			PublicKey string `json:"publicKey"`
			SHA256    string `json:"sha256"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		loc, err := market.ParseLocation(req.URL)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		key := ""
		if strings.TrimSpace(req.PublicKey) != "" {
			pk, err := market.ParsePublicKey(req.PublicKey)
			if err != nil {
				return nil, badRequest(err.Error())
			}
			key = market.FormatPublicKey(pk)
		}
		for _, x := range s.sources() {
			if x.URL == loc.String() {
				return nil, badRequest("this catalog is already added")
			}
		}
		if len(s.sources()) >= maxSources {
			return nil, badRequest(fmt.Sprintf("%d catalogs at most", maxSources))
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		f, cat, invalid, err := fetchVerified(ctx, loc, key)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		if invalid == nil {
			invalid = []market.Invalid{}
		}
		want := strings.ToLower(strings.TrimSpace(req.SHA256))
		if key == "" && want == "" {
			return map[string]any{"confirm": map[string]any{"url": loc.String(), "kind": loc.Kind, "sha256": f.SHA256, "commit": f.Commit,
				"name": cat.Name, "maintainer": cat.Maintainer, "apps": len(cat.Apps), "invalid": invalid, "signed": len(f.Sig) > 0}}, nil
		}
		if key == "" && want != f.SHA256 {
			return nil, badRequest("the catalog's checksum is " + f.SHA256 + ", not the one given")
		}
		src := &store.CatalogSource{ID: randHex(4), URL: loc.String(), Kind: loc.Kind, Name: cat.Name, PublicKey: key, SHA256: f.SHA256,
			Commit: f.Commit, AddedBy: userOf(r), AddedAt: time.Now(), Refreshed: time.Now()}
		if err := os.MkdirAll(s.catalogDir(), 0o750); err != nil {
			return nil, err
		}
		marketState.mu.Lock()
		err = sys.WriteFileAtomic(s.catalogFile(src.ID, false), f.Index, 0o640)
		marketState.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err := s.store.Update(func(st *store.State) error {
			st.CatalogSources = append(st.CatalogSources, src)
			return nil
		}); err != nil {
			return nil, err
		}
		trust := "pinned at " + f.SHA256[:16]
		if key != "" {
			trust = "signed"
		}
		s.audit(r, "apps", "Added the community catalog "+cat.Name+" ("+src.URL+", "+trust+")")
		return s.viewSource(*src), nil
	})

	s.admin("POST /api/apps/sources/{id}/refresh", func(r *http.Request) (any, error) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		return s.refreshSource(ctx, r.PathValue("id"))
	})

	// Accepts the new index of a pinned source, by its checksum.
	s.admin("POST /api/apps/sources/{id}/accept", func(r *http.Request) (any, error) {
		var req struct {
			SHA256 string `json:"sha256"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		id := r.PathValue("id")
		src, err := s.source(id)
		if err != nil {
			return nil, err
		}
		if src.Pending == "" || !strings.EqualFold(strings.TrimSpace(req.SHA256), src.Pending) {
			return nil, badRequest("no pending change with this checksum: refresh the catalog")
		}
		marketState.mu.Lock()
		b, err := os.ReadFile(s.catalogFile(id, true))
		if err == nil {
			_, _, err = market.Parse(b)
		}
		if err == nil {
			err = sys.WriteFileAtomic(s.catalogFile(id, false), b, 0o640)
		}
		if err == nil {
			_ = os.Remove(s.catalogFile(id, true))
		}
		marketState.mu.Unlock()
		if err != nil {
			return nil, badRequest(err.Error())
		}
		cat, _, _ := market.Parse(b)
		_ = s.store.Update(func(st *store.State) error {
			for _, x := range st.CatalogSources {
				if x.ID == id {
					x.SHA256, x.Pending, x.Commit, x.Name = src.Pending, "", "", cat.Name
				}
			}
			return nil
		})
		s.audit(r, "apps", "Accepted the new version of the community catalog "+src.Name+" ("+src.Pending[:16]+")")
		src, err = s.source(id)
		if err != nil {
			return nil, err
		}
		return s.viewSource(src), nil
	})

	// Removes a source; installed apps stay (as stacks, without updates).
	s.admin("DELETE /api/apps/sources/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		src, err := s.source(id)
		if err != nil {
			return nil, err
		}
		if err := s.store.Update(func(st *store.State) error {
			keep := st.CatalogSources[:0]
			for _, x := range st.CatalogSources {
				if x.ID != id {
					keep = append(keep, x)
				}
			}
			st.CatalogSources = keep
			return nil
		}); err != nil {
			return nil, err
		}
		marketState.mu.Lock()
		_ = os.Remove(s.catalogFile(id, false))
		_ = os.Remove(s.catalogFile(id, true))
		delete(marketState.cache, id)
		marketState.mu.Unlock()
		s.audit(r, "apps", "Removed the community catalog "+src.Name)
		return nil, nil
	})

	// Apps of every source, and the community apps installed.
	s.admin("GET /api/apps/community", func(r *http.Request) (any, error) {
		type appView struct {
			market.App
			Source     string `json:"source"`
			SourceName string `json:"sourceName"`
		}
		apps := []appView{}
		latest := map[string]string{} // source/app → version
		views := []sourceView{}
		for _, src := range s.sources() {
			views = append(views, s.viewSource(src))
			cat, _, err := s.loadCatalog(src)
			if err != nil {
				continue
			}
			for _, a := range cat.Apps {
				apps = append(apps, appView{App: a, Source: src.ID, SourceName: src.Name})
				latest[src.ID+"/"+a.ID] = a.Version
			}
		}
		sort.SliceStable(apps, func(i, j int) bool { return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name) })
		installed := []communityInstalled{}
		status := map[string]string{}
		if s.docker != nil {
			stacks, _ := docker.ListStacks(r.Context(), s.docker)
			for _, st := range stacks {
				status[st.Name] = st.Status
			}
		}
		for _, name := range docker.StackNames() {
			m := readCommunity(name)
			if m == nil {
				continue
			}
			ci := communityInstalled{Stack: name, communityMeta: *m, Status: status[name]}
			if v, ok := latest[m.Source+"/"+m.App]; ok {
				ci.Latest, ci.Update = v, v != m.Version
			} else {
				ci.Orphaned = true
			}
			if m.WebPort != "" {
				if p := docker.EnvValue(name, m.WebPort); p != "" {
					if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
						ci.Port = p
					}
				}
			}
			installed = append(installed, ci)
		}
		return map[string]any{"sources": views, "apps": apps, "installed": installed}, nil
	})

	// What an install would grant, with these values.
	s.admin("POST /api/apps/community/{source}/{app}/check", func(r *http.Request) (any, error) {
		_, a, err := s.communityApp(r.PathValue("source"), r.PathValue("app"))
		if err != nil {
			return nil, err
		}
		var req struct {
			Name   string            `json:"name"`
			Values map[string]string `json:"values"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Name == "" {
			req.Name = a.ID
		}
		if err := docker.ValidStack(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		c, _, _, err := analyzeCommunity(a, req.Values, req.Name)
		return c, err
	})

	s.admin("POST /api/apps/community/{source}/{app}/install", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		src, a, err := s.communityApp(r.PathValue("source"), r.PathValue("app"))
		if err != nil {
			return nil, err
		}
		var req struct {
			Name    string            `json:"name"`
			Values  map[string]string `json:"values"`
			Approve market.Approval   `json:"approve"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Name == "" {
			req.Name = a.ID
		}
		if err := docker.ValidStack(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if _, err := os.Stat(filepath.Join(docker.StacksDir, req.Name)); err == nil {
			return nil, badRequest("an app or stack named " + req.Name + " already exists")
		}
		c, env, dirs, err := analyzeCommunity(a, req.Values, req.Name)
		if err != nil {
			return nil, err
		}
		if err := req.Approve.Covers(c.Required); err != nil {
			return nil, badRequest(err.Error())
		}
		app := *a
		meta := communityMeta{Source: src.ID, SourceName: src.Name, App: a.ID, Name: a.Name, Version: a.Version, Icon: a.Icon,
			WebPort: a.WebPort, Approved: c.Required, Installed: time.Now()}
		risky := ""
		if !c.Required.Empty() {
			risky = " with approved risks: " + approvalText(c.Required)
		}
		s.audit(r, "apps", "Installing the community app "+a.Name+" "+a.Version+" from "+src.Name+risky)
		return s.jobs.Start("app", "Install "+a.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			if err := deployCommunity(ctx, j, req.Name, app.Compose, env, dirs, c.Required, false); err != nil {
				return err
			}
			return writeCommunity(req.Name, meta)
		}), nil
	})

	// What updating an installed community app to the catalog's version would grant.
	s.admin("POST /api/apps/community/stacks/{stack}/check", func(r *http.Request) (any, error) {
		stack := r.PathValue("stack")
		m, a, values, err := s.communityUpdate(stack)
		if err != nil {
			return nil, err
		}
		c, _, _, err := analyzeCommunity(a, values, stack)
		if err != nil {
			return nil, err
		}
		c.From, c.Previous = m.Version, &m.Approved
		return c, nil
	})

	s.admin("POST /api/apps/community/stacks/{stack}/update", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		stack := r.PathValue("stack")
		var req struct {
			Approve market.Approval `json:"approve"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		m, a, values, err := s.communityUpdate(stack)
		if err != nil {
			return nil, err
		}
		c, env, dirs, err := analyzeCommunity(a, values, stack)
		if err != nil {
			return nil, err
		}
		if err := req.Approve.Covers(c.Required); err != nil {
			return nil, badRequest(err.Error())
		}
		app := *a
		meta := *m
		meta.Version, meta.Name, meta.Icon, meta.WebPort, meta.Approved = a.Version, a.Name, a.Icon, a.WebPort, c.Required
		s.audit(r, "apps", "Updating the community app "+a.Name+" ("+stack+") from "+m.Version+" to "+a.Version)
		return s.jobs.Start("app", "Update "+a.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			if err := deployCommunity(ctx, j, stack, app.Compose, env, dirs, c.Required, true); err != nil {
				return err
			}
			return writeCommunity(stack, meta)
		}), nil
	})
}

// communityUpdate returns an installed community app, the catalog's
// version of it and its current values (new variables: their defaults).
func (s *Server) communityUpdate(stack string) (*communityMeta, *market.App, map[string]string, error) {
	m := readCommunity(stack)
	if m == nil {
		return nil, nil, nil, notFound("not a community app")
	}
	_, a, err := s.communityApp(m.Source, m.App)
	if err != nil {
		return nil, nil, nil, err
	}
	values := map[string]string{}
	for _, v := range a.Vars {
		values[v.Key] = docker.EnvValue(stack, v.Key)
	}
	return m, a, values, nil
}

func approvalText(a market.Approval) string {
	var p []string
	if a.Privileged {
		p = append(p, "privileged")
	}
	if a.HostNetwork {
		p = append(p, "host network")
	}
	if a.HostNamespaces {
		p = append(p, "host namespaces")
	}
	p = append(p, a.HostPaths...)
	p = append(p, a.Devices...)
	p = append(p, a.Capabilities...)
	p = append(p, a.SecurityOpts...)
	return strings.Join(p, ", ")
}
