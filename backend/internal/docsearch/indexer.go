package docsearch

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// Change is a file or folder reported by the change journal of a folder.
type Change struct {
	Path    string // relative to the folder, slash separated
	Dir     bool
	Deleted bool
	Size    int64
	MTime   int64 // Unix seconds
	Version string
}

// Source is the change journal of one folder (syncjournal in the daemon).
type Source interface {
	// Changes returns what changed after cursor; reset means the page
	// starts a full listing (unknown or too old cursor).
	Changes(cursor string, limit int) (changes []Change, next string, reset, more bool, err error)
	// Files is the number of files of the folder (progress of a listing).
	Files() int
	// Open opens a file of the folder without leaving it.
	Open(rel string) (*os.File, error)
}

// Progress is what the indexer is doing.
type Progress struct {
	Running bool   `json:"running"`
	Root    string `json:"root,omitempty"`
	Done    int    `json:"done"`  // files looked at in this pass
	Total   int    `json:"total"` // files of the folder (during a full listing)
	Indexed int    `json:"indexed"`
	Current string `json:"current,omitempty"`
	Paused  string `json:"paused,omitempty"` // why it waits
	Error   string `json:"error,omitempty"`
}

// Indexer brings the index up to date from the journals.
type Indexer struct {
	Index *Index
	// Tools returns the extractors to use (OCR as enabled).
	Tools func() Tools
	// Gate blocks while indexing must wait (system busy, outside the
	// allowed hours) and returns the reason it waited for, for progress.
	Gate func(ctx context.Context, wait func(reason string)) error

	mu   sync.Mutex
	prog Progress
}

// Progress returns a copy of the progress.
func (x *Indexer) Progress() Progress {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.prog
}

func (x *Indexer) set(f func(p *Progress)) {
	x.mu.Lock()
	f(&x.prog)
	x.mu.Unlock()
}

// Indexable tells whether a path of a journal is indexed: a known type,
// no hidden component (".git", ".strata-…"), images only with OCR.
func Indexable(rel string, ocr bool) bool {
	for _, c := range strings.Split(rel, "/") {
		if strings.HasPrefix(c, ".") || strings.HasPrefix(c, "~$") || c == "node_modules" || c == "@eaDir" || c == "#recycle" || c == "#snapshot" {
			return false
		}
	}
	k := KindOf(rel)
	return k != "" && (k != KindImage || ocr)
}

// Sync indexes what changed in root since the saved cursor, until the
// journal has nothing more. It returns how many files were (re)read.
func (x *Indexer) Sync(ctx context.Context, root string, src Source) (int, error) {
	tools := x.Tools()
	cursor, sweeping := x.Index.Cursor(root)
	read := 0
	x.set(func(p *Progress) { *p = Progress{Running: true, Root: root, Total: src.Files()} })
	defer x.set(func(p *Progress) { p.Running, p.Current, p.Paused = false, "", "" })
	for {
		changes, next, reset, more, err := src.Changes(cursor, 200)
		if err != nil {
			return read, err
		}
		if reset && !sweeping {
			if err := x.Index.StartSweep(root); err != nil {
				return read, err
			}
			sweeping = true
		}
		for _, c := range changes {
			if ctx.Err() != nil {
				return read, ctx.Err()
			}
			abs := root + "/" + c.Path
			x.set(func(p *Progress) { p.Done++ })
			if c.Deleted {
				if err := x.Index.Delete(abs); err != nil {
					return read, err
				}
				continue
			}
			if c.Dir || !Indexable(c.Path, tools.OCR) {
				continue
			}
			if x.Index.Version(abs) == c.Version {
				if sweeping {
					_ = x.Index.Seen(abs)
				}
				continue
			}
			if x.Gate != nil {
				if err := x.Gate(ctx, func(reason string) { x.set(func(p *Progress) { p.Paused = reason }) }); err != nil {
					return read, err
				}
				x.set(func(p *Progress) { p.Paused = "" })
			}
			x.set(func(p *Progress) { p.Current = abs })
			d := x.read(ctx, src, c, abs, tools)
			if ctx.Err() != nil {
				return read, ctx.Err()
			}
			if err := x.Index.Put(d); err != nil {
				return read, err
			}
			read++
			x.set(func(p *Progress) { p.Indexed++ })
		}
		if err := x.Index.SetCursor(root, next); err != nil {
			return read, err
		}
		cursor = next
		if !more {
			break
		}
	}
	if sweeping {
		if err := x.Index.EndSweep(root); err != nil {
			return read, err
		}
	}
	return read, nil
}

// read extracts one file; failures are recorded on the document (its name
// stays searchable) and not retried until the file changes.
func (x *Indexer) read(ctx context.Context, src Source, c Change, abs string, tools Tools) Doc {
	d := Doc{Path: abs, Name: path.Base(c.Path), Kind: KindOf(c.Path), Size: c.Size, MTime: c.MTime, Version: c.Version}
	f, err := src.Open(c.Path)
	if err != nil {
		d.Err = "cannot open the file"
		return d
	}
	defer f.Close()
	text, err := Extract(ctx, f, c.Path, c.Size, tools)
	switch {
	case errors.Is(err, ErrSkipped):
		d.Err = strings.TrimPrefix(err.Error(), ErrSkipped.Error()+": ")
	case err != nil:
		d.Err = err.Error()
		if len(d.Err) > 300 {
			d.Err = d.Err[:300]
		}
	}
	d.Text = text
	return d
}

// Wait waits for d or the end of ctx.
func Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
