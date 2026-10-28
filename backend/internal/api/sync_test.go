package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/syncjournal"
)

func TestValidSyncName(t *testing.T) {
	for n, ok := range map[string]bool{
		"a.txt": true, ".gitignore": true, ".DS_Store": true, "été 2024": true,
		"": false, ".": false, "..": false, "a/b": false, `a\b`: false, "a\x01": false, strings.Repeat("x", 256): false,
		".strata-upload-123": false, ".strataos-sync-part": false, ".recycle": false, "@eaDir": false,
	} {
		if got := validSyncName(n) == nil; got != ok {
			t.Errorf("validSyncName(%q) = %v, want %v", n, got, ok)
		}
	}
}

// TestSyncReplace covers the conditional commit of sync uploads: the file
// replaces the version the client knew, and anything else is a conflict
// that keeps the uploaded bytes for another commit.
func TestSyncReplace(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	rt, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	tg := target{root: dir, rel: ".", abs: dir, acc: access{read: true, write: true}}
	put := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	version := func(name string) string {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			return ""
		}
		return syncjournal.StatVersion(fi)
	}
	n := 0
	upload := func(name, content, ifMatch string) (any, error, *mobileUpload) {
		n++
		u := &mobileUpload{ID: "u" + string(rune('a'+n)), User: "alice", Admin: true, Folder: dir, Name: name, Size: int64(len(content)),
			ModTime: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC).UnixMilli(), Sync: true, Replace: true, IfMatch: ifMatch}
		put(u.temp(), content)
		uploads.mu.Lock()
		s.uploadsLoadLocked()
		uploads.m[u.ID] = u
		uploads.mu.Unlock()
		v, err := s.syncReplace(u, tg, rt)
		return v, err, u
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}
	conflict := func(err error) bool {
		var c *syncConflict
		return errors.As(err, &c)
	}

	// New file: ifMatch "" means "there is none".
	if _, err, _ := upload("new.txt", "one", ""); err != nil || read("new.txt") != "one" {
		t.Fatalf("new file: %v %q", err, read("new.txt"))
	}
	fi, _ := os.Stat(filepath.Join(dir, "new.txt"))
	if !fi.ModTime().Equal(time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("date not kept: %v", fi.ModTime())
	}
	// The same name again without knowing it: conflict, nothing overwritten.
	_, err, u := upload("new.txt", "two", "")
	if !conflict(err) || read("new.txt") != "one" {
		t.Fatalf("blind overwrite: %v %q", err, read("new.txt"))
	}
	if _, e := os.Stat(filepath.Join(dir, u.temp())); e != nil {
		t.Fatal("conflicting upload not kept for another commit")
	}
	var c *syncConflict
	errors.As(err, &c)
	if c.current == nil || c.current.Version != version("new.txt") {
		t.Fatalf("conflict without the current version: %+v", c.current)
	}
	// Committed again under a conflict name.
	u.Name, u.IfMatch = "new (conflict).txt", ""
	if _, err := s.syncReplace(u, tg, rt); err != nil || read("new (conflict).txt") != "two" {
		t.Fatalf("commit under another name: %v", err)
	}
	// With the right version: replaced.
	v1 := version("new.txt")
	if _, err, _ := upload("new.txt", "three", v1); err != nil || read("new.txt") != "three" {
		t.Fatalf("replace: %v %q", err, read("new.txt"))
	}
	// An old version (changed meanwhile, over SMB say): conflict.
	if _, err, _ := upload("new.txt", "four", v1); !conflict(err) || read("new.txt") != "three" {
		t.Fatalf("stale version: %v %q", err, read("new.txt"))
	}
	// Deleted on the NAS meanwhile: conflict too (the client decides).
	v := version("new.txt")
	os.Remove(filepath.Join(dir, "new.txt"))
	if _, err, _ := upload("new.txt", "five", v); !conflict(err) {
		t.Fatalf("deleted meanwhile: %v", err)
	}
	// "*" replaces whatever is there.
	put("any.txt", "x")
	if _, err, _ := upload("any.txt", "forced", "*"); err != nil || read("any.txt") != "forced" {
		t.Fatalf("forced: %v", err)
	}
	// A folder of that name is never replaced.
	os.Mkdir(filepath.Join(dir, "folder"), 0o755)
	if _, err, _ := upload("folder", "x", "*"); !conflict(err) {
		t.Fatalf("folder replaced: %v", err)
	}
	// Incomplete: refused.
	u2 := &mobileUpload{ID: "short", Folder: dir, Name: "short.txt", Size: 10, Replace: true, Admin: true}
	put(u2.temp(), "abc")
	if _, err := s.syncReplace(u2, tg, rt); err == nil || conflict(err) {
		t.Fatalf("incomplete upload committed: %v", err)
	}
}

func TestSyncEntryView(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("hello"), 0o644)
	mt := time.UnixMilli(1700000000123)
	os.Chtimes(p, mt, mt)
	fi, _ := os.Lstat(p)
	e := statEntry("f", fi)
	if e.Size != 5 || e.MTime != 1700000000123 || e.Version != syncjournal.StatVersion(fi) || e.ID == "" || e.Dir {
		t.Fatalf("entry: %+v", e)
	}
	di, _ := os.Lstat(dir)
	if d := statEntry("d", di); !d.Dir || d.Version != "" || d.Size != 0 {
		t.Fatalf("folder entry: %+v", d)
	}
}

func TestDeviceKinds(t *testing.T) {
	s, _ := mobileTestServer(t)
	_, d, err := s.addDeviceKind("alice", "", "", "0.8.0", "desktop", "Windows 11\x07", "192.0.2.11")
	if err != nil {
		t.Fatal(err)
	}
	v := viewDevice(d)
	if v.Kind != "desktop" || v.OS != "Windows 11" || v.Name != "Computer" {
		t.Fatalf("desktop device: %+v", v)
	}
	_, d, _ = s.addDeviceKind("alice", "x", "", "", "something", "", "")
	if viewDevice(d).Kind != "android" {
		t.Fatal("unknown kinds are phones")
	}
	for p, denied := range map[string]bool{"/api/devices": true, "/api/devices/alice/x": true, "/api/sync/changes": false, "/api/me/devices": true} {
		if deviceDenied(p) != denied {
			t.Errorf("deviceDenied(%s) != %v", p, denied)
		}
	}
	// Pairing codes typed by hand, grouped.
	code := mobilePairing.create("alice")
	grouped := strings.ToUpper(code[:8] + "-" + code[8:16] + " " + code[16:])
	if mobilePairing.take(grouped) != "alice" {
		t.Fatal("grouped pairing code refused")
	}
}
