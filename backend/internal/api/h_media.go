package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"strataos/internal/cast"
	"strataos/internal/docker"
	"strataos/internal/guard"
	"strataos/internal/jobs"
	"strataos/internal/media"
	"strataos/internal/store"
)

// streamTTL bounds the life of a stream URL: receivers read the file in
// ranges all along the playback of a film. Stopping the cast revokes it.
const streamTTL = 6 * time.Hour

// ---- Keys and helpers ----

var (
	mediaKeyMu sync.Mutex
	mediaKey   []byte
)

func (s *Server) mediaSecret() []byte {
	mediaKeyMu.Lock()
	defer mediaKeyMu.Unlock()
	if mediaKey != nil {
		return mediaKey
	}
	p := filepath.Join(s.dataDir, "media.key")
	if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
		mediaKey = b
		return b
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	_ = os.WriteFile(p, b, 0o600)
	mediaKey = b
	return b
}

// sharePaths maps the names of shared folders to their paths.
func (s *Server) sharePaths() map[string]string {
	m := map[string]string{}
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			m[sh.Name] = sh.Path
		}
	})
	return m
}

// localAddrFor is the address of this machine on the way to a receiver.
func localAddrFor(ip string) (string, error) {
	c, err := net.Dial("udp", net.JoinHostPort(ip, "9"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	h, _, _ := net.SplitHostPort(c.LocalAddr().String())
	return h, nil
}

func lanInterfaces() []string {
	out := []string{}
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
			continue
		}
		if strings.HasPrefix(i.Name, "veth") || strings.HasPrefix(i.Name, "docker") || strings.HasPrefix(i.Name, "br-") || strings.HasPrefix(i.Name, "virbr") {
			continue
		}
		out = append(out, i.Name)
	}
	return out
}

// ---- DLNA server ----

func (s *Server) dlnaConfig(c store.DLNAServer) media.DLNAConfig {
	paths := s.sharePaths()
	mc := media.DLNAConfig{Name: c.Name, Interface: c.Interface}
	if mc.Name == "" {
		mc.Name, _ = hostname()
	}
	for _, f := range c.Folders {
		if p := paths[f.Share]; p != "" {
			mc.Folders = append(mc.Folders, media.DLNAFolder{Path: p, Type: f.Type})
		}
	}
	return mc
}

// mediaChange tells which media settings a change of shared folder touched.
type mediaChange struct {
	dlna, music bool
	deleted     string // name of the deleted share
}

// mediaFollowShares applies the media settings again after a shared folder
// was renamed, moved or deleted (the store already follows the new name).
func (s *Server) mediaFollowShares(c mediaChange, user string) {
	if c.deleted != "" && c.dlna {
		s.store.Log("warning", "media", user, fmt.Sprintf("Shared folder %s was deleted: the DLNA server no longer publishes it", c.deleted))
	}
	if c.deleted != "" && c.music {
		s.store.Log("warning", "media", user, fmt.Sprintf("Shared folder %s was deleted: it is no longer in the music library", c.deleted))
	}
	var m store.Media
	s.store.Read(func(st *store.State) {
		m = st.Media
		m.Music.Shares = slices.Clone(st.Media.Music.Shares)
	})
	if c.dlna && m.DLNA.Enabled {
		if err := media.ApplyDLNA(true, s.dlnaConfig(m.DLNA)); err != nil {
			s.store.Log("error", "media", user, "Applying the DLNA server settings failed: "+err.Error())
		}
	}
	if stack := owntoneStack(); c.music && stack != "" {
		s.jobs.Start("media", "Apply the music folders", user, func(ctx context.Context, j *jobs.Job) error {
			return s.applyMusic(ctx, j, stack, m.Music.Shares)
		})
	}
}

// ---- Music (OwnTone) ----

// owntoneStack is the stack running the OwnTone app ("" when not installed).
func owntoneStack() string {
	for _, n := range docker.StackNames() {
		b, err := os.ReadFile(filepath.Join(docker.StacksDir, n, ".strata-app"))
		if err == nil && strings.TrimSpace(string(b)) == media.OwnToneApp {
			return n
		}
	}
	return ""
}

func (s *Server) musicAllowed(r *http.Request) bool {
	if isAdmin(r) {
		return true
	}
	var ok bool
	s.store.Read(func(st *store.State) { ok = st.Media.Music.Users })
	return ok
}

// applyMusic writes the music folders in the OwnTone configuration and
// restarts it (it rescans the library when it starts).
func (s *Server) applyMusic(ctx context.Context, j *jobs.Job, stack string, shares []string) error {
	dir := docker.EnvValue(stack, "CONFIG_DIR")
	if !strings.HasPrefix(dir, "/") {
		return errors.New("unknown configuration folder of OwnTone")
	}
	confPath := filepath.Join(filepath.Clean(dir), "owntone.conf")
	paths := s.sharePaths()
	var dirs []string
	for _, n := range shares {
		if p := paths[n]; p != "" {
			dirs = append(dirs, p)
		}
	}
	// The container writes its configuration file on its first start.
	var conf []byte
	for i := 0; ; i++ {
		b, err := os.ReadFile(confPath)
		if err == nil {
			conf = b
			break
		}
		if i == 0 {
			j.Logf("Waiting for OwnTone to create its configuration...")
			_ = docker.StackAction(ctx, stack, "up", j)
		}
		if i >= 60 {
			return errors.New("OwnTone did not create its configuration file: check that the app runs")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	patched, err := media.PatchOwnToneConf(string(conf), dirs)
	if err != nil {
		return err
	}
	if patched != string(conf) {
		if err := os.WriteFile(confPath, []byte(patched), 0o644); err != nil {
			return err
		}
	}
	j.Logf("Music folders: %s", strings.Join(dirs, ", "))
	j.Logf("Restarting OwnTone...")
	return docker.StackAction(ctx, stack, "restart", j)
}

var owntoneProxy = &httputil.ReverseProxy{
	Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme, pr.Out.URL.Host = "http", media.OwnToneAddr
		pr.Out.Host = media.OwnToneAddr
		pr.Out.Header.Del("Cookie")
		pr.Out.Header.Del("Authorization")
		pr.Out.Header.Del("Origin")
		pr.Out.Header.Del("Referer")
	},
	ModifyResponse: func(resp *http.Response) error {
		resp.Header.Del("Set-Cookie")
		resp.Header.Del("Access-Control-Allow-Origin")
		resp.Header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
		resp.Header.Set("X-Content-Type-Options", "nosniff")
		return nil
	},
	ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		writeErr(w, &Error{Status: http.StatusBadGateway, Msg: "the music server does not answer (is the OwnTone app running?)"})
	},
}

// ---- Casting ----

type castReceiver struct {
	cast.Receiver
	url string // the stream URL it was given (holds the signed token)

	mu     sync.Mutex
	player cast.Player
	// resume: restored after a restart of the daemon, the player connects
	// again on first use.
	resume bool
	pos    float64 // last position read (seconds)
	Error  string
}

// use returns the player of the receiver, connecting again to a receiver
// restored after a restart; nil when it cannot be driven.
func (rc *castReceiver) use(ctx context.Context) cast.Player {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.player == nil && rc.resume {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		p, err := cast.Resume(c, rc.Receiver)
		if err != nil {
			rc.Error = "receiver unreachable: " + err.Error()
			return nil
		}
		rc.player, rc.resume, rc.Error = p, false, ""
	}
	return rc.player
}

func (rc *castReceiver) errorText() string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.Error
}

type castSession struct {
	ID        string    `json:"id"`
	User      string    `json:"user"`
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Type      string    `json:"type"`
	Size      int64     `json:"size"`
	Started   time.Time `json:"started"`
	receivers []*castReceiver
	ready     bool // receivers loaded (guarded by castState)
	restoring bool // saved before a restart, being checked (guarded by castState)
}

var castState = struct {
	sync.Mutex
	sessions map[string]*castSession
	found    []cast.Receiver
	foundAt  time.Time
}{sessions: map[string]*castSession{}}

func (s *Server) castSessionActive(id string) bool {
	castState.Lock()
	defer castState.Unlock()
	cs := castState.sessions[id]
	return cs != nil && time.Since(cs.Started) < streamTTL
}

// receivers returns the discovered receivers, from a cache of one minute.
func discoverReceivers(ctx context.Context, refresh bool) []cast.Receiver {
	castState.Lock()
	if !refresh && time.Since(castState.foundAt) < time.Minute {
		out := slices.Clone(castState.found)
		castState.Unlock()
		return out
	}
	castState.Unlock()
	found := cast.Discover(ctx, 3*time.Second)
	// Receivers must be on the local network: the media URL holds a key.
	out := []cast.Receiver{}
	for _, r := range found {
		h := r.Addr
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		if a, err := netip.ParseAddr(h); err == nil && guard.Private(a) {
			out = append(out, r)
		}
	}
	castState.Lock()
	castState.found, castState.foundAt = out, time.Now()
	castState.Unlock()
	return slices.Clone(out)
}

func (s *Server) stopCast(cs *castSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, r := range cs.receivers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p := r.use(ctx); p != nil {
				_ = p.Stop(ctx)
				_ = p.Close()
			}
		}()
	}
	wg.Wait()
}

// castJanitor ends the sessions past the life of their URLs.
// It also saves the sessions (with the positions last read) for a restart.
func (s *Server) castJanitor() {
	for range time.Tick(time.Minute) {
		var old []*castSession
		castState.Lock()
		n := len(castState.sessions)
		for id, cs := range castState.sessions {
			if time.Since(cs.Started) > streamTTL {
				old = append(old, cs)
				delete(castState.sessions, id)
			}
		}
		castState.Unlock()
		if n > 0 {
			s.saveCasts()
		}
		for _, cs := range old {
			s.stopCast(cs)
		}
	}
}

// alignCast brings the receivers of a multi-room session to the position of
// the one furthest ahead (best effort: devices buffer at different speeds).
func alignCast(cs *castSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type pos struct {
		r cast.Player
		p float64
	}
	var mu sync.Mutex
	var ps []pos
	var wg sync.WaitGroup
	for _, r := range cs.receivers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := r.use(ctx)
			if p == nil {
				return
			}
			st := p.Status(ctx)
			if st.State == "PLAYING" {
				mu.Lock()
				ps = append(ps, pos{p, st.Position})
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(ps) < 2 {
		return
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].p > ps[j].p })
	lead := ps[0].p
	for _, x := range ps[1:] {
		if lead-x.p > 0.5 {
			_ = x.r.Seek(ctx, lead+0.5) // a seek takes a moment to apply
		}
	}
}

func (s *Server) castView(ctx context.Context, cs *castSession) map[string]any {
	type rv struct {
		ID     string      `json:"id"`
		Name   string      `json:"name"`
		Kind   string      `json:"kind"`
		Error  string      `json:"error,omitempty"`
		Status cast.Status `json:"status"`
	}
	out := make([]rv, len(cs.receivers))
	var wg sync.WaitGroup
	for i, r := range cs.receivers {
		out[i] = rv{ID: r.ID, Name: r.Name, Kind: r.Kind, Status: cast.Status{State: "ERROR", Volume: -1}}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			if p := r.use(c); p != nil {
				out[i].Status = p.Status(c)
				r.mu.Lock()
				r.pos = out[i].Status.Position
				r.mu.Unlock()
			}
			out[i].Error = r.errorText()
		}()
	}
	wg.Wait()
	return map[string]any{"id": cs.ID, "user": cs.User, "path": cs.Path, "name": cs.Name, "kind": cs.Kind, "started": cs.Started, "receivers": out}
}

func (s *Server) findCast(r *http.Request) (*castSession, error) {
	castState.Lock()
	cs := castState.sessions[r.PathValue("id")]
	if cs != nil && !cs.ready {
		cs = nil
	}
	castState.Unlock()
	if cs == nil || (cs.User != userOf(r) && !isAdmin(r)) {
		return nil, notFound("cast session not found")
	}
	return cs, nil
}

// serveStream sends a file to a receiver holding a signed URL.
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request) {
	if a, ok := guard.ParseIP(clientIP(r)); !ok || !guard.Private(a) {
		writeErr(w, &Error{Status: http.StatusForbidden, Msg: "media links work on the local network only"})
		return
	}
	g, err := media.VerifyStream(s.mediaSecret(), r.PathValue("token"), time.Now())
	if err != nil || !s.castSessionActive(g.Session) {
		writeErr(w, errLinkGone)
		return
	}
	// The rights are checked again: the account may have lost access since.
	var admin, ok bool
	s.store.Read(func(st *store.State) {
		u := st.User(g.User)
		ok = u != nil && !u.Disabled
		admin = ok && u.Role == "admin"
	})
	if !ok {
		writeErr(w, errLinkGone)
		return
	}
	t, err := s.locateFor(g.User, admin, g.Path)
	if err != nil || t.acc.ancestor {
		writeErr(w, errLinkGone)
		return
	}
	ctype, kind := cast.MediaType(t.abs)
	if ctype == "" {
		writeErr(w, notFound("file not found"))
		return
	}
	rt, err := t.open()
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rt.Close()
	f, err := rt.Open(t.rel)
	if err != nil {
		writeErr(w, notFound("file not found"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		writeErr(w, notFound("file not found"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'")
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(t.abs)}))
	h.Set("Access-Control-Allow-Origin", "*") // the Chromecast receiver page reads it from another origin
	h.Set("Cache-Control", "no-store")
	// Set without canonicalization: some TVs only read these spellings.
	h["contentFeatures.dlna.org"] = []string{cast.ContentFeatures(kind)}
	mode := "Streaming"
	if kind == "image" {
		mode = "Interactive"
	}
	h["transferMode.dlna.org"] = []string{mode}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) mediaRoutes() {
	s.restoreCasts()
	go s.castJanitor()

	// ---- DLNA server ----
	s.admin("GET /api/media/dlna", func(r *http.Request) (any, error) {
		var c store.DLNAServer
		s.store.Read(func(st *store.State) { c = st.Media.DLNA })
		if c.Folders == nil {
			c.Folders = []store.DLNAFolder{}
		}
		host, _ := hostname()
		return map[string]any{"config": c, "status": media.GetDLNAStatus(r.Context()), "interfaces": lanInterfaces(), "hostname": host, "port": media.DLNAPort}, nil
	})
	s.admin("PUT /api/media/dlna", func(r *http.Request) (any, error) {
		var req store.DLNAServer
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name != "" {
			if err := media.ValidName(req.Name); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		if err := media.ValidInterface(req.Interface); err != nil {
			return nil, badRequest(err.Error())
		}
		if req.Interface != "" {
			if _, err := net.InterfaceByName(req.Interface); err != nil {
				return nil, badRequest("unknown network interface " + req.Interface)
			}
		}
		paths := s.sharePaths()
		seen := map[string]bool{}
		for _, f := range req.Folders {
			if paths[f.Share] == "" {
				return nil, badRequest("unknown shared folder " + f.Share)
			}
			if seen[f.Share] {
				return nil, badRequest("shared folder chosen twice: " + f.Share)
			}
			seen[f.Share] = true
			if err := media.ValidFolderType(f.Type); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		if req.Enabled && !media.DLNAInstalled() {
			return nil, badRequest("install the DLNA server first (Settings → Components)")
		}
		if err := s.store.Update(func(st *store.State) error { st.Media.DLNA = req; return nil }); err != nil {
			return nil, err
		}
		if req.Enabled {
			s.audit(r, "media", "DLNA server enabled")
		} else {
			s.audit(r, "media", "DLNA server disabled")
		}
		if err := media.ApplyDLNA(req.Enabled, s.dlnaConfig(req)); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return nil, nil
	})
	s.admin("POST /api/media/dlna/rescan", func(r *http.Request) (any, error) {
		var on bool
		s.store.Read(func(st *store.State) { on = st.Media.DLNA.Enabled })
		if !on {
			return nil, badRequest("the DLNA server is not enabled")
		}
		s.audit(r, "media", "DLNA library rescan")
		if err := media.RescanDLNA(); err != nil {
			return nil, err
		}
		return nil, nil
	})

	// ---- Music & speakers ----
	s.user("GET /api/media/music", func(r *http.Request) (any, error) {
		var c store.MusicServer
		s.store.Read(func(st *store.State) { c = st.Media.Music })
		if c.Shares == nil {
			c.Shares = []string{}
		}
		stack := owntoneStack()
		return map[string]any{"installed": stack != "", "stack": stack, "allowed": s.musicAllowed(r), "config": c}, nil
	})
	s.admin("PUT /api/media/music", func(r *http.Request) (any, error) {
		var req store.MusicServer
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		paths := s.sharePaths()
		for _, n := range req.Shares {
			if paths[n] == "" {
				return nil, badRequest("unknown shared folder " + n)
			}
			if _, err := media.PatchOwnToneConf("general {\n}\nlibrary {\n}", []string{paths[n]}); err != nil {
				return nil, badRequest(fmt.Sprintf("the path of %s cannot be used by OwnTone", n))
			}
		}
		if err := s.store.Update(func(st *store.State) error { st.Media.Music = req; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "media", "Music settings saved")
		stack := owntoneStack()
		if stack == "" {
			return nil, nil
		}
		return s.jobs.Start("media", "Apply the music folders", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return s.applyMusic(ctx, j, stack, req.Shares)
		}), nil
	})
	s.userRaw("/api/media/music/proxy/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if !s.musicAllowed(r) {
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "music control is reserved to administrators"})
			return
		}
		p := r.PathValue("path")
		if !media.OwnToneAllowed(r.Method, p, isAdmin(r)) {
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "this music server function is reserved to administrators"})
			return
		}
		if owntoneStack() == "" {
			writeErr(w, notFound("the OwnTone app is not installed"))
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL = &url.URL{Path: "/" + p, RawQuery: r.URL.RawQuery}
		r2.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		owntoneProxy.ServeHTTP(w, r2)
	})

	// ---- Casting ----
	s.user("GET /api/media/cast/receivers", func(r *http.Request) (any, error) {
		return map[string]any{"receivers": discoverReceivers(r.Context(), r.URL.Query().Get("refresh") == "1")}, nil
	})
	s.user("POST /api/media/cast", func(r *http.Request) (any, error) {
		var req struct {
			Path      string   `json:"path"`
			Receivers []string `json:"receivers"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Receivers) == 0 || len(req.Receivers) > 16 {
			return nil, badRequest("choose 1 to 16 receivers")
		}
		t, err := s.locate(r, req.Path)
		if err != nil {
			return nil, err
		}
		if t.acc.ancestor {
			return nil, errForbidden
		}
		ctype, kind := cast.MediaType(t.abs)
		if ctype == "" {
			return nil, badRequest("this type of file cannot be cast (videos, music and pictures only)")
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		st, err := rt.Stat(t.rel)
		rt.Close()
		if err != nil || !st.Mode().IsRegular() {
			return nil, notFound("file not found")
		}
		known := map[string]cast.Receiver{}
		for _, x := range discoverReceivers(r.Context(), false) {
			known[x.ID] = x
		}
		var missing bool
		for _, id := range req.Receivers {
			if _, ok := known[id]; !ok {
				missing = true
			}
		}
		if missing {
			for _, x := range discoverReceivers(r.Context(), true) {
				known[x.ID] = x
			}
		}
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		cs := &castSession{ID: hex.EncodeToString(b), User: userOf(r), Path: t.abs, Name: filepath.Base(t.abs), Kind: kind, Type: ctype, Size: st.Size(), Started: time.Now()}
		tok := media.SignStream(s.mediaSecret(), cs.ID, cs.User, t.abs, cs.Started.Add(streamTTL))
		for _, id := range slices.Compact(slices.Sorted(slices.Values(req.Receivers))) {
			rc, ok := known[id]
			if !ok {
				return nil, badRequest("receiver not found: " + id + " (search again)")
			}
			if !rc.Video && kind != "audio" {
				return nil, badRequest(rc.Name + " plays audio only")
			}
			cs.receivers = append(cs.receivers, &castReceiver{Receiver: rc})
		}
		castState.Lock()
		castState.sessions[cs.ID] = cs
		castState.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		for _, rc := range cs.receivers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				host := rc.Addr
				if h, _, err := net.SplitHostPort(host); err == nil {
					host = h
				}
				local, err := localAddrFor(host)
				if err != nil {
					rc.Error = err.Error()
					return
				}
				u := "http://" + net.JoinHostPort(local, s.httpPort) + "/api/media/stream/" + tok + "/" + url.PathEscape(cs.Name)
				rc.url = u
				p, err := cast.Open(ctx, rc.Receiver)
				if err != nil {
					rc.Error = err.Error()
					return
				}
				if err := p.Load(ctx, cast.Media{URL: u, Type: ctype, Kind: kind, Title: strings.TrimSuffix(cs.Name, path.Ext(cs.Name)), Size: st.Size()}); err != nil {
					rc.Error = err.Error()
					_ = p.Close()
					return
				}
				rc.player = p
			}()
		}
		wg.Wait()
		ok := 0
		for _, rc := range cs.receivers {
			if rc.player != nil {
				ok++
			}
		}
		if ok == 0 {
			castState.Lock()
			delete(castState.sessions, cs.ID)
			castState.Unlock()
			return nil, &Error{Status: http.StatusBadGateway, Msg: "no receiver could play the file: " + cs.receivers[0].Error}
		}
		castState.Lock()
		cs.ready = true
		castState.Unlock()
		s.saveCasts()
		s.audit(r, "media", fmt.Sprintf("Cast %s to %d receivers", cs.Name, ok))
		if ok > 1 && kind != "image" {
			go func() {
				time.Sleep(5 * time.Second)
				alignCast(cs)
			}()
		}
		return s.castView(r.Context(), cs), nil
	})
	s.user("GET /api/media/cast/sessions", func(r *http.Request) (any, error) {
		var mine []*castSession
		castState.Lock()
		for _, cs := range castState.sessions {
			if cs.ready && (cs.User == userOf(r) || isAdmin(r)) {
				mine = append(mine, cs)
			}
		}
		castState.Unlock()
		sort.Slice(mine, func(i, j int) bool { return mine[i].Started.After(mine[j].Started) })
		out := []map[string]any{}
		for _, cs := range mine {
			out = append(out, s.castView(r.Context(), cs))
		}
		return map[string]any{"sessions": out}, nil
	})
	s.user("POST /api/media/cast/sessions/{id}/control", func(r *http.Request) (any, error) {
		cs, err := s.findCast(r)
		if err != nil {
			return nil, err
		}
		var req struct {
			Action   string  `json:"action"`   // play | pause | stop | seek | volume | sync
			Receiver string  `json:"receiver"` // "" = all
			Value    float64 `json:"value"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Action == "sync" {
			alignCast(cs)
			return nil, nil
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		type target struct {
			Name   string
			player cast.Player
		}
		var targets []target
		var unreachable string
		for _, rc := range cs.receivers {
			if req.Receiver != "" && rc.ID != req.Receiver {
				continue
			}
			if p := rc.use(ctx); p != nil {
				targets = append(targets, target{rc.Name, p})
			} else if rc.resume {
				unreachable = rc.Name + ": " + rc.errorText()
			}
		}
		if len(targets) == 0 {
			if unreachable != "" {
				return nil, &Error{Status: http.StatusBadGateway, Msg: unreachable}
			}
			return nil, notFound("receiver not found")
		}
		errs := make([]error, len(targets))
		var wg sync.WaitGroup
		for i, rc := range targets {
			wg.Add(1)
			go func() {
				defer wg.Done()
				switch req.Action {
				case "play":
					errs[i] = rc.player.Play(ctx)
				case "pause":
					errs[i] = rc.player.Pause(ctx)
				case "seek":
					errs[i] = rc.player.Seek(ctx, max(0, req.Value))
				case "stop":
					errs[i] = rc.player.Stop(ctx)
				case "volume":
					errs[i] = rc.player.SetVolume(ctx, int(req.Value))
				default:
					errs[i] = errors.New("unknown action")
				}
			}()
		}
		wg.Wait()
		for i, e := range errs {
			if e != nil {
				return nil, &Error{Status: http.StatusBadGateway, Msg: targets[i].Name + ": " + e.Error()}
			}
		}
		return nil, nil
	})
	s.user("DELETE /api/media/cast/sessions/{id}", func(r *http.Request) (any, error) {
		cs, err := s.findCast(r)
		if err != nil {
			return nil, err
		}
		castState.Lock()
		delete(castState.sessions, cs.ID)
		castState.Unlock()
		s.saveCasts()
		s.stopCast(cs)
		return nil, nil
	})
	// Receivers fetch without a session: the signed token is the credential.
	s.mux.HandleFunc("GET /api/media/stream/{token}/{name}", s.serveStream)
}
