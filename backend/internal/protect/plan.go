package protect

import (
	"fmt"
	"math/rand/v2"
	"path"
	"sort"
	"strings"
	"time"
)

// Entry is a file or folder of a backup point, at its original path.
type Entry struct {
	Path  string    `json:"path"` // original absolute path on the NAS
	Name  string    `json:"name"`
	Dir   bool      `json:"dir"`
	Link  bool      `json:"link,omitempty"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

// What happens to one file of a restore.
const (
	ActNew       = "new"       // nothing there: created
	ActSame      = "same"      // same size and date: left alone
	ActOverwrite = "overwrite" // older or same-age file there: replaced
	ActConflict  = "conflict"  // the file there changed after the backup
)

// Item is a file the preview reports (overwrites and conflicts).
type Item struct {
	Path     string    `json:"path"` // where it is restored
	Action   string    `json:"action"`
	Size     int64     `json:"size"`
	CurSize  int64     `json:"curSize"`
	MTime    time.Time `json:"mtime"`
	CurMTime time.Time `json:"curMtime"`
}

// Preview sums up what a restore would do.
type Preview struct {
	Files      int    `json:"files"`
	Dirs       int    `json:"dirs"`
	Bytes      int64  `json:"bytes"` // size of all the files of the selection
	New        int    `json:"new"`
	NewBytes   int64  `json:"newBytes"`
	Same       int    `json:"same"`
	Overwrite  int    `json:"overwrite"`
	Conflicts  int    `json:"conflicts"`
	Changed    int64  `json:"changedBytes"` // size of the overwrites and conflicts
	Items      []Item `json:"items"`        // overwrites and conflicts, MaxItems at most
	Truncated  bool   `json:"truncated"`    // the listing stopped at the limit
	Sample     []Item `json:"sample"`       // a few new files, to show what comes back
	Target     string `json:"target"`
	ConfigFile string `json:"configFile,omitempty"` // a StrataOS configuration export found in the selection
}

// MaxItems bounds the lists of a preview.
const MaxItems = 200

// Action compares a file of the backup with what is at its destination.
func Action(src Entry, cur Entry, exists bool) string {
	switch {
	case !exists:
		return ActNew
	case cur.Dir != src.Dir:
		return ActConflict
	case cur.Size == src.Size && sameTime(cur.MTime, src.MTime):
		return ActSame
	case cur.MTime.After(src.MTime.Add(time.Second)):
		return ActConflict
	}
	return ActOverwrite
}

// sameTime compares modification times at the second (some backups and
// clouds keep no more).
func sameTime(a, b time.Time) bool {
	d := a.Sub(b)
	return d < time.Second && d > -time.Second
}

// Compare builds the preview. dest maps an entry to its destination path,
// and stat reads what is there.
func Compare(src []Entry, dest func(string) string, stat func(string) (Entry, bool)) Preview {
	p := Preview{Items: []Item{}, Sample: []Item{}}
	for _, e := range src {
		if e.Dir {
			p.Dirs++
			continue
		}
		p.Files++
		p.Bytes += e.Size
		to := dest(e.Path)
		cur, ok := stat(to)
		act := Action(e, cur, ok)
		it := Item{Path: to, Action: act, Size: e.Size, CurSize: cur.Size, MTime: e.MTime, CurMTime: cur.MTime}
		switch act {
		case ActNew:
			p.New++
			p.NewBytes += e.Size
			if len(p.Sample) < 20 {
				p.Sample = append(p.Sample, it)
			}
			continue
		case ActSame:
			p.Same++
			continue
		case ActOverwrite:
			p.Overwrite++
		case ActConflict:
			p.Conflicts++
		}
		p.Changed += e.Size
		if len(p.Items) < MaxItems {
			p.Items = append(p.Items, it)
		}
	}
	// Conflicts first: they need a look.
	sort.SliceStable(p.Items, func(i, j int) bool {
		return p.Items[i].Action == ActConflict && p.Items[j].Action != ActConflict
	})
	return p
}

// Destination maps an original path to where a restore puts it: in place,
// or under target with the path below base (the folder holding the
// selected item).
func Destination(abs, base, target string) string {
	if target == "" {
		return abs
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(abs, base), "/")
	return path.Join(target, rel)
}

// RenamedPath is where a restored file goes next to a file it must not
// replace: "report (restored 2026-11-03 14h05).pdf".
func RenamedPath(p string, at time.Time) string {
	dir, name := path.Split(p)
	ext := path.Ext(name)
	if ext == name { // ".bashrc"
		ext = ""
	}
	return dir + fmt.Sprintf("%s (restored %s)%s", strings.TrimSuffix(name, ext), at.Format("2006-01-02 15h04"), ext)
}

// Sample picks up to n files for a restore test, at random but favouring
// small ones, within budget bytes in total. Empty files prove little.
func Sample(files []Entry, n int, budget int64, rnd *rand.Rand) []Entry {
	var pool []Entry
	for _, f := range files {
		if !f.Dir && !f.Link && f.Size > 0 && f.Size <= budget {
			pool = append(pool, f)
		}
	}
	rnd.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	var out []Entry
	var total int64
	for _, f := range pool {
		if len(out) == n {
			break
		}
		if total+f.Size > budget {
			continue
		}
		out = append(out, f)
		total += f.Size
	}
	return out
}
