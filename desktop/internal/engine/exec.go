package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// errSkip: the op's preconditions no longer hold; it is planned again.
var errSkip = errors.New("changed since planned")

func (pr *pairRun) abs(rel string) string {
	return filepath.Join(pr.p.Local, filepath.FromSlash(rel))
}

// localIDOf returns the file ID of a local entry, computing it when needed.
func (pr *pairRun) localIDOf(l *LocalEntry) uint64 {
	if l.ID == 0 {
		l.ID = fileID(pr.abs(l.Path))
	}
	return l.ID
}

func (pr *pairRun) queue(c change) {
	pr.pending = append(pr.pending, c)
	if len(pr.pending) >= 200 {
		pr.flush()
	}
}

func (pr *pairRun) flush() {
	if len(pr.pending) == 0 {
		return
	}
	if err := pr.e.st.write(pr.p.ID, pr.pending, nil); err == nil {
		pr.pending = pr.pending[:0]
	}
}

// record stores that rel is the same on both sides: the local item as it
// is now, and r on the NAS.
func (pr *pairRun) record(key, localPath string, r *RemoteEntry, hash string) error {
	l := statLocal(pr.p.Local, localPath)
	if l == nil {
		return errSkip
	}
	b := &BaseEntry{Path: r.Path, Dir: r.Dir, Version: r.Version, RemoteID: r.ID, Hash: hash}
	if localPath != r.Path {
		b.LocalPath = localPath
	}
	b.LocalID = pr.localIDOf(l)
	if !l.Dir {
		b.Size, b.LocalMTime = l.Size, l.MTime
	}
	pr.data.base[key] = b
	pr.queue(change{bBase, key, b})
	return nil
}

func (pr *pairRun) forget(key string) {
	if _, ok := pr.data.base[key]; ok {
		delete(pr.data.base, key)
		pr.queue(change{bBase, key, nil})
	}
}

func (pr *pairRun) setRemote(r RemoteEntry) {
	e := r
	pr.data.remote[r.Path] = &e
	pr.queue(change{bRemote, r.Path, &e})
}

func (pr *pairRun) dropRemote(p string) {
	if _, ok := pr.data.remote[p]; ok {
		delete(pr.data.remote, p)
		pr.queue(change{bRemote, p, nil})
	}
}

// rebase moves the base and remote entries below a moved folder.
func (pr *pairRun) rebase(fromKey, toKey, fromRemote, toRemote, fromLocal, toLocal string) {
	for k, b := range pr.data.base {
		if !isUnder(k, fromKey) {
			continue
		}
		nk := toKey + strings.TrimPrefix(k, fromKey)
		nb := *b
		nb.Path = toRemote + strings.TrimPrefix(b.Path, fromRemote)
		if b.LocalPath != "" {
			nb.LocalPath = toLocal + strings.TrimPrefix(b.LocalPath, fromLocal)
		}
		if nb.LocalPath == nb.Path {
			nb.LocalPath = ""
		}
		delete(pr.data.base, k)
		pr.queue(change{bBase, k, nil})
		pr.data.base[nk] = &nb
		pr.queue(change{bBase, nk, &nb})
	}
	for p, r := range pr.data.remote {
		if strings.HasPrefix(p, fromRemote+"/") {
			nr := *r
			nr.Path = toRemote + strings.TrimPrefix(p, fromRemote)
			pr.dropRemote(p)
			pr.setRemote(nr)
		}
	}
}

func fileHash(p string) (string, error) {
	f, err := openShared(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// exec runs one op.
func (pr *pairRun) exec(ctx context.Context, o op) error {
	e := pr.e
	switch o.kind {
	case opRecord:
		return pr.record(o.key, o.l.Path, o.r, "")

	case opForget:
		pr.forget(o.key)
		return nil

	case opMkdirLocal:
		if l := statLocal(pr.p.Local, o.r.Path); l != nil && !l.Dir {
			return errSkip
		}
		if err := os.MkdirAll(pr.abs(o.r.Path), 0o755); err != nil {
			return err
		}
		return pr.record(o.key, o.r.Path, o.r, "")

	case opMkdirRemote:
		if l := statLocal(pr.p.Local, o.l.Path); l == nil || !l.Dir {
			return errSkip
		}
		r, err := e.remote.Mkdir(ctx, pr.p.Remote, o.l.Path)
		if err != nil {
			return err
		}
		pr.setRemote(r)
		return pr.record(o.key, o.l.Path, &r, "")

	case opDownload:
		return pr.download(ctx, o.key, o.r, o.l)

	case opUpload:
		ifMatch := ""
		if o.b != nil && !o.b.Dir && o.r != nil {
			ifMatch = o.b.Version
		}
		return pr.upload(ctx, o.key, o.l, ifMatch)

	case opCompare:
		if !sameLocal(pr.p.Local, o.l) {
			return errSkip
		}
		if o.r.Hash == "" {
			return errSkip // the NAS did not give its hash yet
		}
		h, err := fileHash(pr.abs(o.l.Path))
		if err != nil {
			return err
		}
		if h == o.r.Hash {
			return pr.record(o.key, o.l.Path, o.r, h)
		}
		return pr.conflict(ctx, o)

	case opConflict:
		return pr.conflict(ctx, o)

	case opMoveRemote:
		// A local rename: the item must still be at its new place, and
		// nothing there on the NAS.
		if !sameLocal(pr.p.Local, o.l2) {
			return errSkip
		}
		r, err := e.remote.Move(ctx, pr.p.Remote, o.b.Path, o.toRemote, o.b.Version)
		if err != nil {
			return err
		}
		pr.dropRemote(o.b.Path)
		pr.setRemote(r)
		pr.forget(o.key)
		if o.b.Dir {
			pr.rebase(o.key, o.toKey, o.b.Path, o.toRemote, o.b.local(), o.toLocal)
		}
		return pr.record(o.toKey, o.toLocal, &r, o.b.Hash)

	case opMoveLocal:
		from := o.b.local()
		if !sameLocal(pr.p.Local, &LocalEntry{Path: from, Dir: o.l.Dir, Size: o.l.Size, MTime: o.l.MTime}) {
			return errSkip
		}
		caseOnly := o.key == o.toKey
		if !caseOnly && statLocal(pr.p.Local, o.toLocal) != nil {
			return errSkip // something appeared there
		}
		if err := os.MkdirAll(filepath.Dir(pr.abs(o.toLocal)), 0o755); err != nil {
			return err
		}
		if err := os.Rename(pr.abs(from), pr.abs(o.toLocal)); err != nil {
			return err
		}
		pr.forget(o.key)
		if o.b.Dir {
			pr.rebase(o.key, o.toKey, o.b.Path, o.r2.Path, from, o.toLocal)
		}
		return pr.record(o.toKey, o.toLocal, o.r2, o.b.Hash)

	case opDeleteRemote:
		// Deleted locally: still absent (or, for a type change, still the new type)?
		if l := statLocal(pr.p.Local, o.b.local()); l != nil && l.Dir == o.b.Dir {
			return errSkip
		}
		ifMatch := o.b.Version
		err := e.remote.Delete(ctx, pr.p.Remote, o.b.Path, ifMatch)
		var c *Conflict
		if errors.As(err, &c) {
			// Changed on the NAS (or a folder with new content): it comes
			// back with the next changes.
			return errSkip
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		pr.dropRemote(o.b.Path)
		pr.forget(o.key)
		return nil

	case opDeleteLocal:
		p := o.b.local()
		cur := statLocal(pr.p.Local, p)
		if cur == nil {
			pr.forget(o.key)
			return nil
		}
		if cur.Dir != o.b.Dir || !cur.Dir && (cur.Size != o.b.Size || cur.MTime != o.b.LocalMTime) {
			return errSkip // changed here meanwhile: it goes back to the NAS
		}
		if cur.Dir && !pr.onlyJunk(p) {
			return errSkip // new content inside: kept (and sent back)
		}
		if err := e.opt.Trash(pr.abs(p)); err != nil {
			return fmt.Errorf("move to the trash: %w", err)
		}
		pr.forget(o.key)
		return nil
	}
	return nil
}

// onlyJunk tells whether a local folder holds nothing synchronized.
func (pr *pairRun) onlyJunk(rel string) bool {
	ents, err := os.ReadDir(pr.abs(rel))
	if err != nil {
		return false
	}
	for _, d := range ents {
		if !pr.m.ignored(joinRel(rel, d.Name())) {
			return false
		}
	}
	return true
}

// conflict keeps both versions: the local one is renamed "name (conflict
// from <device> <date>).ext" (sent to the NAS at the next round), and the
// NAS version takes the name.
func (pr *pairRun) conflict(ctx context.Context, o op) error {
	if !sameLocal(pr.p.Local, o.l) {
		return errSkip
	}
	name, err := pr.freeConflictName(o.l.Path)
	if err != nil {
		return err
	}
	if err := os.Rename(pr.abs(o.l.Path), pr.abs(name)); err != nil {
		return err
	}
	pr.forget(o.key)
	pr.e.event(Event{Kind: "conflict", Pair: pr.p.ID, Path: o.l.Path, Msg: name})
	pr.conflicts++
	if o.r.Dir {
		if err := os.MkdirAll(pr.abs(o.r.Path), 0o755); err != nil {
			return err
		}
		return pr.record(o.key, o.r.Path, o.r, "")
	}
	return pr.download(ctx, o.key, o.r, nil)
}

func (pr *pairRun) freeConflictName(rel string) (string, error) {
	dir, name := parentOf(rel), path.Base(rel)
	now := pr.e.opt.Now()
	for n := 1; n < 100; n++ {
		c := joinRel(dir, conflictName(name, pr.e.opt.Device, now, n))
		k := pr.n.key(c)
		if statLocal(pr.p.Local, c) == nil && pr.remoteByKey(k) == nil {
			return c, nil
		}
	}
	return "", errors.New("too many conflict copies")
}

func (pr *pairRun) remoteByKey(k string) *RemoteEntry {
	for p, r := range pr.data.remote {
		if pr.n.key(p) == k {
			return r
		}
	}
	return nil
}

const partPrefix = ".strataos-sync-"

func partName(key string) string {
	h := sha256.Sum256([]byte(key))
	return partPrefix + hex.EncodeToString(h[:8]) + ".part"
}

// download fetches r into its local place, which must still be what the
// plan saw (want: nil = absent). The bytes go to a hidden part file in the
// same folder, resumed after an interruption when the NAS version did not
// change, then renamed over the target.
func (pr *pairRun) download(ctx context.Context, key string, r *RemoteEntry, want *LocalEntry) error {
	target := pr.abs(r.Path)
	check := func() error {
		cur := statLocal(pr.p.Local, r.Path)
		switch {
		case want == nil && cur != nil:
			return errSkip
		case want != nil && (cur == nil || cur.Dir || cur.Size != want.Size || cur.MTime != want.MTime):
			return errSkip
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	part := filepath.Join(dir, partName(key+"\x00"+r.Version))
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	off, _ := f.Seek(0, io.SeekEnd)
	if off > r.Size {
		f.Truncate(0)
		off, _ = f.Seek(0, io.SeekStart)
	}
	pr.setCurrent(r.Path, r.Size, off)
	w := &progressWriter{w: f, n: off, f: func(n int64) { pr.setCurrent(r.Path, r.Size, n) }}
	var hasher io.Writer
	h := sha256.New()
	if off == 0 && r.Hash != "" {
		hasher = h
	}
	var dst io.Writer = w
	if hasher != nil {
		dst = io.MultiWriter(w, hasher)
	}
	_, n, err := pr.e.remote.Download(ctx, pr.p.Remote, r.Path, r.Version, off, dst)
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if errors.Is(err, ErrChanged) {
		os.Remove(part)
		return errSkip
	}
	if err != nil {
		return err // the part stays, to resume
	}
	if cerr != nil {
		return cerr
	}
	if off+n != r.Size {
		os.Remove(part)
		return fmt.Errorf("incomplete download of %s", r.Path)
	}
	if hasher != nil && hex.EncodeToString(h.Sum(nil)) != r.Hash {
		os.Remove(part)
		return fmt.Errorf("checksum mismatch for %s", r.Path)
	}
	mt := time.UnixMilli(r.MTime)
	_ = os.Chtimes(part, mt, mt)
	if err := check(); err != nil {
		return err // changed here meanwhile: the part is replanned as a conflict
	}
	if err := os.Rename(part, target); err != nil {
		return err
	}
	return pr.record(key, r.Path, r, r.Hash)
}

type progressWriter struct {
	w    io.Writer
	n    int64
	f    func(int64)
	last time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if time.Since(p.last) > 300*time.Millisecond {
		p.last = time.Now()
		p.f(p.n)
	}
	return n, err
}

// resumeMin: smaller uploads are not recorded for resuming (one chunk; a
// database transaction per small file would slow a first sync down a lot,
// notably on Windows where each one waits for the disk).
var resumeMin int64 = 8 << 20

// upload sends a local file, replacing the NAS version ifMatch ("" = none
// expected). When the NAS file changed meanwhile, the uploaded copy is kept
// on the NAS under a conflict name and the local file renamed the same way.
func (pr *pairRun) upload(ctx context.Context, key string, l *LocalEntry, ifMatch string) error {
	if !sameLocal(pr.p.Local, l) {
		return errSkip
	}
	// Shared for deletion: on Windows the user may still rename or delete
	// the file during a long upload (the upload then fails and is planned again).
	f, err := openShared(pr.abs(l.Path))
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			f.Close()
		}
	}()
	st := pr.data.uploads[key]
	resume := ""
	if st.ID != "" && st.Size == l.Size && st.MTime == l.MTime && st.IfMatch == ifMatch {
		resume = st.ID
	}
	req := UploadReq{Rel: l.Path, Size: l.Size, MTime: l.MTime / 1e6, IfMatch: ifMatch, Body: f, ResumeID: resume,
		OnID: func(id string) {
			if l.Size <= resumeMin {
				return // sent in one request: nothing to resume (and no write per file)
			}
			u := uploadState{ID: id, Size: l.Size, MTime: l.MTime, IfMatch: ifMatch}
			pr.data.uploads[key] = u
			_ = pr.e.st.write(pr.p.ID, []change{{bUploads, key, u}}, nil)
		},
		Progress: func(n int64) { pr.setCurrent(l.Path, l.Size, n) }}
	pr.setCurrent(l.Path, l.Size, 0)
	r, err := pr.e.remote.Upload(ctx, pr.p.Remote, req)
	var c *Conflict
	if errors.As(err, &c) {
		delete(pr.data.uploads, key)
		pr.queue(change{bUploads, key, nil})
		f.Close() // Windows renames no open file: the local copy is renamed next
		closed = true
		return pr.uploadConflict(ctx, key, l, c)
	}
	if err != nil {
		return err
	}
	delete(pr.data.uploads, key)
	pr.queue(change{bUploads, key, nil})
	pr.setRemote(r)
	if !sameLocal(pr.p.Local, l) {
		// Changed during the upload: the base keeps the state sent, so the
		// next round sends the new content.
		b := &BaseEntry{Path: r.Path, Size: l.Size, LocalMTime: l.MTime, LocalID: pr.localIDOf(l), Version: r.Version, RemoteID: r.ID}
		if l.Path != r.Path {
			b.LocalPath = l.Path
		}
		pr.data.base[key] = b
		pr.queue(change{bBase, key, b})
		return nil
	}
	return pr.record(key, l.Path, &r, r.Hash)
}

// uploadConflict: the NAS file changed while this one was sent. The copy
// sent becomes the conflict copy (on both sides), the NAS version keeps
// the name and is downloaded at the next round.
func (pr *pairRun) uploadConflict(ctx context.Context, key string, l *LocalEntry, c *Conflict) error {
	if c.Current != nil && c.Current.Dir {
		return errSkip // a folder took the name: planned again
	}
	if !sameLocal(pr.p.Local, l) {
		return errSkip
	}
	name, err := pr.freeConflictName(l.Path)
	if err != nil {
		return err
	}
	var r RemoteEntry
	if c.UploadID != "" {
		r, err = pr.e.remote.Commit(ctx, pr.p.Remote, c.UploadID, name, "")
		if err != nil {
			return err
		}
	}
	if err := os.Rename(pr.abs(l.Path), pr.abs(name)); err != nil {
		return err
	}
	pr.forget(key)
	pr.conflicts++
	pr.e.event(Event{Kind: "conflict", Pair: pr.p.ID, Path: l.Path, Msg: name})
	if c.UploadID != "" {
		pr.setRemote(r)
		_ = pr.record(pr.n.key(name), name, &r, "")
	}
	if c.Current != nil {
		pr.setRemote(*c.Current)
	}
	return nil
}

// onlyJunkOrKnown tells whether a local folder holds only ignored files and
// the items of kids (checked recursively from the disk: nothing new).
func (pr *pairRun) onlyJunkOrKnown(rel string, kids []op) bool {
	known := map[string]bool{}
	for _, o := range kids {
		known[pr.n.key(o.b.local())] = true
	}
	ok := true
	var walk func(r string)
	walk = func(r string) {
		ents, err := os.ReadDir(pr.abs(r))
		if err != nil {
			ok = false
			return
		}
		for _, d := range ents {
			p := joinRel(r, d.Name())
			if pr.m.ignored(p) {
				continue
			}
			if !known[pr.n.key(p)] {
				ok = false
				return
			}
			if d.IsDir() {
				walk(p)
			}
		}
	}
	walk(rel)
	return ok
}
