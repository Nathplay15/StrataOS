// Package rootio reads and writes files of user-controlled folders through an
// os.Root, so that a symlink (which users can create over SMB/NFS/SSH) never
// makes the root daemon read or write outside the folder, and never makes it
// read a device or an endless file.
package rootio

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"syscall"
)

// ErrNotRegular is returned for anything but a regular file (symlink to a
// device, FIFO, folder…).
var ErrNotRegular = errors.New("not a regular file")

// ErrTooLarge is returned when a file is over the size limit.
var ErrTooLarge = errors.New("file too large")

// OpenRegular opens rel for reading and checks that it is a regular file
// (after following symlinks inside the root only).
func OpenRegular(rt *os.Root, rel string) (*os.File, fs.FileInfo, error) {
	f, err := rt.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, ErrNotRegular
	}
	return f, st, nil
}

// ReadFile reads a regular file of at most max bytes.
func ReadFile(rt *os.Root, rel string, max int64) ([]byte, error) {
	f, st, err := OpenRegular(rt, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st.Size() > max {
		return nil, ErrTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Owner is the owner given to a written file; -1 keeps the daemon's.
type Owner struct{ UID, GID int }

// NoOwner leaves the file to the daemon (root).
var NoOwner = Owner{-1, -1}

// OwnerOf returns the owner of an existing entry (not following a final
// symlink), or NoOwner.
func OwnerOf(rt *os.Root, rel string) (Owner, fs.FileMode, bool) {
	st, err := rt.Lstat(rel)
	if err != nil || !st.Mode().IsRegular() {
		return NoOwner, 0, false
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return Owner{int(sys.Uid), int(sys.Gid)}, st.Mode().Perm(), true
	}
	return NoOwner, st.Mode().Perm(), true
}

// WriteFrom writes rel atomically: a new temporary file is created next to it
// (O_EXCL, so a planted symlink is never followed), filled by fill, given
// perm and owner through its handle, then renamed over rel inside the root.
func WriteFrom(rt *os.Root, rel string, perm fs.FileMode, owner Owner, fill func(io.Writer) error) error {
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := path.Join(path.Dir(rel), fmt.Sprintf(".%s.strata-tmp-%s", path.Base(rel), hex.EncodeToString(rnd[:])))
	f, err := rt.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			_ = rt.Remove(tmp)
		}
	}()
	if err := fill(f); err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if owner.UID >= 0 || owner.GID >= 0 {
		if err := f.Chown(owner.UID, owner.GID); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	if err := rt.Rename(tmp, rel); err != nil {
		_ = rt.Remove(tmp)
		return err
	}
	return nil
}

// WriteFile is WriteFrom with data.
func WriteFile(rt *os.Root, rel string, data []byte, perm fs.FileMode, owner Owner) error {
	return WriteFrom(rt, rel, perm, owner, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}
