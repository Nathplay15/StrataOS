package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrashSuffixBase(t *testing.T) {
	cases := map[string]string{
		"a.txt.1731234567":  "a.txt",
		"a.txt.17312345678": "a.txt",
		"a.txt":             "",
		"a.txt.abc":         "",
		"backup.2024":       "",
		".1731234567":       "",
	}
	for in, want := range cases {
		if got := trashSuffixBase(in); got != want {
			t.Errorf("trashSuffixBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBinItem(t *testing.T) {
	sp := "/srv/strata/Docs"
	ok := map[string]string{
		"/srv/strata/Docs/.recycle/alice/a.txt":      ".recycle/alice/a.txt",
		"/srv/strata/Docs/.recycle/alice/a.txt.1234": ".recycle/alice/a.txt.1234",
		"/srv/strata/Docs/.recycle/alice/sub/a.txt":  ".recycle/alice/sub/a.txt",
		"/srv/strata/Docs/.recycle/alice/a/b/../c":   ".recycle/alice/a/c",
	}
	for in, want := range ok {
		got, err := binItem(sp, "alice", in)
		if err != nil || got != want {
			t.Errorf("binItem(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	bad := []string{
		"/srv/strata/Docs/.recycle/bob/a.txt",
		"/srv/strata/Docs/.recycle/alice/../bob/a.txt",
		"/srv/strata/Docs/.recycle/alice",
		"/srv/strata/Docs/.recycle/alice/",
		"/srv/strata/Docs/.recycle/alicex/a.txt",
		"/srv/strata/Docs/other/a.txt",
		"/srv/strata/Other/.recycle/alice/a.txt",
		"/etc/passwd",
		sp,
	}
	for _, in := range bad {
		if got, err := binItem(sp, "alice", in); err == nil {
			t.Errorf("binItem(%q) = %q, want an error", in, got)
		}
	}
}

func TestRestoredNames(t *testing.T) {
	for in, want := range map[string]string{"Copy #1 of a.txt": "a.txt", "Copy #12 of Copy #1 of b": "Copy #1 of b", "Copy of a.txt": "", "Copy #x of a": "", "a.txt": ""} {
		if got := sambaCopyBase(in); got != want {
			t.Errorf("sambaCopyBase(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range [][3]string{{"docs/report.pdf", "1", "docs/report (restored).pdf"}, {"docs/report.pdf", "2", "docs/report (restored 2).pdf"}, {"Makefile", "1", "Makefile (restored)"}, {".bashrc", "1", ".bashrc (restored)"}} {
		n := map[string]int{"1": 1, "2": 2}[c[1]]
		if got := restoredName(c[0], n); got != c[2] {
			t.Errorf("restoredName(%s, %d) = %q, want %q", c[0], n, got, c[2])
		}
	}
}

// bin builds a share with alice's bin as Samba (keeptree) leaves it.
func bin(t *testing.T, files map[string]string) string {
	t.Helper()
	sp := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(sp, rel)
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return sp
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return string(b)
}

func TestRestoreRecycle(t *testing.T) {
	sp := bin(t, map[string]string{
		".recycle/alice/a.txt":             "trashed",
		".recycle/alice/docs/b.txt":        "in a folder that still exists",
		".recycle/alice/docs/sub/c.txt":    "in a folder deleted too",
		".recycle/alice/c.txt.1731234567":  "collided at trash time",
		".recycle/alice/Copy #1 of e.txt":  "deleted twice (Samba)",
		".recycle/alice/d.txt":             "in the bin",
		".recycle/alice/photos/2024/x.jpg": "x",
		".recycle/alice/photos/2024/y.jpg": "y",
		".recycle/alice/photos/empty/":     "",
		"docs/kept.txt":                    "live",
		"d.txt":                            "live now",
		"photos/2024/z.jpg":                "live z",
	})
	one := func(rel, want string) {
		t.Helper()
		got, err := restoreRecycle(sp, "alice", rel)
		if err != nil || len(got) != 1 || got[0] != sp+"/"+want {
			t.Fatalf("restoreRecycle(%s) = %q, %v; want %s", rel, got, err, want)
		}
	}
	one(".recycle/alice/a.txt", "a.txt")
	if read(t, filepath.Join(sp, "a.txt")) != "trashed" {
		t.Fatal("a.txt content")
	}
	// A file of a folder that still exists goes back into it, not into a
	// "docs.<time>" copy of the folder.
	one(".recycle/alice/docs/b.txt", "docs/b.txt")
	if read(t, filepath.Join(sp, "docs/kept.txt")) != "live" {
		t.Fatal("docs/kept.txt touched")
	}
	// A missing folder is created again; the bin's empty folders go.
	one(".recycle/alice/docs/sub/c.txt", "docs/sub/c.txt")
	if _, err := os.Stat(filepath.Join(sp, ".recycle/alice/docs")); !os.IsNotExist(err) {
		t.Errorf("empty bin folder left: %v", err)
	}
	// Collision suffix and Samba copy prefix undone when the name is free.
	one(".recycle/alice/c.txt.1731234567", "c.txt")
	one(".recycle/alice/Copy #1 of e.txt", "e.txt")
	// A name taken again gets a new one, the live file stays.
	one(".recycle/alice/d.txt", "d (restored).txt")
	if read(t, filepath.Join(sp, "d.txt")) != "live now" {
		t.Fatal("the live file was overwritten")
	}
	// A whole folder merges into the existing one, item by item.
	got, err := restoreRecycle(sp, "alice", ".recycle/alice/photos")
	if err != nil || len(got) != 3 {
		t.Fatalf("restore photos = %q, %v", got, err)
	}
	if read(t, filepath.Join(sp, "photos/2024/x.jpg")) != "x" || read(t, filepath.Join(sp, "photos/2024/z.jpg")) != "live z" {
		t.Fatal("photos merge")
	}
	if fi, err := os.Stat(filepath.Join(sp, "photos/empty")); err != nil || !fi.IsDir() {
		t.Fatalf("empty folder not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sp, ".recycle/alice/photos")); !os.IsNotExist(err) {
		t.Errorf("bin folder left: %v", err)
	}
	// Restoring something already restored says so.
	if _, err := restoreRecycle(sp, "alice", ".recycle/alice/a.txt"); !isNotFound(err) {
		t.Errorf("second restore: %v", err)
	}
}

func TestRestoreRecycleStaysInShare(t *testing.T) {
	outside := t.TempDir()
	sp := bin(t, map[string]string{".recycle/alice/docs/a.txt": "x"})
	// "docs" became a link to outside the share since the deletion.
	if err := os.Symlink(outside, filepath.Join(sp, "docs")); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreRecycle(sp, "alice", ".recycle/alice/docs/a.txt"); err == nil {
		t.Fatal("restored through a link leading outside the share")
	}
	if _, err := os.Stat(filepath.Join(outside, "a.txt")); err == nil {
		t.Fatal("file written outside the share")
	}
}

func TestListRecycle(t *testing.T) {
	sp := bin(t, map[string]string{
		".recycle/alice/a.txt":          "x",
		".recycle/alice/gone/b.txt":     "yy",
		".recycle/alice/gone/deep/c.md": "zzz",
		".recycle/alice/empty/":         "",
		".recycle/bob/secret.txt":       "no",
	})
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	stamp := func(rel string, atime, mtime time.Time) {
		if err := os.Chtimes(filepath.Join(sp, rel), atime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	stamp(".recycle/alice/a.txt", old, old)
	// Inside a folder deleted from the web: own mtime kept, atime = deletion.
	stamp(".recycle/alice/gone/b.txt", now.Add(-time.Minute), old.Add(-24*time.Hour))
	stamp(".recycle/alice/gone/deep/c.md", now.Add(-time.Minute), old.Add(-24*time.Hour))
	stamp(".recycle/alice/empty", old.Add(-time.Hour), old.Add(-time.Hour))
	items, err := listRecycle(sp, "alice")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, strings.TrimPrefix(it.Origin, sp+"/"))
	}
	if strings.Join(got, ",") != "gone/b.txt,gone/deep/c.md,a.txt,empty" {
		t.Fatalf("items = %v", got)
	}
	if items[0].Size != 2 || items[0].Path != sp+"/.recycle/alice/gone/b.txt" || !items[0].Deleted.After(now.Add(-2*time.Minute)) {
		t.Errorf("items[0] = %+v", items[0])
	}
	if !items[3].Dir || items[2].Dir {
		t.Errorf("dirs: %+v", items)
	}
	if items, err := listRecycle(sp, "carol"); err != nil || len(items) != 0 {
		t.Fatalf("listRecycle(carol) = %d items, %v", len(items), err)
	}
}

func TestStampDeleted(t *testing.T) {
	sp := bin(t, map[string]string{".recycle/alice/f/a.txt": "x"})
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(sp, ".recycle/alice/f/a.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	rt, err := os.OpenRoot(sp)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	stampDeleted(rt, ".recycle/alice/f")
	fi, _ := os.Stat(filepath.Join(sp, ".recycle/alice/f/a.txt"))
	if !fi.ModTime().Equal(old) {
		t.Errorf("modification time changed: %v", fi.ModTime())
	}
	if d := deletedAt(fi); time.Since(d) > time.Minute {
		t.Errorf("deletion time of a file of a deleted folder = %v", d)
	}
}

func TestOtherBin(t *testing.T) {
	for rel, want := range map[string]bool{
		".recycle":              false, // the bin folder itself: listed, filtered
		".recycle/alice":        false,
		".recycle/alice/a.txt":  false,
		".recycle/bob":          true,
		".recycle/bob/a.txt":    true,
		".recycle/alicex/a.txt": true,
		"docs/.recycle/bob":     false, // not a share's bin
		"recycle/bob":           false,
	} {
		if got := otherBin(rel, "alice"); got != want {
			t.Errorf("otherBin(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestRoomWriter(t *testing.T) {
	var b strings.Builder
	w := &roomWriter{w: &b, room: 5}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("def")); err == nil {
		t.Fatal("write over the room accepted")
	}
	if b.String() != "abc" {
		t.Fatalf("written %q", b.String())
	}
}
