package engine

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type harness struct {
	t      *testing.T
	local  string
	fake   *fakeRemote
	eng    *Engine
	trash  []string
	events []Event
	mu     sync.Mutex
}

var t0 = time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)

func newHarness(t *testing.T, opts ...func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, local: t.TempDir(), fake: newFake()}
	trashDir := t.TempDir()
	o := Options{StateFile: filepath.Join(t.TempDir(), "state.db"), Device: "Laptop", Now: func() time.Time { return t0 },
		Logf: func(string, ...any) {},
		Trash: func(p string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			rel, _ := filepath.Rel(h.local, p)
			h.trash = append(h.trash, filepath.ToSlash(rel))
			return os.Rename(p, filepath.Join(trashDir, strings.ReplaceAll(rel, string(filepath.Separator), "_")))
		},
		OnEvent: func(e Event) { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() }}
	for _, f := range opts {
		f(&o)
	}
	e, err := New(h.fake, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	h.eng = e
	if err := e.SetPairs([]Pair{{ID: "p1", Local: h.local, Remote: "/srv/strata/main/Docs"}}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) sync() {
	h.t.Helper()
	if err := h.eng.SyncOnce(context.Background()); err != nil {
		h.t.Fatalf("sync: %v", err)
	}
}

func (h *harness) write(rel, content string, mt time.Time) {
	h.t.Helper()
	p := filepath.Join(h.local, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
	os.Chtimes(p, mt, mt)
}

func (h *harness) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(h.local, filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (h *harness) tree() string {
	var l []string
	filepath.WalkDir(h.local, func(p string, d os.DirEntry, err error) error {
		if p == h.local {
			return nil
		}
		rel, _ := filepath.Rel(h.local, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		l = append(l, rel)
		return nil
	})
	sort.Strings(l)
	return strings.Join(l, " ")
}

func (h *harness) pair() *pairRun {
	h.eng.mu.Lock()
	defer h.eng.mu.Unlock()
	return h.eng.pairs["p1"]
}

func TestFirstSyncBothWays(t *testing.T) {
	h := newHarness(t)
	h.write("local.txt", "from the computer", t0)
	h.write("sub/deep/l2.txt", "deep", t0)
	h.fake.put("remote.txt", "from the NAS", t0)
	h.fake.put("photos/2024/a.jpg", "jpeg", t0)
	h.write("same.txt", "identical", t0)
	h.fake.put("same.txt", "identical", t0.Add(time.Hour)) // same content, other date: no conflict
	h.sync()
	if got := h.tree(); got != "local.txt photos/ photos/2024/ photos/2024/a.jpg remote.txt same.txt sub/ sub/deep/ sub/deep/l2.txt" {
		t.Fatalf("local tree: %s", got)
	}
	if got := h.fake.list(); got != "local.txt photos/ photos/2024/ photos/2024/a.jpg remote.txt same.txt sub/ sub/deep/ sub/deep/l2.txt" {
		t.Fatalf("NAS tree: %s", got)
	}
	if h.read("photos/2024/a.jpg") != "jpeg" {
		t.Fatal("downloaded content")
	}
	if c, _ := h.fake.get("sub/deep/l2.txt"); c != "deep" {
		t.Fatal("uploaded content")
	}
	fi, _ := os.Stat(filepath.Join(h.local, "remote.txt"))
	if !fi.ModTime().Equal(t0) {
		t.Fatalf("date not kept: %v", fi.ModTime())
	}
	// Nothing more to do.
	up, down := h.fake.calls["upload"], h.fake.calls["download"]
	h.sync()
	if h.fake.calls["upload"] != up || h.fake.calls["download"] != down {
		t.Fatal("a second round transferred again")
	}
}

func TestChangesEachSide(t *testing.T) {
	h := newHarness(t)
	h.write("a.txt", "v1", t0)
	h.fake.put("b.txt", "v1", t0)
	h.sync()
	h.write("a.txt", "v2 local", t0.Add(time.Minute))
	h.fake.put("b.txt", "v2 remote", t0.Add(time.Minute))
	h.sync()
	if c, _ := h.fake.get("a.txt"); c != "v2 local" {
		t.Fatalf("local change not uploaded: %q", c)
	}
	if h.read("b.txt") != "v2 remote" {
		t.Fatal("remote change not downloaded")
	}
	if len(h.events) != 0 {
		t.Fatalf("events: %v", h.events)
	}
}

func TestConflictKeepsBoth(t *testing.T) {
	h := newHarness(t)
	h.write("report.docx", "v1", t0)
	h.sync()
	h.write("report.docx", "local edit", t0.Add(time.Minute))
	h.fake.put("report.docx", "remote edit!", t0.Add(2*time.Minute))
	h.sync()
	cname := "report (conflict from Laptop 2024-05-01 10-00).docx"
	if h.read("report.docx") != "remote edit!" || h.read(cname) != "local edit" {
		t.Fatalf("local: %s / %q %q", h.tree(), h.read("report.docx"), h.read(cname))
	}
	if c, _ := h.fake.get(cname); c != "local edit" {
		t.Fatalf("conflict copy not on the NAS: %s", h.fake.list())
	}
	if c, _ := h.fake.get("report.docx"); c != "remote edit!" {
		t.Fatal("NAS version lost")
	}
	if len(h.events) != 1 || h.events[0].Kind != "conflict" {
		t.Fatalf("events: %v", h.events)
	}
	// Both sides created the same name with different content.
	h.write("new.txt", "mine", t0)
	h.fake.put("new.txt", "theirs", t0)
	h.sync()
	if h.read("new.txt") != "theirs" || h.read("new (conflict from Laptop 2024-05-01 10-00).txt") != "mine" {
		t.Fatalf("both new: %s", h.tree())
	}
}

// The NAS file changes while the local one is sent: the copy sent is kept
// as the conflict copy, nothing is overwritten.
func TestUploadConflict(t *testing.T) {
	h := newHarness(t)
	h.write("a.txt", "v1", t0)
	h.sync()
	h.write("a.txt", "local", t0.Add(time.Minute))
	pr := h.pair()
	sc, _ := scanLocal(h.local, pr.n, pr.m)
	l := sc.entries["a.txt"]
	h.fake.put("a.txt", "remote", t0.Add(time.Minute)) // after the plan
	if err := pr.upload(context.Background(), "a.txt", l, pr.data.base["a.txt"].Version); err != nil {
		t.Fatal(err)
	}
	h.sync()
	cname := "a (conflict from Laptop 2024-05-01 10-00).txt"
	if h.read("a.txt") != "remote" || h.read(cname) != "local" {
		t.Fatalf("local: %s", h.tree())
	}
	if c, _ := h.fake.get(cname); c != "local" {
		t.Fatalf("NAS: %s", h.fake.list())
	}
	if c, _ := h.fake.get("a.txt"); c != "remote" {
		t.Fatal("NAS version overwritten")
	}
}

func TestDeletions(t *testing.T) {
	h := newHarness(t)
	h.write("keep.txt", "k", t0)
	h.write("gone-local.txt", "x", t0)
	h.fake.put("gone-remote.txt", "y", t0)
	h.fake.put("dir/inner.txt", "z", t0)
	h.sync()
	os.Remove(filepath.Join(h.local, "gone-local.txt"))
	h.fake.remove("gone-remote.txt")
	h.fake.remove("dir")
	h.sync()
	if got := h.fake.list(); got != "keep.txt" {
		t.Fatalf("NAS: %s", got)
	}
	if len(h.fake.recycle) != 1 || h.fake.recycle[0] != "gone-local.txt" {
		t.Fatalf("NAS recycle bin: %v", h.fake.recycle)
	}
	if got := h.tree(); got != "keep.txt" {
		t.Fatalf("local: %s", got)
	}
	sort.Strings(h.trash)
	if strings.Join(h.trash, ",") != "dir,gone-remote.txt" { // a folder goes to the trash whole
		t.Fatalf("local trash: %v", h.trash)
	}
	// Deleted here, changed there: the NAS version comes back.
	h.write("x.txt", "1", t0)
	h.sync()
	os.Remove(filepath.Join(h.local, "x.txt"))
	h.fake.put("x.txt", "2", t0.Add(time.Minute))
	h.sync()
	if h.read("x.txt") != "2" {
		t.Fatal("changed file deleted")
	}
	// Changed here, deleted there: the local version goes back.
	h.write("y.txt", "1", t0)
	h.sync()
	h.write("y.txt", "2", t0.Add(time.Minute))
	h.fake.remove("y.txt")
	h.sync()
	if c, _ := h.fake.get("y.txt"); c != "2" {
		t.Fatal("changed file lost")
	}
	// A folder deleted on the NAS while a new file was added inside it here.
	h.write("d2/old.txt", "o", t0)
	h.sync()
	h.write("d2/new.txt", "n", t0)
	h.fake.remove("d2")
	h.sync()
	h.sync()
	if h.read("d2/new.txt") != "n" {
		t.Fatal("new local file deleted with its folder")
	}
	if c, _ := h.fake.get("d2/new.txt"); c != "n" {
		t.Fatalf("new file not sent: %s", h.fake.list())
	}
}

func TestMovesWithoutTransfers(t *testing.T) {
	h := newHarness(t)
	h.write("a.txt", "aaa", t0)
	h.write("folder/b.txt", "bbb", t0)
	h.write("folder/sub/c.txt", "ccc", t0)
	h.sync()
	up := h.fake.calls["upload"]
	os.Rename(filepath.Join(h.local, "a.txt"), filepath.Join(h.local, "renamed.txt"))
	os.Rename(filepath.Join(h.local, "folder"), filepath.Join(h.local, "moved"))
	h.sync()
	if got := h.fake.list(); got != "moved/ moved/b.txt moved/sub/ moved/sub/c.txt renamed.txt" {
		t.Fatalf("NAS: %s", got)
	}
	if h.fake.calls["upload"] != up || h.fake.calls["move"] != 2 {
		t.Fatalf("moves sent again: %d uploads, %d moves", h.fake.calls["upload"]-up, h.fake.calls["move"])
	}
	if len(h.fake.recycle) != 0 {
		t.Fatal("a move deleted files")
	}
	// Moves on the NAS: renamed locally, nothing downloaded.
	down := h.fake.calls["download"]
	h.fake.rename("renamed.txt", "back.txt")
	h.fake.rename("moved", "elsewhere")
	h.sync()
	if got := h.tree(); got != "back.txt elsewhere/ elsewhere/b.txt elsewhere/sub/ elsewhere/sub/c.txt" {
		t.Fatalf("local: %s", got)
	}
	if h.fake.calls["download"] != down || len(h.trash) != 0 {
		t.Fatalf("NAS moves downloaded again (%d) or trashed (%v)", h.fake.calls["download"]-down, h.trash)
	}
	h.sync()
	if h.fake.calls["upload"] != up || h.fake.calls["download"] != down {
		t.Fatal("transfers after the moves settled")
	}
}

func TestCaseChange(t *testing.T) {
	h := newHarness(t)
	pr := h.pair()
	pr.n = namer{fold: true} // as on Windows and macOS
	pr.m = newMatcher(pr.n, DefaultIgnore, nil)
	h.write("readme.txt", "r", t0)
	h.sync()
	h.fake.rename("readme.txt", "README.txt")
	h.sync()
	if got := h.tree(); got != "README.txt" {
		t.Fatalf("case change on the NAS: %s", got)
	}
	os.Rename(filepath.Join(h.local, "README.txt"), filepath.Join(h.local, "ReadMe.txt"))
	h.sync()
	if got := h.fake.list(); got != "ReadMe.txt" {
		t.Fatalf("case change here: %s", got)
	}
	// Two NAS names this computer cannot hold side by side: one is skipped.
	h.fake.put("Data.csv", "1", t0)
	h.fake.put("data.csv", "2", t0)
	h.sync()
	st := h.eng.Status().Pairs[0]
	if len(st.Skipped) != 1 || st.Skipped[0].Path != "data.csv" {
		t.Fatalf("skipped: %+v", st.Skipped)
	}
	if h.read("Data.csv") != "1" {
		t.Fatal("first spelling not synced")
	}
}

func TestInterruptedTransfersResume(t *testing.T) {
	h := newHarness(t)
	big := strings.Repeat("0123456789", 1000)
	h.fake.put("big.bin", big, t0)
	h.fake.failDownloadAt = 3000
	if err := h.eng.SyncOnce(context.Background()); err == nil {
		t.Fatal("no error for an interrupted download")
	}
	if h.read("big.bin") != "<missing>" {
		t.Fatal("partial file in place")
	}
	h.sync()
	if h.read("big.bin") != big {
		t.Fatal("resumed download damaged")
	}
	// Upload: interrupted, then resumed with the same upload.
	h.write("up.bin", big+"x", t0)
	h.fake.failUploadAt = 4000
	if err := h.eng.SyncOnce(context.Background()); err == nil {
		t.Fatal("no error for an interrupted upload")
	}
	h.sync()
	if c, _ := h.fake.get("up.bin"); c != big+"x" {
		t.Fatal("resumed upload damaged")
	}
	if h.fake.calls["resumed"] != 1 {
		t.Fatalf("upload not resumed: %v", h.fake.calls)
	}
	// Changed on the NAS during the download: planned again, then right.
	h.fake.put("moving.txt", "v1", t0)
	h.fake.onDownload = func(string) { h.fake.put("moving.txt", "v2", t0.Add(time.Minute)) }
	h.sync()
	if h.read("moving.txt") != "v2" {
		t.Fatalf("got %q", h.read("moving.txt"))
	}
}

func TestDeleteBrake(t *testing.T) {
	for _, apply := range []bool{true, false} {
		h := newHarness(t)
		for i := range 30 {
			h.fake.put("f"+string(rune('a'+i))+".txt", "x", t0)
		}
		h.sync()
		for i := range 25 {
			h.fake.remove("f" + string(rune('a'+i)) + ".txt")
		}
		h.sync()
		st := h.eng.Status().Pairs[0]
		if st.Brake == nil || st.Brake.LocalDeletes != 25 || st.State != "brake" {
			t.Fatalf("no brake: %+v", st)
		}
		if n := len(strings.Fields(h.tree())); n != 30 || len(h.trash) != 0 {
			t.Fatalf("deleted before the user said so: %d files", n)
		}
		if len(h.events) != 1 || h.events[0].Kind != "brake" {
			t.Fatalf("events: %v", h.events)
		}
		h.eng.ResolveBrake("p1", apply)
		h.sync()
		h.sync()
		if apply {
			if n := len(strings.Fields(h.tree())); n != 5 {
				t.Fatalf("deletions not applied: %d files", n)
			}
		} else if n := len(strings.Fields(h.fake.list())); n != 30 {
			t.Fatalf("files not restored on the NAS: %d", n)
		}
		if h.eng.Status().Pairs[0].Brake != nil {
			t.Fatal("brake still on")
		}
	}
}

func TestIgnoredAndExcluded(t *testing.T) {
	h := newHarness(t)
	h.eng.SetPairs([]Pair{{ID: "p1", Local: h.local, Remote: "/x", Exclude: []string{"Big/Videos"}}})
	h.write(".DS_Store", "junk", t0)
	h.write("~$report.docx", "lock", t0)
	h.write("doc.tmp", "tmp", t0)
	h.write(".gitignore", "kept", t0)
	h.fake.put("Big/Videos/movie.mkv", "huge", t0)
	h.fake.put("Big/small.txt", "s", t0)
	if err := os.Symlink("/etc", filepath.Join(h.local, "link")); err != nil {
		t.Fatal(err)
	}
	h.sync()
	if got := h.fake.list(); got != ".gitignore Big/ Big/Videos/ Big/Videos/movie.mkv Big/small.txt" {
		t.Fatalf("NAS: %s", got)
	}
	if h.read("Big/Videos/movie.mkv") != "<missing>" || h.read("Big/small.txt") != "s" {
		t.Fatalf("local: %s", h.tree())
	}
	if h.eng.Status().Pairs[0].Links != 1 {
		t.Fatal("symlink not reported")
	}
}

func TestMissingFoldersDeleteNothing(t *testing.T) {
	h := newHarness(t)
	h.write("a.txt", "a", t0)
	h.sync()
	// The local folder disappears (unplugged drive).
	os.Rename(h.local, h.local+".away")
	if err := h.eng.SyncOnce(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if _, ok := h.fake.get("a.txt"); !ok {
		t.Fatal("NAS file deleted")
	}
	os.Rename(h.local+".away", h.local)
	// An unreadable local folder is not a deleted one.
	h.write("locked/b.txt", "b", t0)
	h.sync()
	os.Chmod(filepath.Join(h.local, "locked"), 0)
	defer os.Chmod(filepath.Join(h.local, "locked"), 0o755)
	if os.Getuid() != 0 {
		h.sync()
		if _, ok := h.fake.get("locked/b.txt"); !ok {
			t.Fatal("file of an unreadable folder deleted on the NAS")
		}
	}
}

func TestNames(t *testing.T) {
	for _, c := range []struct{ name, problem string }{
		{"ok.txt", ""}, {"a:b", "character"}, {"CON", "reserved"}, {"con.txt", "reserved"}, {"COM1.log", "reserved"},
		{"end.", "dot"}, {"end ", "space"}, {"what?.txt", "character"}, {"console.txt", ""},
	} {
		if p := localNameProblem(c.name, "windows"); c.problem == "" && p != "" || c.problem != "" && !strings.Contains(p, c.problem) {
			t.Errorf("%q on Windows: %q", c.name, p)
		}
		if p := localNameProblem(c.name, "linux"); p != "" {
			t.Errorf("%q on Linux: %q", c.name, p)
		}
	}
	for in, want := range map[string]string{
		"report.docx": "report (conflict from Laptop 2024-05-01 10-00).docx",
		".bashrc":     ".bashrc (conflict from Laptop 2024-05-01 10-00)",
		"archive":     "archive (conflict from Laptop 2024-05-01 10-00)",
	} {
		if got := conflictName(in, "Laptop", t0, 1); got != want {
			t.Errorf("conflictName(%q) = %q", in, got)
		}
	}
	if got := conflictName("a.txt", `My/PC:"1"`, t0, 2); got != "a (conflict from My-PC--1- 2024-05-01 10-00 2).txt" {
		t.Errorf("device name: %q", got)
	}
	m := newMatcher(namer{fold: true}, DefaultIgnore, []string{"Photos/RAW"})
	for p, ign := range map[string]bool{"a/.DS_Store": true, "THUMBS.DB": true, "~$x.docx": true, "x.TMP": true, ".~lock.a#": true,
		"photos/raw/1.cr2": true, "Photos/RAWS/1": false, "Photos/raw": true, "doc.txt": false, ".strataos-sync-1.part": true} {
		if m.ignored(p) != ign {
			t.Errorf("ignored(%q) != %v", p, ign)
		}
	}
	nfc := namer{fold: true, nfc: true}
	if nfc.key("Café.txt") != nfc.key("café.TXT") {
		t.Error("composed and decomposed forms differ")
	}
}

// makePlan on its own, for the cases the rounds above do not reach.
func TestPlanTable(t *testing.T) {
	f := func(p string, size, mt int64) *LocalEntry { return &LocalEntry{Path: p, Size: size, MTime: mt} }
	rf := func(p, v string, size int64) *RemoteEntry { return &RemoteEntry{Path: p, Version: v, Size: size} }
	b := func(p, v string, size, mt int64) *BaseEntry { return &BaseEntry{Path: p, Version: v, Size: size, LocalMTime: mt} }
	for _, c := range []struct {
		name string
		v    view
		want string
	}{
		{"unchanged", view{local: map[string]*LocalEntry{"a": f("a", 1, 1)}, remote: map[string]*RemoteEntry{"a": rf("a", "v", 1)}, base: map[string]*BaseEntry{"a": b("a", "v", 1, 1)}}, ""},
		{"both deleted", view{base: map[string]*BaseEntry{"a": b("a", "v", 1, 1)}}, "forget:a"},
		{"same size both new", view{local: map[string]*LocalEntry{"a": f("a", 1, 1)}, remote: map[string]*RemoteEntry{"a": rf("a", "v", 1)}}, "compare:a"},
		{"other size both new", view{local: map[string]*LocalEntry{"a": f("a", 1, 1)}, remote: map[string]*RemoteEntry{"a": rf("a", "v", 2)}}, "conflict:a"},
		{"file became folder here", view{local: map[string]*LocalEntry{"a": {Path: "a", Dir: true}}, remote: map[string]*RemoteEntry{"a": rf("a", "v", 1)}, base: map[string]*BaseEntry{"a": b("a", "v", 1, 1)}}, "delete-remote:a"},
		{"unreadable folder", view{remote: map[string]*RemoteEntry{"d": {Path: "d", Dir: true}, "d/x": rf("d/x", "v", 1)}, local: map[string]*LocalEntry{"d": {Path: "d", Dir: true}},
			base: map[string]*BaseEntry{"d": {Path: "d", Dir: true}, "d/x": b("d/x", "v", 1, 1)}, unreadable: []string{"d"}}, ""},
		{"deletions deepest first", view{local: map[string]*LocalEntry{}, remote: map[string]*RemoteEntry{"d": {Path: "d", Dir: true}, "d/x": rf("d/x", "v", 1)},
			base: map[string]*BaseEntry{"d": {Path: "d", Dir: true}, "d/x": b("d/x", "v", 1, 1)}}, "delete-remote:d/x delete-remote:d"},
	} {
		var got []string
		for _, o := range makePlan(c.v).ops {
			got = append(got, o.kind.String()+":"+o.key)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s: %v, want %q", c.name, got, c.want)
		}
	}
}
