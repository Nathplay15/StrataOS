// Package restore reads the points in time of backups, snapshots and cloud
// copies, and puts files back: in place, to another folder, or as a
// download. It also runs the restore tests. Decisions (what is a conflict,
// retention, samples) are in internal/protect.
package restore

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"strataos/internal/protect"
)

// skipName are folders never listed nor restored: snapshot folders and the
// Windows previous versions links live at the root of volumes.
func skipName(name string) bool {
	switch name {
	case ".snapshots", ".zfs", ".strata-versions":
		return true
	}
	return strings.HasPrefix(name, ".strata-restore-")
}

// tree is a point in time readable as a local folder: Root holds what was
// at Base (an original absolute path).
type tree struct {
	Root string
	Base string
}

// rel is the path of abs inside the tree ("" if outside).
func (t tree) rel(abs string) (string, bool) {
	if abs == t.Base {
		return ".", true
	}
	if r, ok := strings.CutPrefix(abs, t.Base+"/"); ok && r != "" {
		return r, true
	}
	return "", false
}

func entryOf(abs string, fi fs.FileInfo) protect.Entry {
	e := protect.Entry{Path: abs, Name: path.Base(abs), Dir: fi.IsDir(), MTime: fi.ModTime()}
	if fi.Mode()&fs.ModeSymlink != 0 {
		e.Link, e.Dir = true, false
	}
	if fi.Mode().IsRegular() {
		e.Size = fi.Size()
	}
	return e
}

// stat describes abs in the tree.
func (t tree) stat(abs string) (protect.Entry, error) {
	rel, ok := t.rel(abs)
	if !ok {
		return protect.Entry{}, fs.ErrNotExist
	}
	rt, err := os.OpenRoot(t.Root)
	if err != nil {
		return protect.Entry{}, err
	}
	defer rt.Close()
	fi, err := rt.Lstat(rel)
	if err != nil {
		return protect.Entry{}, err
	}
	return entryOf(abs, fi), nil
}

// list lists the folder abs (everything below when recursive, at most max).
func (t tree) list(ctx context.Context, abs string, recursive bool, max int) ([]protect.Entry, bool, error) {
	rel, ok := t.rel(abs)
	if !ok {
		return nil, false, fs.ErrNotExist
	}
	rt, err := os.OpenRoot(t.Root)
	if err != nil {
		return nil, false, err
	}
	defer rt.Close()
	var out []protect.Entry
	truncated := false
	if !recursive {
		f, err := rt.Open(rel)
		if err != nil {
			return nil, false, err
		}
		defer f.Close()
		ents, err := f.ReadDir(-1)
		if err != nil {
			return nil, false, err
		}
		for _, e := range ents {
			if skipName(e.Name()) {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, entryOf(path.Join(abs, e.Name()), fi))
		}
		return out, false, nil
	}
	err = fs.WalkDir(rt.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: skipped
		}
		if p == rel {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if skipName(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if len(out) >= max {
			truncated = true
			return fs.SkipAll
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		sub := strings.TrimPrefix(strings.TrimPrefix(p, rel), "/")
		if rel == "." {
			sub = p
		}
		out = append(out, entryOf(path.Join(abs, sub), fi))
		return nil
	})
	return out, truncated, err
}

// dump writes the file abs, or the folder as a zip, to w.
func (t tree) dump(ctx context.Context, abs string, w io.Writer) error {
	rel, ok := t.rel(abs)
	if !ok {
		return fs.ErrNotExist
	}
	rt, err := os.OpenRoot(t.Root)
	if err != nil {
		return err
	}
	defer rt.Close()
	fi, err := rt.Lstat(rel)
	if err != nil {
		return err
	}
	if fi.Mode().IsRegular() {
		f, err := rt.Open(rel)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	}
	if !fi.IsDir() {
		return errors.New("only files and folders can be downloaded")
	}
	zw := zip.NewWriter(w)
	top := path.Base(abs)
	err = fs.WalkDir(rt.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return ctx.Err()
		}
		if skipName(d.Name()) && d.IsDir() {
			return fs.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		sub := strings.TrimPrefix(strings.TrimPrefix(p, rel), "/")
		if rel == "." {
			sub = p
		}
		h, _ := zip.FileInfoHeader(info)
		h.Name, h.Method = path.Join(top, sub), zip.Deflate
		out, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := rt.Open(p)
		if err != nil {
			return nil
		}
		_, err = io.Copy(out, f)
		f.Close()
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

// stamp formats a time for messages.
func stamp(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }

var errOutside = fmt.Errorf("this path is not in the backup")
