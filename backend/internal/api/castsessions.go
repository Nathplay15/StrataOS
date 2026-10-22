package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"strataos/internal/cast"
	"strataos/internal/sys"
)

// Cast sessions survive a restart of the daemon: they are saved in a file
// next to the key that signs the stream URLs (media.key), so the receivers
// keep reading the file and the remote panel keeps driving them.

const castFile = "cast-sessions.json"

// savedCast is a cast session as written in castFile.
type savedCast struct {
	ID        string          `json:"id"`
	User      string          `json:"user"`
	Path      string          `json:"path"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Type      string          `json:"type"`
	Size      int64           `json:"size"`
	Started   time.Time       `json:"started"`
	Receivers []savedReceiver `json:"receivers"`
}

type savedReceiver struct {
	cast.Receiver
	// The control URLs of a DLNA renderer (not in the JSON of Receiver).
	AV       string  `json:"avTransport,omitempty"`
	RC       string  `json:"renderingControl,omitempty"`
	URL      string  `json:"url"` // stream URL given to the receiver (signed token)
	Position float64 `json:"position,omitempty"`
}

var castSaveMu sync.Mutex

// saveCasts writes the ready sessions and their receivers that play (those
// that failed to load are left out).
func (s *Server) saveCasts() {
	castSaveMu.Lock()
	defer castSaveMu.Unlock()
	list := []savedCast{}
	castState.Lock()
	for _, cs := range castState.sessions {
		if !cs.ready && !cs.restoring {
			continue
		}
		sc := savedCast{ID: cs.ID, User: cs.User, Path: cs.Path, Name: cs.Name, Kind: cs.Kind, Type: cs.Type, Size: cs.Size, Started: cs.Started}
		for _, rc := range cs.receivers {
			rc.mu.Lock()
			if rc.player != nil || rc.resume {
				sc.Receivers = append(sc.Receivers, savedReceiver{Receiver: rc.Receiver, AV: rc.AVTransport, RC: rc.RenderingControl, URL: rc.url, Position: rc.pos})
			}
			rc.mu.Unlock()
		}
		if len(sc.Receivers) > 0 {
			list = append(list, sc)
		}
	}
	castState.Unlock()
	b, _ := json.Marshal(list)
	if err := sys.WriteFileAtomic(filepath.Join(s.dataDir, castFile), b, 0o600); err != nil {
		slog.Warn("save cast sessions", "err", err)
	}
}

// restoredCasts turns the saved sessions into sessions whose receivers
// connect again on first use; the sessions past the life of their URLs are
// dropped. Their stream URLs work at once; they are listed and driven once
// checkRestoredCasts has seen them still playing.
func restoredCasts(list []savedCast, now time.Time) []*castSession {
	var out []*castSession
	for _, sc := range list {
		if sc.ID == "" || now.Sub(sc.Started) >= streamTTL || now.Before(sc.Started.Add(-time.Minute)) {
			continue
		}
		cs := &castSession{ID: sc.ID, User: sc.User, Path: sc.Path, Name: sc.Name, Kind: sc.Kind, Type: sc.Type, Size: sc.Size, Started: sc.Started, restoring: true}
		for _, sr := range sc.Receivers {
			r := sr.Receiver
			r.AVTransport, r.RenderingControl = sr.AV, sr.RC
			if r.Kind != "chromecast" && r.Kind != "dlna" {
				continue
			}
			cs.receivers = append(cs.receivers, &castReceiver{Receiver: r, url: sr.URL, resume: true, pos: sr.Position})
		}
		if len(cs.receivers) > 0 {
			out = append(out, cs)
		}
	}
	return out
}

// streamMark is the part of a stream URL unique to its session (the token).
func streamMark(u string) string {
	const p = "/api/media/stream/"
	i := strings.Index(u, p)
	if i < 0 {
		return u
	}
	tok, _, _ := strings.Cut(u[i+len(p):], "/")
	return p + tok + "/"
}

// restoreCasts loads the saved sessions at startup, then checks in the
// background which receivers still play their media: the sessions that
// ended while the daemon was down are removed.
func (s *Server) restoreCasts() {
	b, err := os.ReadFile(filepath.Join(s.dataDir, castFile))
	if err != nil {
		return
	}
	var list []savedCast
	if err := json.Unmarshal(b, &list); err != nil {
		slog.Warn("cast sessions file unreadable", "err", err)
		return
	}
	restored := restoredCasts(list, time.Now())
	castState.Lock()
	for _, cs := range restored {
		castState.sessions[cs.ID] = cs
	}
	castState.Unlock()
	if len(restored) != len(list) {
		s.saveCasts()
	}
	if len(restored) > 0 {
		go s.checkRestoredCasts(restored)
	}
}

func (s *Server) checkRestoredCasts(list []*castSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	changed := false
	for _, cs := range list {
		var wg sync.WaitGroup
		ended := make([]bool, len(cs.receivers))
		for i, rc := range cs.receivers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if p := rc.use(c); p != nil {
					ended[i] = cast.Ended(p.Status(c), streamMark(rc.url))
				}
			}()
		}
		wg.Wait()
		var keep []*castReceiver
		for i, rc := range cs.receivers {
			if ended[i] {
				rc.mu.Lock()
				if rc.player != nil {
					_ = rc.player.Close()
				}
				rc.mu.Unlock()
				changed = true
				continue
			}
			keep = append(keep, rc)
		}
		castState.Lock()
		if len(keep) == 0 {
			delete(castState.sessions, cs.ID)
			slog.Info("cast session ended while the daemon was stopped", "file", cs.Name)
		} else {
			// Nothing else reads the receivers of a session being restored.
			cs.receivers, cs.ready, cs.restoring = keep, true, false
		}
		castState.Unlock()
	}
	if changed {
		s.saveCasts()
	}
}
