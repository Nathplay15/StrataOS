package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"strataos-sync/internal/api"
	"strataos-sync/internal/e2e"
)

// E2ERemote encrypts the folders that are end-to-end encrypted on the way
// to the NAS and decrypts them on the way back; the others go through as
// they are. The engine sees plaintext names and sizes, so its plan, moves
// and conflicts work unchanged. Differences for an encrypted folder:
// uploads are not resumed after an interruption (a new random nonce each
// time, never the same one for different bytes), hashes come from the
// encrypted metadata of each file, and items whose names do not decrypt
// (put there by something else than this app) are left out.
type E2ERemote struct {
	Inner Remote
	// Skipped is told about items of an encrypted folder that do not decrypt.
	Skipped func(folder, path string)

	mu      sync.RWMutex
	folders map[string]*e2eFolder
}

type e2eFolder struct {
	id string
	k  *e2e.Keys
}

// SetFolder encrypts a NAS folder with these keys (nil: no longer).
func (r *E2ERemote) SetFolder(folder, id string, k *e2e.Keys) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.folders == nil {
		r.folders = map[string]*e2eFolder{}
	}
	if k == nil {
		delete(r.folders, folder)
		return
	}
	r.folders[folder] = &e2eFolder{id: id, k: k}
}

func (r *E2ERemote) of(ctx context.Context, folder string) (*e2eFolder, context.Context) {
	r.mu.RLock()
	f := r.folders[folder]
	r.mu.RUnlock()
	if f == nil {
		return nil, ctx
	}
	return f, api.WithE2E(ctx, f.id)
}

// plain turns an entry of the NAS into its plaintext view (rel: its
// plaintext path when the caller knows it).
func (f *e2eFolder) plain(e RemoteEntry, rel string) (RemoteEntry, error) {
	if rel == "" {
		p, err := f.k.DecryptPath(e.Path)
		if err != nil {
			return e, err
		}
		rel = p
	}
	e.Path, e.Hash = rel, ""
	if !e.Dir {
		n, err := e2e.PlainSize(e.Size)
		if err != nil {
			return e, err
		}
		e.Size = n
	}
	return e, nil
}

func (f *e2eFolder) err(err error, rel string) error {
	var c *Conflict
	if errors.As(err, &c) && c.Current != nil {
		if p, perr := f.plain(*c.Current, rel); perr == nil {
			c.Current = &p
		} else {
			c.Current = nil
		}
	}
	return err
}

func (r *E2ERemote) Changes(ctx context.Context, folder, cursor string, wait time.Duration) (Changes, error) {
	f, ctx := r.of(ctx, folder)
	c, err := r.Inner.Changes(ctx, folder, cursor, wait)
	if f == nil || err != nil {
		return c, err
	}
	out := Changes{Cursor: c.Cursor, Reset: c.Reset, More: c.More}
	for _, e := range c.Entries {
		p, err := f.plain(e, "")
		if err != nil {
			if r.Skipped != nil {
				r.Skipped(folder, e.Path)
			}
			continue
		}
		out.Entries = append(out.Entries, p)
	}
	for _, d := range c.Deleted {
		if p, err := f.k.DecryptPath(d); err == nil {
			out.Deleted = append(out.Deleted, p)
		}
	}
	return out, nil
}

// errEnough stops a download once the bytes needed are there.
var errEnough = errors.New("enough")

type headWriter struct{ b []byte }

func (h *headWriter) Write(p []byte) (int, error) {
	n := min(len(p), e2e.HeadLen-len(h.b))
	h.b = append(h.b, p[:n]...)
	if len(h.b) == e2e.HeadLen {
		return n, errEnough
	}
	return n, nil
}

// head reads the header and metadata of an encrypted file.
func (r *E2ERemote) head(ctx context.Context, f *e2eFolder, folder, enc, version string) (e2e.Meta, []byte, string, error) {
	var h headWriter
	v, _, err := r.Inner.Download(ctx, folder, enc, version, 0, &h)
	if err != nil && !errors.Is(err, errEnough) {
		return e2e.Meta{}, nil, v, err
	}
	m, hdr, err := f.k.ReadHead(h.b)
	return m, hdr, v, err
}

func (r *E2ERemote) Hashes(ctx context.Context, folder string, paths []string) (map[string]string, error) {
	f, ctx := r.of(ctx, folder)
	if f == nil {
		return r.Inner.Hashes(ctx, folder, paths)
	}
	out := map[string]string{}
	for _, p := range paths {
		enc, err := f.k.EncryptPath(p)
		if err != nil {
			continue
		}
		m, _, _, err := r.head(ctx, f, folder, enc, "")
		if err != nil {
			if errors.Is(err, ErrUnauthorized) || ctx.Err() != nil {
				return out, err
			}
			continue
		}
		out[p] = m.SHA256
	}
	return out, nil
}

// streamDec decrypts a whole file as it arrives: header, then chunks.
type streamDec struct {
	f   *e2eFolder
	out io.Writer
	h   headWriter
	d   *e2e.Decrypter
}

func (s *streamDec) Write(p []byte) (int, error) {
	n := 0
	if s.d == nil {
		c, err := s.h.Write(p)
		n = c
		if err == nil {
			return n, nil
		}
		m, hdr, err := s.f.k.ReadHead(s.h.b)
		if err != nil {
			return n, err
		}
		s.d = s.f.k.NewDecrypter(hdr, m, 0, 0, s.out)
		p = p[c:]
	}
	if _, err := s.d.Write(p); err != nil {
		return n, err
	}
	return n + len(p), nil
}

func (r *E2ERemote) Download(ctx context.Context, folder, rel, version string, offset int64, w io.Writer) (string, int64, error) {
	f, ctx := r.of(ctx, folder)
	if f == nil {
		return r.Inner.Download(ctx, folder, rel, version, offset, w)
	}
	enc, err := f.k.EncryptPath(rel)
	if err != nil {
		return "", 0, err
	}
	var d *e2e.Decrypter
	var v string
	if offset == 0 {
		s := &streamDec{f: f, out: w}
		v, _, err = r.Inner.Download(ctx, folder, enc, version, 0, s)
		if err == nil && s.d == nil {
			err = e2e.ErrCorrupt
		}
		d = s.d
	} else {
		// Resume: the header first, then from the chunk holding offset.
		var m e2e.Meta
		var hdr []byte
		m, hdr, v, err = r.head(ctx, f, folder, enc, version)
		if err != nil {
			return v, 0, err
		}
		if version == "" {
			version = v
		}
		first := offset / e2e.ChunkSize
		d = f.k.NewDecrypter(hdr, m, first, offset-first*e2e.ChunkSize, w)
		v, _, err = r.Inner.Download(ctx, folder, enc, version, e2e.ChunkOffset(first), d)
	}
	if err == nil {
		err = d.Finish()
	}
	n := int64(0)
	if d != nil {
		n = d.Written()
	}
	if err != nil && (errors.Is(err, e2e.ErrCorrupt)) {
		err = fmt.Errorf("%s: %w", rel, err)
	}
	return v, n, err
}

func (r *E2ERemote) Upload(ctx context.Context, folder string, u UploadReq) (RemoteEntry, error) {
	f, ctx := r.of(ctx, folder)
	if f == nil {
		return r.Inner.Upload(ctx, folder, u)
	}
	enc, err := f.k.EncryptPath(u.Rel)
	if err != nil {
		return RemoteEntry{}, err
	}
	e, err := f.k.NewEncrypter(u.Body, u.Size)
	if err != nil {
		return RemoteEntry{}, err
	}
	progress := u.Progress
	req := UploadReq{Rel: enc, Size: e.Size(), MTime: u.MTime, IfMatch: u.IfMatch, Body: e,
		Progress: func(n int64) {
			if progress != nil {
				progress(min(n, u.Size))
			}
		}}
	res, err := r.Inner.Upload(ctx, folder, req)
	if err != nil {
		return RemoteEntry{}, f.err(err, u.Rel)
	}
	return f.plain(res, u.Rel)
}

func (r *E2ERemote) Commit(ctx context.Context, folder, uploadID, rel, ifMatch string) (RemoteEntry, error) {
	f, ctx := r.of(ctx, folder)
	if f == nil {
		return r.Inner.Commit(ctx, folder, uploadID, rel, ifMatch)
	}
	enc, err := f.k.EncryptPath(rel)
	if err != nil {
		return RemoteEntry{}, err
	}
	e, err := r.Inner.Commit(ctx, folder, uploadID, enc, ifMatch)
	if err != nil {
		return RemoteEntry{}, f.err(err, rel)
	}
	return f.plain(e, rel)
}

func (r *E2ERemote) Mkdir(ctx context.Context, folder, rel string) (RemoteEntry, error) {
	f, ctx := r.of(ctx, folder)
	if f == nil {
		return r.Inner.Mkdir(ctx, folder, rel)
	}
	enc, err := f.k.EncryptPath(rel)
	if err != nil {
		return RemoteEntry{}, err
	}
	e, err := r.Inner.Mkdir(ctx, folder, enc)
	if err != nil {
		return RemoteEntry{}, f.err(err, rel)
	}
	return f.plain(e, rel)
}

func (r *E2ERemote) Move(ctx context.Context, folder, from, to, ifMatch string) (RemoteEntry, error) {
	f, ctx := r.of(ctx, folder)
	if f == nil {
		return r.Inner.Move(ctx, folder, from, to, ifMatch)
	}
	ef, err := f.k.EncryptPath(from)
	if err != nil {
		return RemoteEntry{}, err
	}
	et, err := f.k.EncryptPath(to)
	if err != nil {
		return RemoteEntry{}, err
	}
	e, err := r.Inner.Move(ctx, folder, ef, et, ifMatch)
	if err != nil {
		return RemoteEntry{}, f.err(err, from)
	}
	return f.plain(e, to)
}

func (r *E2ERemote) Delete(ctx context.Context, folder, rel, ifMatch string) error {
	f, ctx := r.of(ctx, folder)
	if f == nil {
		return r.Inner.Delete(ctx, folder, rel, ifMatch)
	}
	enc, err := f.k.EncryptPath(rel)
	if err != nil {
		return err
	}
	return f.err(r.Inner.Delete(ctx, folder, enc, ifMatch), rel)
}
