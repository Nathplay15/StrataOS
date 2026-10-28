package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// scanResult is the local tree of a pair.
type scanResult struct {
	entries    map[string]*LocalEntry // by key
	unreadable []string               // keys of folders that could not be read
	dirs       []string               // absolute folders (to watch)
	skipped    map[string]string      // local paths not synchronized, and why
	links      int                    // symbolic links skipped
}

// ErrLocalMissing: the local folder of a pair is missing (unplugged drive,
// moved folder). Nothing is deleted on the NAS then.
var ErrLocalMissing = errors.New("the local folder is missing")

// scanLocal walks the local folder of a pair.
func scanLocal(root string, n namer, m matcher) (scanResult, error) {
	res := scanResult{entries: map[string]*LocalEntry{}, skipped: map[string]string{}}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return res, ErrLocalMissing
	}
	res.dirs = append(res.dirs, root)
	var walk func(rel string) error
	walk = func(rel string) error {
		ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if rel == "" {
				return err
			}
			res.unreadable = append(res.unreadable, n.key(rel))
			return nil
		}
		for _, de := range ents {
			name := de.Name()
			p := joinRel(rel, name)
			if m.ignored(p) {
				continue
			}
			t := de.Type()
			if t&fs.ModeSymlink != 0 {
				res.links++
				continue
			}
			if !t.IsDir() && !t.IsRegular() {
				continue // devices, sockets, pipes
			}
			if prob := remoteNameProblem(name); prob != "" {
				res.skipped[p] = prob
				continue
			}
			fi, err := de.Info()
			if err != nil {
				continue // gone meanwhile
			}
			e := &LocalEntry{Path: p, Dir: fi.IsDir(), ID: statID(fi)}
			if !e.Dir {
				e.Size, e.MTime = fi.Size(), fi.ModTime().UnixNano()
			}
			res.entries[n.key(p)] = e
			if e.Dir {
				res.dirs = append(res.dirs, filepath.Join(root, filepath.FromSlash(p)))
				if err := walk(p); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return res, ErrLocalMissing
	}
	return res, nil
}

// statLocal returns the current local entry at rel (nil when absent).
func statLocal(root, rel string) *LocalEntry {
	fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || fi.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	e := &LocalEntry{Path: rel, Dir: fi.IsDir(), ID: statID(fi)}
	if !e.Dir {
		e.Size, e.MTime = fi.Size(), fi.ModTime().UnixNano()
	}
	return e
}

// sameLocal tells whether the item at rel is still what the plan saw.
func sameLocal(root string, want *LocalEntry) bool {
	cur := statLocal(root, want.Path)
	if cur == nil || cur.Dir != want.Dir {
		return false
	}
	return cur.Dir || cur.Size == want.Size && cur.MTime == want.MTime
}

// caseInsensitive tells whether the file system of dir ignores case.
func caseInsensitive(dir string) bool {
	f, err := os.CreateTemp(dir, ".strataos-sync-case-")
	if err != nil {
		return defaultFold
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	base := filepath.Base(name)
	_, err = os.Stat(filepath.Join(dir, strings.ToUpper(base)))
	return err == nil
}
