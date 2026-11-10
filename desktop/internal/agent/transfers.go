package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"strataos-sync/internal/api"
)

var jsonMarshal = json.Marshal

// Transfer is a download or an upload of the file browser, done here so
// that it goes on with the window closed and reads or writes files the
// webview cannot reach.
type Transfer struct {
	ID      string    `json:"id"`
	Account string    `json:"account"`
	Kind    string    `json:"kind"` // download, upload
	Name    string    `json:"name"`
	Local   string    `json:"local"`
	Remote  string    `json:"remote"`
	Size    int64     `json:"size"` // 0: unknown
	Done    int64     `json:"done"`
	Files   int       `json:"files,omitempty"`
	State   string    `json:"state"` // running, done, error, cancelled
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`

	cancel context.CancelFunc
}

type transfers struct {
	a    *Agent
	mu   sync.Mutex
	list map[string]*Transfer
}

func (t *transfers) init(a *Agent) { t.a, t.list = a, map[string]*Transfer{} }

func (t *transfers) start(x *account, kind, name, local, remote string, size int64, run func(ctx context.Context, tr *Transfer) error) *Transfer {
	ctx, cancel := context.WithCancel(t.a.ctx)
	tr := &Transfer{ID: newKey(6), Account: x.id, Kind: kind, Name: name, Local: local, Remote: remote, Size: size, State: "running", Started: time.Now(), cancel: cancel}
	t.mu.Lock()
	t.list[tr.ID] = tr
	t.mu.Unlock()
	go func() {
		defer cancel()
		err := run(ctx, tr)
		t.mu.Lock()
		tr.Ended = time.Now()
		switch {
		case err == nil:
			tr.State = "done"
		case ctx.Err() != nil:
			tr.State = "cancelled"
		default:
			tr.State, tr.Error = "error", t.a.message(err)
		}
		t.mu.Unlock()
		t.a.events.add(Event{Kind: "transfer", Account: x.id, Title: tr.State, Body: tr.Name})
	}()
	return tr
}

func (t *transfers) progress(tr *Transfer, done int64) {
	t.mu.Lock()
	tr.Done = done
	t.mu.Unlock()
}

func (t *transfers) snapshot() []Transfer {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Transfer, 0, len(t.list))
	for _, tr := range t.list {
		c := *tr
		c.cancel = nil
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

func (t *transfers) cancel(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr := t.list[id]
	if tr == nil {
		return &httpError{http.StatusNotFound, "transfer not found"}
	}
	tr.cancel()
	return nil
}

// clear forgets the finished transfers.
func (t *transfers) clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, tr := range t.list {
		if tr.State != "running" {
			delete(t.list, id)
		}
	}
}

// download saves a NAS file (a folder: as a ZIP) to dest, through a
// temporary file next to it.
func (t *transfers) download(x *account, remote, dest string, size int64) (*Transfer, error) {
	c := x.app.Client()
	if c == nil {
		return nil, errors.New(t.a.T("err.notpaired"))
	}
	if !strings.HasPrefix(remote, "/") || !filepath.IsAbs(dest) {
		return nil, errors.New("invalid paths")
	}
	if st, err := os.Stat(filepath.Dir(dest)); err != nil || !st.IsDir() {
		return nil, errors.New("the destination folder does not exist")
	}
	return t.start(x, "download", filepath.Base(dest), dest, remote, size, func(ctx context.Context, tr *Transfer) error {
		r, err := c.Request(ctx, "GET", "/api/files/download", url.Values{"path": {remote}}, nil)
		if err != nil {
			return err
		}
		resp, err := c.Transport().RoundTrip(r)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return api.ReadError(resp)
		}
		if resp.ContentLength > 0 {
			t.mu.Lock()
			tr.Size = resp.ContentLength
			t.mu.Unlock()
		}
		part := dest + ".part"
		f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, &countReader{r: resp.Body, f: func(n int64) { t.progress(tr, n) }})
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(part, dest)
		}
		if err != nil {
			os.Remove(part)
		}
		return err
	}), nil
}

type countReader struct {
	r io.Reader
	n int64
	f func(int64)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	c.f(c.n)
	return n, err
}

type upFile struct {
	local, rel string
	size       int64
	mtime      time.Time
}

// upload sends files and folders of this computer into a NAS folder
// (resumable uploads; folders keep their tree).
func (t *transfers) upload(x *account, paths []string, folder string, overwrite bool) (*Transfer, error) {
	c := x.app.Client()
	if c == nil {
		return nil, errors.New(t.a.T("err.notpaired"))
	}
	folder = strings.TrimRight(folder, "/")
	if !strings.HasPrefix(folder, "/") || strings.Contains(folder, "..") || len(paths) == 0 {
		return nil, errors.New("invalid paths")
	}
	var files []upFile
	var total int64
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return nil, errors.New("invalid paths")
		}
		p = filepath.Clean(p)
		base := filepath.Dir(p)
		err := filepath.WalkDir(p, func(f string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() && !d.IsDir() {
				return nil // links and special files stay here
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(base, f)
			files = append(files, upFile{local: f, rel: filepath.ToSlash(rel), size: info.Size(), mtime: info.ModTime()})
			total += info.Size()
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(files) == 0 {
		return nil, errors.New(t.a.T("err.upload.empty"))
	}
	name := filepath.Base(paths[0])
	if len(paths) > 1 {
		name = fmt.Sprintf("%s (+%d)", name, len(paths)-1)
	}
	ifMatch := ""
	if overwrite {
		ifMatch = "*"
	}
	tr := t.start(x, "upload", name, paths[0], folder, total, func(ctx context.Context, tr *Transfer) error {
		var sent int64
		var failed []string
		for i, f := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			fh, err := os.Open(f.local)
			if err != nil {
				failed = append(failed, f.rel+": "+err.Error())
				continue
			}
			before := sent
			_, err = c.UploadFile(ctx, api.Upload{Folder: folder, Rel: f.rel, Size: f.size, MTime: f.mtime.UnixMilli(), IfMatch: ifMatch, Body: fh,
				Progress: func(done int64) { t.progress(tr, before+done) }})
			fh.Close()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if _, ok := api.ConflictOf(err); ok {
					err = errors.New(t.a.T("err.upload.exists"))
				}
				failed = append(failed, path.Base(f.rel)+": "+err.Error())
			}
			sent += f.size
			t.progress(tr, sent)
			t.mu.Lock()
			tr.Files = i + 1
			t.mu.Unlock()
		}
		if len(failed) > 0 {
			if len(failed) > 3 {
				failed = append(failed[:3], fmt.Sprintf("+%d", len(failed)-3))
			}
			return errors.New(strings.Join(failed, "; "))
		}
		return nil
	})
	return tr, nil
}
