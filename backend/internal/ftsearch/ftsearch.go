// Package ftsearch indexes the text content of the documents stored on the
// NAS (plain text, code, Office and OpenDocument files, PDF, and images or
// scans through OCR when the component is installed) in a persistent inverted
// index, so the interface can search inside file contents. The index covers
// every share; access is filtered at query time by the API layer.
package ftsearch

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	// Dir holds index.json (overridable for tests).
	Dir = "/var/lib/strata/fulltext"
)

const (
	// Files bigger than this are not read.
	maxFileBytes = 20 << 20
	// Only this much text is kept per document.
	maxTextBytes = 512 << 10
	// Distinct terms kept per document.
	maxTermsPerDoc = 10000
	// Documents kept in the index.
	maxDocs = 100000
	// Term/document pairs kept in the index: bounds its memory (roughly
	// 150 bytes each) whatever the documents look like.
	maxPostings = 8_000_000
	// Minimum length of a searchable term.
	minTermLen = 2
)

// doc is one indexed file.
type doc struct {
	Path   string         `json:"path"`
	Name   string         `json:"name"`
	Size   int64          `json:"size"`
	Mtime  int64          `json:"mtime"`
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
	// Failed: the text could not be extracted (the parser crashed, timed out
	// or rejected the file). The file is skipped until it changes.
	Failed bool `json:"failed,omitempty"`
}

// Hit is a document matching a query.
type Hit struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	Score int    `json:"score"`
}

// Index is the in-memory inverted index, saved to disk after every scan.
type Index struct {
	mu     sync.RWMutex
	docs   map[string]*doc
	inv    map[string]map[string]int // term -> path -> count
	failed map[string]*doc
	// postings counts the term/document pairs of inv.
	postings int
	Updated  time.Time
	running  bool
}

// Default is the index of the NAS.
var Default = &Index{docs: map[string]*doc{}, inv: map[string]map[string]int{}, failed: map[string]*doc{}}

func indexFile() string { return filepath.Join(Dir, "index.json") }

// Load reads the saved index.
func (x *Index) Load() {
	b, err := os.ReadFile(indexFile())
	if err != nil {
		return
	}
	var docs []*doc
	if json.Unmarshal(b, &docs) != nil {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.docs, x.inv, x.failed, x.postings = map[string]*doc{}, map[string]map[string]int{}, map[string]*doc{}, 0
	for _, d := range docs {
		if d.Path == "" {
			continue
		}
		if d.Failed {
			x.failed[d.Path] = d
			continue
		}
		if len(d.Counts) == 0 {
			continue
		}
		x.add(d)
	}
}

// save writes the index to disk (caller holds the lock).
func (x *Index) save() {
	if err := os.MkdirAll(Dir, 0o750); err != nil {
		return
	}
	docs := make([]*doc, 0, len(x.docs)+len(x.failed))
	for _, d := range x.docs {
		docs = append(docs, d)
	}
	for _, d := range x.failed {
		docs = append(docs, d)
	}
	b, err := json.Marshal(docs)
	if err != nil {
		return
	}
	tmp := indexFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o640) == nil {
		_ = os.Rename(tmp, indexFile())
	}
}

// add inserts d into the maps (caller holds the write lock).
func (x *Index) add(d *doc) {
	x.docs[d.Path] = d
	for term, n := range d.Counts {
		m := x.inv[term]
		if m == nil {
			m = map[string]int{}
			x.inv[term] = m
		}
		m[d.Path] = n
	}
	x.postings += len(d.Counts)
}

// remove drops a path from the maps (caller holds the write lock).
func (x *Index) remove(p string) {
	d, ok := x.docs[p]
	if !ok {
		return
	}
	delete(x.docs, p)
	x.postings -= len(d.Counts)
	for term := range d.Counts {
		if m := x.inv[term]; m != nil {
			delete(m, p)
			if len(m) == 0 {
				delete(x.inv, term)
			}
		}
	}
}

// SeedDoc is a document injected into an index for tests.
type SeedDoc struct {
	Name string
	Text string
}

// Seed fills an index with synthetic documents (tests of the API layer).
func Seed(docs map[string]SeedDoc) {
	x := Default
	x.mu.Lock()
	defer x.mu.Unlock()
	for p, sd := range docs {
		x.remove(p)
		if d := indexText(p, sd.Name, sd.Text, int64(len(sd.Text)), time.Now().Unix()); d != nil {
			x.add(d)
			saveText(d, sd.Text)
		}
	}
}

// Running reports whether a scan is in progress.
func (x *Index) Running() bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.running
}

// Stats returns the number of documents and the last update.
func (x *Index) Stats() (int, time.Time) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.docs), x.Updated
}

// Tokenize splits a query into the lower-case terms the index understands.
func Tokenize(s string) []string { return tokenize(s) }

// tokenize splits s into lower-case terms of letters and digits.
func tokenize(s string) []string {
	var out []string
	var b []byte
	flush := func() {
		if len(b) >= minTermLen {
			out = append(out, string(b))
		}
		b = b[:0]
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			size := utf8.RuneLen(r)
			if len(b)+size > 40 {
				flush()
			}
			b = utf8.AppendRune(b, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// indexText turns extracted text into a doc (nil if nothing usable).
func indexText(path, name, text string, size, mtime int64) *doc {
	counts := map[string]int{}
	for _, t := range tokenize(text) {
		if len(counts) >= maxTermsPerDoc {
			break
		}
		counts[t]++
	}
	if len(counts) == 0 {
		return nil
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	return &doc{Path: path, Name: name, Size: size, Mtime: mtime, Counts: counts, Total: total}
}

func skipName(name string) bool {
	return strings.HasPrefix(name, ".") || name == "#recycle" || name == "@eaDir"
}

// Scan walks the roots and (re)indexes the supported files whose size or
// modification time changed. It returns the number of documents indexed.
// Documents of folders that are no longer roots (removed shares) are dropped.
func (x *Index) Scan(ctx context.Context, roots []string, logf func(string, ...any)) (int, error) {
	x.mu.Lock()
	if x.running {
		x.mu.Unlock()
		return 0, nil
	}
	x.running = true
	if x.failed == nil {
		x.failed = map[string]*doc{}
	}
	x.mu.Unlock()
	defer func() {
		x.mu.Lock()
		x.running = false
		x.Updated = time.Now()
		x.mu.Unlock()
	}()

	if logf == nil {
		logf = func(string, ...any) {}
	}
	seen := map[string]bool{}
	// Roots that could not be opened (an unmounted volume): their documents
	// are kept until the folder comes back.
	unreachable := map[string]bool{}
	indexed := 0
	full := false
	for _, root := range roots {
		rt, err := openRoot(root)
		if err != nil {
			unreachable[root] = true
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entry: skip it
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				if p != root && skipName(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if !supported(ext) {
				return nil
			}
			if imageExt[ext] && !ocrAvailable() {
				return nil // OCR is optional: images stay out of the index
			}
			info, err := d.Info()
			if err != nil || info.Size() == 0 || info.Size() > maxFileBytes {
				return nil
			}
			seen[p] = true
			size, mtime := info.Size(), info.ModTime().Unix()
			x.mu.RLock()
			old, known := x.docs[p]
			bad := x.failed[p]
			x.mu.RUnlock()
			if known && old.Size == size && old.Mtime == mtime && hasText(old) {
				return nil // unchanged (indexes made before 0.12.1 lack the saved text once)
			}
			if bad != nil && bad.Size == size && bad.Mtime == mtime {
				return nil // failed before and unchanged since: not again
			}
			if full {
				return nil // keep walking: seen files must not be dropped
			}
			text, err := extractAt(rt, root, p, ext, info)
			if err != nil {
				logf("skipped", "path", p, "err", err)
				if !errors.Is(err, errChanged) {
					slog.Warn("fulltext: cannot extract text", "path", p, "err", err)
					x.mu.Lock()
					x.remove(p)
					x.failed[p] = &doc{Path: p, Name: d.Name(), Size: size, Mtime: mtime, Failed: true}
					x.mu.Unlock()
				}
				return nil
			}
			nd := indexText(p, d.Name(), text, size, mtime)
			x.mu.Lock()
			x.remove(p)
			delete(x.failed, p)
			if nd != nil {
				if len(x.docs) >= maxDocs || x.postings+len(nd.Counts) > maxPostings {
					x.mu.Unlock()
					if !full {
						slog.Warn("fulltext: index full, some documents are not indexed", "docs", maxDocs, "postings", maxPostings)
					}
					full = true
					return nil
				}
				x.add(nd)
				indexed++
			}
			x.mu.Unlock()
			if nd != nil {
				saveText(nd, text)
			}
			return nil
		})
		rt.Close()
		if err != nil {
			return indexed, err
		}
	}

	// Drop documents whose file disappeared, and those of folders that are
	// no longer indexed (a removed share).
	x.mu.Lock()
	drop := make([]string, 0, 16)
	for p := range x.docs {
		if !seen[p] && !underAny(keys(unreachable), p) {
			drop = append(drop, p)
		}
	}
	for _, p := range drop {
		x.remove(p)
	}
	for p := range x.failed {
		if !seen[p] && !underAny(keys(unreachable), p) {
			delete(x.failed, p)
		}
	}
	x.save()
	live := make(map[string]bool, len(x.docs))
	for _, d := range x.docs {
		live[textKey(d)] = true
	}
	x.mu.Unlock()
	pruneTexts(live)
	if n := len(drop); n > 0 {
		slog.Debug("fulltext scan", "removed", n)
	}
	return indexed, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// errChanged: the file changed between the listing and its opening (it was
// replaced, possibly by a symbolic link). It is looked at again next scan.
var errChanged = errors.New("file changed during the scan")

// openRoot opens a folder to index. A root that is itself a symbolic link is
// refused, as for the file manager.
func openRoot(root string) (*os.Root, error) {
	if real, err := filepath.EvalSymlinks(root); err != nil || real != root {
		return nil, errChanged
	}
	return os.OpenRoot(root)
}

// extractAt opens p inside rt (which stops ".." and symbolic links leading
// out of the root), checks it is still the regular file the walk listed, and
// extracts its text from the open file.
func extractAt(rt *os.Root, root, p, ext string, listed fs.FileInfo) (string, error) {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", errChanged
	}
	// O_NONBLOCK: a file swapped for a FIFO must not block the scan.
	f, err := rt.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errChanged
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, listed) {
		return "", errChanged
	}
	return extract(f, p, ext, info)
}

func underAny(roots []string, p string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// Search returns the documents containing every term, best first. A term
// found in the file name boosts the score. At most limit hits are returned.
func (x *Index) Search(terms []string, limit int) []Hit {
	terms = dedupe(terms)
	if len(terms) == 0 || limit <= 0 {
		return nil
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	candidates := map[string]int{}
	for i, t := range terms {
		m := x.inv[t]
		if len(m) == 0 {
			return nil // AND: one missing term means no result
		}
		if i == 0 {
			for p, n := range m {
				candidates[p] = n
			}
			continue
		}
		for p := range candidates {
			if _, ok := m[p]; !ok {
				delete(candidates, p)
			}
		}
		if len(candidates) == 0 {
			return nil
		}
	}
	hits := make([]Hit, 0, len(candidates))
	for p := range candidates {
		d := x.docs[p]
		if d == nil {
			continue
		}
		score := candidates[p]
		name := strings.ToLower(d.Name)
		for _, t := range terms {
			if strings.Contains(name, t) {
				score += 1000
			}
		}
		hits = append(hits, Hit{Path: d.Path, Name: d.Name, Size: d.Size, Mtime: d.Mtime, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func dedupe(terms []string) []string {
	seen := map[string]bool{}
	out := terms[:0]
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Snippet returns an extract of a hit around the first term found, with the
// terms of the query highlighted. It reads the text saved when the document
// was indexed, never the file itself: the search runs no parser and opens
// nothing inside the shares. Empty when the saved text is gone.
func (x *Index) Snippet(h Hit, terms []string) string {
	text := loadText(h.Path, h.Size, h.Mtime)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	const width = 160
	lower := strings.ToLower(text)
	best := -1
	for _, t := range dedupe(terms) {
		if i := strings.Index(lower, t); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	if best < 0 {
		if len(text) > width {
			return text[:width] + "…"
		}
		return text
	}
	start := best - width/3
	if start < 0 {
		start = 0
	}
	end := min(start+width, len(text))
	snippet := text[start:end]
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(text) {
		snippet += "…"
	}
	return snippet
}
