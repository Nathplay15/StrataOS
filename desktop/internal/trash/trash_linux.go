package trash

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// systemTrash follows the freedesktop.org trash specification: the home
// trash when the file is on the same disk, else $topdir/.Trash-$uid.
func systemTrash(p string) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	dir := homeTrash()
	if dir == "" || !sameDevice(abs, filepath.Dir(dir)) {
		top := mountPoint(abs)
		if top == "" {
			return errors.New("no trash on this disk")
		}
		dir = filepath.Join(top, ".Trash-"+strconv.Itoa(os.Getuid()))
	}
	files, info := filepath.Join(dir, "files"), filepath.Join(dir, "info")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(info, 0o700); err != nil {
		return err
	}
	name := filepath.Base(abs)
	for i := 1; i < 1000; i++ {
		n := name
		if i > 1 {
			ext := filepath.Ext(name)
			n = strings.TrimSuffix(name, ext) + "." + strconv.Itoa(i) + ext
		}
		f, err := os.OpenFile(filepath.Join(info, n+".trashinfo"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			continue
		}
		u := (&url.URL{Path: abs}).EscapedPath()
		_, werr := f.WriteString("[Trash Info]\nPath=" + u + "\nDeletionDate=" + time.Now().Format("2006-01-02T15:04:05") + "\n")
		f.Close()
		if werr == nil {
			if err := os.Rename(abs, filepath.Join(files, n)); err == nil {
				return nil
			}
		}
		os.Remove(filepath.Join(info, n+".trashinfo"))
		return errors.New("cannot move to the trash")
	}
	return errors.New("trash full of this name")
}

func homeTrash() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "Trash")
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".local", "share", "Trash")
}

func dev(p string) (uint64, bool) {
	var st syscall.Stat_t
	if syscall.Stat(p, &st) != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

func sameDevice(a, b string) bool {
	_ = os.MkdirAll(b, 0o700)
	da, ok1 := dev(a)
	db, ok2 := dev(b)
	return ok1 && ok2 && da == db
}

// mountPoint returns the top folder of the disk holding p.
func mountPoint(p string) string {
	d, ok := dev(p)
	if !ok {
		return ""
	}
	cur := filepath.Dir(p)
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur
		}
		if pd, ok := dev(parent); !ok || pd != d {
			return cur
		}
		cur = parent
	}
}
