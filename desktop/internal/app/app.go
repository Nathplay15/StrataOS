// Package app ties the settings, the NAS client and the sync engine
// together, for the tray app and the headless mode alike.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"strataos-sync/internal/api"
	"strataos-sync/internal/config"
	"strataos-sync/internal/engine"
	"strataos-sync/internal/i18n"
	"strataos-sync/internal/platform"
	"strataos-sync/internal/ratelimit"
	"strataos-sync/internal/trash"
)

// App is the running app.
type App struct {
	Store   *config.Store
	Version string
	// Notify shows a desktop notification (nil: only logged).
	Notify func(title, body string)

	mu        sync.Mutex
	cfg       config.Config
	client    *api.Client
	eng       *engine.Engine
	engCancel context.CancelFunc
	engDone   chan struct{}
	up, down  ratelimit.Limiter
	ctx       context.Context
	lastErr   map[string]time.Time
	changed   []func()
}

// New loads the configuration of the app folder.
func New(store *config.Store, version string) (*App, error) {
	c, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	if c.DeviceName == "" {
		h, _ := os.Hostname()
		c.DeviceName = h
	}
	return &App{Store: store, Version: version, cfg: c, lastErr: map[string]time.Time{}}, nil
}

// OnChange registers a function called when the state changes (tray).
func (a *App) OnChange(f func()) {
	a.mu.Lock()
	a.changed = append(a.changed, f)
	a.mu.Unlock()
}

func (a *App) fire() {
	a.mu.Lock()
	fs := append([]func(){}, a.changed...)
	a.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

// Lang returns the language of the texts.
func (a *App) Lang() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return i18n.Pick(a.cfg.Language)
}

// T translates a text of the catalog.
func (a *App) T(id string, args ...any) string { return i18n.T(a.Lang(), id, args...) }

// Config returns the settings (without the token).
func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.cfg
	c.Token = ""
	return c
}

// Start runs the engine when paired, until ctx ends.
func (a *App) Start(ctx context.Context) error {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	return a.restart()
}

// Stop stops the engine.
func (a *App) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLocked()
}

func (a *App) stopLocked() {
	if a.engCancel != nil {
		a.engCancel()
		<-a.engDone
		a.engCancel = nil
	}
	if a.eng != nil {
		a.eng.Close()
		a.eng = nil
	}
}

// newEngine builds the engine for the current settings (caller holds mu).
func (a *App) newEngineLocked() (*engine.Engine, error) {
	c := a.cfg
	a.client = api.New(c.Server, c.Token, c.Fingerprint)
	a.client.Agent = "StrataOS-Sync/" + a.Version + " (" + runtime.GOOS + ")"
	a.client.Up, a.client.Down = &a.up, &a.down
	a.up.SetRate(c.UpKBps * 1024)
	a.down.SetRate(c.DownKBps * 1024)
	ignore := c.Ignore
	if ignore == nil {
		ignore = engine.DefaultIgnore
	}
	tr := trash.Trasher{Fallback: a.Store.TrashDir()}
	return engine.New(engine.APIRemote{C: a.client}, engine.Options{
		StateFile: a.Store.StateFile(), Device: c.DeviceName, Ignore: ignore, Trash: tr.Trash, OnEvent: a.onEvent,
	})
}

// restart rebuilds the engine (after pairing or a change of settings).
func (a *App) restart() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLocked()
	if !a.cfg.Paired() || a.ctx == nil {
		a.client = nil
		if a.cfg.Paired() {
			a.client = api.New(a.cfg.Server, a.cfg.Token, a.cfg.Fingerprint)
		}
		return nil
	}
	eng, err := a.newEngineLocked()
	if err != nil {
		return err
	}
	if err := eng.SetPairs(a.cfg.Pairs); err != nil {
		eng.Close()
		return err
	}
	eng.Pause(a.cfg.Paused)
	a.eng = eng
	ctx, cancel := context.WithCancel(a.ctx)
	a.engCancel, a.engDone = cancel, make(chan struct{})
	go func() {
		defer close(a.engDone)
		eng.Run(ctx)
	}()
	cl := a.client
	go func() {
		// Tell the NAS which app and system this is (device list).
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_ = cl.UpdateDevice(c, a.Version, platform.OSName())
	}()
	go a.fire()
	return nil
}

// Engine returns the running engine (nil when not paired).
func (a *App) Engine() *engine.Engine {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.eng
}

// Client returns the NAS client (nil when not paired).
func (a *App) Client() *api.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.client
}

// Status returns the state of the engine.
func (a *App) Status() engine.Status {
	a.mu.Lock()
	eng, paused := a.eng, a.cfg.Paused
	a.mu.Unlock()
	if eng == nil {
		return engine.Status{Paused: paused}
	}
	return eng.Status()
}

func (a *App) onEvent(ev engine.Event) {
	log.Printf("event %s %s %s: %s", ev.Kind, ev.Pair, ev.Path, ev.Msg)
	var title, body string
	switch ev.Kind {
	case "conflict":
		title, body = a.T("notify.conflict.title"), a.T("notify.conflict.body", ev.Path, filepath.Base(ev.Msg))
	case "brake":
		title, body = a.T("notify.brake.title"), a.T("notify.brake.body", ev.Msg)
	case "signedout":
		title, body = a.T("notify.signedout.title"), a.T("notify.signedout.body")
	case "error":
		a.mu.Lock()
		last := a.lastErr[ev.Pair]
		quiet := time.Since(last) < 30*time.Minute
		if !quiet {
			a.lastErr[ev.Pair] = time.Now()
		}
		a.mu.Unlock()
		if quiet {
			return // one error notification per pair every half hour
		}
		title, body = a.T("notify.error.title"), ev.Msg
	default:
		return
	}
	a.mu.Lock()
	off := a.cfg.NoNotify
	a.mu.Unlock()
	if a.Notify != nil && !off {
		a.Notify(title, body)
	}
	go a.fire()
}

// ---- Pairing ----

// PairInput is what the user gives to pair this computer.
type PairInput struct {
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"` // the one accepted (shown at the probe, or from the link)
	Link        string `json:"link"`        // strataos://pair?… pasted
	Code        string `json:"code"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	OTP         string `json:"otp"`
	DeviceName  string `json:"deviceName"`
}

// ParseLink reads a pairing link (strataos://pair?url=…&fp=…&code=…).
func ParseLink(link string) (base, fp, code, user string, err error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "strataos" {
		return "", "", "", "", errors.New("not a pairing link")
	}
	q := u.Query()
	return q.Get("url"), api.NormalizeFingerprint(q.Get("fp")), q.Get("code"), q.Get("user"), nil
}

// NormalizeURL accepts "nas.local", "192.168.1.10:7443" or a full address.
func NormalizeURL(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("address required")
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "", errors.New("invalid address")
	}
	if u.Scheme == "http" {
		u.Scheme = "https" // the API needs HTTPS (the token travels with each request)
		if u.Port() == "7070" || u.Port() == "" {
			u.Host = strings.TrimSuffix(u.Host, ":7070") + ":7443"
		}
	}
	if u.Scheme != "https" {
		return "", errors.New("invalid address")
	}
	if u.Port() == "" {
		u.Host += ":7443"
	}
	return "https://" + u.Host, nil
}

// Pair signs this computer in to a NAS. The fingerprint must be the one
// the user accepted (or carried by the link); a CA-signed certificate
// needs none.
func (a *App) Pair(ctx context.Context, in PairInput) error {
	if in.Link != "" {
		base, fp, code, _, err := ParseLink(in.Link)
		if err != nil {
			return err
		}
		in.URL, in.Code = base, code
		if fp != "" {
			in.Fingerprint = fp
		}
	}
	base, err := NormalizeURL(in.URL)
	if err != nil {
		return err
	}
	fp := api.NormalizeFingerprint(in.Fingerprint)
	if in.Fingerprint != "" && fp == "" {
		return errors.New("invalid certificate fingerprint")
	}
	a.mu.Lock()
	device := a.cfg.DeviceName
	a.mu.Unlock()
	if in.DeviceName != "" {
		device = in.DeviceName
	}
	c := api.New(base, "", fp)
	res, err := c.Pair(ctx, api.PairRequest{Username: in.Username, Password: in.Password, Code: in.OTP, PairingCode: in.Code,
		DeviceName: device, App: a.Version, OS: platform.OSName(), Model: runtime.GOOS + "/" + runtime.GOARCH})
	if err != nil {
		return err
	}
	_, err = a.Store.Update(func(cfg *config.Config) error {
		if cfg.Server != base {
			cfg.Pairs = nil // another NAS: its folders start from nothing
		}
		cfg.Server, cfg.Fingerprint, cfg.Token = base, fp, res.Token
		cfg.User, cfg.NASName, cfg.DeviceName = res.User.Username, res.Server.Hostname, device
		a.mu.Lock()
		a.cfg = *cfg
		a.mu.Unlock()
		return nil
	})
	if err != nil {
		return err
	}
	return a.restart()
}

// SignOut revokes this computer on the NAS and forgets the folders (their
// local files stay).
func (a *App) SignOut(ctx context.Context) error {
	if c := a.Client(); c != nil {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_ = c.SignOut(cctx)
		cancel()
	}
	a.Stop()
	_, err := a.update(func(c *config.Config) error {
		c.Token, c.Pairs = "", nil
		return nil
	})
	os.Remove(a.Store.StateFile())
	if err == nil {
		err = a.restart()
	}
	return err
}

// update changes the settings (saved) and keeps them in memory.
func (a *App) update(f func(*config.Config) error) (config.Config, error) {
	c, err := a.Store.Update(f)
	if err != nil {
		return c, err
	}
	a.mu.Lock()
	a.cfg = c
	a.mu.Unlock()
	return c, nil
}

// ---- Folders ----

// AddPair synchronizes a local folder (created when missing) with a NAS folder.
func (a *App) AddPair(local, remote string) (engine.Pair, error) {
	local = strings.TrimSpace(local)
	if strings.HasPrefix(local, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			local = filepath.Join(h, strings.TrimPrefix(local, "~"))
		}
	}
	if !filepath.IsAbs(local) {
		return engine.Pair{}, errors.New(a.T("err.local.absolute"))
	}
	local = filepath.Clean(local)
	remote = strings.TrimRight(strings.TrimSpace(remote), "/")
	if !strings.HasPrefix(remote, "/") {
		return engine.Pair{}, errors.New(a.T("err.remote.choose"))
	}
	if st, err := os.Stat(local); err == nil && !st.IsDir() {
		return engine.Pair{}, errors.New(a.T("err.local.notdir"))
	} else if err != nil {
		if err := os.MkdirAll(local, 0o755); err != nil {
			return engine.Pair{}, err
		}
	}
	inside := func(a, b string) bool { // a inside b, or the same
		rel, err := filepath.Rel(b, a)
		return err == nil && (rel == "." || !strings.HasPrefix(rel, ".."))
	}
	if inside(a.Store.Dir, local) || inside(local, a.Store.Dir) {
		return engine.Pair{}, errors.New(a.T("err.local.appdir"))
	}
	if c := a.Client(); c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, err := c.Changes(ctx, remote, "", 0)
		cancel()
		if err != nil {
			return engine.Pair{}, err
		}
	}
	p := engine.Pair{ID: config.NewID(), Local: local, Remote: remote}
	_, err := a.update(func(c *config.Config) error {
		for _, x := range c.Pairs {
			if inside(local, x.Local) || inside(x.Local, local) {
				return errors.New(a.T("err.local.overlap", x.Local))
			}
		}
		c.Pairs = append(c.Pairs, p)
		return nil
	})
	if err != nil {
		return engine.Pair{}, err
	}
	return p, a.applyPairs()
}

func (a *App) applyPairs() error {
	a.mu.Lock()
	eng, pairs := a.eng, a.cfg.Pairs
	a.mu.Unlock()
	if eng != nil {
		if err := eng.SetPairs(pairs); err != nil {
			return err
		}
	}
	go a.fire()
	return nil
}

// RemovePair stops synchronizing a folder (its files stay on both sides).
func (a *App) RemovePair(id string) error {
	_, err := a.update(func(c *config.Config) error {
		var keep []engine.Pair
		for _, p := range c.Pairs {
			if p.ID != id {
				keep = append(keep, p)
			}
		}
		c.Pairs = keep
		return nil
	})
	if err != nil {
		return err
	}
	return a.applyPairs()
}

// UpdatePair pauses a folder or changes its excluded subfolders.
func (a *App) UpdatePair(id string, paused *bool, exclude []string) error {
	_, err := a.update(func(c *config.Config) error {
		for i := range c.Pairs {
			if c.Pairs[i].ID == id {
				if paused != nil {
					c.Pairs[i].Paused = *paused
				}
				if exclude != nil {
					var ex []string
					for _, e := range exclude {
						if e = strings.Trim(strings.TrimSpace(e), "/"); e != "" && !strings.Contains(e, "..") {
							ex = append(ex, e)
						}
					}
					c.Pairs[i].Exclude = ex
				}
				return nil
			}
		}
		return errors.New("folder not found")
	})
	if err != nil {
		return err
	}
	return a.applyPairs()
}

// Settings are the general settings the user changes.
type Settings struct {
	DeviceName *string   `json:"deviceName"`
	UpKBps     *int64    `json:"upKBps"`
	DownKBps   *int64    `json:"downKBps"`
	Ignore     *[]string `json:"ignore"`
	Language   *string   `json:"language"`
	Autostart  *bool     `json:"autostart"`
	Notify     *bool     `json:"notify"`
}

// UpdateSettings applies settings; the engine restarts when needed.
func (a *App) UpdateSettings(s Settings) error {
	restart := false
	c, err := a.update(func(c *config.Config) error {
		if s.DeviceName != nil && strings.TrimSpace(*s.DeviceName) != "" && *s.DeviceName != c.DeviceName {
			c.DeviceName, restart = strings.TrimSpace(*s.DeviceName), true
		}
		if s.UpKBps != nil && *s.UpKBps >= 0 {
			c.UpKBps = *s.UpKBps
		}
		if s.DownKBps != nil && *s.DownKBps >= 0 {
			c.DownKBps = *s.DownKBps
		}
		if s.Ignore != nil {
			var l []string
			for _, p := range *s.Ignore {
				if p = strings.TrimSpace(p); p != "" {
					l = append(l, p)
				}
			}
			if l == nil {
				l = []string{}
			}
			c.Ignore, restart = l, true
		}
		if s.Language != nil && (*s.Language == "" || i18n.Has(*s.Language)) {
			c.Language = *s.Language
		}
		if s.Notify != nil {
			c.NoNotify = !*s.Notify
		}
		if s.Autostart != nil {
			if err := platform.SetAutostart(*s.Autostart); err != nil {
				return err
			}
			c.Autostart = *s.Autostart
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.up.SetRate(c.UpKBps * 1024)
	a.down.SetRate(c.DownKBps * 1024)
	if restart {
		return a.restart()
	}
	go a.fire()
	return nil
}

// SetPaused pauses or resumes every folder.
func (a *App) SetPaused(on bool) error {
	_, err := a.update(func(c *config.Config) error { c.Paused = on; return nil })
	if eng := a.Engine(); eng != nil {
		eng.Pause(on)
	}
	go a.fire()
	return err
}

// SyncNow starts a round on every folder.
func (a *App) SyncNow() {
	if eng := a.Engine(); eng != nil {
		eng.Trigger()
	}
}

// ResolveBrake answers the deletion brake of a folder.
func (a *App) ResolveBrake(id string, apply bool) {
	if eng := a.Engine(); eng != nil {
		eng.ResolveBrake(id, apply)
	}
}

// WebURL is the address of the NAS web interface.
func (a *App) WebURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Server
}

// FirstRun turns start at login on the first time the app runs.
func (a *App) FirstRun() {
	a.mu.Lock()
	done := a.cfg.Initialized
	a.mu.Unlock()
	if done {
		return
	}
	on := platform.SetAutostart(true) == nil
	_, _ = a.update(func(c *config.Config) error { c.Initialized, c.Autostart = true, on; return nil })
}

// Paired tells whether the app is signed in to a NAS.
func (a *App) Paired() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Paired()
}
