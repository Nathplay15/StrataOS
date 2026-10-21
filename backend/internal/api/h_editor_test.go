package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strataos/internal/store"
)

// editTarget is a target inside a temporary root, as locate would build it.
func editTarget(root, rel string, write bool) target {
	return target{root: root, rel: rel, abs: "/srv/strata/main/Docs/" + rel, acc: access{read: true, write: write}}
}

func TestSaveFileRules(t *testing.T) {
	s := newTestServer(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Lstat(filepath.Join(root, "a.txt"))
	etag := fileETag(fi)
	admin := saveOpts{user: "admin", admin: true}

	// Stale or missing version: conflict, file untouched.
	o := admin
	o.etag = "stale"
	_, err := s.saveFile(editTarget(root, "a.txt", true), strings.NewReader("two"), o)
	var ce *conflictError
	if !errors.As(err, &ce) || ce.etag != etag {
		t.Fatalf("stale version: %v", err)
	}
	// Right version: saved, mode kept, new version returned.
	o.etag = etag
	res, err := s.saveFile(editTarget(root, "a.txt", true), strings.NewReader("two!"), o)
	if err != nil || res.Size != 4 || res.ETag == etag {
		t.Fatalf("save: %v %+v", err, res)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "two!" {
		t.Fatalf("content %q", b)
	}
	if st, _ := os.Stat(filepath.Join(root, "a.txt")); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640 kept", st.Mode().Perm())
	}
	// The old version is now stale too; force overwrites.
	if _, err := s.saveFile(editTarget(root, "a.txt", true), strings.NewReader("x"), o); !errors.As(err, &ce) {
		t.Fatalf("second save with old version: %v", err)
	}
	o.force = true
	if _, err := s.saveFile(editTarget(root, "a.txt", true), strings.NewReader("forced"), o); err != nil {
		t.Fatalf("force: %v", err)
	}
	// New file: create refuses an existing name.
	c := admin
	c.create = true
	if _, err := s.saveFile(editTarget(root, "a.txt", true), strings.NewReader("n"), c); !errors.As(err, &ce) {
		t.Fatalf("create over existing: %v", err)
	}
	if _, err := s.saveFile(editTarget(root, "new.md", true), strings.NewReader("# hi"), c); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Deleted meanwhile.
	d := admin
	d.etag = "x"
	if _, err := s.saveFile(editTarget(root, "gone.txt", true), strings.NewReader("n"), d); !errors.As(err, &ce) {
		t.Fatalf("deleted file: %v", err)
	}
	// Read-only access.
	if _, err := s.saveFile(editTarget(root, "a.txt", false), strings.NewReader("n"), c); !isStatus(err, http.StatusForbidden) {
		t.Fatalf("read-only: %v", err)
	}
	// Confinement: os.Root refuses to leave the root, also through a symlink.
	outside := t.TempDir()
	_ = os.Symlink(outside, filepath.Join(root, "escape"))
	for _, rel := range []string{"../evil.txt", "escape/evil.txt"} {
		if _, err := s.saveFile(editTarget(root, rel, true), strings.NewReader("n"), c); err == nil {
			t.Errorf("%s: written outside the root", rel)
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("files written outside: %v", ents)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil.txt")); err == nil {
		t.Error("file written next to the root")
	}
	// A symlink itself is not replaced by a file.
	_ = os.Symlink("a.txt", filepath.Join(root, "link.txt"))
	if _, err := s.saveFile(editTarget(root, "link.txt", true), strings.NewReader("n"), saveOpts{admin: true, force: true}); !isStatus(err, http.StatusBadRequest) {
		t.Errorf("symlink: %v", err)
	}
	// No temporary file left behind.
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".strata-") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}
}

func TestSaveFileFrozenAndQuota(t *testing.T) {
	s := newTestServer(t)
	root := t.TempDir()
	_ = s.store.Update(func(st *store.State) error {
		st.Shares = append(st.Shares, &store.Share{Name: "Docs", Path: "/srv/strata/main/Docs", Frozen: true})
		return nil
	})
	o := saveOpts{user: "admin", admin: true, create: true}
	_, err := s.saveFile(editTarget(root, "a.txt", true), strings.NewReader("n"), o)
	if !isStatus(err, http.StatusForbidden) || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("frozen share, even for an administrator: %v", err)
	}
	_ = s.store.Update(func(st *store.State) error {
		st.Shares[0].Frozen, st.Shares[0].QuotaGB, st.Shares[0].QuotaBlock, st.Shares[0].Used = false, 1e-8, true, 5
		return nil
	})
	// 10 bytes allowed, 5 used: 6 bytes do not fit for a user, fit for an administrator.
	u := saveOpts{user: "nobody-e2e", create: true}
	if _, err := s.saveFile(editTarget(root, "b.txt", true), strings.NewReader("123456"), u); !isStatus(err, http.StatusInsufficientStorage) {
		t.Fatalf("share quota: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("file kept over quota")
	}
	if _, err := s.saveFile(editTarget(root, "b.txt", true), strings.NewReader("1234"), u); err != nil {
		t.Fatalf("within quota: %v", err)
	}
	if _, err := s.saveFile(editTarget(root, "c.txt", true), strings.NewReader("123456"), o); err != nil {
		t.Fatalf("administrator: %v", err)
	}
}

func isStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}
