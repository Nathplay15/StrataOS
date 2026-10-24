package syncjournal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Manager keeps the indexes of the folders clients follow: loaded and
// watched while clients ask for them, saved, and closed after an hour
// without requests (the next request loads and rescans them).
type Manager struct {
	Dir string // where the indexes are saved

	mu   sync.Mutex
	open map[string]*active
}

type active struct {
	j        *Journal
	w        *Watcher
	ready    chan struct{}
	err      error
	lastUse  time.Time
	lastFull time.Time
}

const (
	idleClose    = time.Hour
	fullInterval = 5 * time.Minute // full rescans, in case an event was missed
	pollInterval = time.Minute     // the same without inotify (limit reached)
)

func (m *Manager) file(root string) string {
	h := sha256.Sum256([]byte(root))
	return filepath.Join(m.Dir, hex.EncodeToString(h[:12])+".idx")
}

// Get returns the index of root, loading and scanning it on first use.
func (m *Manager) Get(root string) (*Journal, error) {
	m.mu.Lock()
	if m.open == nil {
		m.open = map[string]*active{}
	}
	a := m.open[root]
	if a == nil {
		a = &active{ready: make(chan struct{})}
		m.open[root] = a
		go m.start(root, a)
	}
	a.lastUse = time.Now()
	m.mu.Unlock()
	<-a.ready
	if a.err != nil {
		m.mu.Lock()
		if m.open[root] == a {
			delete(m.open, root) // try again at the next request
		}
		m.mu.Unlock()
		return nil, a.err
	}
	return a.j, nil
}

func (m *Manager) start(root string, a *active) {
	defer close(a.ready)
	j := Load(m.file(root), root)
	if _, err := j.Scan("", true); err != nil {
		a.err = err
		return
	}
	a.j, a.lastFull = j, time.Now()
	if w, err := Watch(j); err == nil {
		a.w = w
	} else {
		slog.Warn("sync index: no inotify", "root", root, "err", err)
	}
	_ = j.Save(m.file(root))
}

// Refresh rescans the folder holding rel at once (after a change made
// through the API, so its answer and the next changes include it).
func (m *Manager) Refresh(root, rel string) {
	if j, err := m.Get(root); err == nil {
		added, _ := j.Scan(parent(rel), false)
		var w *Watcher
		m.mu.Lock()
		if a := m.open[root]; a != nil {
			w = a.w
		}
		m.mu.Unlock()
		if w != nil {
			w.add(added)
		}
	}
}

// Loop saves, rescans, prunes, hashes and closes the indexes (run once).
func (m *Manager) Loop() {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	lastPrune := time.Time{}
	go m.hashLoop()
	for range tick.C {
		m.mu.Lock()
		list := map[string]*active{}
		for root, a := range m.open {
			select {
			case <-a.ready:
				if a.err == nil {
					list[root] = a
				}
			default:
			}
		}
		m.mu.Unlock()
		prune := time.Since(lastPrune) > 24*time.Hour
		if prune {
			lastPrune = time.Now()
		}
		for root, a := range list {
			if prune {
				a.j.Prune()
			}
			every := fullInterval
			if a.w == nil || a.w.Degraded() || a.w.Watching() == 0 {
				every = pollInterval
			}
			if time.Since(a.lastFull) > every {
				added, err := a.j.Scan("", true)
				a.lastFull = time.Now()
				switch {
				case err != nil:
				case a.w == nil || a.w.Watching() == 0:
					if a.w != nil {
						a.w.Close()
					}
					w, _ := Watch(a.j)
					m.mu.Lock()
					a.w = w
					m.mu.Unlock()
				default:
					a.w.add(added)
				}
			}
			if err := a.j.Save(m.file(root)); err != nil {
				slog.Warn("sync index: save", "root", root, "err", err)
			}
			m.mu.Lock()
			idle := time.Since(a.lastUse) > idleClose
			if idle {
				delete(m.open, root)
			}
			m.mu.Unlock()
			if idle {
				if a.w != nil {
					a.w.Close()
				}
				_ = a.j.Save(m.file(root))
			}
		}
	}
}

// Forget closes and deletes the index of root (share deleted).
func (m *Manager) Forget(root string) {
	m.mu.Lock()
	a := m.open[root]
	delete(m.open, root)
	m.mu.Unlock()
	if a != nil {
		<-a.ready
		if a.w != nil {
			a.w.Close()
		}
	}
	os.Remove(m.file(root))
}

// hashLoop computes the missing hashes of the open indexes, one file at a time.
func (m *Manager) hashLoop() {
	for {
		m.mu.Lock()
		var js []*Journal
		for _, a := range m.open {
			select {
			case <-a.ready:
				if a.err == nil {
					js = append(js, a.j)
				}
			default:
			}
		}
		m.mu.Unlock()
		did := false
		for _, j := range js {
			for _, e := range j.Unhashed(32) {
				if e.Size > backgroundHashMax {
					continue // hashed when a client asks
				}
				if _, err := HashFile(j, e.Path); err == nil {
					did = true
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		if !did {
			time.Sleep(10 * time.Second)
		}
	}
}

// backgroundHashMax: larger files are hashed on demand only.
const backgroundHashMax = 256 << 20

// ErrChanged: the file changed while it was read.
var ErrChanged = errors.New("file changed while reading")

// HashFile returns the SHA-256 of a file of j, from the index when it is
// known for the file's current inode, size and date, else read and stored.
func HashFile(j *Journal, rel string) (string, error) {
	rt, err := os.OpenRoot(j.Root)
	if err != nil {
		return "", ErrRootUnavailable
	}
	defer rt.Close()
	st, err := rt.Lstat(rel)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("not a file")
	}
	k := hkey{inode(st), st.Size(), st.ModTime().UnixNano()}
	j.mu.Lock()
	h, ok := j.hashes[k]
	j.mu.Unlock()
	if ok {
		j.SetHash(rel, k.ino, k.size, k.mtime, h)
		return h, nil
	}
	f, err := rt.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	after, err := f.Stat()
	if err != nil || after.Size() != k.size || after.ModTime().UnixNano() != k.mtime {
		return "", ErrChanged
	}
	h = hex.EncodeToString(sum.Sum(nil))
	j.SetHash(rel, k.ino, k.size, k.mtime, h)
	return h, nil
}
