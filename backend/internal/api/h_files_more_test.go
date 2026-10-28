package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInRecycleBin(t *testing.T) {
	for p, want := range map[string]bool{
		"/srv/strata/main/Docs/.recycle":       true,
		"/srv/strata/main/Docs/.recycle/bob/a": true,
		"/srv/strata/main/Docs/.recyclebin":    false,
		"/srv/strata/main/Docs/sub/.recycle/x": false,
		"/srv/strata/main/Docs/report.recycle": false,
	} {
		if inRecycleBin("/srv/strata/main/Docs", p) != want {
			t.Errorf("%s: want %v", p, want)
		}
	}
}

func TestSearchAndCopyTree(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "a/Holidays/2024"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "a/.recycle/bob"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "a/Holidays/2024/beach.jpg"), []byte("jpg"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "a/.recycle/bob/beach-old.jpg"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "a/notes.txt"), []byte("hello"), 0o644))
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	must(os.Chtimes(filepath.Join(dir, "a/notes.txt"), old, old))
	must(os.Symlink("/etc/passwd", filepath.Join(dir, "a/Holidays/link")))

	tg := target{root: dir, rel: "a", abs: filepath.Join(dir, "a"), acc: access{read: true}}
	var out []fileEntry
	visited := 0
	must(searchTarget(context.Background(), tg, "beach", &out, &visited))
	if len(out) != 1 || out[0].Path != filepath.Join(dir, "a/Holidays/2024/beach.jpg") || out[0].Size != 3 {
		t.Errorf("search: %+v (recycle bins and hidden folders must be skipped)", out)
	}
	out = nil
	must(searchTarget(context.Background(), tg, "holi", &out, &visited))
	if len(out) != 1 || !out[0].Dir {
		t.Errorf("folder search: %+v", out)
	}

	rt, err := os.OpenRoot(dir)
	must(err)
	defer rt.Close()
	owned := 0
	must(copyTree(context.Background(), rt, rt, "a", "b", func(string) { owned++ }))
	if b, err := os.ReadFile(filepath.Join(dir, "b/Holidays/2024/beach.jpg")); err != nil || string(b) != "jpg" {
		t.Errorf("copied file: %q %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "b/notes.txt")); err != nil || !fi.ModTime().Equal(old) {
		t.Errorf("modification time not kept: %v %v", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "b/Holidays/link")); err == nil {
		t.Error("symlink copied")
	}
	if owned < 5 {
		t.Errorf("owner callback called %d times", owned)
	}
	// Never overwrites: copying again onto an existing file fails.
	if err := copyTree(context.Background(), rt, rt, "a/notes.txt", "b/notes.txt", func(string) {}); err == nil {
		t.Error("existing file overwritten")
	}
}
