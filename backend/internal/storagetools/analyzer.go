package storagetools

import (
	"bufio"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// File type categories of the space analyzer.
const (
	CatVideo     = "video"
	CatPhotos    = "photos"
	CatMusic     = "music"
	CatDocuments = "documents"
	CatArchives  = "archives"
	CatImages    = "diskimages"
	CatOther     = "other"
)

var categories = []string{CatVideo, CatPhotos, CatMusic, CatDocuments, CatArchives, CatImages, CatOther}

var extCategory = func() map[string]string {
	m := map[string]string{}
	add := func(cat string, exts ...string) {
		for _, e := range exts {
			m["."+e] = cat
		}
	}
	add(CatVideo, "mp4", "m4v", "mkv", "avi", "mov", "wmv", "flv", "webm", "mpg", "mpeg", "m2ts", "mts", "ts", "vob", "3gp", "ogv", "divx", "rmvb")
	add(CatPhotos, "jpg", "jpeg", "png", "gif", "bmp", "tif", "tiff", "webp", "heic", "heif", "raw", "cr2", "cr3", "nef", "arw", "dng", "orf", "rw2", "raf", "psd", "svg", "avif")
	add(CatMusic, "mp3", "flac", "wav", "aac", "m4a", "ogg", "oga", "opus", "wma", "alac", "aiff", "aif", "ape", "dsf", "mka")
	add(CatDocuments, "pdf", "doc", "docx", "odt", "rtf", "txt", "md", "xls", "xlsx", "ods", "csv", "ppt", "pptx", "odp", "epub", "mobi", "pages", "numbers", "key", "tex", "html", "htm", "xml", "json")
	add(CatArchives, "zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "tbz2", "xz", "txz", "zst", "lz4", "lzma", "cab", "deb", "rpm")
	add(CatImages, "iso", "img", "qcow2", "vmdk", "vdi", "vhd", "vhdx", "dmg", "ova", "nrg", "wim", "sparseimage")
	return m
}()

// Category classifies a file name by its extension.
func Category(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if c, ok := extCategory[ext]; ok {
		return c
	}
	return CatOther
}

// TypeStat is the space used by one category of files.
type TypeStat struct {
	Category string `json:"category"`
	Size     int64  `json:"size"`
	Files    int64  `json:"files"`
}

// DirNode is a folder of the size tree. Folders deeper than the scan depth, or
// beyond the node budget, are counted in their nearest kept ancestor.
type DirNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"` // relative to the scanned root ("" for the root)
	Size     int64      `json:"size"` // space used on disk, sub-folders included
	Files    int64      `json:"files"`
	Children []*DirNode `json:"children,omitempty"`
	// More: the folder has kept sub-folders that were not included in this view.
	More bool `json:"more,omitempty"`
}

// Report is the result of a space analysis.
type Report struct {
	Root     string      `json:"root"`
	Started  time.Time   `json:"started"`
	Finished time.Time   `json:"finished"`
	Size     int64       `json:"size"`     // space used on disk
	Apparent int64       `json:"apparent"` // sum of file sizes
	Files    int64       `json:"files"`
	Dirs     int64       `json:"dirs"`
	Errors   int64       `json:"errors"` // unreadable entries
	Depth    int         `json:"depth"`
	Tree     *DirNode    `json:"tree"`
	Largest  []FileEntry `json:"largest"`
	Old      []FileEntry `json:"old"`
	OldSize  int64       `json:"oldSize"`
	OldFiles int64       `json:"oldFiles"`
	Types    []TypeStat  `json:"types"`
}

// ScanOpts bounds a scan. Zero values take the defaults.
type ScanOpts struct {
	Depth    int           // folder levels kept in the tree (default 6)
	MaxNodes int           // folders kept in memory (default 50000)
	TopN     int           // largest / old files kept (default 100)
	OldAge   time.Duration // "old" files: not modified for (default 1 year)
	Now      time.Time
	// Progress is called now and then with the space counted so far.
	Progress func(files, size int64)
}

func (o *ScanOpts) defaults() {
	if o.Depth <= 0 {
		o.Depth = 6
	}
	if o.MaxNodes <= 0 {
		o.MaxNodes = 50000
	}
	if o.TopN <= 0 {
		o.TopN = 100
	}
	if o.OldAge <= 0 {
		o.OldAge = 365 * 24 * time.Hour
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
}

type node struct {
	name     string
	parent   *node
	size     int64
	files    int64
	children []*node
}

// aggregator sums sizes per folder up to a depth and keeps the top-N files:
// memory depends on the budgets, never on the number of files.
type aggregator struct {
	o        ScanOpts
	root     *node
	nodes    int
	largest  *topN
	old      *topN
	types    map[string]*TypeStat
	cutoff   time.Time
	rep      Report
	links    map[[2]uint64]bool // hard-linked inodes already counted
	lastProg int64
}

func newAggregator(o ScanOpts) *aggregator {
	o.defaults()
	a := &aggregator{o: o, root: &node{}, nodes: 1, largest: newTopN(o.TopN), old: newTopN(o.TopN), types: map[string]*TypeStat{}, cutoff: o.Now.Add(-o.OldAge), links: map[[2]uint64]bool{}}
	for _, c := range categories {
		a.types[c] = &TypeStat{Category: c}
	}
	return a
}

// enter returns the node that counts the folder name under parent (at depth,
// the root being 0): a new node when within the budgets, else parent itself.
func (a *aggregator) enter(parent *node, name string, depth int) *node {
	a.rep.Dirs++
	if depth > a.o.Depth || a.nodes >= a.o.MaxNodes || parent == nil {
		return parent
	}
	n := &node{name: name, parent: parent}
	parent.children = append(parent.children, n)
	a.nodes++
	return n
}

// file counts a regular file in the folder node n.
func (a *aggregator) file(n *node, rel string, size, alloc int64, mtime time.Time) {
	a.rep.Files++
	a.rep.Size += alloc
	a.rep.Apparent += size
	for x := n; x != nil; x = x.parent {
		x.size += alloc
		x.files++
	}
	t := a.types[Category(rel)]
	t.Size += alloc
	t.Files++
	f := FileEntry{Path: rel, Size: size, ModTime: mtime}
	a.largest.push(f)
	if mtime.Before(a.cutoff) {
		a.rep.OldFiles++
		a.rep.OldSize += alloc
		a.old.push(f)
	}
	if a.o.Progress != nil && a.rep.Files-a.lastProg >= 20000 {
		a.lastProg = a.rep.Files
		a.o.Progress(a.rep.Files, a.rep.Size)
	}
}

// seenLink reports whether a hard-linked inode was already counted.
func (a *aggregator) seenLink(dev, ino uint64) bool {
	k := [2]uint64{dev, ino}
	if a.links[k] {
		return true
	}
	a.links[k] = true
	return false
}

func (a *aggregator) report(root string) *Report {
	r := a.rep
	r.Root = root
	r.Depth = a.o.Depth
	r.Tree = convertNode(a.root, "")
	r.Largest = a.largest.sorted()
	r.Old = a.old.sorted()
	for _, c := range categories {
		if t := a.types[c]; t.Files > 0 {
			r.Types = append(r.Types, *t)
		}
	}
	sort.SliceStable(r.Types, func(i, j int) bool { return r.Types[i].Size > r.Types[j].Size })
	return &r
}

func convertNode(n *node, rel string) *DirNode {
	d := &DirNode{Name: n.name, Path: rel, Size: n.size, Files: n.files}
	for _, c := range n.children {
		d.Children = append(d.Children, convertNode(c, path.Join(rel, c.name)))
	}
	sort.Slice(d.Children, func(i, j int) bool { return d.Children[i].Size > d.Children[j].Size })
	return d
}

// Subtree returns the folder at rel with at most levels of sub-folders and
// limit children per folder: the full tree can hold tens of thousands of nodes.
func (r *Report) Subtree(rel string, levels, limit int) *DirNode {
	n := r.Tree
	if n == nil {
		return nil
	}
	rel = strings.Trim(path.Clean("/"+rel), "/")
	if rel != "" {
		for _, part := range strings.Split(rel, "/") {
			var next *DirNode
			for _, c := range n.Children {
				if c.Name == part {
					next = c
					break
				}
			}
			if next == nil {
				return nil
			}
			n = next
		}
	}
	return trimNode(n, levels, limit)
}

func trimNode(n *DirNode, levels, limit int) *DirNode {
	d := *n
	d.Children = nil
	if levels <= 0 {
		d.More = len(n.Children) > 0
		return &d
	}
	for i, c := range n.Children {
		if i >= limit {
			d.More = true
			break
		}
		d.Children = append(d.Children, trimNode(c, levels-1, limit))
	}
	return &d
}

// mountPoints lists the mount points under root (other filesystems are not counted).
func mountPoints(root string) map[string]bool {
	out := map[string]bool{}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) < 5 {
			continue
		}
		mp := strings.ReplaceAll(fl[4], `\040`, " ")
		if strings.HasPrefix(mp, root+"/") {
			out[mp] = true
		}
	}
	return out
}

// Analyze walks root (never following symbolic links nor crossing into other
// mounted filesystems) at low I/O priority and returns its report.
func Analyze(ctx context.Context, root string, o ScanOpts) (*Report, error) {
	a := newAggregator(o)
	a.rep.Started = time.Now()
	mounts := mountPoints(root)
	var err error
	inBackground(func() { err = a.walk(ctx, root, "", 0, a.root, mounts) })
	if err != nil {
		return nil, err
	}
	rep := a.report(root)
	rep.Finished = time.Now()
	return rep, nil
}

func (a *aggregator) walk(ctx context.Context, dir, rel string, depth int, n *node, mounts map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(dir)
	if err != nil {
		a.rep.Errors++
		return nil
	}
	defer f.Close()
	for {
		// Batches keep memory bounded on folders with millions of entries.
		entries, err := f.ReadDir(1024)
		for _, e := range entries {
			name := e.Name()
			full := filepath.Join(dir, name)
			crel := path.Join(rel, name)
			info, ierr := e.Info()
			if ierr != nil {
				a.rep.Errors++
				continue
			}
			st, _ := info.Sys().(*syscall.Stat_t)
			switch {
			case info.IsDir():
				// Btrfs snapshots and the ZFS snapshot folder share blocks with the live data.
				if depth == 0 && (name == ".snapshots" || name == ".zfs") {
					continue
				}
				if mounts[full] {
					continue
				}
				if werr := a.walk(ctx, full, crel, depth+1, a.enter(n, name, depth+1), mounts); werr != nil {
					return werr
				}
			case info.Mode().IsRegular():
				alloc := info.Size()
				if st != nil {
					alloc = st.Blocks * 512
					if st.Nlink > 1 && a.seenLink(uint64(st.Dev), st.Ino) {
						continue
					}
				}
				a.file(n, crel, info.Size(), alloc, info.ModTime())
			}
		}
		if err == io.EOF || len(entries) == 0 {
			return nil
		}
		if err != nil {
			a.rep.Errors++
			return nil
		}
	}
}
