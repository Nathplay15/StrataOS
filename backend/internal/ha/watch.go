package ha

// Recursive inotify watcher used for near-continuous replication: it reports
// the folders whose content changed, so that only those are copied.

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// Change is a folder to copy again; Recursive when its whole tree is new
// (created or moved in) or when events were lost.
type Change struct {
	Dir       string
	Recursive bool
}

// Watcher follows the folders under some roots.
type Watcher struct {
	fd      int
	mu      sync.Mutex
	dirs    map[int]string // watch descriptor → folder
	roots   []string
	Changes chan Change
	// Full is set when the kernel limit of watches is reached: some folders
	// are not followed and only the periodic replication covers them.
	Full bool
}

const watchMask = syscall.IN_CLOSE_WRITE | syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MOVED_FROM |
	syscall.IN_MOVED_TO | syscall.IN_ATTRIB | syscall.IN_DELETE_SELF

// raiseWatchLimit makes room for large shares (one watch per folder).
func raiseWatchLimit(n int) {
	const p = "/proc/sys/fs/inotify/max_user_watches"
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	if cur, _ := strconv.Atoi(strings.TrimSpace(string(b))); cur < n {
		_ = os.WriteFile(p, []byte(strconv.Itoa(n)), 0o644)
	}
}

func skipWatch(name string) bool { return name == ".recycle" || name == ".strata-versions" || name == "#recycle" || name == "@eaDir" }

// NewWatcher starts following roots and their sub-folders.
func NewWatcher(roots []string) (*Watcher, error) {
	raiseWatchLimit(524288)
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	w := &Watcher{fd: fd, dirs: map[int]string{}, roots: roots, Changes: make(chan Change, 1024)}
	for _, r := range roots {
		w.addTree(r)
	}
	go w.read()
	return w, nil
}

func (w *Watcher) add(dir string) {
	wd, err := syscall.InotifyAddWatch(w.fd, dir, watchMask)
	if err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			w.Full = true
		}
		return
	}
	w.mu.Lock()
	w.dirs[wd] = dir
	w.mu.Unlock()
}

func (w *Watcher) addTree(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != root && skipWatch(d.Name()) {
			return filepath.SkipDir
		}
		w.add(p)
		return nil
	})
}

// Close stops the watcher (Changes is closed).
func (w *Watcher) Close() { _ = syscall.Close(w.fd) }

func (w *Watcher) send(c Change) {
	select {
	case w.Changes <- c:
	default: // the consumer is late: it will copy everything anyway
	}
}

func (w *Watcher) read() {
	defer close(w.Changes)
	buf := make([]byte, 64*1024)
	for {
		n, err := syscall.Read(w.fd, buf)
		if err != nil || n <= 0 {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := ""
			if ev.Len > 0 {
				raw := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+int(ev.Len)]
				name = string(bytes.TrimRight(raw, "\x00"))
			}
			off += syscall.SizeofInotifyEvent + int(ev.Len)
			if ev.Mask&syscall.IN_Q_OVERFLOW != 0 {
				for _, r := range w.roots {
					w.send(Change{r, true})
				}
				continue
			}
			w.mu.Lock()
			dir, ok := w.dirs[int(ev.Wd)]
			if ev.Mask&syscall.IN_IGNORED != 0 {
				delete(w.dirs, int(ev.Wd))
			}
			w.mu.Unlock()
			if !ok || skipWatch(name) {
				continue
			}
			isDir := ev.Mask&syscall.IN_ISDIR != 0
			switch {
			case isDir && ev.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0:
				// A new tree: follow it and copy it whole (files may already be in it).
				sub := filepath.Join(dir, name)
				w.addTree(sub)
				w.send(Change{dir, false})
				w.send(Change{sub, true})
			case ev.Mask&syscall.IN_DELETE_SELF != 0:
				w.send(Change{filepath.Dir(dir), false})
			case ev.Mask&syscall.IN_IGNORED != 0:
			default:
				w.send(Change{dir, false})
			}
		}
	}
}
