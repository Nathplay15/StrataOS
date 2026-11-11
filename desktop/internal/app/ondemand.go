package app

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"strataos-sync/internal/api"
	"strataos-sync/internal/config"
	"strataos-sync/internal/engine"
	"strataos-sync/internal/ondemand"
)

// Files on demand of the account (internal/ondemand): its Service and the
// provider of this system, started with the engine when enabled.

// OnDemandStatus is the state of files on demand for the window.
type OnDemandStatus struct {
	Supported   bool                   `json:"supported"`
	Kind        string                 `json:"kind,omitempty"` // fuse, cfapi, fileprovider
	Enabled     bool                   `json:"enabled"`
	Path        string                 `json:"path,omitempty"`        // chosen folder ("" = default)
	DefaultPath string                 `json:"defaultPath,omitempty"` //
	CacheMB     int64                  `json:"cacheMB,omitempty"`     //
	Provider    *ondemand.ProviderInfo `json:"provider,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Ready       bool                   `json:"ready"`
	CacheUsed   int64                  `json:"cacheUsed"`
	CacheBudget int64                  `json:"cacheBudget"`
	Pinned      int                    `json:"pinned"`
	Dirty       int                    `json:"dirty"`
	Hidden      []string               `json:"hidden,omitempty"` // encrypted folders left out
}

// DefaultOnDemandPath is the folder shown when the user chose none.
func DefaultOnDemandPath(nasName string) string {
	home, _ := os.UserHomeDir()
	n := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
			return '-'
		}
		return r
	}, strings.TrimSpace(nasName))
	if n == "" {
		n = "NAS"
	}
	return filepath.Join(home, "StrataOS - "+n)
}

func (a *App) onDemandRoot() string {
	if od := a.cfg.OnDemand; od != nil && od.Path != "" {
		return od.Path
	}
	return DefaultOnDemandPath(a.cfg.NASName)
}

// startOnDemandLocked starts files on demand when enabled (caller holds mu).
func (a *App) startOnDemandLocked() {
	a.odErr = ""
	od := a.cfg.OnDemand
	if od == nil || !od.Enabled || a.client == nil || a.ctx == nil {
		return
	}
	if ok, _ := ondemand.Supported(); !ok {
		a.odErr = ondemand.ErrUnsupported.Error()
		return
	}
	cl := a.client
	s := &ondemand.Service{
		Remote: engine.APIRemote{C: cl},
		Shares: func(ctx context.Context) ([]ondemand.Share, error) {
			l, err := cl.Shares(ctx)
			if err != nil {
				if api.StatusOf(err) == 401 {
					return nil, engine.ErrUnauthorized
				}
				return nil, err
			}
			out := make([]ondemand.Share, 0, len(l))
			for _, sh := range l {
				out = append(out, ondemand.Share{Name: sh.Name, Path: sh.Path, Writable: sh.Writable})
			}
			return out, nil
		},
		Dir:    filepath.Join(a.Store.Dir, "ondemand"),
		Device: a.cfg.DeviceName,
		Budget: od.CacheMB << 20,
		Cache:  ondemand.UsesCache(),
		Notify: a.onEvent,
	}
	if err := s.Open(); err != nil {
		a.odErr = err.Error()
		return
	}
	s.Start(a.ctx)
	name := "StrataOS"
	if a.cfg.NASName != "" {
		name += " - " + a.cfg.NASName
	}
	p, err := ondemand.StartProvider(s, ondemand.ProviderOptions{Root: a.onDemandRoot(), Account: a.Account, Name: name, Shell: a.Shell})
	if err != nil {
		log.Printf("files on demand: %v", err)
		a.odErr = err.Error()
		s.Close()
		return
	}
	a.od, a.odProv = s, p
}

func (a *App) stopOnDemandLocked() {
	if a.odProv != nil {
		if err := a.odProv.Close(); err != nil {
			log.Printf("files on demand: stop: %v", err)
		}
		a.odProv = nil
	}
	if a.od != nil {
		a.od.Close()
		a.od = nil
	}
}

// OnDemand returns the running service and provider (nil when off).
func (a *App) OnDemand() (*ondemand.Service, ondemand.Provider) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.od, a.odProv
}

// OnDemandStatus returns the state of files on demand.
func (a *App) OnDemandStatus() OnDemandStatus {
	a.mu.Lock()
	ok, kind := ondemand.Supported()
	st := OnDemandStatus{Supported: ok, Kind: kind, DefaultPath: DefaultOnDemandPath(a.cfg.NASName), Error: a.odErr}
	if od := a.cfg.OnDemand; od != nil {
		st.Enabled, st.Path, st.CacheMB = od.Enabled, od.Path, od.CacheMB
	}
	s, p := a.od, a.odProv
	a.mu.Unlock()
	if p != nil {
		i := p.Info()
		st.Provider = &i
	}
	if s != nil {
		st.Ready = s.Ready()
		st.CacheUsed, st.CacheBudget = s.CacheUsage()
		st.Pinned, st.Dirty = s.Counts()
		st.Hidden = s.Hidden()
	}
	return st
}

// SetOnDemand turns files on demand on or off, or changes its folder or cache.
func (a *App) SetOnDemand(enabled *bool, path *string, cacheMB *int64) error {
	_, err := a.update(func(c *config.Config) error {
		od := config.OnDemand{}
		if c.OnDemand != nil {
			od = *c.OnDemand
		}
		if enabled != nil {
			od.Enabled = *enabled
		}
		if path != nil {
			p := strings.TrimSpace(*path)
			if p != "" && !filepath.IsAbs(p) {
				return errors.New("absolute folder required")
			}
			if p != "" {
				for _, pr := range c.Pairs {
					if rel, err := filepath.Rel(pr.Local, p); err == nil && !strings.HasPrefix(rel, "..") {
						return errors.New("this folder is inside a synchronized folder")
					}
					if rel, err := filepath.Rel(p, pr.Local); err == nil && !strings.HasPrefix(rel, "..") {
						return errors.New("a synchronized folder is inside this folder")
					}
				}
			}
			od.Path = p
		}
		if cacheMB != nil && *cacheMB >= 0 {
			od.CacheMB = *cacheMB
		}
		c.OnDemand = &od
		return nil
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.stopOnDemandLocked()
	if a.cfg.Paired() {
		a.startOnDemandLocked()
	}
	a.mu.Unlock()
	go a.fire()
	return nil
}
