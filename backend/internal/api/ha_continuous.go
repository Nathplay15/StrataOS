package api

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/backup"
	"strataos/internal/ha"
	"strataos/internal/store"
)

// State of the near-continuous replication, shown in the interface.
var haCont struct {
	sync.Mutex
	Pending int       `json:"pending"`
	Last    time.Time `json:"last,omitzero"`
	Error   string    `json:"error,omitempty"`
	Full    bool      `json:"watchLimit,omitempty"`
	Active  bool      `json:"active"`
}

func haContSnapshot() map[string]any {
	haCont.Lock()
	defer haCont.Unlock()
	return map[string]any{"pending": haCont.Pending, "last": haCont.Last, "error": haCont.Error, "watchLimit": haCont.Full, "active": haCont.Active}
}

// coveredBy tells whether dir is inside a folder copied with its whole tree.
func coveredBy(dir string, pending map[string]bool) bool {
	for p, rec := range pending {
		if rec && p != dir && strings.HasPrefix(dir, p+"/") {
			return true
		}
	}
	return false
}

// haContinuousLoop copies the folders that changed on the active primary.
func (s *Server) haContinuousLoop() {
	var w *ha.Watcher
	var roots []string
	pending := map[string]bool{} // folder → with its whole tree
	var first, lastEv, retryAt time.Time
	stop := func() {
		if w != nil {
			w.Close()
			w = nil
		}
		roots = nil
	}
	setState := func(f func()) { haCont.Lock(); f(); haCont.Pending = len(pending); haCont.Unlock() }
	for {
		var c store.HA
		var want []string
		s.store.Read(func(st *store.State) {
			c = st.HA
			for _, sh := range st.Shares {
				want = append(want, sh.DataPath())
			}
		})
		slices.Sort(want)
		if c.Role != "primary" || !c.Continuous || c.Maintenance || ha.State() != "MASTER" {
			stop()
			clear(pending)
			setState(func() { haCont.Active = false })
			time.Sleep(2 * time.Second)
			continue
		}
		if w == nil || !slices.Equal(want, roots) {
			stop()
			var err error
			if w, err = ha.NewWatcher(want); err != nil {
				setState(func() { haCont.Error = err.Error() })
				time.Sleep(30 * time.Second)
				continue
			}
			roots = want
			setState(func() { haCont.Active, haCont.Full = true, w.Full })
		}
		select {
		case ch, ok := <-w.Changes:
			if !ok {
				stop()
				continue
			}
			pending[ch.Dir] = pending[ch.Dir] || ch.Recursive
			if first.IsZero() {
				first = time.Now()
			}
			lastEv = time.Now()
			setState(func() {})
			continue // read the whole burst first
		case <-time.After(time.Second):
		}
		now := time.Now()
		if len(pending) == 0 || now.Before(retryAt) || (now.Sub(lastEv) < 5*time.Second && now.Sub(first) < 30*time.Second) {
			continue
		}
		// Never at the same time as a full replication (it would be redone after).
		if _, busy := running.LoadOrStore("ha-sync", true); busy {
			continue
		}
		dirs := make([]string, 0, len(pending))
		for d := range pending {
			dirs = append(dirs, d)
		}
		slices.Sort(dirs)
		_, peer := s.peer()
		key := filepath.Join(backup.KeyDir, "id_ed25519")
		known := filepath.Join(backup.KeyDir, "known_hosts")
		var failed error
		for _, d := range dirs {
			rec := pending[d]
			if coveredBy(d, pending) {
				delete(pending, d)
				continue
			}
			if _, err := os.Stat(d); err != nil {
				delete(pending, d) // deleted: its parent is in the list and removes it on the peer
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			err := ha.SyncDir(ctx, key, known, peer.PeerHost, d, rec, func(string, ...any) {})
			cancel()
			if err != nil {
				failed = err
				break
			}
			delete(pending, d)
		}
		running.Delete("ha-sync")
		first = time.Time{}
		if failed != nil {
			slog.Warn("continuous replication", "err", failed)
			retryAt = now.Add(30 * time.Second)
			first = now
			setState(func() { haCont.Error = failed.Error() })
			continue
		}
		setState(func() { haCont.Last, haCont.Error, haCont.Full = time.Now(), "", w.Full })
	}
}
