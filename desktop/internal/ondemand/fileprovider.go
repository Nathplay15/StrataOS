package ondemand

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Pieces of the macOS provider (fileprovider_darwin.go) that need no macOS,
// tested everywhere.

// AgentJSON is agent.json, which the helper writes in the app group folder
// for the extensions: the agent's port, their token (/v1/fp/ only) and the
// folders whose files get the Finder menu (synchronized and on demand).
func AgentJSON(port int, token string, folders []string) []byte {
	seen := map[string]bool{}
	l := []string{}
	for _, f := range folders {
		f = filepath.Clean(f)
		if f != "." && filepath.IsAbs(f) && !seen[f] {
			seen[f] = true
			l = append(l, f)
		}
	}
	sort.Strings(l)
	b, _ := json.Marshal(map[string]any{"port": port, "token": token, "folders": l})
	return b
}

// HelperRoot reads the folder printed by `strataos-fileprovider add`: its
// last line, an absolute path.
func HelperRoot(out string) (string, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	p := strings.TrimSpace(lines[len(lines)-1])
	if p == "" || !filepath.IsAbs(p) {
		return "", errors.New("the File Provider helper gave no folder")
	}
	return filepath.Clean(p), nil
}

// debouncer runs f at most once per delay, after the last call of a burst
// started (the system re-reads the changes itself, a signal is enough).
type debouncer struct {
	mu      sync.Mutex
	delay   time.Duration
	f       func()
	pending bool
}

func (d *debouncer) call() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending {
		return
	}
	d.pending = true
	time.AfterFunc(d.delay, func() {
		d.mu.Lock()
		d.pending = false
		d.mu.Unlock()
		d.f()
	})
}
