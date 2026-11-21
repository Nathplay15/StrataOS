package photoml

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB holds what was computed for each picture (CLIP embedding, tags,
// faces) and, per user, the people: each user groups and names the faces
// of the pictures they can see, nobody else sees their names.
type DB struct {
	file string
	db   *sql.DB
	mu   sync.Mutex // one writer at a time
}

// Steps done for a picture (bits of photos.done).
const (
	DoneFaces = 1
	DoneClip  = 2
)

const dbSchema = `
CREATE TABLE IF NOT EXISTS photos(
	path TEXT PRIMARY KEY,
	mtime INTEGER NOT NULL,
	done INTEGER NOT NULL DEFAULT 0,
	err TEXT NOT NULL DEFAULT '',
	clip BLOB,
	tags TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS faces(
	id INTEGER PRIMARY KEY,
	path TEXT NOT NULL,
	x REAL NOT NULL, y REAL NOT NULL, w REAL NOT NULL, h REAL NOT NULL,
	score REAL NOT NULL,
	emb BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS faces_path ON faces(path);
CREATE TABLE IF NOT EXISTS people(
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	hidden INTEGER NOT NULL DEFAULT 0,
	created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS people_owner ON people(owner);
CREATE TABLE IF NOT EXISTS assign(
	owner TEXT NOT NULL,
	face INTEGER NOT NULL,
	person INTEGER NOT NULL, -- 0: nobody yet
	manual INTEGER NOT NULL DEFAULT 0, -- set by the user: never changed by grouping
	PRIMARY KEY(owner, face)
);
CREATE INDEX IF NOT EXISTS assign_person ON assign(person);
CREATE TABLE IF NOT EXISTS labels(prompt TEXT PRIMARY KEY, emb BLOB NOT NULL);
`

// OpenDB opens (or creates) the database.
func OpenDB(file string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(0)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(dbSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{file: file, db: db}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// Bytes is the size of the database files.
func (d *DB) Bytes() int64 {
	var n int64
	for _, f := range []string{d.file, d.file + "-wal"} {
		if fi, err := os.Stat(f); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// Processed returns the date (mtime) and steps of every analysed picture.
func (d *DB) Processed() map[string][2]int64 {
	out := map[string][2]int64{}
	rows, err := d.db.Query(`SELECT path, mtime, done FROM photos`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var m, done int64
		if rows.Scan(&p, &m, &done) == nil {
			out[p] = [2]int64{m, done}
		}
	}
	return out
}

// FaceResult is a face to store.
type FaceResult struct {
	Box   [4]float64
	Score float64
	Emb   Vec
}

// PutPhoto stores the analysis of a picture, replacing a previous one (its
// faces too: their people lose them).
func (d *DB) PutPhoto(path string, mtime int64, done int, errText string, clip Vec, tags []string, faces []FaceResult, replaceFaces bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var clipB []byte
	if clip != nil {
		clipB = clip.Bytes()
	}
	if _, err := tx.Exec(`INSERT INTO photos(path, mtime, done, err, clip, tags) VALUES(?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET mtime=excluded.mtime, done=excluded.done, err=excluded.err,
		clip=COALESCE(excluded.clip, photos.clip), tags=CASE WHEN excluded.clip IS NULL THEN photos.tags ELSE excluded.tags END`,
		path, mtime, done, errText, clipB, strings.Join(tags, ",")); err != nil {
		return err
	}
	if replaceFaces {
		if _, err := tx.Exec(`DELETE FROM assign WHERE face IN (SELECT id FROM faces WHERE path = ?)`, path); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM faces WHERE path = ?`, path); err != nil {
			return err
		}
		for _, f := range faces {
			if _, err := tx.Exec(`INSERT INTO faces(path, x, y, w, h, score, emb) VALUES(?,?,?,?,?,?,?)`,
				path, f.Box[0], f.Box[1], f.Box[2], f.Box[3], f.Score, f.Emb.Bytes()); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Forget removes pictures no longer indexed.
func (d *DB) Forget(paths []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range paths {
		for _, q := range []string{
			`DELETE FROM assign WHERE face IN (SELECT id FROM faces WHERE path = ?)`,
			`DELETE FROM faces WHERE path = ?`,
			`DELETE FROM photos WHERE path = ?`,
		} {
			if _, err := tx.Exec(q, p); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Clear removes every analysis (people and their names stay, without faces).
func (d *DB) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, q := range []string{`DELETE FROM assign`, `DELETE FROM faces`, `DELETE FROM photos`, `DELETE FROM people`, `VACUUM`} {
		if _, err := d.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// Counts of the analysis.
type Counts struct {
	Photos int `json:"photos"`
	Failed int `json:"failed"`
	Faces  int `json:"faces"`
}

func (d *DB) Counts() Counts {
	var c Counts
	_ = d.db.QueryRow(`SELECT count(*), coalesce(sum(err != ''), 0) FROM photos`).Scan(&c.Photos, &c.Failed)
	_ = d.db.QueryRow(`SELECT count(*) FROM faces`).Scan(&c.Faces)
	return c
}

// ---- Label embeddings (cached) ----

// LabelEmbeddings returns the stored embeddings of prompts (missing ones nil).
func (d *DB) LabelEmbeddings(prompts []string) []Vec {
	out := make([]Vec, len(prompts))
	for i, p := range prompts {
		var b []byte
		if d.db.QueryRow(`SELECT emb FROM labels WHERE prompt = ?`, p).Scan(&b) == nil && len(b) == ClipDim {
			out[i] = VecOf(b)
		}
	}
	return out
}

// PutLabel stores the embedding of a prompt.
func (d *DB) PutLabel(prompt string, v Vec) error {
	_, err := d.db.Exec(`INSERT OR REPLACE INTO labels(prompt, emb) VALUES(?, ?)`, prompt, v.Bytes())
	return err
}

// ---- Reading for the views ----

// PhotoInfo is what the views need of a picture.
type PhotoInfo struct {
	Clip Vec
	Tags []string
}

// Photos calls f for every analysed picture with a CLIP embedding.
func (d *DB) Photos(f func(path string, info PhotoInfo)) error {
	rows, err := d.db.Query(`SELECT path, clip, tags FROM photos WHERE clip IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p, tags string
		var clip []byte
		if err := rows.Scan(&p, &clip, &tags); err != nil {
			return err
		}
		info := PhotoInfo{Clip: VecOf(clip)}
		if tags != "" {
			info.Tags = strings.Split(tags, ",")
		}
		f(p, info)
	}
	return rows.Err()
}

// FaceRow is a stored face.
type FaceRow struct {
	ID     int64      `json:"id"`
	Path   string     `json:"path"`
	Box    [4]float64 `json:"box"`
	Score  float64    `json:"score"`
	Person int64      `json:"person"`
	Manual bool       `json:"manual,omitempty"`
	Emb    Vec        `json:"-"`
}

// Faces returns the faces of the pictures allow accepts, with their person
// for owner (0: nobody, -1: not grouped yet for this user).
func (d *DB) Faces(owner string, allow func(string) bool, withEmb bool) ([]FaceRow, error) {
	cols := "f.id, f.path, f.x, f.y, f.w, f.h, f.score, coalesce(a.person, -1), coalesce(a.manual, 0)"
	if withEmb {
		cols += ", f.emb"
	}
	rows, err := d.db.Query(`SELECT `+cols+` FROM faces f LEFT JOIN assign a ON a.face = f.id AND a.owner = ? ORDER BY f.id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FaceRow
	for rows.Next() {
		var f FaceRow
		var manual int
		dest := []any{&f.ID, &f.Path, &f.Box[0], &f.Box[1], &f.Box[2], &f.Box[3], &f.Score, &f.Person, &manual}
		var emb []byte
		if withEmb {
			dest = append(dest, &emb)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if !allow(f.Path) {
			continue
		}
		f.Manual = manual != 0
		if withEmb {
			f.Emb = VecOf(emb)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Face returns one face.
func (d *DB) Face(id int64) (FaceRow, error) {
	var f FaceRow
	err := d.db.QueryRow(`SELECT id, path, x, y, w, h, score FROM faces WHERE id = ?`, id).Scan(&f.ID, &f.Path, &f.Box[0], &f.Box[1], &f.Box[2], &f.Box[3], &f.Score)
	return f, err
}

// PersonRow is a person of a user.
type PersonRow struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Hidden  bool   `json:"hidden"`
	Created int64  `json:"created"`
}

// People returns the people of owner.
func (d *DB) People(owner string) (map[int64]PersonRow, error) {
	rows, err := d.db.Query(`SELECT id, name, hidden, created FROM people WHERE owner = ?`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]PersonRow{}
	for rows.Next() {
		var p PersonRow
		var hidden int
		if err := rows.Scan(&p.ID, &p.Name, &hidden, &p.Created); err != nil {
			return nil, err
		}
		p.Hidden = hidden != 0
		out[p.ID] = p
	}
	return out, rows.Err()
}

// ErrNoPerson: the person does not exist or belongs to someone else.
var ErrNoPerson = errors.New("person not found")

func (d *DB) owns(tx *sql.Tx, owner string, id int64) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM people WHERE id = ? AND owner = ?`, id, owner).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNoPerson
	}
	return nil
}

// UpdatePerson renames or hides a person of owner (nil: unchanged).
func (d *DB) UpdatePerson(owner string, id int64, name *string, hidden *bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := d.owns(tx, owner, id); err != nil {
		return err
	}
	if name != nil {
		if _, err := tx.Exec(`UPDATE people SET name = ? WHERE id = ?`, *name, id); err != nil {
			return err
		}
	}
	if hidden != nil {
		h := 0
		if *hidden {
			h = 1
		}
		if _, err := tx.Exec(`UPDATE people SET hidden = ? WHERE id = ?`, h, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Merge moves the faces of the people from into the person into, and
// deletes them. The name of into wins; when it has none, the first name found.
func (d *DB) Merge(owner string, into int64, from []int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := d.owns(tx, owner, into); err != nil {
		return err
	}
	for _, id := range from {
		if id == into {
			continue
		}
		if err := d.owns(tx, owner, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE people SET name = (SELECT name FROM people WHERE id = ?) WHERE id = ? AND name = ''`, id, into); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE assign SET person = ?, manual = 1 WHERE owner = ? AND person = ?`, into, owner, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM people WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MoveFaces gives faces of owner to a person: to > 0 an existing one, 0 a
// new one (named name), -1 nobody ("not this person"). The choice is the
// user's: grouping never changes it. It returns the person.
func (d *DB) MoveFaces(owner string, faces []int64, to int64, name string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	switch {
	case to > 0:
		if err := d.owns(tx, owner, to); err != nil {
			return 0, err
		}
	case to == 0:
		res, err := tx.Exec(`INSERT INTO people(owner, name, created) VALUES(?, ?, ?)`, owner, name, time.Now().Unix())
		if err != nil {
			return 0, err
		}
		to, _ = res.LastInsertId()
	default:
		to = 0
	}
	for _, f := range faces {
		if _, err := tx.Exec(`INSERT INTO assign(owner, face, person, manual) VALUES(?, ?, ?, 1)
			ON CONFLICT(owner, face) DO UPDATE SET person = excluded.person, manual = 1`, owner, f, to); err != nil {
			return 0, err
		}
	}
	// People left without faces go away.
	if _, err := tx.Exec(`DELETE FROM people WHERE owner = ? AND id NOT IN (SELECT DISTINCT person FROM assign WHERE owner = ?)`, owner, owner); err != nil {
		return 0, err
	}
	return to, tx.Commit()
}

// Group runs Cluster for owner on the faces allow accepts and stores the
// result: faces the user moved by hand stay where they are.
func (d *DB) Group(owner string, allow func(string) bool) (newPeople int, err error) {
	faces, err := d.Faces(owner, allow, true)
	if err != nil {
		return 0, err
	}
	byPerson := map[int64][]Vec{}
	var pool, fresh []Face
	for _, f := range faces {
		switch {
		case f.Person > 0:
			byPerson[f.Person] = append(byPerson[f.Person], f.Emb)
		case f.Person == 0 && !f.Manual:
			pool = append(pool, Face{f.ID, f.Emb})
		case f.Person < 0:
			fresh = append(fresh, Face{f.ID, f.Emb})
		}
	}
	if len(fresh) == 0 {
		return 0, nil
	}
	var people []Person
	for id, vs := range byPerson {
		people = append(people, PersonOf(id, vs))
	}
	g := Cluster(people, pool, fresh)
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	set := func(face, person int64) error {
		_, err := tx.Exec(`INSERT INTO assign(owner, face, person) VALUES(?, ?, ?)
			ON CONFLICT(owner, face) DO UPDATE SET person = excluded.person WHERE assign.manual = 0`, owner, face, person)
		return err
	}
	for f, p := range g.Assign {
		if err := set(f, p); err != nil {
			return 0, err
		}
	}
	now := time.Now().Unix()
	for _, group := range g.New {
		res, err := tx.Exec(`INSERT INTO people(owner, created) VALUES(?, ?)`, owner, now)
		if err != nil {
			return 0, err
		}
		id, _ := res.LastInsertId()
		for _, f := range group {
			if err := set(f, id); err != nil {
				return 0, err
			}
		}
	}
	for _, f := range g.Alone {
		if err := set(f, 0); err != nil {
			return 0, err
		}
	}
	return len(g.New), tx.Commit()
}

// ForgetOwner removes the people of a deleted user.
func (d *DB) ForgetOwner(owner string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.db.Exec(`DELETE FROM assign WHERE owner = ?`, owner); err != nil {
		return err
	}
	_, err := d.db.Exec(`DELETE FROM people WHERE owner = ?`, owner)
	return err
}
