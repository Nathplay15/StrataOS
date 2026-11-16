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
	}
	for in, want := range ok {
		got, err := binItem(sp, "alice", in)
		if err != nil || got != want {
			t.Errorf("binItem(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	bad := []string{
		"/srv/strata/Docs/.recycle/bob/a.txt",
		"/srv/strata/Docs/.recycle/alice/sub/a.txt",
		"/srv/strata/Docs/.recycle/alice/a/b.txt",
		"/srv/strata/Docs/.recycle/alice",
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

func TestRestoreRecycle(t *testing.T) {
	sp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sp, ".recycle", "alice", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(sp, rel), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(".recycle", "alice", "a.txt"), "trashed")
	write(filepath.Join(".recycle", "alice", "sub", "b.txt"), "trashed too")
	write(filepath.Join(".recycle", "alice", "c.txt.1731234567"), "collided at trash time")

	// Plain restore, back to the original place.
	to, err := restoreRecycle(sp, "alice", ".recycle/alice/a.txt")
	if err != nil || to != sp+"/a.txt" {
		t.Fatalf("restoreRecycle(a.txt) = %q, %v", to, err)
	}
	if b, err := os.ReadFile(filepath.Join(sp, "a.txt")); err != nil || string(b) != "trashed" {
		t.Fatalf("restored a.txt = %q, %v", b, err)
	}

	// Nested restore recreates the missing folders.
	to, err = restoreRecycle(sp, "alice", ".recycle/alice/sub/b.txt")
	if err != nil || to != sp+"/sub/b.txt" {
		t.Fatalf("restoreRecycle(sub/b.txt) = %q, %v", to, err)
	}

	// The trash-time collision suffix is undone when the plain name is free.
	to, err = restoreRecycle(sp, "alice", ".recycle/alice/c.txt.1731234567")
	if err != nil || to != sp+"/c.txt" {
		t.Fatalf("restoreRecycle(c.txt.<ts>) = %q, %v", to, err)
	}

	// A name that is taken again is suffixed, never overwritten.
	write(filepath.Join(".recycle", "alice", "d.txt"), "in the bin")
	write("d.txt", "live now")
	to, err = restoreRecycle(sp, "alice", ".recycle/alice/d.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(to, sp+"/d.txt.") {
		t.Fatalf("colliding restore went to %q, want a suffixed name", to)
	}
	if b, _ := os.ReadFile(filepath.Join(sp, "d.txt")); string(b) != "live now" {
		t.Fatalf("the live file was overwritten: %q", b)
	}
}

func TestListRecycle(t *testing.T) {
	sp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sp, ".recycle", "alice", "gone"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sp, ".recycle", "alice", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sp, ".recycle", "alice", "gone", "b.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(sp, ".recycle", "alice", "a.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	items, err := listRecycle(sp, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("listRecycle returned %d items, want 2: %+v", len(items), items)
	}
	// Most recent deletion first.
	if items[0].Name != "gone" || !items[0].Dir || items[0].Files != 1 || items[0].Origin != sp+"/gone" {
		t.Errorf("items[0] = %+v", items[0])
	}
	if items[1].Name != "a.txt" || items[1].Dir || items[1].Size != 1 || items[1].Origin != sp+"/a.txt" {
		t.Errorf("items[1] = %+v", items[1])
	}
	if !items[1].Deleted.Equal(old) {
		t.Errorf("deleted time = %v, want %v", items[1].Deleted, old)
	}
	// A missing bin is an empty list, not an error.
	if items, err := listRecycle(sp, "bob"); err != nil || len(items) != 0 {
		t.Fatalf("listRecycle(bob) = %d items, %v", len(items), err)
	}
}
