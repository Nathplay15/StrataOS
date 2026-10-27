package syncjournal

import (
	"encoding/binary"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Watcher follows the folders of a journal with inotify and rescans the
// ones that changed. inotify sees the changes of every local process and of
// the kernel NFS server; when the watch limit is reached, the journal
// falls back on periodic full scans (Degraded).
type Watcher struct {
	j    *Journal
	fd   int
	mu   sync.Mutex
	wds  map[int]string // watch → folder
	byP  map[string]int
	full bool        // a watch could not be added
	busy atomic.Bool // changes seen, not rescanned yet
	stop chan struct{}
	done chan struct{}
}

const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_CLOSE_WRITE |
	unix.IN_ATTRIB | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_ONLYDIR | unix.IN_DONT_FOLLOW | unix.IN_EXCL_UNLINK

// Watch starts following j (after its first full scan).
func Watch(j *Journal) (*Watcher, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	w := &Watcher{j: j, fd: fd, wds: map[int]string{}, byP: map[string]int{}, stop: make(chan struct{}), done: make(chan struct{})}
	raiseWatchLimit()
	w.add(j.Dirs())
	go w.loop()
	return w, nil
}

// Degraded tells whether some folders are not watched (limit reached).
func (w *Watcher) Degraded() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.full
}

// Pending tells whether changes were reported and not rescanned yet
// (including events still queued in the kernel).
func (w *Watcher) Pending() bool {
	if w.busy.Load() {
		return true
	}
	n, err := unix.IoctlGetInt(w.fd, unix.TIOCINQ) // FIONREAD: bytes queued
	return err == nil && n > 0
}

// Watching returns the number of folders watched (0: the root is gone).
func (w *Watcher) Watching() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.byP)
}

// Close stops the watcher.
func (w *Watcher) Close() {
	close(w.stop)
	<-w.done
}

func (w *Watcher) add(dirs []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, d := range dirs {
		if _, ok := w.byP[d]; ok {
			continue
		}
		p := w.j.Root
		if d != "" {
			p += "/" + d
		}
		wd, err := unix.InotifyAddWatch(w.fd, p, watchMask)
		if err != nil {
			if errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.ENOMEM) {
				w.full = true
			}
			continue
		}
		if old, ok := w.wds[wd]; ok && old != d {
			delete(w.byP, old) // the same folder seen under a new name
		}
		w.wds[wd] = d
		w.byP[d] = wd
	}
}

// forget drops the watches of a folder that moved away, and of its
// subfolders: their paths are stale (the rescan of the parent adds them again).
func (w *Watcher) forget(d string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for p, wd := range w.byP {
		if p == d || d == "" || strings.HasPrefix(p, d+"/") {
			_, _ = unix.InotifyRmWatch(w.fd, uint32(wd))
			delete(w.byP, p)
			delete(w.wds, wd)
		}
	}
}

func (w *Watcher) loop() {
	defer close(w.done)
	defer unix.Close(w.fd)
	buf := make([]byte, 256*1024)
	dirty := map[string]bool{}
	overflow := false
	var first time.Time
	flush := func() {
		if overflow {
			if added, err := w.j.Scan("", true); err == nil {
				w.add(added)
			}
		} else {
			for d := range dirty {
				if added, err := w.j.Scan(d, false); err == nil {
					w.add(added)
				}
			}
		}
		clear(dirty)
		overflow, first = false, time.Time{}
		w.busy.Store(false)
	}
	for {
		select {
		case <-w.stop:
			return
		default:
		}
		n, _ := unix.Poll([]unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}}, 300)
		if n <= 0 && (len(dirty) > 0 || overflow) {
			// Quiet for a moment: rescan what changed.
			flush()
			continue
		}
		if n <= 0 {
			continue
		}
		w.busy.Store(true) // before the queue empties: Pending never misses a change
		r, err := unix.Read(w.fd, buf)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil || r <= 0 {
			return
		}
		for off := 0; off+unix.SizeofInotifyEvent <= r; {
			wd := int(int32(binary.NativeEndian.Uint32(buf[off:])))
			mask := binary.NativeEndian.Uint32(buf[off+4:])
			nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
			off += unix.SizeofInotifyEvent + nameLen
			if mask&unix.IN_Q_OVERFLOW != 0 {
				overflow = true // events lost: everything
				continue
			}
			w.mu.Lock()
			d, ok := w.wds[wd]
			if mask&unix.IN_IGNORED != 0 {
				delete(w.wds, wd)
				if ok && w.byP[d] == wd {
					delete(w.byP, d)
				}
			}
			w.mu.Unlock()
			if !ok {
				continue
			}
			if mask&(unix.IN_MOVE_SELF|unix.IN_DELETE_SELF) != 0 {
				w.forget(d)
				dirty[parent(d)] = true
				continue
			}
			if first.IsZero() {
				first = time.Now()
			}
			dirty[d] = true
			w.busy.Store(true)
		}
		if len(dirty) == 0 && !overflow {
			w.busy.Store(false) // only events of nothing indexed
		}
		// A busy folder is still rescanned at least every 2 seconds.
		if !first.IsZero() && time.Since(first) > 2*time.Second {
			flush()
		}
	}
}

// raiseWatchLimit lets the daemon watch large trees: one watch per folder.
func raiseWatchLimit() {
	const want = 524288
	const f = "/proc/sys/fs/inotify/max_user_watches"
	b, err := os.ReadFile(f)
	if err != nil {
		return
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n < want {
		_ = os.WriteFile(f, []byte(strconv.Itoa(want)), 0o644)
	}
}
