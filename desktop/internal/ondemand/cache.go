package ondemand

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"strataos-sync/internal/engine"
)

// The cache of downloaded files (Linux): Dir/cache/<key>, sparse files
// holding the parts of each file read so far (FileState.Ranges), the
// whole file once complete. Parts are fetched in blocks of fetchAlign
// bytes; a read that continues the previous one fetches readAhead bytes
// at once. Files kept on this device and files changed here are never
// evicted; the others go, least recently used first, when the cache is
// over its budget.

const (
	fetchAlign = 256 << 10
	fetchGap   = 1 << 20
	readAhead  = 8 << 20
	fetchPiece = 8 << 20
	lowWater   = 0.8
)

func (s *Service) cacheFile(key string) string { return filepath.Join(s.Dir, "cache", key) }

func (s *Service) lock(p string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[p]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[p] = l
	}
	return l
}

// markLocked queues the state of p for saving.
func (s *Service) markLocked(p string) { s.unsaved[p] = true }

// stateLocked returns the state of a file, created from its node.
func (s *Service) stateLocked(p string, n Node) *FileState {
	f := s.files[p]
	if f == nil {
		f = &FileState{Key: randomKey(), Version: n.Version, Size: n.Size, MTime: n.MTime}
		s.files[p] = f
		s.markLocked(p)
	} else if !f.Dirty && f.Version != n.Version && !n.Local {
		// The feed has not told us yet (or the state is older): follow the NAS.
		if f.Ranges.Total() > 0 {
			s.used -= f.Ranges.Total()
			_ = os.Truncate(s.cacheFile(f.Key), 0)
		}
		f.Version, f.Size, f.MTime, f.Ranges = n.Version, n.Size, n.MTime, nil
		s.markLocked(p)
	}
	return f
}

// applyLocked applies an event and keeps the cache size counted.
func (s *Service) applyLocked(p string, f *FileState, ev Event) (Action, error) {
	before := f.Ranges.Total()
	act, err := f.Apply(ev)
	s.used += f.Ranges.Total() - before
	s.markLocked(p)
	return act, err
}

// dropLocked forgets a file and its cached bytes.
func (s *Service) dropLocked(p string, f *FileState) {
	s.used -= f.Ranges.Total()
	_ = os.Remove(s.cacheFile(f.Key))
	delete(s.files, p)
	s.unsaved[p] = true
}

// act does what an event asked for (old: the state before it).
func (s *Service) act(p string, a Action, old FileState) {
	s.mu.Lock()
	f := s.files[p]
	if f == nil {
		s.mu.Unlock()
		return
	}
	if a&ActConflict != 0 {
		// Keep the local bytes for their conflict copy, under another key.
		keep := randomKey()
		if err := os.Rename(s.cacheFile(f.Key), s.cacheFile(keep)); err == nil {
			go s.conflictCopy(p, keep, old)
		}
		f.Key = randomKey()
	}
	if a&ActDrop != 0 {
		_ = os.Remove(s.cacheFile(f.Key))
	}
	if a&ActForget != 0 {
		s.dropLocked(p, f)
	}
	s.mu.Unlock()
	if a&ActHydrate != 0 && s.ctx != nil {
		go func() {
			if err := s.Hydrate(s.ctx, p); err != nil && s.ctx.Err() == nil {
				log.Printf("files on demand: keep %s: %v", p, err)
			}
		}()
	}
	if a&ActUpload != 0 {
		s.kickUploads()
	}
}

// conflictCopy sends the bytes of a file changed on both sides under a
// conflict name.
func (s *Service) conflictCopy(p, key string, old FileState) {
	defer os.Remove(s.cacheFile(key))
	fh, err := os.Open(s.cacheFile(key))
	if err != nil {
		return
	}
	defer fh.Close()
	dir, base := path.Dir(p), path.Base(p)
	cp := dir + "/" + engine.ConflictName(base, s.Device, time.Now(), 1)
	if _, err := s.Upload(s.ctx, cp, fh, old.Size, old.MTime, ""); err != nil {
		log.Printf("files on demand: conflict copy of %s: %v", p, err)
		return
	}
	if s.Notify != nil {
		s.Notify(engine.Event{Kind: "conflict", Path: cp, Msg: "files on demand"})
	}
}

// ReadAt reads a file, downloading the parts missing.
func (s *Service) ReadAt(ctx context.Context, p string, fh *os.File, b []byte, off int64) (int, error) {
	x, rel, nd, err := s.node(p)
	if err != nil {
		return 0, err
	}
	if nd.Dir {
		return 0, ErrIsDir
	}
	s.mu.Lock()
	f := s.stateLocked(p, nd)
	size := f.Size
	f.Used = time.Now().Unix()
	s.mu.Unlock()
	if off >= size {
		return 0, io.EOF
	}
	end := min(off+int64(len(b)), size)
	if err := s.ensure(ctx, x, rel, p, fh, off, end); err != nil {
		return 0, err
	}
	n, err := fh.ReadAt(b[:end-off], off)
	if err == io.EOF && int64(n) == end-off {
		err = nil
	}
	return n, err
}

// ensure downloads what is missing of [off, end) into the cached copy.
func (s *Service) ensure(ctx context.Context, x *source, rel, p string, fh *os.File, off, end int64) error {
	l := s.lock(p)
	l.Lock()
	defer l.Unlock()
	s.mu.Lock()
	f := s.files[p]
	if f == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	if f.Dirty || f.Ranges.Has(off, end) {
		s.mu.Unlock()
		return nil
	}
	// A read continuing a part already here is sequential: read ahead.
	for _, r := range f.Ranges {
		if r.End == off || (r.Off < off && off < r.End) {
			end = min(max(end, off+readAhead), f.Size)
			break
		}
	}
	missing := f.Ranges.Missing(off, end, f.Size, fetchAlign, fetchGap)
	version := f.Version
	s.mu.Unlock()
	for _, m := range missing {
		for _, piece := range m.Split(fetchPiece) {
			w := io.NewOffsetWriter(fh, piece.Off)
			if err := s.download(ctx, x, rel, version, piece.Off, piece.Len(), w); err != nil {
				return err
			}
			s.mu.Lock()
			if f := s.files[p]; f != nil && f.Version == version {
				_, _ = s.applyLocked(p, f, Event{Kind: EvFilled, Off: piece.Off, End: piece.End})
			}
			s.mu.Unlock()
		}
	}
	s.maybeEvict()
	return nil
}

// Hydrate downloads a whole file into the cache (keep on this device).
func (s *Service) Hydrate(ctx context.Context, p string) error {
	x, rel, nd, err := s.node(p)
	if err != nil {
		return err
	}
	if nd.Dir {
		return ErrIsDir
	}
	s.mu.Lock()
	f := s.stateLocked(p, nd)
	key, size := f.Key, f.Size
	s.mu.Unlock()
	fh, err := os.OpenFile(s.cacheFile(key), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	return s.ensure(ctx, x, rel, p, fh, 0, size)
}

// ---- Keep on this device / free up space ----

// Pin keeps a file, or every file of a folder, on this device (on), or
// frees their space (off). Files changed here and not sent yet stay.
func (s *Service) Pin(p string, on bool) error {
	it, err := s.Stat(p)
	if err != nil {
		return err
	}
	if it.Dir {
		s.setPinnedDir(p, on)
		var errs []error
		s.Walk(p, func(c Item) {
			if !c.Dir {
				if err := s.pinFile(c.Path, on); err != nil && !errors.Is(err, ErrNotSent) && !errors.Is(err, ErrBusy) {
					errs = append(errs, err)
				}
			}
		})
		return errors.Join(errs...)
	}
	return s.pinFile(p, on)
}

func (s *Service) pinFile(p string, on bool) error {
	_, _, nd, err := s.node(p)
	if err != nil {
		return err
	}
	s.mu.Lock()
	f := s.files[p]
	if f == nil && !on {
		s.mu.Unlock()
		return nil
	}
	f = s.stateLocked(p, nd)
	old := *f
	ev := EvUnpin
	if on {
		ev = EvPin
	}
	act, err := s.applyLocked(p, f, Event{Kind: ev})
	s.mu.Unlock()
	if act != 0 {
		s.act(p, act, old)
	}
	if err == nil {
		s.changed([]Change{{Path: p, Kind: Modified}})
	}
	return err
}

// pinned folders: new files below them are kept on this device too.
func (s *Service) setPinnedDir(p string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := "\x01dir:" + p
	if on {
		s.files[k] = &FileState{Pinned: true}
	} else {
		for q := range s.files {
			if q == k || strings.HasPrefix(q, "\x01dir:"+p+"/") {
				delete(s.files, q)
				s.unsaved[q] = true
			}
		}
		return
	}
	s.unsaved[k] = true
}

// underPinnedLocked tells whether p is in a folder kept on this device.
func (s *Service) underPinnedLocked(p string) bool {
	for d := path.Dir(p); d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if s.files["\x01dir:"+d] != nil {
			return true
		}
	}
	return false
}

// FreeUp frees the space of every file not kept on this device (nor
// changed here, nor open).
func (s *Service) FreeUp() int64 {
	s.mu.Lock()
	var freed int64
	for p, f := range s.files {
		if strings.HasPrefix(p, "\x01") || !f.Evictable() {
			continue
		}
		n := f.Ranges.Total()
		if act, err := s.applyLocked(p, f, Event{Kind: EvEvict}); err == nil && act&ActDrop != 0 {
			_ = os.Remove(s.cacheFile(f.Key))
			freed += n
		}
	}
	s.mu.Unlock()
	return freed
}

// CacheUsage returns the bytes cached and the budget.
func (s *Service) CacheUsage() (used, budget int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used, s.budgetLocked()
}

// Counts returns the number of files kept here and changed here.
func (s *Service) Counts() (pinned, dirty int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, f := range s.files {
		if strings.HasPrefix(p, "\x01") {
			continue
		}
		if f.Pinned {
			pinned++
		}
		if f.Dirty {
			dirty++
		}
	}
	return
}

func (s *Service) budgetLocked() int64 {
	if s.Budget > 0 {
		return s.Budget
	}
	total, free := diskSpace(filepath.Join(s.Dir, "cache"))
	if total == 0 {
		return 10 << 30
	}
	return DefaultBudget(total, free+s.used)
}

func (s *Service) maybeEvict() {
	s.mu.Lock()
	defer s.mu.Unlock()
	budget := s.budgetLocked()
	if s.used <= budget {
		return
	}
	files := map[string]*FileState{}
	for p, f := range s.files {
		if !strings.HasPrefix(p, "\x01") {
			files[p] = f
		}
	}
	for _, p := range PickEvictions(files, s.used, budget, lowWater) {
		f := s.files[p]
		if act, err := s.applyLocked(p, f, Event{Kind: EvEvict}); err == nil && act&ActDrop != 0 {
			_ = os.Remove(s.cacheFile(f.Key))
		}
	}
}

// ---- Saving ----

func (s *Service) saveLoop() {
	defer s.wg.Done()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.flush()
		}
	}
}

func (s *Service) flush() {
	s.mu.Lock()
	if len(s.unsaved) == 0 {
		s.mu.Unlock()
		return
	}
	m := make(map[string]*FileState, len(s.unsaved))
	for p := range s.unsaved {
		if f := s.files[p]; f != nil {
			c := *f
			m[p] = &c
		} else {
			m[p] = nil
		}
	}
	s.unsaved = map[string]bool{}
	s.mu.Unlock()
	if err := s.db.saveFiles(m); err != nil {
		log.Printf("files on demand: save: %v", err)
	}
}

// moveStates follows a move in the states of the files below it.
func (s *Service) moveStates(from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, f := range s.files {
		var np string
		switch {
		case p == from:
			np = to
		case strings.HasPrefix(p, from+"/"):
			np = to + strings.TrimPrefix(p, from)
		case p == "\x01dir:"+from || strings.HasPrefix(p, "\x01dir:"+from+"/"):
			np = "\x01dir:" + to + strings.TrimPrefix(p, "\x01dir:"+from)
		default:
			continue
		}
		delete(s.files, p)
		s.files[np] = f
		s.unsaved[p], s.unsaved[np] = true, true
	}
	for h := range s.handles {
		if h.p == from || strings.HasPrefix(h.p, from+"/") {
			h.p = to + strings.TrimPrefix(h.p, from)
		}
	}
}
