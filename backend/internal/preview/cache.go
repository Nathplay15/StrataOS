// Package preview turns files the browser cannot show into files it can:
// office documents into PDF (LibreOffice), camera RAW, HEIC, TIFF or PSD
// pictures into JPEG/WebP (libvips, ffmpeg, exiftool). Converters run as the
// sandbox user on a private copy, within time and size limits, one or two at
// a time; results are cached by path, modification time and size.
package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache keeps converted files under Dir, at most Max bytes (oldest use first out).
type Cache struct {
	Dir string
	Max int64
	mu  sync.Mutex
}

// DefaultCache is the cache of the daemon.
var DefaultCache = &Cache{Dir: "/var/cache/strata/previews", Max: 2 << 30}

// Key identifies a version of a file and a variant of its conversion: any
// change of the file (time or size) gives a new key, so stale entries are
// never served and simply age out.
func Key(path string, mod time.Time, size int64, variant string) string {
	h := sha256.New()
	for _, s := range []string{path, strconv.FormatInt(mod.UnixNano(), 10), strconv.FormatInt(size, 10), variant} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:40]
}

func (c *Cache) path(key, ext string) string { return filepath.Join(c.Dir, key+ext) }

// Get returns the cached file of key, marking it used.
func (c *Cache) Get(key, ext string) (string, bool) {
	p := c.path(key, ext)
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return p, true
}

// Put moves a finished conversion into the cache, then trims the cache.
func (c *Cache) Put(key, ext, file string) (string, error) {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return "", err
	}
	p := c.path(key, ext)
	if err := os.Rename(file, p); err != nil {
		// Work folders may sit on another file system.
		data, rerr := os.ReadFile(file)
		if rerr != nil {
			return "", err
		}
		if err := os.WriteFile(p+".tmp", data, 0o600); err != nil {
			return "", err
		}
		if err := os.Rename(p+".tmp", p); err != nil {
			return "", err
		}
	}
	// Written by the sandbox user: give it to root, so a later conversion cannot change it.
	_ = os.Lchown(p, 0, 0)
	_ = os.Chmod(p, 0o600)
	c.Evict()
	return p, nil
}

// Evict removes the least recently used entries until the cache fits in Max.
func (c *Cache) Evict() {
	c.mu.Lock()
	defer c.mu.Unlock()
	type entry struct {
		path string
		size int64
		used time.Time
	}
	var list []entry
	var total int64
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue // work folders
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, entry{filepath.Join(c.Dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(list, func(i, j int) bool { return list[i].used.Before(list[j].used) })
	for _, e := range list {
		if total <= c.Max {
			break
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}

// WorkDir creates a private folder for one conversion, owned by uid/gid when
// they are not -1 (the sandbox user). The caller removes it.
func (c *Cache) WorkDir(uid, gid int) (string, error) {
	if err := os.MkdirAll(c.Dir, 0o711); err != nil {
		return "", err
	}
	_ = os.Chmod(c.Dir, 0o711) // the sandbox user enters its own folder only
	// Its parents must be traversable too: /var/cache/strata may have been
	// created 0700 by another feature (the restic cache). Add search
	// permission only, never read.
	for p := filepath.Dir(c.Dir); p != "/" && p != "/var/cache" && p != "/var" && p != "."; p = filepath.Dir(p) {
		if st, err := os.Stat(p); err == nil && st.Mode().Perm()&0o011 != 0o011 {
			_ = os.Chmod(p, st.Mode().Perm()|0o011)
		}
	}
	d, err := os.MkdirTemp(c.Dir, ".work-")
	if err != nil {
		return "", err
	}
	if uid >= 0 {
		if err := os.Chown(d, uid, gid); err != nil {
			os.RemoveAll(d)
			return "", err
		}
	}
	return d, nil
}

// CleanWork removes work folders left by a crash.
func (c *Cache) CleanWork() {
	_ = filepath.WalkDir(c.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != c.Dir && d.IsDir() {
			if strings.HasPrefix(d.Name(), ".work-") {
				os.RemoveAll(p)
			}
			return filepath.SkipDir
		}
		return nil
	})
}
