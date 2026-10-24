package syncjournal

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(p Page) string {
	var l []string
	for _, c := range p.Changes {
		s := c.Path
		if c.Dir {
			s += "/"
		}
		if c.Deleted != 0 {
			s = "-" + s
		}
		l = append(l, s)
	}
	sort.Strings(l)
	return strings.Join(l, " ")
}

func scan(t *testing.T, j *Journal) {
	t.Helper()
	if _, err := j.Scan("", true); err != nil {
		t.Fatal(err)
	}
}

func changes(t *testing.T, j *Journal, prefix, cursor string) Page {
	t.Helper()
	p, err := j.Changes(prefix, cursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestJournalChanges(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "a")
	write(t, root, "docs/b.txt", "b")
	write(t, root, "docs/sub/c.txt", "c")
	write(t, root, ".strata-upload-x", "temp")
	write(t, root, ".recycle/alice/old.txt", "old")
	write(t, root, "docs/.recycle", "not at the root: synced")
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	j := New(root)
	scan(t, j)

	full := changes(t, j, "", "")
	if !full.Reset || paths(full) != "a.txt docs/ docs/.recycle docs/b.txt docs/sub/ docs/sub/c.txt" {
		t.Fatalf("full listing: reset=%v %q", full.Reset, paths(full))
	}
	if got := paths(changes(t, j, "docs", "")); got != ".recycle b.txt sub/ sub/c.txt" {
		t.Fatalf("listing of a folder: %q", got)
	}
	cur := full.Cursor
	if p := changes(t, j, "", cur); len(p.Changes) != 0 || p.Reset || p.Cursor != cur {
		t.Fatalf("nothing changed: %+v", p)
	}

	// A rescan without changes changes nothing.
	scan(t, j)
	if p := changes(t, j, "", cur); len(p.Changes) != 0 {
		t.Fatalf("rescan made changes: %q", paths(p))
	}

	// Modify, create, delete a folder.
	time.Sleep(10 * time.Millisecond)
	write(t, root, "a.txt", "aa")
	write(t, root, "new/d.txt", "d")
	if err := os.RemoveAll(filepath.Join(root, "docs/sub")); err != nil {
		t.Fatal(err)
	}
	scan(t, j)
	p := changes(t, j, "", cur)
	if got := paths(p); got != "-docs/sub/ -docs/sub/c.txt a.txt new/ new/d.txt" {
		t.Fatalf("changes: %q", got)
	}
	if p.Reset {
		t.Fatal("unexpected reset")
	}
	// Under a folder only.
	if got := paths(changes(t, j, "docs", cur)); got != "-sub/ -sub/c.txt" {
		t.Fatalf("changes under docs: %q", got)
	}
	// The next cursor sees nothing more.
	if p2 := changes(t, j, "", p.Cursor); len(p2.Changes) != 0 {
		t.Fatalf("after the cursor: %q", paths(p2))
	}
	// A deleted file has no entry; a full listing has no tombstones.
	if _, ok := j.Lookup("docs/sub/c.txt"); ok {
		t.Fatal("deleted file still found")
	}
	if got := paths(changes(t, j, "", "")); strings.Contains(got, "-") {
		t.Fatalf("full listing with deletions: %q", got)
	}
}

func TestJournalPaging(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		write(t, root, n, n)
	}
	j := New(root)
	scan(t, j)
	var all []string
	cur := ""
	for range 10 {
		p, err := j.Changes("", cur, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range p.Changes {
			all = append(all, c.Path)
		}
		cur = p.Cursor
		if !p.More {
			break
		}
		if len(p.Changes) != 2 {
			t.Fatalf("page of %d", len(p.Changes))
		}
	}
	sort.Strings(all)
	if strings.Join(all, ",") != "1,2,3,4,5" {
		t.Fatalf("pages: %v", all)
	}
}

func TestJournalCursorReset(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a", "a")
	j := New(root)
	scan(t, j)
	cur := j.Cursor()
	for _, c := range []string{"other:1", "x", "e:-1"} {
		p, err := j.Changes("", c, 0)
		if c == "other:1" {
			if err != nil || !p.Reset || paths(p) != "a" {
				t.Fatalf("foreign epoch: %v %+v", err, p)
			}
			continue
		}
		if err == nil {
			t.Fatalf("cursor %q accepted", c)
		}
	}
	// Deletions forgotten after the TTL: older cursors start over.
	os.Remove(filepath.Join(root, "a"))
	scan(t, j)
	j.now = func() time.Time { return time.Now().Add(TombstoneTTL + time.Hour) }
	j.Prune()
	if p := changes(t, j, "", cur); !p.Reset {
		t.Fatal("cursor older than the forgotten deletions not reset")
	}
}

func TestJournalHashCacheAndMoves(t *testing.T) {
	root := t.TempDir()
	write(t, root, "big.bin", "content")
	j := New(root)
	scan(t, j)
	h, err := HashFile(j, "big.bin")
	if err != nil || len(h) != 64 {
		t.Fatalf("hash: %v %q", err, h)
	}
	e, _ := j.Lookup("big.bin")
	if e.Hash != h {
		t.Fatal("hash not kept in the index")
	}
	cur := j.Cursor()
	if p := changes(t, j, "", cur); len(p.Changes) != 0 {
		t.Fatal("a computed hash is not a change")
	}
	// Moved: same inode, size and date, so the hash is known without reading.
	if err := os.Rename(filepath.Join(root, "big.bin"), filepath.Join(root, "moved.bin")); err != nil {
		t.Fatal(err)
	}
	scan(t, j)
	p := changes(t, j, "", cur)
	if paths(p) != "-big.bin moved.bin" {
		t.Fatalf("move: %q", paths(p))
	}
	for _, c := range p.Changes {
		if c.Hash != h {
			t.Fatalf("%s lost its hash", c.Path)
		}
	}
	if p.Changes[0].Ino != p.Changes[1].Ino {
		t.Fatal("a move keeps the id")
	}
	// Changed content: no hash until computed again.
	time.Sleep(10 * time.Millisecond)
	write(t, root, "moved.bin", "other content")
	scan(t, j)
	if e, _ := j.Lookup("moved.bin"); e.Hash != "" {
		t.Fatal("stale hash after a change")
	}
	if u := j.Unhashed(10); len(u) != 1 || u[0].Path != "moved.bin" {
		t.Fatalf("unhashed: %v", u)
	}
}

func TestJournalVersions(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f", "1")
	j := New(root)
	scan(t, j)
	e1, _ := j.Lookup("f")
	st, _ := os.Stat(filepath.Join(root, "f"))
	if StatVersion(st) != e1.Version() {
		t.Fatal("version from stat differs from the index")
	}
	// Replaced by an editor (new file renamed over it): another version, even with the same size and date.
	write(t, root, "f.new", "2")
	os.Chtimes(filepath.Join(root, "f.new"), st.ModTime(), st.ModTime())
	os.Rename(filepath.Join(root, "f.new"), filepath.Join(root, "f"))
	scan(t, j)
	e2, _ := j.Lookup("f")
	if e2.Version() == e1.Version() || e2.Seq == e1.Seq {
		t.Fatal("replacement not seen")
	}
	// A file turned into a folder.
	os.Remove(filepath.Join(root, "f"))
	write(t, root, "f/inside", "x")
	scan(t, j)
	if e, ok := j.Lookup("f"); !ok || !e.Dir {
		t.Fatal("folder not seen")
	}
	if _, ok := j.Lookup("f/inside"); !ok {
		t.Fatal("content of the new folder not seen")
	}
}

func TestJournalShallowScan(t *testing.T) {
	root := t.TempDir()
	write(t, root, "d/a", "a")
	j := New(root)
	scan(t, j)
	cur := j.Cursor()
	// A folder moved in with content: a scan of the parent sees it all.
	write(t, root, "elsewhere/x/y/z.txt", "z")
	os.Rename(filepath.Join(root, "elsewhere"), filepath.Join(root, "d/in"))
	added, err := j.Scan("d", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(changes(t, j, "", cur)); got != "d/in/ d/in/x/ d/in/x/y/ d/in/x/y/z.txt" {
		t.Fatalf("moved-in folder: %q", got)
	}
	if len(added) != 3 {
		t.Fatalf("added folders: %v", added)
	}
	// A scan of a folder that vanished goes to its parent.
	os.RemoveAll(filepath.Join(root, "d"))
	if _, err := j.Scan("d/in/x", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := j.Lookup("d/a"); ok {
		t.Fatal("deletion not seen through the parent")
	}
}

func TestJournalRootUnavailable(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "share")
	write(t, root, "a", "a")
	j := New(root)
	scan(t, j)
	os.Rename(root, root+".away") // volume not mounted
	if _, err := j.Scan("", true); err != ErrRootUnavailable {
		t.Fatalf("missing root: %v", err)
	}
	if _, ok := j.Lookup("a"); !ok {
		t.Fatal("files marked deleted while the folder is unavailable")
	}
}

func TestJournalSaveLoad(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "idx")
	write(t, root, "a", "a")
	j := New(root)
	scan(t, j)
	if err := j.Save(file); err != nil {
		t.Fatal(err)
	}
	saved := j.Cursor()
	// Changes after the save, seen by a client, then a crash.
	write(t, root, "b", "b")
	scan(t, j)
	seen := j.Cursor()

	k := Load(file, root)
	if p := changes(t, k, "", saved); p.Reset || len(p.Changes) != 0 {
		t.Fatalf("saved cursor after loading: %+v", p)
	}
	scan(t, k)
	p := changes(t, k, "", seen)
	if p.Reset || paths(p) != "b" {
		t.Fatalf("changes lost in the crash are found again: reset=%v %q", p.Reset, paths(p))
	}
	if Load(file, "/other").Cursor() == k.Cursor() {
		t.Fatal("index of another folder loaded")
	}
}

func TestJournalWatcher(t *testing.T) {
	root := t.TempDir()
	write(t, root, "d/a", "a")
	j := New(root)
	scan(t, j)
	w, err := Watch(j)
	if err != nil {
		t.Skip("no inotify:", err)
	}
	defer w.Close()
	cur := j.Cursor()
	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			got := paths(changes(t, j, "", cur))
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("watcher: got %q, want %q", got, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	write(t, root, "d/b", "b")
	wait("d/b")
	os.MkdirAll(filepath.Join(root, "d/new"), 0o755)
	wait("d/b d/new/")
	write(t, root, "d/new/c", "c") // inside a folder created after the start
	wait("d/b d/new/ d/new/c")
	os.Rename(filepath.Join(root, "d"), filepath.Join(root, "e"))
	wait("-d/ -d/a -d/b -d/new/ -d/new/c e/ e/a e/b e/new/ e/new/c")
	cur = j.Cursor()
	write(t, root, "e/new/d", "d") // the watches followed the move
	wait("e/new/d")
}
