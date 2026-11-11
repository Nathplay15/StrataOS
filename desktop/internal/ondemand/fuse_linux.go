package ondemand

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Linux has no system API for files on demand: the shared folders are a
// FUSE file system mounted in the user's home (go-fuse, through the
// fusermount3 helper: no root needed). Reads download what they need into
// the cache, writes go to the cache and are sent when the file is closed.
// "Keep on this device" and "Free up space" are the extended attribute
// user.strataos.pinned (1/0; the file manager menus and `strataos-sync pin`
// set it), user.strataos.status tells the state.

func supported() (bool, string) { return true, "fuse" }

const (
	xattrStatus = "user.strataos.status"
	xattrPinned = "user.strataos.pinned"
)

type fuseProvider struct {
	s      *Service
	root   *odNode
	srv    *fuse.Server
	dir    string
	notes  chan []Change
	done   chan struct{}
	mu     sync.Mutex
	errMsg string
}

// StartProvider mounts the shared folders at o.Root.
func StartProvider(s *Service, o ProviderOptions) (Provider, error) {
	if o.Root == "" {
		return nil, errors.New("no folder for files on demand")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		return nil, errors.New("FUSE is not available (/dev/fuse missing)")
	}
	unmountStale(o.Root)
	if err := os.MkdirAll(o.Root, 0o755); err != nil {
		return nil, err
	}
	if ents, _ := os.ReadDir(o.Root); len(ents) > 0 {
		return nil, fmt.Errorf("%s is not empty", o.Root)
	}
	p := &fuseProvider{s: s, dir: o.Root, notes: make(chan []Change, 256), done: make(chan struct{})}
	p.root = &odNode{p: p}
	sec := time.Second
	neg := 500 * time.Millisecond
	srv, err := fs.Mount(o.Root, p.root, &fs.Options{
		EntryTimeout: &sec, AttrTimeout: &sec, NegativeTimeout: &neg,
		UID: uint32(os.Getuid()), GID: uint32(os.Getgid()),
		MountOptions: fuse.MountOptions{
			FsName: "strataos", Name: "strataos",
			DirectMount:  true, // as root (containers); fusermount3 otherwise
			MaxReadAhead: 1 << 20, MaxBackground: 32,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("mount %s: %w", o.Root, err)
	}
	p.srv = srv
	s.OnChange = func(ch []Change) {
		select {
		case p.notes <- ch:
		default: // the timeouts catch up
		}
	}
	go p.notifyLoop()
	return p, nil
}

// unmountStale removes a mount left by an agent that died.
func unmountStale(dir string) {
	if _, err := os.Stat(dir); err == nil || !errors.Is(err, syscall.ENOTCONN) {
		if !isMount(dir) {
			return
		}
	}
	for _, bin := range []string{"fusermount3", "fusermount"} {
		if exec.Command(bin, "-u", "-z", dir).Run() == nil {
			return
		}
	}
}

func isMount(dir string) bool {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	esc := strings.NewReplacer(" ", `\040`, "\t", `\011`, "\n", `\012`, `\`, `\134`).Replace(dir)
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) > 4 && f[4] == esc {
			return true
		}
	}
	return false
}

func (p *fuseProvider) Info() ProviderInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := "ready"
	if p.errMsg != "" {
		st = "error"
	}
	return ProviderInfo{Kind: "fuse", Root: p.dir, State: st, Error: p.errMsg}
}

func (p *fuseProvider) Pin(path string, on bool) error { return p.s.Pin(path, on) }

func (p *fuseProvider) Close() error {
	p.s.OnChange = nil
	close(p.done)
	err := p.srv.Unmount()
	if err != nil {
		unmountStale(p.dir)
	}
	return err
}

// notifyLoop tells the kernel about changes on the NAS (outside of the
// requests: notifying from inside one can deadlock).
func (p *fuseProvider) notifyLoop() {
	for {
		select {
		case <-p.done:
			return
		case ch := <-p.notes:
			for _, c := range ch {
				dir, name := path.Split(c.Path)
				parent := p.lookup(strings.TrimSuffix(dir, "/"))
				if parent == nil {
					continue
				}
				switch c.Kind {
				case Modified:
					if n := parent.GetChild(name); n != nil {
						n.NotifyContent(0, 0)
					}
				default:
					parent.NotifyEntry(name)
				}
			}
		}
	}
}

// lookup finds the inode of a path the kernel knows (nil: none).
func (p *fuseProvider) lookup(rel string) *fs.Inode {
	n := p.root.EmbeddedInode()
	if rel == "" {
		return n
	}
	for _, part := range strings.Split(rel, "/") {
		if n = n.GetChild(part); n == nil {
			return nil
		}
	}
	return n
}

// ---- Nodes ----

type odNode struct {
	fs.Inode
	p *fuseProvider
}

var (
	_ fs.NodeLookuper    = (*odNode)(nil)
	_ fs.NodeReaddirer   = (*odNode)(nil)
	_ fs.NodeGetattrer   = (*odNode)(nil)
	_ fs.NodeSetattrer   = (*odNode)(nil)
	_ fs.NodeOpener      = (*odNode)(nil)
	_ fs.NodeCreater     = (*odNode)(nil)
	_ fs.NodeMkdirer     = (*odNode)(nil)
	_ fs.NodeUnlinker    = (*odNode)(nil)
	_ fs.NodeRmdirer     = (*odNode)(nil)
	_ fs.NodeRenamer     = (*odNode)(nil)
	_ fs.NodeGetxattrer  = (*odNode)(nil)
	_ fs.NodeSetxattrer  = (*odNode)(nil)
	_ fs.NodeListxattrer = (*odNode)(nil)
	_ fs.NodeStatfser    = (*odNode)(nil)
)

func (n *odNode) rel() string { return n.Path(n.Root()) }

func (n *odNode) child(name string) string {
	if r := n.rel(); r != "" {
		return r + "/" + name
	}
	return name
}

// errno maps the service's errors to the file system's.
func errno(err error) syscall.Errno {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrNotFound):
		return syscall.ENOENT
	case errors.Is(err, ErrReadOnly), errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, ErrExists):
		return syscall.EEXIST
	case errors.Is(err, ErrIsDir):
		return syscall.EISDIR
	case errors.Is(err, ErrNotEmpty):
		return syscall.ENOTEMPTY
	case errors.Is(err, ErrCrossShare):
		return syscall.EXDEV
	case errors.Is(err, ErrStale):
		return syscall.ESTALE
	case errors.Is(err, context.Canceled):
		return syscall.EINTR
	}
	log.Printf("files on demand: %v", err)
	return syscall.EIO
}

func (n *odNode) fill(it Item, out *fuse.Attr) {
	mode := uint32(0o644)
	if it.Dir {
		mode = syscall.S_IFDIR | 0o755
	} else {
		mode |= syscall.S_IFREG
	}
	if !it.Writable {
		mode &^= 0o222
	}
	out.Mode = mode
	out.Size = uint64(it.Size)
	t := time.UnixMilli(it.MTime)
	out.SetTimes(nil, &t, &t)
	out.Nlink = 1
	out.Uid, out.Gid = uint32(os.Getuid()), uint32(os.Getgid())
	out.Blksize = 1 << 16
	if !it.Dir {
		// du and file managers see the space really used here.
		n.p.s.mu.Lock()
		if f := n.p.s.files[it.Path]; f != nil {
			out.Blocks = uint64((f.Ranges.Total() + 511) / 512)
		}
		n.p.s.mu.Unlock()
	}
}

func (n *odNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	it, err := n.p.s.Stat(n.child(name))
	if err != nil {
		return nil, errno(err)
	}
	n.fill(it, &out.Attr)
	mode := uint32(syscall.S_IFREG)
	if it.Dir {
		mode = syscall.S_IFDIR
	}
	if c := n.GetChild(name); c != nil && c.Mode() == mode {
		return c, 0
	}
	return n.NewInode(ctx, &odNode{p: n.p}, fs.StableAttr{Mode: mode}), 0
}

func (n *odNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	l, err := n.p.s.List(n.rel())
	if err != nil {
		return nil, errno(err)
	}
	out := make([]fuse.DirEntry, 0, len(l))
	for _, it := range l {
		m := uint32(syscall.S_IFREG)
		if it.Dir {
			m = syscall.S_IFDIR
		}
		out = append(out, fuse.DirEntry{Name: it.Name, Mode: m})
	}
	return fs.NewListDirStream(out), 0
}

func (n *odNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	it, err := n.p.s.Stat(n.rel())
	if err != nil {
		return errno(err)
	}
	n.fill(it, &out.Attr)
	if h, ok := f.(*odFile); ok && h.h.write {
		out.Size = uint64(h.h.Size())
	}
	return 0
}

func (n *odNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	p := n.rel()
	if sz, ok := in.GetSize(); ok {
		h, own := (*Handle)(nil), false
		if of, ok := f.(*odFile); ok && of.h.write {
			h = of.h
		} else {
			var err error
			if h, err = n.p.s.OpenFile(ctx, p, true, sz == 0); err != nil {
				return errno(err)
			}
			own = true
		}
		err := h.Truncate(int64(sz))
		if own {
			h.Close()
		}
		if err != nil {
			return errno(err)
		}
	}
	if mt, ok := in.GetMTime(); ok {
		n.p.s.SetMTime(p, mt.UnixMilli()) // files changed here; the NAS keeps its dates otherwise
	}
	return n.Getattr(ctx, f, out)
}

// thumbnailers read every file a folder shows: they must not download
// files that are only on the NAS.
var thumbnailers = []string{"gnome-thumbnail", "totem-video-thu", "ffmpegthumbnail", "gdk-pixbuf-thum", "tracker-extract", "localsearch-ext", "baloo_file_extr", "kioworker", "thumbnail.so", "tumblerd", "evince-thumbnai", "gsf-office-thum"}

// Thumbnailer tells whether a process name (/proc/PID/comm) is a thumbnailer
// or indexer.
func Thumbnailer(comm string) bool {
	comm = strings.TrimSpace(comm)
	for _, t := range thumbnailers {
		if strings.HasPrefix(comm, t) {
			return true
		}
	}
	return false
}

func callerIsThumbnailer(ctx context.Context) bool {
	c, ok := fuse.FromContext(ctx)
	if !ok || c.Pid == 0 {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", c.Pid))
	return err == nil && Thumbnailer(string(b))
}

func (n *odNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	p := n.rel()
	write := flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0
	if !write && callerIsThumbnailer(ctx) {
		if it, err := n.p.s.Stat(p); err == nil && it.Status != StLocal && it.Status != StPinned && it.Status != StDirty {
			return nil, 0, syscall.EACCES
		}
	}
	h, err := n.p.s.OpenFile(ctx, p, write, flags&syscall.O_TRUNC != 0)
	if err != nil {
		return nil, 0, errno(err)
	}
	return &odFile{h: h}, 0, 0
}

func (n *odNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	p := n.child(name)
	h, err := n.p.s.Create(ctx, p)
	if err != nil {
		return nil, nil, 0, errno(err)
	}
	it, err := n.p.s.Stat(p)
	if err != nil {
		h.Close()
		return nil, nil, 0, errno(err)
	}
	n.fill(it, &out.Attr)
	ch := n.NewInode(ctx, &odNode{p: n.p}, fs.StableAttr{Mode: syscall.S_IFREG})
	return ch, &odFile{h: h}, 0, 0
}

func (n *odNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	it, err := n.p.s.Mkdir(ctx, n.child(name))
	if err != nil {
		return nil, errno(err)
	}
	n.fill(it, &out.Attr)
	return n.NewInode(ctx, &odNode{p: n.p}, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

func (n *odNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return errno(n.p.s.Delete(ctx, n.child(name)))
}

func (n *odNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return errno(n.p.s.Delete(ctx, n.child(name)))
}

func (n *odNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	const noReplace = 1 // RENAME_NOREPLACE
	if flags&^noReplace != 0 {
		return syscall.EINVAL // RENAME_EXCHANGE, RENAME_WHITEOUT
	}
	np, ok := newParent.(*odNode)
	if !ok {
		return syscall.EXDEV
	}
	from, to := n.child(name), np.child(newName)
	if _, err := n.p.s.Stat(to); err == nil {
		if flags&noReplace != 0 {
			return syscall.EEXIST
		}
		return errno(n.p.s.Replace(ctx, from, to))
	}
	return errno(n.p.s.Move(ctx, from, to))
}

func (n *odNode) Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	it, err := n.p.s.Stat(n.rel())
	if err != nil {
		return 0, errno(err)
	}
	var v string
	switch attr {
	case xattrStatus:
		v = string(it.Status)
		if it.Dir {
			v = "folder"
		}
	case xattrPinned:
		v = "0"
		if it.Status == StPinned || it.Status == StPinning || it.Dir && n.p.s.pinnedDir(it.Path) {
			v = "1"
		}
	default:
		return 0, syscall.ENODATA
	}
	if len(dest) < len(v) {
		return uint32(len(v)), syscall.ERANGE
	}
	return uint32(copy(dest, v)), 0
}

func (n *odNode) Setxattr(ctx context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	if attr != xattrPinned {
		return syscall.ENOTSUP
	}
	on := strings.TrimSpace(string(data)) == "1"
	return errno(n.p.s.Pin(n.rel(), on))
}

func (n *odNode) Listxattr(ctx context.Context, dest []byte) (uint32, syscall.Errno) {
	v := xattrStatus + "\x00" + xattrPinned + "\x00"
	if len(dest) < len(v) {
		return uint32(len(v)), syscall.ERANGE
	}
	return uint32(copy(dest, v)), 0
}

func (n *odNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	total, free := diskSpace(n.p.s.Dir)
	out.Bsize = 4096
	out.Frsize = 4096
	out.Blocks = uint64(total / 4096)
	out.Bfree = uint64(free / 4096)
	out.Bavail = out.Bfree
	out.NameLen = 255
	return 0
}

// ---- Open files ----

type odFile struct{ h *Handle }

var (
	_ fs.FileReader   = (*odFile)(nil)
	_ fs.FileWriter   = (*odFile)(nil)
	_ fs.FileReleaser = (*odFile)(nil)
	_ fs.FileFsyncer  = (*odFile)(nil)
	_ fs.FileFlusher  = (*odFile)(nil)
)

func (f *odFile) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n, err := f.h.ReadAt(ctx, dest, off)
	if err != nil && n == 0 && !errors.Is(err, io.EOF) {
		return nil, errno(err)
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (f *odFile) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	n, err := f.h.WriteAt(data, off)
	if err != nil {
		return uint32(n), errno(err)
	}
	return uint32(n), 0
}

func (f *odFile) Flush(ctx context.Context) syscall.Errno { return 0 }

func (f *odFile) Fsync(ctx context.Context, flags uint32) syscall.Errno { return 0 }

func (f *odFile) Release(ctx context.Context) syscall.Errno {
	f.h.Close()
	return 0
}
