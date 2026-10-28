// Package trash moves local files deleted by synchronization to the trash
// of the system (recoverable by the user), or, when that fails, to a
// folder of the app: a deletion never destroys a file.
package trash

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Trasher moves files to the system trash, else to Fallback.
type Trasher struct {
	Fallback string // folder of the app (…/StrataOS Sync/trash)
}

// Trash moves a file or folder away.
func (t Trasher) Trash(p string) error {
	if err := systemTrash(p); err == nil {
		return nil
	}
	return t.fallback(p)
}

func (t Trasher) fallback(p string) error {
	if t.Fallback == "" {
		return errors.New("no trash available")
	}
	dir := filepath.Join(t.Fallback, time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dst := unique(dir, filepath.Base(p))
	if err := os.Rename(p, dst); err == nil {
		return nil
	}
	// Another disk: copy, then remove.
	if err := copyTree(p, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(p)
}

// unique returns dir/name, or dir/name.2… when taken.
func unique(dir, name string) string {
	dst := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			return dst
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s.%d", name, i))
	}
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if info, ierr := d.Info(); ierr == nil {
			_ = os.Chtimes(target, info.ModTime(), info.ModTime())
		}
		return err
	})
}
