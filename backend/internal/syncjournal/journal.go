// Package syncjournal keeps, for each shared folder followed by a sync
// client, an index of its files numbered by change: every creation,
// modification or deletion gets the next sequence number, so a client asks
// "what changed after N under this folder" without the NAS walking the tree.
// The index is refreshed from the disk (one folder when inotify reports a
// change, the whole tree from time to time), so changes made over SMB, NFS,
// SFTP, the web interface or another client appear like the client's own.
//
// A cursor is "<epoch>:<seq>". The epoch changes when the index starts over
// (new index, the folder moved to another file system); a cursor from another
// epoch, or older than the deletions forgotten after TombstoneTTL, gets a
// reset: the client lists everything again and compares with what it has.
package syncjournal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// TombstoneTTL is how long deletions are remembered for clients that were away.
const TombstoneTTL = 30 * 24 * time.Hour

// Entry is a file or folder of the index. Path is relative to the root
// ("a/b.txt"), MTime in nanoseconds.
type Entry struct {
	Path    string `json:"p"`
	Dir     bool   `json:"d,omitempty"`
	Size    int64  `json:"s,omitempty"`
	MTime   int64  `json:"m,omitempty"`
	Ino     uint64 `json:"i,omitempty"`
	Hash    string `json:"h,omitempty"` // SHA-256, "" until computed
	Seq     int64  `json:"q"`
	Deleted int64  `json:"x,omitempty"` // Unix time of the deletion (tombstone)
}

// Version identifies the content of a file as clients see it: any write,
// replacement or touch changes it. Folders have no version.
func (e Entry) Version() string {
	if e.Dir {
		return ""
	}
	return MakeVersion(e.Ino, e.Size, e.MTime)
}

// MakeVersion is the version of a file of that inode, size and date.
func MakeVersion(ino uint64, size, mtime int64) string {
	return strconv.FormatUint(ino, 36) + "-" + strconv.FormatInt(size, 36) + "-" + strconv.FormatInt(mtime, 36)
}

// StatVersion returns the version of a file from its stat.
func StatVersion(fi fs.FileInfo) string {
	return MakeVersion(inode(fi), fi.Size(), fi.ModTime().UnixNano())
}

func inode(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

func device(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}

// Ignored tells whether a name is never synchronized: StrataOS temporary
// files, and at the root, the recycle bin and snapshot folders.
func Ignored(dir, name string) bool {
	if strings.HasPrefix(name, ".strata-upload-") || strings.HasPrefix(name, ".strataos-sync-") || name == "@eaDir" || name == "#recycle" || name == "#snapshot" {
		return true
	}
	return dir == "" && (name == ".recycle" || name == ".snapshots" || name == ".zfs")
}

type hkey struct {
	ino         uint64
	size, mtime int64
}

type rec struct {
	seq  int64
	path string
}

// Journal is the index of one root folder.
type Journal struct {
	Root string

	mu      sync.Mutex
	epoch   string
	seq     int64
	minSeq  int64
	dev     uint64
	entries map[string]*Entry          // live entries and tombstones
	kids    map[string]map[string]bool // folder → names of its live children
	log     []rec                      // ascending seq; superseded records are skipped
	hashes  map[hkey]string            // content hashes by (inode, size, date): moves keep theirs
	wake    chan struct{}              // closed at each change (long polls)
	dirty   bool                       // not saved yet
	scanned bool                       // a full scan was made since loading
	now     func() time.Time
}

// New returns an empty index of root (no scan yet).
func New(root string) *Journal {
	return &Journal{Root: root, epoch: newEpoch(), entries: map[string]*Entry{}, kids: map[string]map[string]bool{"": {}},
		hashes: map[hkey]string{}, wake: make(chan struct{}), now: time.Now}
}

func newEpoch() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func parent(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// Cursor returns the current cursor.
func (j *Journal) Cursor() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cursorLocked()
}

func (j *Journal) cursorLocked() string { return j.epoch + ":" + strconv.FormatInt(j.seq, 10) }

// ParseCursor splits a cursor ("" is the start: everything).
func ParseCursor(c string) (epoch string, seq int64, err error) {
	if c == "" {
		return "", 0, nil
	}
	e, s, ok := strings.Cut(c, ":")
	n, perr := strconv.ParseInt(s, 10, 64)
	if !ok || perr != nil || n < 0 || e == "" || len(e) > 32 {
		return "", 0, errors.New("invalid cursor")
	}
	return e, n, nil
}

// Wait returns a channel closed at the next change.
func (j *Journal) Wait() <-chan struct{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.wake
}

// Page is an answer to Changes.
type Page struct {
	Changes []Entry // paths relative to the prefix
	Cursor  string  // where to continue
	More    bool    // more changes follow: ask again at once
	Reset   bool    // the cursor is unknown or too old: this is a full listing
}

// Changes returns the entries changed after cursor below prefix (a folder
// relative to the root, "" for all), at most limit of them, oldest first.
func (j *Journal) Changes(prefix, cursor string, limit int) (Page, error) {
	epoch, since, err := ParseCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	if limit <= 0 {
		limit = 1000
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	var p Page
	if cursor != "" && (epoch != j.epoch || since < j.minSeq || since > j.seq) {
		p.Reset, since = true, 0
	}
	if cursor == "" {
		p.Reset = true
	}
	i := sort.Search(len(j.log), func(i int) bool { return j.log[i].seq > since })
	last := since
	for ; i < len(j.log); i++ {
		r := j.log[i]
		e := j.entries[r.path]
		if e == nil || e.Seq != r.seq {
			continue // changed again since, or forgotten
		}
		if len(p.Changes) >= limit {
			p.More = true
			break
		}
		last = r.seq
		if since == 0 && e.Deleted != 0 {
			continue // a full listing has no use for deletions
		}
		rel, ok := under(prefix, e.Path)
		if !ok {
			continue
		}
		c := *e
		c.Path = rel
		p.Changes = append(p.Changes, c)
	}
	if !p.More {
		last = j.seq
	}
	p.Cursor = j.epoch + ":" + strconv.FormatInt(last, 10)
	return p, nil
}

// under returns p relative to prefix when p is strictly inside it.
func under(prefix, p string) (string, bool) {
	if prefix == "" {
		return p, p != ""
	}
	if rest, ok := strings.CutPrefix(p, prefix+"/"); ok {
		return rest, true
	}
	return "", false
}

// Lookup returns the live entry of a path.
func (j *Journal) Lookup(p string) (Entry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e := j.entries[p]
	if e == nil || e.Deleted != 0 {
		return Entry{}, false
	}
	return *e, true
}

// SetHash records the hash of a file read with that inode, size and date
// (ignored when the file changed meanwhile). A new hash is not a change.
func (j *Journal) SetHash(p string, ino uint64, size, mtime int64, hash string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.hashes[hkey{ino, size, mtime}] = hash
	if e := j.entries[p]; e != nil && e.Deleted == 0 && !e.Dir && e.Ino == ino && e.Size == size && e.MTime == mtime && e.Hash != hash {
		e.Hash = hash
		j.dirty = true
	}
}

// Unhashed returns up to n live files without a hash yet.
func (j *Journal) Unhashed(n int) []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Entry
	for _, e := range j.entries {
		if e.Deleted == 0 && !e.Dir && e.Hash == "" && e.Size <= backgroundHashMax {
			out = append(out, *e)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

// Stats returns the number of live files and folders.
func (j *Journal) Stats() (files, dirs int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.entries {
		if e.Deleted == 0 {
			if e.Dir {
				dirs++
			} else {
				files++
			}
		}
	}
	return
}

// Dirs returns the live folders (for the watcher), the root first.
func (j *Journal) Dirs() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []string{""}
	for p, e := range j.entries {
		if e.Dir && e.Deleted == 0 {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// obs is what a scan saw of an entry.
type obs struct {
	dir         bool
	size, mtime int64
	ino         uint64
}

// ErrRootUnavailable: the root folder cannot be read (volume missing,
// encrypted share locked). Nothing is marked deleted then.
var ErrRootUnavailable = errors.New("folder unavailable")

// Scan refreshes dir from the disk: its direct children, and the whole
// content of folders that appeared. deep refreshes the whole subtree. It
// returns the folders that appeared (to be watched).
func (j *Journal) Scan(dir string, deep bool) ([]string, error) {
	rt, err := os.OpenRoot(j.Root)
	if err != nil {
		return nil, ErrRootUnavailable
	}
	defer rt.Close()
	rst, err := rt.Stat(".")
	if err != nil || !rst.IsDir() {
		return nil, ErrRootUnavailable
	}
	// known: a folder the index has (same inode: not another one moved there).
	known := func(p string, ino uint64) bool {
		j.mu.Lock()
		defer j.mu.Unlock()
		e := j.entries[p]
		return e != nil && e.Deleted == 0 && e.Dir && e.Ino == ino
	}
	if dir != "" {
		if st, err := rt.Lstat(dir); err != nil || !st.IsDir() || !known(dir, inode(st)) {
			// Gone, replaced or new: what changed is in its parent.
			return j.Scan(parent(dir), false)
		}
	}
	seen := map[string]obs{}
	var walk func(d string, all bool) error
	walk = func(d string, all bool) error {
		name := d
		if name == "" {
			name = "."
		}
		f, err := rt.Open(name)
		if err != nil {
			return err
		}
		ents, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return err
		}
		for _, de := range ents {
			if Ignored(d, de.Name()) || !validName(de.Name()) {
				continue
			}
			t := de.Type()
			if t&fs.ModeSymlink != 0 || (!t.IsDir() && !t.IsRegular()) {
				continue // links and special files are not synchronized
			}
			fi, err := de.Info()
			if err != nil {
				continue // removed meanwhile
			}
			p := join(d, de.Name())
			if fi.IsDir() {
				seen[p] = obs{dir: true, ino: inode(fi)}
				if all || !known(p, inode(fi)) {
					if err := walk(p, true); err != nil && !errors.Is(err, fs.ErrNotExist) {
						return err
					}
				}
				continue
			}
			seen[p] = obs{size: fi.Size(), mtime: fi.ModTime().UnixNano(), ino: inode(fi)}
		}
		return nil
	}
	if err := walk(dir, deep); err != nil {
		if dir == "" {
			return nil, ErrRootUnavailable
		}
		return nil, err
	}
	return j.apply(dir, deep, seen, device(rst)), nil
}

// validName refuses names clients cannot represent safely (control characters, invalid UTF-8).
func validName(n string) bool {
	for _, r := range n {
		if r < 0x20 || r == 0x7f || r == 0xfffd {
			return false
		}
	}
	return true
}

// apply merges a scan of dir into the index.
func (j *Journal) apply(dir string, deep bool, seen map[string]obs, dev uint64) []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.dev != 0 && dev != 0 && dev != j.dev {
		// Another file system under the same path (volume recreated,
		// restored): start over rather than guess.
		j.resetLocked()
	}
	j.dev = dev
	if dir == "" && deep {
		j.scanned = true
	}
	changed := false
	now := j.now().Unix()
	// What the index had there and the scan did not see is gone.
	var gone []string
	var collect func(d string)
	collect = func(d string) {
		for name := range j.kids[d] {
			p := join(d, name)
			if _, ok := seen[p]; !ok {
				gone = append(gone, p)
				continue
			}
			if deep {
				collect(p)
			}
		}
	}
	collect(dir)
	for _, p := range gone {
		changed = j.removeLocked(p, now) || changed
	}
	var added []string
	// Parents first, so children find their folder.
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		o := seen[p]
		e := j.entries[p]
		if e != nil && e.Deleted == 0 && e.Dir == o.dir && e.Ino == o.ino && (o.dir || e.Size == o.size && e.MTime == o.mtime) {
			continue
		}
		if e != nil && e.Deleted == 0 && (e.Dir != o.dir || o.dir) {
			// A file became a folder or the reverse, or another folder took
			// this name: what was there is gone (its content was rescanned).
			j.removeLocked(p, now)
		}
		n := &Entry{Path: p, Dir: o.dir, Ino: o.ino}
		if !o.dir {
			n.Size, n.MTime = o.size, o.mtime
			n.Hash = j.hashes[hkey{o.ino, o.size, o.mtime}]
		} else {
			added = append(added, p)
			if j.kids[p] == nil {
				j.kids[p] = map[string]bool{}
			}
		}
		j.putLocked(n)
		changed = true
	}
	if changed {
		j.dirty = true
		close(j.wake)
		j.wake = make(chan struct{})
	}
	return added
}

// putLocked stores a new state of an entry with the next sequence number.
func (j *Journal) putLocked(e *Entry) {
	j.seq++
	e.Seq = j.seq
	j.entries[e.Path] = e
	j.log = append(j.log, rec{j.seq, e.Path})
	pd, name := parent(e.Path), path.Base(e.Path)
	if j.kids[pd] == nil {
		j.kids[pd] = map[string]bool{}
	}
	if e.Deleted == 0 {
		j.kids[pd][name] = true
	} else {
		delete(j.kids[pd], name)
	}
	if len(j.log) > 2*len(j.entries)+1024 {
		j.compactLocked()
	}
}

// removeLocked turns p (and everything below it) into tombstones.
func (j *Journal) removeLocked(p string, now int64) bool {
	e := j.entries[p]
	if e == nil || e.Deleted != 0 {
		return false
	}
	if e.Dir {
		for name := range j.kids[p] {
			j.removeLocked(join(p, name), now)
		}
		delete(j.kids, p)
	}
	t := *e
	t.Deleted = now
	if !t.Dir && t.Hash != "" {
		j.hashes[hkey{t.Ino, t.Size, t.MTime}] = t.Hash // moved elsewhere, probably
	}
	j.putLocked(&t)
	return true
}

func (j *Journal) compactLocked() {
	j.log = j.log[:0]
	for p, e := range j.entries {
		j.log = append(j.log, rec{e.Seq, p})
	}
	sort.Slice(j.log, func(a, b int) bool { return j.log[a].seq < j.log[b].seq })
}

func (j *Journal) resetLocked() {
	j.epoch = newEpoch()
	j.entries = map[string]*Entry{}
	j.kids = map[string]map[string]bool{"": {}}
	j.log = nil
	j.minSeq = j.seq
	j.dirty = true
}

// Prune forgets the deletions older than TombstoneTTL and the cached hashes
// nothing refers to any more. Older cursors get a reset from then on.
func (j *Journal) Prune() {
	j.mu.Lock()
	defer j.mu.Unlock()
	limit := j.now().Add(-TombstoneTTL).Unix()
	keep := map[hkey]string{}
	recent := j.now().Add(-24 * time.Hour).Unix()
	for p, e := range j.entries {
		if e.Deleted != 0 && e.Deleted < limit {
			delete(j.entries, p)
			if e.Seq > j.minSeq {
				j.minSeq = e.Seq
			}
			j.dirty = true
			continue
		}
		if !e.Dir && e.Hash != "" && (e.Deleted == 0 || e.Deleted > recent) {
			keep[hkey{e.Ino, e.Size, e.MTime}] = e.Hash
		}
	}
	j.hashes = keep
	j.compactLocked()
}

// Scanned tells whether a full scan was made since the index was loaded.
func (j *Journal) Scanned() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.scanned
}

func (e Entry) String() string {
	return fmt.Sprintf("%s dir=%v size=%d seq=%d deleted=%v", e.Path, e.Dir, e.Size, e.Seq, e.Deleted != 0)
}
