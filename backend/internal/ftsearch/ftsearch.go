// Package ftsearch indexes the text content of the documents stored on the
// NAS (plain text, code, Office and OpenDocument files, PDF) in a persistent
// inverted index, so the interface can search inside file contents. The index
// covers every share; access is filtered at query time by the API layer.
package ftsearch

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	maxTermsPerDoc = 20000
	// Documents kept in the index.
	maxDocs = 100000
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
	mu      sync.RWMutex
	docs    map[string]*doc
	inv     map[string]map[string]int // term -> path -> count
	Updated time.Time
	running bool
}

// Default is the index of the NAS.
var Default = &Index{docs: map[string]*doc{}, inv: map[string]map[string]int{}}

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
	x.docs, x.inv = map[string]*doc{}, map[string]map[string]int{}
	for _, d := range docs {
		if d.Path == "" || len(d.Counts) == 0 {
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
	docs := make([]*doc, 0, len(x.docs))
	for _, d := range x.docs {
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
}

// remove drops a path from the maps (caller holds the write lock).
func (x *Index) remove(p string) {
	d, ok := x.docs[p]
	if !ok {
		return
	}
	delete(x.docs, p)
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
func (x *Index) Scan(ctx context.Context, roots []string, logf func(string, ...any)) (int, error) {
	x.mu.Lock()
	if x.running {
		x.mu.Unlock()
		return 0, nil
	}
	x.running = true
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
	indexed := 0
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
			info, err := d.Info()
			if err != nil || info.Size() == 0 || info.Size() > maxFileBytes {
				return nil
			}
			seen[p] = true
			x.mu.RLock()
			old, known := x.docs[p]
			x.mu.RUnlock()
			if known && old.Size == info.Size() && old.Mtime == info.ModTime().Unix() {
				return nil
			}
			text, err := extract(p, ext, info.Size())
			if err != nil {
				logf("skipped", "path", p, "err", err)
				return nil
			}
			nd := indexText(p, d.Name(), text, info.Size(), info.ModTime().Unix())
			x.mu.Lock()
			x.remove(p)
			if nd != nil {
				if len(x.docs) >= maxDocs {
					x.mu.Unlock()
					return filepath.SkipAll
				}
				x.add(nd)
				indexed++
			}
			x.mu.Unlock()
			return nil
		}); err != nil {
			return indexed, err
		}
	}

	// Drop documents whose file disappeared or is no longer under a root.
	x.mu.Lock()
	drop := make([]string, 0, 16)
	for p := range x.docs {
		if !seen[p] && underAny(roots, p) {
			drop = append(drop, p)
		}
	}
	for _, p := range drop {
		x.remove(p)
	}
	x.save()
	x.mu.Unlock()
	if n := len(drop); n > 0 {
		slog.Debug("fulltext scan", "removed", n)
	}
	return indexed, nil
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

// Snippet returns an extract of the document around the first term found,
// with the terms of the query highlighted. Empty when the file cannot be
// read any more.
func (x *Index) Snippet(path string, terms []string) string {
	ext := strings.ToLower(filepath.Ext(path))
	text, err := extract(path, ext, maxFileBytes)
	if err != nil {
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
