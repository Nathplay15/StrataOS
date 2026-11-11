package ondemand

import (
	"context"
	"errors"
	"log"
	"os"
	"sort"
	"time"

	"strataos-sync/internal/engine"
)

// Handle is an open file of the cache (Linux): reads download what is
// missing, writes change the cached copy, which is sent to the NAS once
// the last handle is closed.
type Handle struct {
	s     *Service
	p     string
	f     *os.File
	write bool
}

// Path is the current path of the file (it follows renames).
func (h *Handle) Path() string {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	return h.p
}

// OpenFile opens a file of the cache. Opening for writing downloads the
// whole file first, unless it is truncated.
func (s *Service) OpenFile(ctx context.Context, p string, write, trunc bool) (*Handle, error) {
	_, _, nd, err := s.node(p)
	if err != nil {
		return nil, err
	}
	if nd.Dir {
		return nil, ErrIsDir
	}
	if write {
		if _, _, err := s.writableRel(p); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	f := s.stateLocked(p, nd)
	key := f.Key
	_, _ = s.applyLocked(p, f, Event{Kind: EvOpen})
	f.Used = time.Now().Unix()
	s.mu.Unlock()
	fh, err := os.OpenFile(s.cacheFile(key), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		s.closeState(p)
		return nil, err
	}
	h := &Handle{s: s, p: p, f: fh, write: write}
	s.mu.Lock()
	s.handles[h] = true
	s.mu.Unlock()
	if write {
		if trunc {
			err = h.Truncate(0)
		} else {
			err = s.Hydrate(ctx, p)
		}
		if err != nil {
			h.Close()
			return nil, err
		}
	}
	return h, nil
}

// Create makes a new empty file (sent to the NAS when closed).
func (s *Service) Create(ctx context.Context, p string) (*Handle, error) {
	x, rel, err := s.writableRel(p)
	if err != nil {
		return nil, err
	}
	if _, ok := x.tree.Get(rel); ok {
		return nil, ErrExists
	}
	if par := parentOf(rel); par != "" {
		if n, ok := x.tree.Get(par); !ok || !n.Dir {
			return nil, ErrNotFound
		}
	}
	now := time.Now().UnixMilli()
	x.tree.Put(Node{RemoteEntry: engine.RemoteEntry{Path: rel, MTime: now}, Local: true})
	s.saveNodes(x, rel)
	s.mu.Lock()
	if old := s.files[p]; old != nil {
		s.dropLocked(p, old)
	}
	f := &FileState{Key: randomKey(), Dirty: true, MTime: now, Gen: 1}
	s.files[p] = f
	s.markLocked(p)
	s.mu.Unlock()
	return s.OpenFile(ctx, p, true, true)
}

func (s *Service) closeState(p string) Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.files[p]
	if f == nil {
		return 0
	}
	act, _ := s.applyLocked(p, f, Event{Kind: EvClose})
	return act
}

// ReadAt reads, downloading what is missing.
func (h *Handle) ReadAt(ctx context.Context, b []byte, off int64) (int, error) {
	return h.s.ReadAt(ctx, h.Path(), h.f, b, off)
}

// WriteAt writes into the cached copy.
func (h *Handle) WriteAt(b []byte, off int64) (int, error) {
	if !h.write {
		return 0, os.ErrPermission
	}
	n, err := h.f.WriteAt(b, off)
	if n > 0 {
		p := h.Path()
		h.s.mu.Lock()
		if f := h.s.files[p]; f != nil {
			_, _ = h.s.applyLocked(p, f, Event{Kind: EvWrite, Off: off, End: off + int64(n)})
			f.MTime = time.Now().UnixMilli()
		}
		h.s.mu.Unlock()
	}
	return n, err
}

// Truncate sets the size of the file.
func (h *Handle) Truncate(size int64) error {
	if !h.write {
		return os.ErrPermission
	}
	if err := h.f.Truncate(size); err != nil {
		return err
	}
	p := h.Path()
	h.s.mu.Lock()
	if f := h.s.files[p]; f != nil {
		_, _ = h.s.applyLocked(p, f, Event{Kind: EvTruncate, Size: size})
		f.MTime = time.Now().UnixMilli()
	}
	h.s.mu.Unlock()
	return nil
}

// Size is the size of the file as seen here.
func (h *Handle) Size() int64 {
	p := h.Path()
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if f := h.s.files[p]; f != nil {
		return f.Size
	}
	return 0
}

// Close closes the handle; the last one of a changed file sends it.
func (h *Handle) Close() error {
	h.s.mu.Lock()
	delete(h.s.handles, h)
	h.s.mu.Unlock()
	err := h.f.Close()
	if act := h.s.closeState(h.Path()); act&ActUpload != 0 {
		h.s.kickUploads()
	}
	return err
}

// SetMTime sets the date of a file changed here (sent with it).
func (s *Service) SetMTime(p string, ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.files[p]; f != nil && f.Dirty {
		f.MTime = ms
		s.markLocked(p)
	}
}

// ---- Sending changed files ----

func (s *Service) kickUploads() {
	select {
	case s.upKick <- struct{}{}:
	default:
	}
}

func (s *Service) uploadLoop() {
	defer s.wg.Done()
	for {
		// Let a burst of writes end (an editor saving several times).
		select {
		case <-s.ctx.Done():
			return
		case <-s.upKick:
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		case <-time.After(30 * time.Second):
		}
		s.sendDirty()
	}
}

// sendDirty sends every closed file changed here.
func (s *Service) sendDirty() {
	s.mu.Lock()
	var l []string
	for p, f := range s.files {
		if f.Dirty && f.Open == 0 && p != "" && p[0] != '\x01' {
			l = append(l, p)
		}
	}
	s.mu.Unlock()
	sort.Strings(l) // folders' files in order, parents' new files first
	for _, p := range l {
		if s.ctx.Err() != nil {
			return
		}
		if err := s.send(p); err != nil {
			log.Printf("files on demand: send %s: %v", p, err)
			if s.Notify != nil && !errors.Is(err, context.Canceled) {
				s.Notify(engine.Event{Kind: "error", Path: p, Msg: err.Error()})
			}
		}
	}
}

// send uploads a file changed here, then follows what the NAS answered.
func (s *Service) send(p string) error {
	s.mu.Lock()
	f := s.files[p]
	if f == nil || !f.Dirty || f.Open > 0 {
		s.mu.Unlock()
		return nil
	}
	snap := *f
	s.sending(p, true)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.sending(p, false)
		s.mu.Unlock()
	}()
	fh, err := os.Open(s.cacheFile(snap.Key))
	if err != nil {
		return err
	}
	defer fh.Close()
	it, err := s.Upload(s.ctx, p, fh, snap.Size, snap.MTime, snap.Version)
	if errors.Is(err, ErrReadOnly) || errors.Is(err, ErrNotFound) {
		// Nowhere to send it (rights or folder gone): keep it, say it once.
		s.mu.Lock()
		if f := s.files[p]; f != nil && f.Gen == snap.Gen {
			f.Gen++ // not again at every round
		}
		s.mu.Unlock()
		return err
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f = s.files[p]
	if f == nil {
		return nil
	}
	if it.Path != p {
		// Sent as a conflict copy: this path follows the NAS again.
		if f.Gen == snap.Gen {
			name, rel := Split(p)
			if x := s.srcs[name]; x != nil {
				if n, ok := x.tree.Get(rel); ok && !n.Local {
					s.used -= f.Ranges.Total()
					_ = os.Remove(s.cacheFile(f.Key))
					f.Ranges, f.Dirty, f.Version, f.Size, f.MTime = nil, false, n.Version, n.Size, n.MTime
					s.markLocked(p)
				}
			}
		}
		return nil
	}
	act, _ := s.applyLocked(p, f, Event{Kind: EvUploaded, Version: it.Version, Gen: snap.Gen})
	if act&ActUpload != 0 {
		s.kickUploads()
	}
	return nil
}

func (s *Service) sending(p string, on bool) {
	if s.busy == nil {
		s.busy = map[string]bool{}
	}
	if on {
		s.busy[p] = true
	} else {
		delete(s.busy, p)
	}
}
