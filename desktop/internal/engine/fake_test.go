package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fakeRemote is an in-memory NAS folder with the semantics of the sync
// API: versions (inode, size, date), a numbered change feed, conditional
// uploads kept on conflict, moves, deletions to a recycle bin.
type fakeRemote struct {
	mu      sync.Mutex
	files   map[string]*fakeFile
	seq     int
	changed map[string]int // path → seq of its last change
	deleted map[string]bool
	ino     uint64
	uploads map[string]*fakeUpload
	nextUp  int
	recycle []string

	// Failures injected once.
	failDownloadAt int64 // stop a download after this many bytes
	failUploadAt   int64 // stop an upload after this many bytes
	onDownload     func(rel string)
	calls          map[string]int
}

type fakeFile struct {
	dir   bool
	data  []byte
	mtime int64 // ms
	ino   uint64
}

type fakeUpload struct {
	rel     string
	size    int64
	mtime   int64
	ifMatch string
	data    []byte
}

func newFake() *fakeRemote {
	return &fakeRemote{files: map[string]*fakeFile{}, changed: map[string]int{}, deleted: map[string]bool{}, uploads: map[string]*fakeUpload{}, calls: map[string]int{}}
}

func (f *fakeRemote) version(x *fakeFile) string {
	if x.dir {
		return ""
	}
	return fmt.Sprintf("%d-%d-%d", x.ino, len(x.data), x.mtime)
}

func (f *fakeRemote) entry(p string, x *fakeFile) RemoteEntry {
	e := RemoteEntry{Path: p, Dir: x.dir, MTime: x.mtime, Version: f.version(x), ID: strconv.FormatUint(x.ino, 10)}
	if !x.dir {
		e.Size = int64(len(x.data))
		h := sha256.Sum256(x.data)
		e.Hash = hex.EncodeToString(h[:])
	}
	return e
}

func (f *fakeRemote) touch(p string, del bool) {
	f.seq++
	f.changed[p] = f.seq
	f.deleted[p] = del
}

// put writes a file on the "NAS" (as SMB would), creating its folders.
func (f *fakeRemote) put(p, content string, mtime time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkdirsLocked(path.Dir(p))
	x := f.files[p]
	if x == nil {
		f.ino++
		x = &fakeFile{ino: f.ino}
		f.files[p] = x
	}
	x.data, x.mtime = []byte(content), mtime.UnixMilli()
	f.touch(p, false)
}

func (f *fakeRemote) mkdirsLocked(d string) {
	if d == "." || d == "" {
		return
	}
	f.mkdirsLocked(path.Dir(d))
	if f.files[d] == nil {
		f.ino++
		f.files[d] = &fakeFile{dir: true, ino: f.ino}
		f.touch(d, false)
	}
}

func (f *fakeRemote) remove(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for q := range f.files {
		if q == p || strings.HasPrefix(q, p+"/") {
			delete(f.files, q)
			f.touch(q, true)
		}
	}
}

func (f *fakeRemote) rename(a, b string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renameLocked(a, b)
}

func (f *fakeRemote) renameLocked(a, b string) {
	for q, x := range f.files {
		if q == a || strings.HasPrefix(q, a+"/") {
			n := b + strings.TrimPrefix(q, a)
			delete(f.files, q)
			f.touch(q, true)
			f.files[n] = x
			f.touch(n, false)
		}
	}
}

func (f *fakeRemote) get(p string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	x := f.files[p]
	if x == nil || x.dir {
		return "", false
	}
	return string(x.data), true
}

func (f *fakeRemote) list() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var l []string
	for p, x := range f.files {
		if x.dir {
			p += "/"
		}
		l = append(l, p)
	}
	sort.Strings(l)
	return strings.Join(l, " ")
}

func (f *fakeRemote) Changes(ctx context.Context, folder, cursor string, wait time.Duration) (Changes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["changes"]++
	since := 0
	reset := cursor == ""
	if !reset {
		since, _ = strconv.Atoi(strings.TrimPrefix(cursor, "e:"))
	}
	c := Changes{Cursor: "e:" + strconv.Itoa(f.seq), Reset: reset}
	var ps []string
	for p, s := range f.changed {
		if s > since {
			ps = append(ps, p)
		}
	}
	sort.Strings(ps)
	for _, p := range ps {
		if f.deleted[p] {
			if !reset {
				c.Deleted = append(c.Deleted, p)
			}
			continue
		}
		c.Entries = append(c.Entries, f.entry(p, f.files[p]))
	}
	return c, nil
}

func (f *fakeRemote) Hashes(ctx context.Context, folder string, paths []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, p := range paths {
		if x := f.files[p]; x != nil && !x.dir {
			out[p] = f.entry(p, x).Hash
		}
	}
	return out, nil
}

func (f *fakeRemote) Download(ctx context.Context, folder, rel, version string, offset int64, w io.Writer) (string, int64, error) {
	f.mu.Lock()
	f.calls["download"]++
	hook := f.onDownload
	f.onDownload = nil
	f.mu.Unlock()
	if hook != nil {
		hook(rel) // a change on the NAS as the download starts
	}
	f.mu.Lock()
	x := f.files[rel]
	if x == nil || x.dir {
		f.mu.Unlock()
		return "", 0, ErrNotFound
	}
	v := f.version(x)
	data := append([]byte(nil), x.data...)
	fail := f.failDownloadAt
	f.failDownloadAt = 0
	f.mu.Unlock()
	if version != "" && v != version {
		return v, 0, ErrChanged
	}
	data = data[offset:]
	if fail > 0 && fail < int64(len(data)) {
		n, _ := w.Write(data[:fail])
		return v, int64(n), errors.New("connection reset")
	}
	n, err := w.Write(data)
	return v, int64(n), err
}

func (f *fakeRemote) Upload(ctx context.Context, folder string, u UploadReq) (RemoteEntry, error) {
	f.mu.Lock()
	f.calls["upload"]++
	up := f.uploads[u.ResumeID]
	if up == nil {
		f.nextUp++
		id := "up" + strconv.Itoa(f.nextUp)
		up = &fakeUpload{rel: u.Rel, size: u.Size, mtime: u.MTime, ifMatch: u.IfMatch}
		f.uploads[id] = up
		u.ResumeID = id
		f.mu.Unlock()
		if u.OnID != nil {
			u.OnID(id)
		}
	} else {
		f.calls["resumed"]++
		f.mu.Unlock()
	}
	id := u.ResumeID
	off := int64(len(up.data))
	rest := make([]byte, u.Size-off)
	if _, err := u.Body.ReadAt(rest, off); err != nil && err != io.EOF {
		return RemoteEntry{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if fail := f.failUploadAt; fail > 0 && fail < int64(len(rest)) {
		f.failUploadAt = 0
		up.data = append(up.data, rest[:fail]...)
		return RemoteEntry{}, errors.New("connection reset")
	}
	up.data = append(up.data, rest...)
	up.ifMatch = u.IfMatch
	return f.commitLocked(id, up.rel, up.ifMatch)
}

func (f *fakeRemote) commitLocked(id, rel, ifMatch string) (RemoteEntry, error) {
	up := f.uploads[id]
	cur := f.files[rel]
	switch {
	case cur != nil && cur.dir:
		e := f.entry(rel, cur)
		return RemoteEntry{}, &Conflict{Msg: "a folder has this name", Current: &e, UploadID: id}
	case ifMatch == "*":
	case cur != nil && f.version(cur) != ifMatch:
		e := f.entry(rel, cur)
		return RemoteEntry{}, &Conflict{Msg: "changed", Current: &e, UploadID: id}
	case cur == nil && ifMatch != "":
		return RemoteEntry{}, &Conflict{Msg: "deleted", UploadID: id}
	}
	f.mkdirsLocked(path.Dir(rel))
	f.ino++
	x := &fakeFile{data: up.data, mtime: up.mtime, ino: f.ino}
	f.files[rel] = x
	f.touch(rel, false)
	delete(f.uploads, id)
	return f.entry(rel, x), nil
}

func (f *fakeRemote) Commit(ctx context.Context, folder, id, rel, ifMatch string) (RemoteEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploads[id] == nil {
		return RemoteEntry{}, errors.New("gone")
	}
	return f.commitLocked(id, rel, ifMatch)
}

func (f *fakeRemote) Mkdir(ctx context.Context, folder, rel string) (RemoteEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if x := f.files[rel]; x != nil && !x.dir {
		e := f.entry(rel, x)
		return RemoteEntry{}, &Conflict{Msg: "a file has this name", Current: &e}
	}
	f.mkdirsLocked(rel)
	return f.entry(rel, f.files[rel]), nil
}

func (f *fakeRemote) Move(ctx context.Context, folder, from, to, ifMatch string) (RemoteEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["move"]++
	x := f.files[from]
	if x == nil {
		return RemoteEntry{}, &Conflict{Msg: "gone"}
	}
	if !x.dir && ifMatch != "" && f.version(x) != ifMatch {
		e := f.entry(from, x)
		return RemoteEntry{}, &Conflict{Msg: "changed", Current: &e}
	}
	if f.files[to] != nil && !strings.EqualFold(from, to) {
		e := f.entry(to, f.files[to])
		return RemoteEntry{}, &Conflict{Msg: "exists", Current: &e}
	}
	f.mkdirsLocked(path.Dir(to))
	f.renameLocked(from, to)
	return f.entry(to, x), nil
}

func (f *fakeRemote) Delete(ctx context.Context, folder, rel, ifMatch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x := f.files[rel]
	if x == nil {
		return nil
	}
	if x.dir {
		for q := range f.files {
			if strings.HasPrefix(q, rel+"/") {
				return &Conflict{Msg: "not empty"}
			}
		}
	} else if ifMatch != "*" && f.version(x) != ifMatch {
		e := f.entry(rel, x)
		return &Conflict{Msg: "changed", Current: &e}
	}
	delete(f.files, rel)
	f.touch(rel, true)
	if !x.dir {
		f.recycle = append(f.recycle, rel)
	}
	return nil
}
