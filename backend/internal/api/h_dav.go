package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/webdav"

	"strataos/internal/accounts"
	"strataos/internal/store"
)

// WebDAV exposes the shares at /dav/<share>/… on the web interface ports, with
// the same rights as SMB: users see the shares they may read, write where they
// may write. Every operation goes through os.Root on the share folder.

// davAuthCache remembers recently verified credentials: DAV clients send the
// password with every request, and bcrypt or a directory check is slow.
type davAuthCache struct {
	mu  sync.Mutex
	key []byte
	ok  map[string]davCached
}

type davCached struct {
	user    string // account the login resolved to (DOMAIN\name for AD)
	expires time.Time
}

func newDavAuthCache() *davAuthCache {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return &davAuthCache{key: k, ok: map[string]davCached{}}
}

func (c *davAuthCache) id(login, pass, hash string) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(login + "\x00" + pass + "\x00" + hash))
	return string(m.Sum(nil))
}

func (c *davAuthCache) check(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.ok[id]
	return e.user, ok && time.Now().Before(e.expires)
}

func (c *davAuthCache) add(id, user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, e := range c.ok {
		if now.After(e.expires) {
			delete(c.ok, k)
		}
	}
	c.ok[id] = davCached{user: user, expires: now.Add(5 * time.Minute)}
}

// davAuth checks HTTP Basic credentials against the StrataOS accounts and,
// when the NAS is in a directory, against Active Directory / LDAP.
func (s *Server) davAuth(w http.ResponseWriter, r *http.Request, cache *davAuthCache) (store.User, bool) {
	deny := func(status int, msg string) (store.User, bool) {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Basic realm="StrataOS", charset="UTF-8"`)
		}
		http.Error(w, msg, status)
		return store.User{}, false
	}
	login, pass, ok := r.BasicAuth()
	if !ok {
		return deny(http.StatusUnauthorized, "authentication required")
	}
	login = strings.ToLower(strings.TrimSpace(login))
	lookup := func(name string) (store.User, bool) {
		var u store.User
		var found bool
		s.store.Read(func(st *store.State) {
			if p := st.User(name); p != nil {
				u, found = *p, true
			}
		})
		return u, found
	}
	u, found := lookup(login)
	key := cache.id(login, pass, u.PasswordHash)
	if name, hit := cache.check(key); hit {
		if cu, ok := lookup(name); ok && !cu.Disabled && cu.TOTPSecret == "" {
			return cu, true
		}
	}
	ip := clientIP(r)
	if !s.limiter.attempt(ip) {
		return deny(http.StatusTooManyRequests, "too many failed attempts, try again in a few minutes")
	}
	valid := false
	switch {
	case found && u.Source == "":
		valid = !u.Disabled && checkPassword(u.PasswordHash, pass)
	default:
		u, valid = s.directoryLogin(r.Context(), login, pass)
		if !valid && !found {
			checkPassword(dummyHash(), pass)
		}
	}
	if !valid {
		s.store.Log("warning", "auth", login, "Failed WebDAV login from "+ip)
		return deny(http.StatusUnauthorized, "invalid credentials")
	}
	if u.TOTPSecret != "" {
		// Basic authentication cannot carry the second factor.
		return deny(http.StatusForbidden, "two-factor authentication is enabled for this account: WebDAV is not available")
	}
	s.limiter.reset(ip)
	cache.add(key, u.Username)
	return u, true
}

func (s *Server) davRoutes() {
	locks := webdav.NewMemLS()
	cache := newDavAuthCache()
	s.mux.Handle("/dav/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var on bool
		s.store.Read(func(st *store.State) { on = st.Files.WebDAV })
		if !on {
			http.NotFound(w, r)
			return
		}
		u, ok := s.davAuth(w, r, cache)
		if !ok {
			return
		}
		if s.ransomBlocked(u.Username) {
			http.Error(w, "account blocked by the ransomware protection", http.StatusForbidden)
			return
		}
		// Files are served from the web interface's origin: never let one run as a page.
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Disposition", "attachment")
		}
		h := &webdav.Handler{
			Prefix:     "/dav",
			FileSystem: &davFS{s: s, ip: clientIP(r), user: u.Username, admin: u.Role == "admin", groups: accounts.UserGroups(u.Username)},
			LockSystem: locks,
		}
		h.ServeHTTP(w, r)
	}))
}

type davFS struct {
	s      *Server
	ip     string
	user   string
	admin  bool
	groups []string
}

// davPath is a resolved WebDAV path: the virtual root, or a path inside a share.
type davPath struct {
	share *store.Share
	write bool
	rel   string // "." for the share root
}

func (f *davFS) shares() []davPath {
	var out []davPath
	f.s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			perm := "rw"
			if !f.admin {
				perm = sharePerm(sh, f.user, f.groups)
			}
			if perm == "rw" || perm == "ro" {
				c := *sh
				out = append(out, davPath{share: &c, write: perm == "rw", rel: "."})
			}
		}
	})
	return out
}

func (f *davFS) resolve(name string) (davPath, error) {
	clean := path.Clean("/" + name)
	if clean == "/" {
		return davPath{rel: "."}, nil
	}
	first, rest, _ := strings.Cut(strings.TrimPrefix(clean, "/"), "/")
	for _, p := range f.shares() {
		if p.share.Name != first {
			continue
		}
		if rest == "" {
			return p, nil
		}
		// The SMB recycle bin stays out of reach, like over SMB.
		if rest == ".recycle" || strings.HasPrefix(rest, ".recycle/") {
			return davPath{}, os.ErrNotExist
		}
		p.rel = rest
		return p, nil
	}
	return davPath{}, os.ErrNotExist
}

func (p davPath) open() (*os.Root, error) {
	// A share folder replaced by a symlink would let os.OpenRoot follow it.
	if fi, err := os.Lstat(p.share.Path); err != nil || !fi.IsDir() {
		return nil, os.ErrNotExist
	}
	return os.OpenRoot(p.share.Path)
}

func (f *davFS) chown(rt *os.Root, rel string) {
	uid, _, err := accounts.IDs(f.user)
	if err != nil {
		return
	}
	gid := -1
	if st, err := rt.Stat(path.Dir(rel)); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			gid = int(sys.Gid)
		}
	}
	_ = rt.Lchown(rel, uid, gid)
}

func (f *davFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	p, err := f.resolve(name)
	if err != nil {
		if p.share == nil && path.Clean("/"+name) != "/" && strings.Count(path.Clean("/"+name), "/") > 1 {
			return err
		}
		return os.ErrPermission
	}
	if p.share == nil || p.rel == "." {
		return os.ErrExist
	}
	if !p.write {
		return os.ErrPermission
	}
	rt, err := p.open()
	if err != nil {
		return err
	}
	defer rt.Close()
	if err := rt.Mkdir(p.rel, 0o770); err != nil {
		return err
	}
	f.chown(rt, p.rel)
	return nil
}

func (f *davFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	p, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	writing := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	if p.share == nil {
		if writing {
			return nil, os.ErrPermission
		}
		return &davRoot{fs: f}, nil
	}
	if writing && (!p.write || p.rel == ".") {
		return nil, os.ErrPermission
	}
	rt, err := p.open()
	if err != nil {
		return nil, err
	}
	defer rt.Close()
	_, statErr := rt.Lstat(p.rel)
	file, err := rt.OpenFile(p.rel, flag, 0o660)
	if err != nil {
		return nil, err
	}
	if flag&os.O_CREATE != 0 && os.IsNotExist(statErr) {
		f.chown(rt, p.rel)
	}
	if p.rel == "." {
		return &namedFile{File: file, name: p.share.Name}, nil
	}
	return file, nil
}

func (f *davFS) RemoveAll(ctx context.Context, name string) error {
	p, err := f.resolve(name)
	if err != nil {
		return err
	}
	if p.share == nil || p.rel == "." || !p.write {
		return os.ErrPermission
	}
	rt, err := p.open()
	if err != nil {
		return err
	}
	defer rt.Close()
	if _, err := rt.Lstat(p.rel); err != nil {
		return err
	}
	f.s.ransomObserve(f.user, f.ip, p.share.Name, p.rel)
	if p.share.RecycleBin {
		// Same place Samba uses: .recycle/<user>/<path>, never overwritten.
		dst := path.Join(".recycle", f.user, p.rel)
		if err := rt.MkdirAll(path.Dir(dst), 0o770); err == nil {
			if _, err := rt.Lstat(dst); err == nil {
				dst += "." + strconv.FormatInt(time.Now().Unix(), 10)
			}
			if err := rt.Rename(p.rel, dst); err == nil {
				now := time.Now()
				_ = rt.Chtimes(dst, now, now)
				return nil
			}
		}
	}
	return rt.RemoveAll(p.rel)
}

func (f *davFS) Rename(ctx context.Context, oldName, newName string) error {
	a, err := f.resolve(oldName)
	if err != nil {
		return err
	}
	b, err := f.resolve(newName)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if b.share == nil {
		// The destination share may not exist or be readable: resolve the parent.
		return os.ErrPermission
	}
	if a.share == nil || a.rel == "." || b.rel == "." || !a.write || !b.write || a.share.Name != b.share.Name {
		return os.ErrPermission
	}
	rt, err := a.open()
	if err != nil {
		return err
	}
	defer rt.Close()
	if err := rt.Rename(a.rel, b.rel); err != nil {
		return err
	}
	f.s.ransomObserve(f.user, f.ip, a.share.Name, b.rel)
	return nil
}

func (f *davFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	p, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	if p.share == nil {
		return rootInfo{}, nil
	}
	rt, err := p.open()
	if err != nil {
		return nil, err
	}
	defer rt.Close()
	fi, err := rt.Stat(p.rel)
	if err != nil {
		return nil, err
	}
	if p.rel == "." {
		return namedInfo{FileInfo: fi, name: p.share.Name}, nil
	}
	return fi, nil
}

// namedFile / namedInfo present a share folder under the share's name.
type namedFile struct {
	*os.File
	name string
}

func (n *namedFile) Stat() (os.FileInfo, error) {
	fi, err := n.File.Stat()
	if err != nil {
		return nil, err
	}
	return namedInfo{FileInfo: fi, name: n.name}, nil
}

func (n *namedFile) Readdir(count int) ([]fs.FileInfo, error) {
	list, err := n.File.Readdir(count)
	out := list[:0]
	for _, fi := range list {
		if fi.Name() != ".recycle" {
			out = append(out, fi)
		}
	}
	return out, err
}

type namedInfo struct {
	os.FileInfo
	name string
}

func (n namedInfo) Name() string { return n.name }

// davRoot is the virtual folder listing the shares of the user.
type davRoot struct {
	fs   *davFS
	done bool
}

type rootInfo struct{}

func (rootInfo) Name() string       { return "/" }
func (rootInfo) Size() int64        { return 0 }
func (rootInfo) Mode() os.FileMode  { return os.ModeDir | 0o555 }
func (rootInfo) ModTime() time.Time { return time.Time{} }
func (rootInfo) IsDir() bool        { return true }
func (rootInfo) Sys() any           { return nil }

func (d *davRoot) Close() error                   { return nil }
func (d *davRoot) Read([]byte) (int, error)       { return 0, io.EOF }
func (d *davRoot) Write([]byte) (int, error)      { return 0, os.ErrPermission }
func (d *davRoot) Seek(int64, int) (int64, error) { return 0, nil }
func (d *davRoot) Stat() (os.FileInfo, error)     { return rootInfo{}, nil }
func (d *davRoot) Readdir(count int) ([]fs.FileInfo, error) {
	if d.done {
		if count > 0 {
			return nil, io.EOF
		}
		return nil, nil
	}
	d.done = true
	var out []fs.FileInfo
	for _, p := range d.fs.shares() {
		if fi, err := os.Stat(p.share.Path); err == nil && fi.IsDir() {
			out = append(out, namedInfo{FileInfo: fi, name: p.share.Name})
		}
	}
	return out, nil
}
