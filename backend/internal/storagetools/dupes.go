package storagetools

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/blake2b"
)

// DupFile is one copy in a group of identical files.
type DupFile struct {
	Path    string    `json:"path"`
	ModTime time.Time `json:"modTime"`
	Dev     uint64    `json:"dev"`
	Ino     uint64    `json:"ino"`
	// Links > 1: the file has other names (hard links) outside this group.
	Links uint64 `json:"links,omitempty"`
}

// DupGroup is a set of files with the same content.
type DupGroup struct {
	ID     string    `json:"id"` // content hash
	Size   int64     `json:"size"`
	Wasted int64     `json:"wasted"` // size × (copies − 1)
	Files  []DupFile `json:"files"`
}

// DupReport is the result of a duplicate search.
type DupReport struct {
	Roots     []string   `json:"roots"`
	MinSize   int64      `json:"minSize"`
	Started   time.Time  `json:"started"`
	Finished  time.Time  `json:"finished"`
	Scanned   int64      `json:"scanned"` // files of at least MinSize
	Groups    []DupGroup `json:"groups"`
	Wasted    int64      `json:"wasted"`
	Truncated bool       `json:"truncated,omitempty"`
}

// DefaultMinSize skips small files: they waste little space and cost a lot of reads.
const DefaultMinSize = 1 << 20

const (
	maxGroups   = 5000
	partialSize = 64 << 10
)

// skipDir: recycle bins, snapshots and file versions are copies on purpose.
func skipDir(name string) bool {
	switch name {
	case ".recycle", "#recycle", ".snapshots", ".zfs", ".strata-versions", "@eaDir":
		return true
	}
	return false
}

type candidate struct {
	path  string
	size  int64
	mtime time.Time
	dev   uint64
	ino   uint64
	links uint64
}

// walkFiles calls fn for every regular file of at least minSize under root,
// without following links or entering skipped folders.
func walkFiles(ctx context.Context, root string, minSize int64, fn func(c candidate)) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: skip
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() < minSize || info.Size() == 0 {
			return nil
		}
		c := candidate{path: p, size: info.Size(), mtime: info.ModTime()}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			c.dev, c.ino, c.links = uint64(st.Dev), st.Ino, uint64(st.Nlink)
		}
		fn(c)
		return nil
	})
}

// uniqueRoots drops the folders that are inside another chosen folder.
func uniqueRoots(roots []string) []string {
	var out []string
	sorted := append([]string{}, roots...)
	sort.Strings(sorted)
	for _, r := range sorted {
		r = filepath.Clean(r)
		dup := false
		for _, o := range out {
			if r == o || strings.HasPrefix(r, o+"/") {
				dup = true
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}

// collapseLinks merges the names of one inode: hard links share their data and
// are not duplicates. The first name (in path order) represents the inode.
func collapseLinks(files []candidate) []candidate {
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	seen := map[[2]uint64]bool{}
	var out []candidate
	for _, f := range files {
		k := [2]uint64{f.dev, f.ino}
		if f.ino != 0 && seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// refine splits each group by key and keeps the subgroups of two files or more.
func refine(ctx context.Context, groups [][]candidate, key func(c candidate) (string, error)) ([][]candidate, error) {
	var out [][]candidate
	for _, g := range groups {
		by := map[string][]candidate{}
		var order []string
		for _, c := range g {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			k, err := key(c)
			if err != nil {
				continue // unreadable or changed: left out
			}
			if _, ok := by[k]; !ok {
				order = append(order, k)
			}
			by[k] = append(by[k], c)
		}
		for _, k := range order {
			if len(by[k]) > 1 {
				out = append(out, by[k])
			}
		}
	}
	return out, nil
}

// partialHash hashes the first and last 64 KiB of a file.
func partialHash(c candidate) (string, error) {
	f, err := os.Open(c.path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h, _ := blake2b.New256(nil)
	buf := make([]byte, partialSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", err
	}
	h.Write(buf[:n])
	if c.size > 2*partialSize {
		if n, err = f.ReadAt(buf, c.size-partialSize); err != nil && err != io.EOF {
			return "", err
		}
		h.Write(buf[:n])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fullHash hashes a whole file (BLAKE2b-256), checking ctx between chunks.
func fullHash(ctx context.Context, c candidate, progress func(int64)) (string, error) {
	f, err := os.Open(c.path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h, _ := blake2b.New256(nil)
	buf := make([]byte, 1<<20)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		total += int64(n)
		if progress != nil {
			progress(int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if total != c.size {
		return "", fmt.Errorf("%s changed during the scan", c.path)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DupProgress reports the stage of a search: "list", "compare" (bytes hashed / to hash).
type DupProgress func(stage string, done, total int64)

// FindDuplicates searches identical files under roots: files are grouped by
// size, then by a hash of their ends, then by a hash of their whole content.
// Only the paths of files sharing their size with another file are kept in
// memory (a first pass counts the sizes).
func FindDuplicates(ctx context.Context, roots []string, minSize int64, progress DupProgress) (*DupReport, error) {
	if minSize <= 0 {
		minSize = DefaultMinSize
	}
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	rep := &DupReport{Roots: uniqueRoots(roots), MinSize: minSize, Started: time.Now(), Groups: []DupGroup{}}
	var err error
	inBackground(func() { err = findDuplicates(ctx, rep, progress) })
	if err != nil {
		return nil, err
	}
	rep.Finished = time.Now()
	return rep, nil
}

func findDuplicates(ctx context.Context, rep *DupReport, progress DupProgress) error {
	sizes := map[int64]uint32{}
	for _, r := range rep.Roots {
		err := walkFiles(ctx, r, rep.MinSize, func(c candidate) {
			rep.Scanned++
			if sizes[c.size] < 2 {
				sizes[c.size]++
			}
			if rep.Scanned%10000 == 0 {
				progress("list", rep.Scanned, 0)
			}
		})
		if err != nil {
			return err
		}
	}
	bySize := map[int64][]candidate{}
	for _, r := range rep.Roots {
		err := walkFiles(ctx, r, rep.MinSize, func(c candidate) {
			if sizes[c.size] >= 2 {
				bySize[c.size] = append(bySize[c.size], c)
			}
		})
		if err != nil {
			return err
		}
	}
	sizes = nil
	var groups [][]candidate
	for _, g := range bySize {
		if g = collapseLinks(g); len(g) > 1 {
			groups = append(groups, g)
		}
	}
	bySize = nil
	groups, err := refine(ctx, groups, partialHash)
	if err != nil {
		return err
	}
	var todo, done int64
	for _, g := range groups {
		todo += g[0].size * int64(len(g))
	}
	progress("compare", 0, todo)
	groups, err = refine(ctx, groups, func(c candidate) (string, error) {
		return fullHash(ctx, c, func(n int64) {
			done += n
			if done%(256<<20) < n {
				progress("compare", done, todo)
			}
		})
	})
	if err != nil {
		return err
	}
	for _, g := range groups {
		h, _ := partialHash(g[0]) // group id: stable across runs for the same content
		dg := DupGroup{ID: h[:16] + fmt.Sprintf("-%d", g[0].size), Size: g[0].size, Wasted: g[0].size * int64(len(g)-1)}
		for _, c := range g {
			f := DupFile{Path: c.path, ModTime: c.mtime, Dev: c.dev, Ino: c.ino}
			if c.links > 1 {
				f.Links = c.links
			}
			dg.Files = append(dg.Files, f)
		}
		rep.Groups = append(rep.Groups, dg)
	}
	sortGroups(rep)
	return nil
}

func sortGroups(rep *DupReport) {
	sort.Slice(rep.Groups, func(i, j int) bool {
		if rep.Groups[i].Wasted != rep.Groups[j].Wasted {
			return rep.Groups[i].Wasted > rep.Groups[j].Wasted
		}
		return rep.Groups[i].Files[0].Path < rep.Groups[j].Files[0].Path
	})
	rep.Wasted = 0
	for _, g := range rep.Groups {
		rep.Wasted += g.Wasted
	}
	if len(rep.Groups) > maxGroups {
		rep.Groups, rep.Truncated = rep.Groups[:maxGroups], true
	}
}

// Deletion is one file to delete and a copy of the same group that stays.
type Deletion struct {
	File  DupFile
	Size  int64
	Keeps []DupFile // copies left in place (at least one)
}

// PlanDeletion checks a selection of paths against a report: every path must be
// a member of a group, and each group must keep at least one unselected copy.
func PlanDeletion(rep *DupReport, paths []string) ([]Deletion, error) {
	sel := map[string]bool{}
	for _, p := range paths {
		sel[filepath.Clean(p)] = true
	}
	found := map[string]bool{}
	var out []Deletion
	for _, g := range rep.Groups {
		var del, keep []DupFile
		for _, f := range g.Files {
			if sel[f.Path] {
				del = append(del, f)
				found[f.Path] = true
			} else {
				keep = append(keep, f)
			}
		}
		if len(del) == 0 {
			continue
		}
		if len(keep) == 0 {
			return nil, fmt.Errorf("every copy of %s is selected: at least one must stay", filepath.Base(g.Files[0].Path))
		}
		for _, f := range del {
			out = append(out, Deletion{File: f, Size: g.Size, Keeps: keep})
		}
	}
	for p := range sel {
		if !found[p] {
			return nil, fmt.Errorf("%s is not in the duplicate list (search again)", p)
		}
	}
	return out, nil
}

// Unchanged reports whether a file is still the one the search found.
func Unchanged(f DupFile, size int64) bool {
	info, err := os.Lstat(f.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size || !info.ModTime().Equal(f.ModTime) {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Ino == f.Ino && uint64(st.Dev) == f.Dev
}

// Forget removes deleted files from a report and drops groups left with one copy.
func Forget(rep *DupReport, deleted map[string]bool) {
	var groups []DupGroup
	for _, g := range rep.Groups {
		var files []DupFile
		for _, f := range g.Files {
			if !deleted[f.Path] {
				files = append(files, f)
			}
		}
		if len(files) > 1 {
			g.Files = files
			g.Wasted = g.Size * int64(len(files)-1)
			groups = append(groups, g)
		}
	}
	rep.Groups = groups
	if rep.Groups == nil {
		rep.Groups = []DupGroup{}
	}
	sortGroups(rep)
}
