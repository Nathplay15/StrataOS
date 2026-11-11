package ondemand

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos-sync/internal/engine"
)

// fakeNAS is one shared folder in memory with the semantics of the Sync
// API that files on demand uses: versions, a change feed, ranged reads,
// conditional uploads kept on conflict, moves and deletions.
type fakeNAS struct {
	mu      sync.Mutex
	files   map[string]*fakeFile
	seq     int
	log     []string // path changed at each seq
	ino     int
	uploads map[string]engine.UploadReq
	upData  map[string][]byte
	read    int64 // bytes served
	ranges  []string
	wake    chan struct{}
}

type fakeFile struct {
	dir  bool
	data []byte
	ver  int
	e2e  bool
}

func newNAS() *fakeNAS {
	return &fakeNAS{files: map[string]*fakeFile{}, uploads: map[string]engine.UploadReq{}, upData: map[string][]byte{}, wake: make(chan struct{})}
}

func (f *fakeNAS) touch(p string) {
	f.seq++
	f.log = append(f.log, p)
	close(f.wake)
	f.wake = make(chan struct{})
}

func (f *fakeNAS) put(p string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for d := path.Dir(p); d != "."; d = path.Dir(d) {
		if f.files[d] == nil {
			f.files[d] = &fakeFile{dir: true}
			f.touch(d)
		}
	}
	f.ino++
	f.files[p] = &fakeFile{data: data, ver: f.ino}
	f.touch(p)
}

func (f *fakeNAS) entry(p string, x *fakeFile) engine.RemoteEntry {
	e := engine.RemoteEntry{Path: p, Dir: x.dir, E2E: x.e2e}
	if !x.dir {
		e.Size = int64(len(x.data))
		e.Version = fmt.Sprintf("v%d", x.ver)
		e.MTime = int64(x.ver)
	}
	return e
}

func (f *fakeNAS) Changes(ctx context.Context, folder, cursor string, wait time.Duration) (engine.Changes, error) {
	for {
		f.mu.Lock()
		var from int
		fmt.Sscan(cursor, &from)
		if cursor == "" || from > f.seq {
			c := engine.Changes{Cursor: fmt.Sprint(f.seq), Reset: true}
			for p, x := range f.files {
				c.Entries = append(c.Entries, f.entry(p, x))
			}
			f.mu.Unlock()
			return c, nil
		}
		if from < f.seq || wait == 0 {
			c := engine.Changes{Cursor: fmt.Sprint(f.seq)}
			seen := map[string]bool{}
			for _, p := range f.log[from:] {
				if seen[p] {
					continue
				}
				seen[p] = true
				if x := f.files[p]; x != nil {
					c.Entries = append(c.Entries, f.entry(p, x))
				} else {
					c.Deleted = append(c.Deleted, p)
				}
			}
			f.mu.Unlock()
			return c, nil
		}
		w := f.wake
		f.mu.Unlock()
		select {
		case <-w:
		case <-ctx.Done():
			return engine.Changes{}, ctx.Err()
		case <-time.After(wait):
			wait = 0
		}
	}
}

func (f *fakeNAS) Hashes(ctx context.Context, folder string, paths []string) (map[string]string, error) {
	return nil, nil
}

func (f *fakeNAS) Download(ctx context.Context, folder, rel, version string, offset int64, w io.Writer) (string, int64, error) {
	f.mu.Lock()
	x := f.files[rel]
	if x == nil {
		f.mu.Unlock()
		return "", 0, engine.ErrNotFound
	}
	v := fmt.Sprintf("v%d", x.ver)
	data := x.data
	f.mu.Unlock()
	if version != "" && version != v {
		return v, 0, engine.ErrChanged
	}
	n, err := w.Write(data[offset:])
	return v, int64(n), err
}

func (f *fakeNAS) DownloadRange(ctx context.Context, folder, rel, version string, off, n int64, w io.Writer) error {
	f.mu.Lock()
	x := f.files[rel]
	if x == nil {
		f.mu.Unlock()
		return engine.ErrNotFound
	}
	if version != fmt.Sprintf("v%d", x.ver) {
		f.mu.Unlock()
		return engine.ErrChanged
	}
	end := min(off+n, int64(len(x.data)))
	b := x.data[off:end]
	f.read += int64(len(b))
	f.ranges = append(f.ranges, fmt.Sprintf("%d-%d", off, end))
	f.mu.Unlock()
	_, err := w.Write(b)
	return err
}

func (f *fakeNAS) Upload(ctx context.Context, folder string, u engine.UploadReq) (engine.RemoteEntry, error) {
	b := make([]byte, u.Size)
	if _, err := u.Body.ReadAt(b, 0); err != nil && err != io.EOF {
		return engine.RemoteEntry{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	x := f.files[u.Rel]
	cur := ""
	if x != nil {
		cur = fmt.Sprintf("v%d", x.ver)
	}
	if cur != u.IfMatch {
		id := fmt.Sprintf("up%d", len(f.uploads))
		f.uploads[id], f.upData[id] = u, b
		var c *engine.RemoteEntry
		if x != nil {
			e := f.entry(u.Rel, x)
			c = &e
		}
		return engine.RemoteEntry{}, &engine.Conflict{Msg: "changed", Current: c, UploadID: id}
	}
	f.ino++
	f.files[u.Rel] = &fakeFile{data: b, ver: f.ino}
	f.touch(u.Rel)
	return f.entry(u.Rel, f.files[u.Rel]), nil
}

func (f *fakeNAS) Commit(ctx context.Context, folder, id, rel, ifMatch string) (engine.RemoteEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files[rel] != nil {
		return engine.RemoteEntry{}, &engine.Conflict{Msg: "exists", UploadID: id}
	}
	f.ino++
	f.files[rel] = &fakeFile{data: f.upData[id], ver: f.ino}
	f.touch(rel)
	return f.entry(rel, f.files[rel]), nil
}

func (f *fakeNAS) Mkdir(ctx context.Context, folder, rel string) (engine.RemoteEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[rel] = &fakeFile{dir: true}
	f.touch(rel)
	return engine.RemoteEntry{Path: rel, Dir: true}, nil
}

func (f *fakeNAS) Move(ctx context.Context, folder, from, to, ifMatch string) (engine.RemoteEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files[to] != nil {
		return engine.RemoteEntry{}, &engine.Conflict{Msg: "exists"}
	}
	for p, x := range f.files {
		if p == from || strings.HasPrefix(p, from+"/") {
			np := to + strings.TrimPrefix(p, from)
			delete(f.files, p)
			f.files[np] = x
			f.touch(p)
			f.touch(np)
		}
	}
	return f.entry(to, f.files[to]), nil
}

func (f *fakeNAS) Delete(ctx context.Context, folder, rel, ifMatch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x := f.files[rel]
	if x == nil {
		return nil
	}
	if !x.dir && ifMatch != "*" && ifMatch != fmt.Sprintf("v%d", x.ver) {
		return &engine.Conflict{Msg: "changed"}
	}
	delete(f.files, rel)
	f.touch(rel)
	return nil
}

func (f *fakeNAS) data(p string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if x := f.files[p]; x != nil {
		return append([]byte(nil), x.data...)
	}
	return nil
}

func newService(t *testing.T, nas *fakeNAS) *Service {
	t.Helper()
	s := &Service{Remote: nas, Dir: t.TempDir(), Device: "Laptop", Cache: true,
		Shares: func(ctx context.Context) ([]Share, error) {
			return []Share{{Name: "Docs", Path: "/srv/strata/docs", Writable: true}}, nil
		}}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	s.Start(context.Background())
	t.Cleanup(func() { s.Close() })
	return s
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timeout: " + what)
}

func TestServiceOnDemand(t *testing.T) {
	nas := newNAS()
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<17) // 2 MiB
	nas.put("film.bin", big)
	nas.put("notes/todo.txt", []byte("milk\n"))
	nas.mu.Lock()
	nas.files["vault"] = &fakeFile{dir: true, e2e: true}
	nas.files["vault/Q0lQSEVS"] = &fakeFile{data: []byte("x"), ver: 99}
	nas.mu.Unlock()
	s := newService(t, nas)
	waitFor(t, "listing", s.Ready)

	root, _ := s.List("")
	if len(root) != 1 || root[0].Name != "Docs" {
		t.Fatalf("root: %v", root)
	}
	l, _ := s.List("Docs")
	if len(l) != 2 || l[0].Name != "film.bin" || l[0].Status != StCloud || l[0].Size != int64(len(big)) {
		t.Fatalf("Docs: %+v", l)
	}
	if h := s.Hidden(); len(h) != 1 || h[0] != "Docs/vault" {
		t.Fatalf("hidden: %v", h)
	}

	// Reading a little downloads one block, not the file.
	h, err := s.OpenFile(context.Background(), "Docs/film.bin", false, false)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 100)
	if n, err := h.ReadAt(context.Background(), b, 1<<20); err != nil || n != 100 || !bytes.Equal(b, big[1<<20:1<<20+100]) {
		t.Fatal("read", n, err)
	}
	if nas.read != fetchAlign {
		t.Fatalf("downloaded %d bytes for a 100-byte read", nas.read)
	}
	if it, _ := s.Stat("Docs/film.bin"); it.Status != StPartial {
		t.Fatal(it.Status)
	}
	h.Close()

	// Keep on this device: the whole file; free up space: nothing left.
	if err := s.Pin("Docs/film.bin", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pinned", func() bool { it, _ := s.Stat("Docs/film.bin"); return it.Status == StPinned })
	if used, _ := s.CacheUsage(); used != int64(len(big)) {
		t.Fatal("used", used)
	}
	if s.FreeUp() != 0 {
		t.Fatal("freed a pinned file")
	}
	if err := s.Pin("Docs/film.bin", false); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Stat("Docs/film.bin"); it.Status != StCloud {
		t.Fatal("after free up", it.Status)
	}
	if used, _ := s.CacheUsage(); used != 0 {
		t.Fatal("used after free up", used)
	}

	// Pinning a folder pins the files that arrive in it later.
	if err := s.Pin("Docs/notes", true); err != nil {
		t.Fatal(err)
	}
	nas.put("notes/new.txt", []byte("new"))
	waitFor(t, "new file pinned", func() bool { it, _ := s.Stat("Docs/notes/new.txt"); return it.Status == StPinned })

	// A change on the NAS drops the cached bytes and reads the new ones.
	nas.put("notes/todo.txt", []byte("milk\neggs\n"))
	waitFor(t, "change seen", func() bool { it, _ := s.Stat("Docs/notes/todo.txt"); return it.Size == 10 })
	waitFor(t, "pinned again", func() bool { it, _ := s.Stat("Docs/notes/todo.txt"); return it.Status == StPinned })
	h, _ = s.OpenFile(context.Background(), "Docs/notes/todo.txt", false, false)
	b = make([]byte, 64)
	n, _ := h.ReadAt(context.Background(), b, 0)
	h.Close()
	if string(b[:n]) != "milk\neggs\n" {
		t.Fatalf("read %q", b[:n])
	}
}

func TestServiceWrites(t *testing.T) {
	nas := newNAS()
	nas.put("a.txt", []byte("hello"))
	s := newService(t, nas)
	waitFor(t, "listing", s.Ready)
	ctx := context.Background()

	// A new file is sent when closed.
	h, err := s.Create(ctx, "Docs/dir/b.txt")
	if err == nil {
		t.Fatal("created in a missing folder")
	}
	if _, err := s.Mkdir(ctx, "Docs/dir"); err != nil {
		t.Fatal(err)
	}
	h, err = s.Create(ctx, "Docs/dir/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	h.WriteAt([]byte("bonjour"), 0)
	if it, _ := s.Stat("Docs/dir/b.txt"); it.Status != StDirty || it.Size != 7 {
		t.Fatal(it)
	}
	h.Close()
	waitFor(t, "uploaded", func() bool { return string(nas.data("dir/b.txt")) == "bonjour" })
	waitFor(t, "clean", func() bool { it, _ := s.Stat("Docs/dir/b.txt"); return it.Status == StLocal })

	// Editing an existing file downloads it first, then sends it.
	h, err = s.OpenFile(ctx, "Docs/a.txt", true, false)
	if err != nil {
		t.Fatal(err)
	}
	h.WriteAt([]byte("J"), 0)
	h.Close()
	waitFor(t, "edited", func() bool { return string(nas.data("a.txt")) == "Jello" })

	// Both sides change: the NAS keeps its version, ours becomes a conflict copy.
	waitFor(t, "clean a", func() bool { it, _ := s.Stat("Docs/a.txt"); return it.Status == StLocal })
	h, _ = s.OpenFile(ctx, "Docs/a.txt", true, false)
	h.WriteAt([]byte("M"), 0)
	nas.put("a.txt", []byte("NAS version"))
	waitFor(t, "change seen", func() bool {
		it, _ := s.Stat("Docs/a.txt")
		return it.Version != "" && !strings.HasPrefix(string(nas.data("a.txt")), "J")
	})
	h.Close()
	waitFor(t, "conflict copy", func() bool {
		l, _ := s.List("Docs")
		for _, it := range l {
			if strings.Contains(it.Name, "conflict from Laptop") && string(nas.data(it.Name)) == "Mello" {
				return true
			}
		}
		return false
	})
	if string(nas.data("a.txt")) != "NAS version" {
		t.Fatal("NAS version overwritten")
	}
	waitFor(t, "a follows the NAS", func() bool {
		hh, err := s.OpenFile(ctx, "Docs/a.txt", false, false)
		if err != nil {
			return false
		}
		defer hh.Close()
		b := make([]byte, 32)
		n, _ := hh.ReadAt(ctx, b, 0)
		return string(b[:n]) == "NAS version"
	})

	// Moves and deletions go to the NAS.
	if err := s.Move(ctx, "Docs/dir", "Docs/folder"); err != nil {
		t.Fatal(err)
	}
	if string(nas.data("folder/b.txt")) != "bonjour" {
		t.Fatal("move")
	}
	if it, err := s.Stat("Docs/folder/b.txt"); err != nil || it.Status != StLocal {
		t.Fatal("state after move", it, err)
	}
	if err := s.Delete(ctx, "Docs/folder"); err != ErrNotEmpty {
		t.Fatal("deleted a full folder", err)
	}
	if err := s.Delete(ctx, "Docs/folder/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "Docs/folder"); err != nil || nas.data("folder/b.txt") != nil {
		t.Fatal("delete", err)
	}
	if _, err := s.Mkdir(ctx, "Docs"); err != ErrReadOnly {
		t.Fatal("a folder next to the shares", err)
	}
}

func TestServiceEviction(t *testing.T) {
	nas := newNAS()
	for i := range 4 {
		nas.put(fmt.Sprintf("f%d", i), bytes.Repeat([]byte{byte(i)}, 1<<20))
	}
	s := newService(t, nas)
	s.Budget = 3 << 20
	waitFor(t, "listing", s.Ready)
	ctx := context.Background()
	for i := range 4 {
		h, err := s.OpenFile(ctx, fmt.Sprintf("Docs/f%d", i), false, false)
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 1<<20)
		if _, err := h.ReadAt(ctx, b, 0); err != nil {
			t.Fatal(err)
		}
		h.Close()
		s.mu.Lock()
		s.files[fmt.Sprintf("Docs/f%d", i)].Used = int64(i) // order of use
		s.mu.Unlock()
	}
	used, _ := s.CacheUsage()
	if used > 3<<20 {
		t.Fatal("over budget", used)
	}
	if it, _ := s.Stat("Docs/f0"); it.Status != StCloud {
		t.Fatal("least recently used kept", it.Status)
	}
	if it, _ := s.Stat("Docs/f3"); it.Status != StLocal {
		t.Fatal("last read evicted", it.Status)
	}
	// Restart: the state is saved.
	s.Close()
	s2 := &Service{Remote: nas, Dir: s.Dir, Cache: true, Shares: s.Shares}
	if err := s2.Open(); err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if u, _ := s2.CacheUsage(); u != used {
		t.Fatal("usage after restart", u, used)
	}
	if _, err := os.Stat(s2.cacheFile(s2.files["Docs/f3"].Key)); err != nil {
		t.Fatal(err)
	}
}
