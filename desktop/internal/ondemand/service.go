// Package ondemand shows the shared folders of a NAS in the computer's file
// manager without copying them: every file is there with its name, size
// and date, its contents are downloaded when it is opened, and the user
// can keep files on this device or free their space.
//
// The Service is the part every system shares: the trees of the shared
// folders (followed with the change feed of the Sync API, saved in
// ondemand.db), ranged downloads, uploads of changed files (conditional,
// with a conflict copy when both sides changed), folder operations, and
// on Linux the cache of downloaded parts with its eviction. Each system
// plugs it into its own file manager: a FUSE mount on Linux (fuse_linux.go),
// the Cloud Files API on Windows (cfapi_windows.go), a File Provider
// extension on macOS (desktop/macos, through the agent's /v1/fp API).
//
// Paths are "Share/folder/file": the first part is the shared folder's name.
package ondemand

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"strataos-sync/internal/engine"
)

// Share is a shared folder of the NAS shown at the top.
type Share struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
}

// Item is a file or folder as file managers see it.
type Item struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Dir      bool   `json:"dir,omitempty"`
	Size     int64  `json:"size"`
	MTime    int64  `json:"mtime"` // ms
	Version  string `json:"version,omitempty"`
	ID       string `json:"id,omitempty"`
	Writable bool   `json:"writable"`
	Status   Status `json:"status,omitempty"` // files, from the cache (Linux)
}

// Errors of operations.
var (
	ErrNotFound = errors.New("no such file or folder")
	ErrReadOnly = errors.New("read-only")
	ErrExists   = errors.New("already exists")
	ErrIsDir    = errors.New("is a folder")
	ErrNotEmpty = errors.New("the folder is not empty")
	ErrOffline  = errors.New("the NAS cannot be reached")
	ErrStale    = errors.New("the file changed on the NAS")
)

// RangeRemote downloads part of a file (engine.APIRemote does).
type RangeRemote interface {
	DownloadRange(ctx context.Context, folder, rel, version string, off, n int64, w io.Writer) error
}

// Service is files on demand for one account.
type Service struct {
	Remote engine.Remote
	// Shares lists the shared folders the user may follow.
	Shares func(ctx context.Context) ([]Share, error)
	Dir    string // ondemand.db and cache/
	Device string // for conflict names
	// Budget is the size of the cache of downloaded files (Linux); 0: DefaultBudget.
	Budget int64
	// Notify tells the user (conflicts, errors).
	Notify func(engine.Event)
	// OnChange tells the file manager what changed on the NAS.
	OnChange func([]Change)
	// Cache: keep downloaded bytes in Dir/cache (Linux); off elsewhere,
	// where the system keeps the files itself.
	Cache bool

	mu      sync.Mutex
	db      *db
	srcs    map[string]*source
	files   map[string]*FileState
	unsaved map[string]bool
	locks   map[string]*sync.Mutex
	handles map[*Handle]bool
	busy    map[string]bool // files being sent: their echo in the feed is ours
	used    int64
	log     changeLog
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	upKick  chan struct{}
	state   string // "", "starting", "ready", "offline"
	lastErr string
	shares  time.Time
}

type source struct {
	share  Share
	tree   *Tree
	cancel context.CancelFunc
	ready  bool // listed once
}

// Open loads the saved state; Start runs it.
func (s *Service) Open() error {
	if err := os.MkdirAll(filepath.Join(s.Dir, "cache"), 0o700); err != nil {
		return err
	}
	d, err := openDB(filepath.Join(s.Dir, "ondemand.db"))
	if err != nil {
		return err
	}
	s.db = d
	s.srcs = map[string]*source{}
	s.files = d.loadFiles()
	s.unsaved = map[string]bool{}
	s.locks = map[string]*sync.Mutex{}
	s.handles = map[*Handle]bool{}
	s.upKick = make(chan struct{}, 1)
	s.log.init()
	for _, f := range s.files {
		s.used += f.Ranges.Total()
	}
	return nil
}

// Start follows the shared folders until Close.
func (s *Service) Start(ctx context.Context) {
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.state = "starting"
	s.wg.Add(2)
	go s.sharesLoop()
	go s.uploadLoop()
	if s.Cache {
		s.wg.Add(1)
		go s.saveLoop()
	}
}

// Close stops and saves.
func (s *Service) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	if s.db == nil {
		return nil
	}
	s.flush()
	return s.db.close()
}

// ---- Paths ----

// Split cuts "Share/rel" into the share and the path inside it.
func Split(p string) (share, rel string) {
	p = strings.Trim(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

// CleanPath checks a path given by a file manager or the window.
func CleanPath(p string) (string, error) {
	p = strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/")
	if p == "" {
		return "", nil
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) {
			return "", fmt.Errorf("invalid path: %q", p)
		}
	}
	return p, nil
}

func (s *Service) src(name string) *source {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srcs[name]
}

func item(sh *source, rel string, n Node) Item {
	name := path.Base(rel)
	full := sh.share.Name
	if rel != "" {
		full += "/" + rel
	} else {
		name = sh.share.Name
	}
	return Item{Path: full, Name: name, Dir: n.Dir, Size: n.Size, MTime: n.MTime, Version: n.Version, ID: n.ID, Writable: sh.share.Writable}
}

// Stat returns an item ("" is the root).
func (s *Service) Stat(p string) (Item, error) {
	if p == "" {
		return Item{Dir: true, MTime: time.Now().UnixMilli()}, nil
	}
	name, rel := Split(p)
	sh := s.src(name)
	if sh == nil {
		return Item{}, ErrNotFound
	}
	n, ok := sh.tree.Get(rel)
	if !ok {
		return Item{}, ErrNotFound
	}
	it := item(sh, rel, n)
	if !it.Dir {
		s.mu.Lock()
		if f := s.files[p]; f != nil {
			it.Status = f.Status()
			if f.Dirty {
				it.Size, it.MTime = f.Size, max(f.MTime, it.MTime)
			}
		} else if s.Cache {
			it.Status = StCloud
		}
		s.mu.Unlock()
	}
	return it, nil
}

// List returns the items of a folder ("": the shared folders).
func (s *Service) List(dir string) ([]Item, error) {
	if dir == "" {
		s.mu.Lock()
		var out []Item
		for _, sh := range s.srcs {
			out = append(out, Item{Path: sh.share.Name, Name: sh.share.Name, Dir: true, Writable: sh.share.Writable, MTime: time.Now().UnixMilli()})
		}
		s.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out, nil
	}
	name, rel := Split(dir)
	sh := s.src(name)
	if sh == nil {
		return nil, ErrNotFound
	}
	nodes, ok := sh.tree.List(rel)
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]Item, 0, len(nodes))
	s.mu.Lock()
	for _, n := range nodes {
		it := item(sh, n.Path, n)
		if !it.Dir {
			if f := s.files[it.Path]; f != nil {
				it.Status = f.Status()
				if f.Dirty {
					it.Size = f.Size
				}
			} else if s.Cache {
				it.Status = StCloud
			}
		}
		out = append(out, it)
	}
	s.mu.Unlock()
	return out, nil
}

// Walk calls f for every item below dir (itself excluded).
func (s *Service) Walk(dir string, f func(Item)) {
	l, err := s.List(dir)
	if err != nil {
		return
	}
	for _, it := range l {
		f(it)
		if it.Dir {
			s.Walk(it.Path, f)
		}
	}
}

// RemotePath is the absolute NAS path of "Share/rel".
func (s *Service) RemotePath(p string) (string, error) {
	name, rel := Split(p)
	x := s.src(name)
	if x == nil || x.share.Path == "" {
		return "", ErrNotFound
	}
	if rel == "" {
		return x.share.Path, nil
	}
	return x.share.Path + "/" + rel, nil
}

// Hidden lists the encrypted folders left out, as "Share/path".
func (s *Service) Hidden() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, sh := range s.srcs {
		for _, h := range sh.tree.Hidden() {
			out = append(out, sh.share.Name+"/"+h)
		}
	}
	sort.Strings(out)
	return out
}

// Ready tells whether every shared folder was listed once.
func (s *Service) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "ready" {
		return false
	}
	for _, sh := range s.srcs {
		if !sh.ready {
			return false
		}
	}
	return true
}

// ---- Following the NAS ----

func (s *Service) sharesLoop() {
	defer s.wg.Done()
	// The saved trees show at once, before the NAS answers.
	if l := s.db.shareNames(); len(l) > 0 {
		var saved []Share
		for _, n := range l {
			saved = append(saved, Share{Name: n})
		}
		s.setShares(saved)
	}
	for {
		l, err := s.Shares(s.ctx)
		if s.ctx.Err() != nil {
			s.mu.Lock()
			for _, sh := range s.srcs {
				sh.cancel()
			}
			s.mu.Unlock()
			return
		}
		next := 5 * time.Minute
		if err != nil {
			s.setErr(err)
			next = 30 * time.Second
		} else {
			s.setShares(l)
		}
		select {
		case <-s.ctx.Done():
		case <-time.After(next):
		}
	}
}

func (s *Service) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.lastErr = ""
		if s.state != "ready" {
			s.state = "ready"
		}
		return
	}
	s.lastErr = err.Error()
	s.state = "offline"
}

// setShares starts following new shared folders and drops the others.
// Shares without a path are the saved trees shown before the NAS answers.
func (s *Service) setShares(l []Share) {
	s.mu.Lock()
	want := map[string]Share{}
	for _, sh := range l {
		if sh.Name == "" || strings.ContainsAny(sh.Name, "/\\\x00") || sh.Name == "." || sh.Name == ".." {
			continue
		}
		want[sh.Name] = sh
	}
	var added, removed []string
	for name, x := range s.srcs {
		if _, ok := want[name]; !ok {
			x.cancel()
			delete(s.srcs, name)
			removed = append(removed, name)
		}
	}
	for name, sh := range want {
		old, ok := s.srcs[name]
		if ok && old.share.Path == sh.Path {
			old.share.Writable = sh.Writable
			continue
		}
		if ok {
			old.cancel()
			if old.share.Path != "" {
				_ = s.db.dropTree(name) // another folder under the same name
			}
		} else {
			added = append(added, name)
		}
		ctx, cancel := context.WithCancel(s.ctx)
		x := &source{share: sh, tree: s.db.loadTree(name), cancel: cancel}
		x.ready = x.tree.Cursor() != ""
		s.srcs[name] = x
		if sh.Path != "" {
			s.wg.Add(1)
			go s.follow(ctx, x)
		}
	}
	s.mu.Unlock()
	for _, name := range removed {
		_ = s.db.dropTree(name)
	}
	var ch []Change
	for _, n := range added {
		ch = append(ch, Change{Path: n, Kind: Added, Dir: true})
	}
	for _, n := range removed {
		ch = append(ch, Change{Path: n, Kind: Removed, Dir: true})
	}
	s.changed(ch)
}

// follow keeps the tree of a shared folder up to date (long poll).
func (s *Service) follow(ctx context.Context, x *source) {
	defer s.wg.Done()
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		err := s.pull(ctx, x, x.tree.Cursor() != "")
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if errors.Is(err, engine.ErrNotFound) {
				backoff = 5 * time.Minute // the share is gone: the next list of shares drops it
			}
			s.setErr(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 5*time.Minute)
			continue
		}
		backoff = 5 * time.Second
		s.setErr(nil)
	}
}

// pull reads the change feed once (wait: long poll when caught up).
func (s *Service) pull(ctx context.Context, x *source, wait bool) error {
	cursor := x.tree.Cursor()
	var acc *engine.Changes
	w := time.Duration(0)
	if wait {
		w = 50 * time.Second
	}
	for {
		c, err := s.Remote.Changes(ctx, x.share.Path, cursor, w)
		if err != nil {
			return err
		}
		w = 0
		if c.Reset && acc == nil {
			acc = &engine.Changes{}
		}
		if acc != nil {
			acc.Entries = append(acc.Entries, c.Entries...)
			acc.Deleted = append(acc.Deleted, c.Deleted...)
			acc.Cursor = c.Cursor
		} else {
			s.applyChanges(x, c, false)
		}
		cursor = c.Cursor
		if !c.More {
			break
		}
	}
	if acc != nil {
		s.applyChanges(x, *acc, true)
	}
	s.mu.Lock()
	x.ready = true
	s.mu.Unlock()
	return nil
}

func (s *Service) applyChanges(x *source, c engine.Changes, reset bool) {
	ch := x.tree.Apply(c, reset)
	if err := s.db.saveTree(x.share.Name, x.tree, ch, reset); err != nil {
		log.Printf("files on demand: save %s: %v", x.share.Name, err)
	}
	if len(ch) == 0 {
		return
	}
	out := make([]Change, 0, len(ch))
	for _, c := range ch {
		c.Path = x.share.Name + "/" + c.Path
		out = append(out, c)
		if !c.Dir {
			s.remoteChanged(x, c)
		}
	}
	s.changed(out)
}

// remoteChanged follows a file changed on the NAS in the cache.
func (s *Service) remoteChanged(x *source, c Change) {
	s.mu.Lock()
	f := s.files[c.Path]
	if f == nil {
		pin := c.Kind == Added && s.Cache && s.underPinnedLocked(c.Path)
		s.mu.Unlock()
		if pin {
			go s.pinFile(c.Path, true)
		}
		return
	}
	if s.busy[c.Path] {
		s.mu.Unlock()
		return
	}
	old := *f
	var act Action
	var err error
	_, rel := Split(c.Path)
	if c.Kind == Removed {
		act, err = s.applyLocked(c.Path, f, Event{Kind: EvRemoteDeleted})
	} else if n, ok := x.tree.Get(rel); ok {
		act, err = s.applyLocked(c.Path, f, Event{Kind: EvRemoteChanged, Version: n.Version, Size: n.Size})
		f.MTime = n.MTime
	}
	s.mu.Unlock()
	if err != nil || act == 0 {
		return
	}
	s.act(c.Path, act, old)
}

func (s *Service) changed(ch []Change) {
	if len(ch) == 0 {
		return
	}
	s.log.add(ch)
	if s.OnChange != nil {
		s.OnChange(ch)
	}
}

// ChangesSince returns the paths changed after anchor (macOS enumerators);
// ok false: the anchor is too old or from another run (list everything).
func (s *Service) ChangesSince(anchor string) (out []Change, next string, ok bool) {
	return s.log.since(anchor)
}

// Anchor is the current position of the change log.
func (s *Service) Anchor() string { return s.log.anchor() }

// ---- Downloads ----

func (s *Service) node(p string) (*source, string, Node, error) {
	name, rel := Split(p)
	x := s.src(name)
	if x == nil || rel == "" {
		if x != nil {
			return x, "", Node{RemoteEntry: engine.RemoteEntry{Dir: true}}, nil
		}
		return nil, "", Node{}, ErrNotFound
	}
	n, ok := x.tree.Get(rel)
	if !ok {
		return nil, "", Node{}, ErrNotFound
	}
	return x, rel, n, nil
}

// Fetch writes the bytes [off, off+n) of a file at version (as the system
// last saw it; "" any) to w, straight from the NAS (Windows, macOS).
func (s *Service) Fetch(ctx context.Context, p, version string, off, n int64, w io.Writer) error {
	x, rel, nd, err := s.node(p)
	if err != nil {
		return err
	}
	if nd.Dir {
		return ErrIsDir
	}
	if nd.Local {
		return ErrNotFound
	}
	if version == "" {
		version = nd.Version
	}
	if version != nd.Version {
		return ErrStale
	}
	return s.download(ctx, x, rel, version, off, n, w)
}

func (s *Service) download(ctx context.Context, x *source, rel, version string, off, n int64, w io.Writer) error {
	var err error
	if rr, ok := s.Remote.(RangeRemote); ok {
		err = rr.DownloadRange(ctx, x.share.Path, rel, version, off, n, w)
	} else {
		lw := &limitWriter{w: w, n: n}
		_, _, err = s.Remote.Download(ctx, x.share.Path, rel, version, off, lw)
		if errors.Is(err, errEnough) {
			err = nil
		}
	}
	if errors.Is(err, engine.ErrChanged) {
		return ErrStale
	}
	return err
}

var errEnough = errors.New("enough")

type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) >= l.n {
		k, err := l.w.Write(p[:l.n])
		l.n -= int64(k)
		if err == nil {
			err = errEnough
		}
		return k, err
	}
	k, err := l.w.Write(p)
	l.n -= int64(k)
	return k, err
}

// ---- Uploads and operations ----

func (s *Service) writable(x *source) error {
	if !x.share.Writable {
		return ErrReadOnly
	}
	return nil
}

// mapErr turns errors of the engine's remote into the service's.
func mapErr(err error) error {
	var c *engine.Conflict
	switch {
	case err == nil:
		return nil
	case errors.As(err, &c):
		if c.Current == nil {
			return ErrNotFound
		}
		return ErrExists
	case errors.Is(err, engine.ErrNotFound):
		return ErrNotFound
	}
	return err
}

// Upload sends a file of this computer to path p, replacing the version
// ifMatch ("": there must be none). When the NAS has another version, the
// file is sent under a conflict name instead, which is returned.
func (s *Service) Upload(ctx context.Context, p string, body io.ReaderAt, size, mtime int64, ifMatch string) (Item, error) {
	x, rel, err := s.writableRel(p)
	if err != nil {
		return Item{}, err
	}
	e, err := s.Remote.Upload(ctx, x.share.Path, engine.UploadReq{Rel: rel, Size: size, MTime: mtime, IfMatch: ifMatch, Body: body})
	var c *engine.Conflict
	if errors.As(err, &c) && c.UploadID != "" {
		// Both sides changed: keep this computer's version next to it.
		dir, base := parentOf(rel), path.Base(rel)
		for n := 1; n < 20; n++ {
			cr := path.Join(dir, engine.ConflictName(base, s.Device, time.Now(), n))
			e, err = s.Remote.Commit(ctx, x.share.Path, c.UploadID, cr, "")
			var c2 *engine.Conflict
			if !errors.As(err, &c2) {
				if err == nil {
					x.tree.Put(Node{RemoteEntry: e})
					s.saveNodes(x, e.Path)
					if s.Notify != nil {
						s.Notify(engine.Event{Kind: "conflict", Path: x.share.Name + "/" + cr, Msg: "files on demand"})
					}
				}
				break
			}
		}
		if err != nil {
			return Item{}, mapErr(err)
		}
		return item(x, e.Path, Node{RemoteEntry: e}), nil
	}
	if err != nil {
		return Item{}, mapErr(err)
	}
	x.tree.Put(Node{RemoteEntry: e})
	s.saveNodes(x, e.Path)
	s.changed([]Change{{Path: x.share.Name + "/" + e.Path, Kind: Modified}})
	return item(x, e.Path, Node{RemoteEntry: e}), nil
}

func (s *Service) writableRel(p string) (*source, string, error) {
	name, rel := Split(p)
	x := s.src(name)
	if x == nil || rel == "" {
		if x != nil {
			return nil, "", ErrReadOnly // the top folders are the shares
		}
		return nil, "", ErrNotFound
	}
	if err := s.writable(x); err != nil {
		return nil, "", err
	}
	return x, rel, nil
}

func (s *Service) saveNodes(x *source, rels ...string) {
	var ch []Change
	for _, r := range rels {
		ch = append(ch, Change{Path: r})
	}
	_ = s.db.saveTree(x.share.Name, x.tree, ch, false)
}

// Mkdir creates a folder.
func (s *Service) Mkdir(ctx context.Context, p string) (Item, error) {
	x, rel, err := s.writableRel(p)
	if err != nil {
		return Item{}, err
	}
	if n, ok := x.tree.Get(rel); ok {
		if n.Dir {
			return item(x, rel, n), ErrExists
		}
		return Item{}, ErrExists
	}
	e, err := s.Remote.Mkdir(ctx, x.share.Path, rel)
	if err != nil {
		return Item{}, mapErr(err)
	}
	e.Dir = true
	x.tree.Put(Node{RemoteEntry: e})
	s.saveNodes(x, rel)
	return item(x, rel, Node{RemoteEntry: e}), nil
}

// Move renames or moves an item inside its shared folder.
func (s *Service) Move(ctx context.Context, from, to string) error {
	x, rf, err := s.writableRel(from)
	if err != nil {
		return err
	}
	y, rt, err := s.writableRel(to)
	if err != nil {
		return err
	}
	if x != y {
		return ErrCrossShare
	}
	n, ok := x.tree.Get(rf)
	if !ok {
		return ErrNotFound
	}
	if !n.Local {
		if _, err := s.Remote.Move(ctx, x.share.Path, rf, rt, n.Version); err != nil {
			return mapErr(err)
		}
	}
	ch := x.tree.Move(rf, rt)
	s.saveMoved(x, rf, ch)
	s.moveStates(from, to)
	s.changed(append([]Change{{Path: from, Kind: Removed, Dir: n.Dir}}, Change{Path: to, Kind: Added, Dir: n.Dir}))
	return nil
}

// ErrCrossShare: a move between shared folders (copy then delete instead).
var ErrCrossShare = errors.New("cannot move between shared folders")

func (s *Service) saveMoved(x *source, from string, ch []Change) {
	ch = append(ch, Change{Path: from})
	_ = s.db.saveTree(x.share.Name, x.tree, ch, false)
}

// Delete removes a file (to the recycle bin of the share when it has one)
// or an empty folder.
func (s *Service) Delete(ctx context.Context, p string) error {
	x, rel, err := s.writableRel(p)
	if err != nil {
		return err
	}
	n, ok := x.tree.Get(rel)
	if !ok {
		return ErrNotFound
	}
	if n.Dir {
		if l, _ := x.tree.List(rel); len(l) > 0 {
			return ErrNotEmpty
		}
	}
	if !n.Local {
		ifm := n.Version
		if n.Dir {
			ifm = "*"
		}
		if err := s.Remote.Delete(ctx, x.share.Path, rel, ifm); err != nil {
			var c *engine.Conflict
			if errors.As(err, &c) && n.Dir {
				return ErrNotEmpty
			}
			return mapErr(err)
		}
	}
	ch := x.tree.Remove(rel)
	_ = s.db.saveTree(x.share.Name, x.tree, ch, false)
	s.mu.Lock()
	if f := s.files[p]; f != nil {
		s.dropLocked(p, f)
	}
	s.mu.Unlock()
	return nil
}

// ---- Change log (macOS enumerators) ----

type changeLog struct {
	mu    sync.Mutex
	run   string
	first int64
	seq   int64
	items []Change
}

const changeLogMax = 20000

func (l *changeLog) init() {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	l.run = hex.EncodeToString(b)
}

func (l *changeLog) add(ch []Change) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range ch {
		c.Old = nil
		l.items = append(l.items, c)
		l.seq++
	}
	if over := len(l.items) - changeLogMax; over > 0 {
		l.items = append([]Change(nil), l.items[over:]...)
		l.first += int64(over)
	}
}

func (l *changeLog) anchor() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return fmt.Sprintf("%s:%d", l.run, l.seq)
}

func (l *changeLog) since(a string) ([]Change, string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := fmt.Sprintf("%s:%d", l.run, l.seq)
	run, n, ok := strings.Cut(a, ":")
	var seq int64
	if _, err := fmt.Sscan(n, &seq); err != nil || !ok || run != l.run || seq < l.first || seq > l.seq {
		return nil, cur, false
	}
	return append([]Change(nil), l.items[seq-l.first:]...), cur, true
}

func randomKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
