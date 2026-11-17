package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"strataos/internal/storage"
)

func TestSnapTreesWalksUpToTheVolume(t *testing.T) {
	t1 := time.Date(2026, 11, 17, 8, 0, 0, 0, time.Local)
	trees := []storage.SnapshotTree{{Name: "main@20261117-080000", Time: t1, Root: "/snap", Base: "/srv/strata/main"}}
	old := snapshotTreesAt
	snapshotTreesAt = func(p string) []storage.SnapshotTree {
		if p == "/srv/strata/main" {
			return trees
		}
		return nil
	}
	defer func() { snapshotTreesAt = old }()

	got := snapTrees("/srv/strata/main/Docs/deleted/folder")
	if len(got) != 1 || got[0].Name != trees[0].Name {
		t.Fatalf("snapTrees of a deleted folder: got %v, want the volume snapshot", got)
	}
	// A tree whose base does not hold p is not offered.
	snapshotTreesAt = func(p string) []storage.SnapshotTree {
		if p == "/srv/strata/main" {
			return []storage.SnapshotTree{{Name: "sub@x", Base: "/srv/strata/main/other"}}
		}
		return nil
	}
	if got := snapTrees("/srv/strata/main/Docs"); len(got) != 0 {
		t.Fatalf("a snapshot of another sub-volume must not be listed: %v", got)
	}
}

func TestVersionName(t *testing.T) {
	stamp := "2026-11-17 08h00"
	for name, want := range map[string]string{
		"report.docx": "report (version of " + stamp + ").docx",
		"beach.jpg":   "beach (version of " + stamp + ").jpg",
		"archive":     "archive (version of " + stamp + ")",
		".hidden":     ".hidden (version of " + stamp + ")",
	} {
		if got := versionName(name, stamp); got != want {
			t.Errorf("versionName(%q): %q, want %q", name, got, want)
		}
	}
}

// snapFixture builds a fake snapshot tree and a live folder to restore into.
func snapFixture(t *testing.T) (snap, live *os.Root, snapRoot, liveRoot string) {
	t.Helper()
	dir := t.TempDir()
	snapRoot = filepath.Join(dir, "snapshot")
	liveRoot = filepath.Join(dir, "live")
	for _, d := range []string{snapRoot + "/Docs/sub", liveRoot + "/Docs"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(snapRoot+"/Docs/report.txt", []byte("old report"), 0o644))
	must(os.WriteFile(snapRoot+"/Docs/photo.jpg", []byte("jpg!"), 0o640))
	must(os.WriteFile(snapRoot+"/Docs/sub/nested.txt", []byte("nested"), 0o644))
	must(os.Symlink("/etc/passwd", snapRoot+"/Docs/link"))
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.Local)
	must(os.Chtimes(snapRoot+"/Docs/report.txt", old, old))
	s, err := os.OpenRoot(snapRoot)
	must(err)
	l, err := os.OpenRoot(liveRoot)
	must(err)
	t.Cleanup(func() { s.Close(); l.Close() })
	return s, l, snapRoot, liveRoot
}

func TestCopySnapshotFile(t *testing.T) {
	snap, live, _, _ := snapFixture(t)
	fi, err := snap.Lstat("Docs/report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := copySnapshotFile(snap, "Docs/report.txt", live, "Docs/report.txt", fi, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(live.Name(), "Docs/report.txt"))
	if err != nil || string(b) != "old report" {
		t.Fatalf("restored file: %q %v", b, err)
	}
	if gotfi, err := os.Stat(filepath.Join(live.Name(), "Docs/report.txt")); err != nil || !gotfi.ModTime().Equal(fi.ModTime()) {
		t.Errorf("mod time not kept: %v", err)
	}
	// A second restore of the same name must not overwrite: the caller
	// renames first, but a direct copy over an existing file fails.
	if err := copySnapshotFile(snap, "Docs/report.txt", live, "Docs/report.txt", fi, false); err == nil {
		t.Error("copying over an existing file must fail (O_EXCL)")
	}
	// Replace: the content is swapped through a temporary file.
	if err := copySnapshotFile(snap, "Docs/photo.jpg", live, "Docs/photo.jpg", mustStat(t, snap, "Docs/photo.jpg"), true); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(live.Name(), "Docs/photo.jpg"))
	if err != nil || string(b) != "jpg!" {
		t.Fatalf("replaced file: %q %v", b, err)
	}
}

func TestCopySnapshotDir(t *testing.T) {
	snap, live, _, _ := snapFixture(t)
	fi, err := snap.Lstat("Docs/sub")
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := copySnapshotDir(snap, "Docs/sub", live, "Docs/restored", fi)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped: %v", skipped)
	}
	if b, err := os.ReadFile(filepath.Join(live.Name(), "Docs/restored/nested.txt")); err != nil || string(b) != "nested" {
		t.Fatalf("nested file: %q %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(live.Name(), "Docs/restored")); err != nil || !fi.IsDir() {
		t.Fatalf("restored folder: %v", err)
	}
}

func mustStat(t *testing.T, rt *os.Root, rel string) os.FileInfo {
	t.Helper()
	fi, err := rt.Lstat(rel)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}
