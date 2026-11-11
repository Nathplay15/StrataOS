package docsearch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // pure Go SQLite with FTS5
)

// Index is the full-text database: one row per file in docs, its text in
// the FTS5 table fts (same rowid), the journal cursor of each folder.
type Index struct {
	file string
	db   *sql.DB
	mu   sync.Mutex // one writer at a time
}

const schema = `
CREATE TABLE IF NOT EXISTS docs(
	id INTEGER PRIMARY KEY,
	path TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	version TEXT NOT NULL,
	chars INTEGER NOT NULL DEFAULT 0,
	err TEXT NOT NULL DEFAULT '',
	seen INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS docs_mtime ON docs(mtime);
CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(name, body, tokenize = 'unicode61 remove_diacritics 2', prefix = '2 3');
CREATE TABLE IF NOT EXISTS cursors(root TEXT PRIMARY KEY, cursor TEXT NOT NULL, sweep INTEGER NOT NULL DEFAULT 0);
`

// Open opens (or creates) the index in file.
func Open(file string) (*Index, error) {
	if err := os.MkdirAll(path.Dir(file), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("search index: %w", err)
	}
	return &Index{file: file, db: db}, nil
}

// Close closes the database.
func (x *Index) Close() error { return x.db.Close() }

// Doc is an indexed file.
type Doc struct {
	Path    string
	Name    string
	Kind    string
	Size    int64
	MTime   int64 // Unix seconds
	Version string
	Text    string
	Err     string
}

// Version returns the stored version of a file ("" when unknown).
func (x *Index) Version(p string) string {
	var v string
	_ = x.db.QueryRow(`SELECT version FROM docs WHERE path = ?`, p).Scan(&v)
	return v
}

// Put adds or replaces a document.
func (x *Index) Put(d Doc) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	tx, err := x.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`SELECT id FROM docs WHERE path = ?`, d.Path).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.Exec(`INSERT INTO docs(path, name, kind, size, mtime, version, chars, err) VALUES(?,?,?,?,?,?,?,?)`,
			d.Path, d.Name, d.Kind, d.Size, d.MTime, d.Version, len(d.Text), d.Err)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
	case err != nil:
		return err
	default:
		if _, err := tx.Exec(`UPDATE docs SET name=?, kind=?, size=?, mtime=?, version=?, chars=?, err=?, seen=1 WHERE id=?`,
			d.Name, d.Kind, d.Size, d.MTime, d.Version, len(d.Text), d.Err, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM fts WHERE rowid = ?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO fts(rowid, name, body) VALUES(?,?,?)`, id, nameWords(d.Name), d.Text); err != nil {
		return err
	}
	return tx.Commit()
}

// nameWords makes the parts of a file name findable: "tax_report-2024.pdf"
// is also indexed as "tax report 2024 pdf".
func nameWords(n string) string {
	split := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, n)
	return n + " " + split
}

// Delete removes a file, or everything below a folder.
func (x *Index) Delete(p string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.deleteWhere(`path = ? OR (path >= ? AND path < ?)`, p, p+"/", p+"0")
}

func (x *Index) deleteWhere(cond string, args ...any) error {
	tx, err := x.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM fts WHERE rowid IN (SELECT id FROM docs WHERE `+cond+`)`, args...); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM docs WHERE `+cond, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// Seen marks a file as present during a full listing.
func (x *Index) Seen(p string) error {
	_, err := x.db.Exec(`UPDATE docs SET seen = 1 WHERE path = ?`, p)
	return err
}

// StartSweep marks every file of root as not seen (a full listing begins).
func (x *Index) StartSweep(root string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, err := x.db.Exec(`UPDATE docs SET seen = 0 WHERE path >= ? AND path < ?`, root+"/", root+"0"); err != nil {
		return err
	}
	_, err := x.db.Exec(`INSERT INTO cursors(root, cursor, sweep) VALUES(?, '', 1) ON CONFLICT(root) DO UPDATE SET sweep = 1`, root)
	return err
}

// EndSweep removes the files of root the listing did not mention.
func (x *Index) EndSweep(root string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.deleteWhere(`seen = 0 AND path >= ? AND path < ?`, root+"/", root+"0"); err != nil {
		return err
	}
	_, err := x.db.Exec(`UPDATE cursors SET sweep = 0 WHERE root = ?`, root)
	return err
}

// Cursor returns the journal cursor of root and whether a sweep is open.
func (x *Index) Cursor(root string) (string, bool) {
	var c string
	var sweep int
	_ = x.db.QueryRow(`SELECT cursor, sweep FROM cursors WHERE root = ?`, root).Scan(&c, &sweep)
	return c, sweep != 0
}

// SetCursor saves the journal cursor of root.
func (x *Index) SetCursor(root, cursor string) error {
	_, err := x.db.Exec(`INSERT INTO cursors(root, cursor) VALUES(?, ?) ON CONFLICT(root) DO UPDATE SET cursor = excluded.cursor`, root, cursor)
	return err
}

// ResetCursors makes every folder be listed again (OCR switched on, for
// example); unchanged files are not read again.
func (x *Index) ResetCursors() error {
	_, err := x.db.Exec(`UPDATE cursors SET cursor = ''`)
	return err
}

// Forget removes a folder no longer indexed.
func (x *Index) Forget(root string) error {
	if err := x.Delete(root); err != nil {
		return err
	}
	_, err := x.db.Exec(`DELETE FROM cursors WHERE root = ?`, root)
	return err
}

// Roots lists the folders with a cursor.
func (x *Index) Roots() []string {
	rows, err := x.db.Query(`SELECT root FROM cursors`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if rows.Scan(&r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// Stats sums up the index.
type Stats struct {
	Docs   int            `json:"docs"`
	Failed int            `json:"failed"`
	Chars  int64          `json:"chars"`
	Bytes  int64          `json:"bytes"` // database files on disk
	Kinds  map[string]int `json:"kinds"`
}

// Stats counts the documents and measures the database.
func (x *Index) Stats() Stats {
	s := Stats{Kinds: map[string]int{}}
	rows, err := x.db.Query(`SELECT kind, count(*), sum(chars), sum(err != '') FROM docs GROUP BY kind`)
	if err == nil {
		for rows.Next() {
			var k string
			var n, failed int
			var chars sql.NullInt64
			if rows.Scan(&k, &n, &chars, &failed) == nil {
				s.Kinds[k] = n
				s.Docs += n
				s.Failed += failed
				s.Chars += chars.Int64
			}
		}
		rows.Close()
	}
	for _, f := range []string{x.file, x.file + "-wal"} {
		if fi, err := os.Stat(f); err == nil {
			s.Bytes += fi.Size()
		}
	}
	return s
}

// Failures lists the last files that could not be read.
func (x *Index) Failures(n int) []map[string]string {
	out := []map[string]string{}
	rows, err := x.db.Query(`SELECT path, err FROM docs WHERE err != '' ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p, e string
		if rows.Scan(&p, &e) == nil {
			out = append(out, map[string]string{"path": p, "error": e})
		}
	}
	return out
}

// Compact rewrites the database (after a rebuild or many deletions).
func (x *Index) Compact(ctx context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, err := x.db.ExecContext(ctx, `INSERT INTO fts(fts) VALUES('optimize')`); err != nil {
		return err
	}
	_, err := x.db.ExecContext(ctx, `VACUUM`)
	return err
}

// Clear empties the index (rebuild).
func (x *Index) Clear() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, q := range []string{`DELETE FROM fts`, `DELETE FROM docs`, `DELETE FROM cursors`, `VACUUM`} {
		if _, err := x.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// ---- Search ----

// Query is a search. Roots are the folders the user may read: nil means
// everything (administrators), an empty list nothing.
type Query struct {
	Text    string
	Roots   []string
	Folder  string // only below this folder
	Kinds   []string
	From    time.Time // modified after
	To      time.Time // modified before
	MinSize int64
	MaxSize int64 // 0: no limit
	Limit   int
	Offset  int
}

// Part is a piece of a snippet; Hit marks the words that matched.
type Part struct {
	Text string `json:"t"`
	Hit  bool   `json:"h,omitempty"`
}

// Hit is a document found.
type Hit struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"`
	Snippet []Part `json:"snippet"`
}

// Result is a page of hits; Total stops counting at 1000.
type Result struct {
	Hits  []Hit `json:"hits"`
	Total int   `json:"total"`
	More  bool  `json:"more"`
}

// MatchExpr turns what a user typed into a safe FTS5 expression: every
// word must appear (quoted, so FTS5 operators typed by the user are plain
// words), "quoted phrases" stay phrases, the last word also matches as a
// prefix. It returns "" when nothing is searchable.
func MatchExpr(s string) string {
	var terms []string
	quote := func(t string) string { return `"` + strings.ReplaceAll(t, `"`, `""`) + `"` }
	words := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	}
	parts := strings.Split(s, `"`)
	lastPrefix := false
	for i, p := range parts {
		if i%2 == 1 && i < len(parts)-1 { // inside quotes
			if w := words(p); len(w) > 0 {
				terms = append(terms, quote(strings.Join(w, " ")))
				lastPrefix = false
			}
			continue
		}
		for _, w := range words(p) {
			terms = append(terms, quote(w))
			lastPrefix = true
		}
	}
	if len(terms) == 0 {
		return ""
	}
	if len(terms) > 32 {
		terms = terms[:32]
	}
	if lastPrefix && !strings.HasSuffix(strings.TrimSpace(s), `"`) {
		last := terms[len(terms)-1]
		if len([]rune(last)) >= 4 { // two letters at least
			terms[len(terms)-1] = last + "*"
		}
	}
	return strings.Join(terms, " ")
}

// Search runs q. The rights are applied in the query and checked again on
// each hit.
func (x *Index) Search(ctx context.Context, q Query) (Result, error) {
	res := Result{Hits: []Hit{}}
	expr := MatchExpr(q.Text)
	if expr == "" || (q.Roots != nil && len(q.Roots) == 0) {
		return res, nil
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	where := []string{"fts MATCH ?"}
	args := []any{expr}
	if q.Roots != nil {
		var or []string
		for _, r := range q.Roots {
			or = append(or, "(d.path >= ? AND d.path < ?)")
			args = append(args, r+"/", r+"0")
		}
		where = append(where, "("+strings.Join(or, " OR ")+")")
	}
	if q.Folder != "" {
		where = append(where, "d.path >= ? AND d.path < ?")
		args = append(args, q.Folder+"/", q.Folder+"0")
	}
	if len(q.Kinds) > 0 {
		where = append(where, "d.kind IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Kinds)), ",")+")")
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	if !q.From.IsZero() {
		where = append(where, "d.mtime >= ?")
		args = append(args, q.From.Unix())
	}
	if !q.To.IsZero() {
		where = append(where, "d.mtime < ?")
		args = append(args, q.To.Unix())
	}
	if q.MinSize > 0 {
		where = append(where, "d.size >= ?")
		args = append(args, q.MinSize)
	}
	if q.MaxSize > 0 {
		where = append(where, "d.size <= ?")
		args = append(args, q.MaxSize)
	}
	cond := strings.Join(where, " AND ")
	from := ` FROM fts JOIN docs d ON d.id = fts.rowid WHERE ` + cond
	if err := x.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1`+from+` LIMIT 1000)`, args...).Scan(&res.Total); err != nil {
		return res, queryError(err)
	}
	rows, err := x.db.QueryContext(ctx, `SELECT d.path, d.name, d.kind, d.size, d.mtime, snippet(fts, 1, char(2), char(3), '…', 24)`+from+
		` ORDER BY bm25(fts, 6.0, 1.0) LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return res, queryError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h Hit
		var snip string
		if err := rows.Scan(&h.Path, &h.Name, &h.Kind, &h.Size, &h.MTime, &snip); err != nil {
			return res, err
		}
		if q.Roots != nil && !Under(q.Roots, h.Path) {
			continue // never happens: the query already filters
		}
		h.Snippet = SnippetParts(snip)
		res.Hits = append(res.Hits, h)
	}
	res.More = q.Offset+len(res.Hits) < res.Total
	return res, rows.Err()
}

func queryError(err error) error {
	if strings.Contains(err.Error(), "fts5") {
		return fmt.Errorf("invalid search: %w", err)
	}
	return err
}

// SnippetParts splits a snippet marked with \x02…\x03 into parts.
func SnippetParts(s string) []Part {
	out := []Part{}
	hit := false
	for s != "" {
		sep := "\x02"
		if hit {
			sep = "\x03"
		}
		i := strings.Index(s, sep)
		t := s
		if i >= 0 {
			t, s = s[:i], s[i+1:]
		} else {
			s = ""
		}
		if t != "" {
			out = append(out, Part{Text: strings.ReplaceAll(t, "\n", " "), Hit: hit})
		}
		hit = !hit
	}
	return out
}

// Under tells whether p is inside one of roots.
func Under(roots []string, p string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}
