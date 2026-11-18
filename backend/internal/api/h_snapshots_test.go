package api

import (
	"os"
	"path"
	"path/filepath"
	"slices"
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
	for _, d := range []string{snapRoot + "/Docs/sub/deeper", liveRoot + "/Docs"} {
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
	must(os.WriteFile(snapRoot+"/Docs/sub/deeper/deep.txt", []byte("deep"), 0o644))
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

// restorer is a snapRestore from the fixture, recording the chowned paths.
func restorer(snap, live *os.Root, room int64, owned *[]string) *snapRestore {
	return &snapRestore{src: snap, dst: live, room: room, own: func(_ *os.Root, rel string) { *owned = append(*owned, rel) }}
}

func snapItems(t *testing.T, snap *os.Root, names ...string) []snapItem {
	var out []snapItem
	for _, n := range names {
		out = append(out, snapItem{name: n, rel: "Docs/" + n, fi: mustStat(t, snap, "Docs/"+n)})
	}
	return out
}

func TestSnapRestoreFiles(t *testing.T) {
	snap, live, _, _ := snapFixture(t)
	var owned []string
	got, err := restorer(snap, live, -1, &owned).restore(snapItems(t, snap, "report.txt"), "Docs", "2026-11-17 08h00", false)
	if err != nil || len(got) != 1 || got[0] != "report.txt" {
		t.Fatalf("restore: %v %v", got, err)
	}
	b, err := os.ReadFile(filepath.Join(live.Name(), "Docs/report.txt"))
	if err != nil || string(b) != "old report" {
		t.Fatalf("restored file: %q %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(live.Name(), "Docs/report.txt")); err != nil || !fi.ModTime().Equal(mustStat(t, snap, "Docs/report.txt").ModTime()) {
		t.Errorf("mod time not kept: %v", err)
	}
	// Restored again: a copy next to it, then "(2)", never an overwrite.
	for _, want := range []string{"report (version of 2026-11-17 08h00).txt", "report (version of 2026-11-17 08h00) (2).txt"} {
		got, err := restorer(snap, live, -1, &owned).restore(snapItems(t, snap, "report.txt"), "Docs", "2026-11-17 08h00", false)
		if err != nil || len(got) != 1 || got[0] != want {
			t.Fatalf("restore again: %v %v, want %s", got, err, want)
		}
	}
	// Replace: the content is swapped through a temporary file.
	if err := os.WriteFile(filepath.Join(live.Name(), "Docs/photo.jpg"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := restorer(snap, live, -1, &owned).restore(snapItems(t, snap, "photo.jpg"), "Docs", "x", true); err != nil || got[0] != "photo.jpg" {
		t.Fatalf("replace: %v %v", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(live.Name(), "Docs/photo.jpg")); string(b) != "jpg!" {
		t.Fatalf("replaced file: %q", b)
	}
	if len(owned) != 4 {
		t.Errorf("chowned: %v", owned)
	}
}

func TestSnapRestoreFolderTwice(t *testing.T) {
	snap, live, _, _ := snapFixture(t)
	var owned []string
	rs := restorer(snap, live, -1, &owned)
	got, err := rs.restore(snapItems(t, snap, "sub", "link"), "Docs", "2026-11-17 08h00", false)
	if err != nil || len(got) != 1 || got[0] != "sub" || len(rs.skipped) != 1 {
		t.Fatalf("restore: %v %v skipped %v", got, err, rs.skipped)
	}
	if b, err := os.ReadFile(filepath.Join(live.Name(), "Docs/sub/deeper/deep.txt")); err != nil || string(b) != "deep" {
		t.Fatalf("nested file: %q %v", b, err)
	}
	// Every created folder and file belongs to the user, not only the top.
	want := []string{"Docs/sub", "Docs/sub/deeper", "Docs/sub/deeper/deep.txt", "Docs/sub/nested.txt"}
	slices.Sort(owned)
	if !slices.Equal(owned, want) {
		t.Errorf("chowned %v, want %v", owned, want)
	}
	// The same folder again: a new copy, complete, not a failure half-way.
	for _, name := range []string{"sub (version of 2026-11-17 08h00)", "sub (version of 2026-11-17 08h00) (2)"} {
		got, err := restorer(snap, live, -1, &owned).restore(snapItems(t, snap, "sub"), "Docs", "2026-11-17 08h00", false)
		if err != nil || len(got) != 1 || got[0] != name {
			t.Fatalf("restore again: %v %v, want %s", got, err, name)
		}
		if _, err := os.Stat(filepath.Join(live.Name(), "Docs", name, "deeper/deep.txt")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapRestoreRoom(t *testing.T) {
	snap, live, _, _ := snapFixture(t)
	var owned []string
	// sub holds 6 + 4 bytes: 9 bytes of room refuse it before writing anything.
	if _, err := restorer(snap, live, 9, &owned).restore(snapItems(t, snap, "sub"), "Docs", "x", false); err != errSnapNoRoom {
		t.Fatalf("over the room: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(live.Name(), "Docs/sub")); !os.IsNotExist(err) {
		t.Fatalf("something was written: %v", err)
	}
	if _, err := restorer(snap, live, 10, &owned).restore(snapItems(t, snap, "sub"), "Docs", "x", false); err != nil {
		t.Fatalf("within the room: %v", err)
	}
}

// A relative link kept in a snapshot (A/link -> ../B) must not lead from
// share A into share B: reads are confined to A's folder in the snapshot and
// refuse links on the way, for folders, downloads and single-file versions.
func TestSnapScopeSymlinkEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "snap")
	for _, d := range []string{"A/docs", "B"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for f, data := range map[string]string{"A/docs/mine.txt": "mine", "B/secret.txt": "secret"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, to := range map[string]string{"A/link": "../B", "A/docs/up": "../../B/secret.txt", "C": "B"} {
		if err := os.Symlink(to, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	tree := storage.SnapshotTree{Name: "vol@20261117-080000", Root: root, Base: "/srv/strata/vol"}
	shareA := func(rel string) target {
		return target{root: "/srv/strata/vol/A", rel: rel, abs: path.Join("/srv/strata/vol/A", rel), acc: access{read: true}}
	}

	sc, err := scopeIn(tree, shareA("."))
	if err != nil || sc.root != filepath.Join(root, "A") || sc.rel != "." {
		t.Fatalf("share root: %+v %v", sc, err)
	}
	if _, err := sc.child("link"); err != errSnapLink {
		t.Errorf("browsing A/link: %v", err)
	}
	for _, rel := range []string{"link", "link/secret.txt", "docs/up"} {
		if _, err := scopeIn(tree, shareA(rel)); err != errSnapLink {
			t.Errorf("A/%s: %v, want refused", rel, err)
		}
	}
	// Even past the checks, the os.Root of A cannot read B.
	rt, err := os.OpenRoot(sc.root)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	for _, p := range []string{"link/secret.txt", "../B/secret.txt", "docs/up"} {
		if b, err := rt.ReadFile(p); err == nil {
			t.Errorf("read %s through A: %q", p, b)
		}
	}
	if b, err := rt.ReadFile("docs/mine.txt"); err != nil || string(b) != "mine" {
		t.Errorf("own file: %q %v", b, err)
	}
	// A share whose folder is a link in the snapshot is refused.
	c := target{root: "/srv/strata/vol/C", rel: ".", abs: "/srv/strata/vol/C", acc: access{read: true}}
	if _, err := scopeIn(tree, c); err != errSnapLink {
		t.Errorf("share C (a link in the snapshot): %v", err)
	}
	// Administrators see the whole snapshot, links still refused.
	admin := target{root: storage.MountRoot, rel: "vol/A/link", abs: "/srv/strata/vol/A/link", acc: access{read: true, write: true}}
	if _, err := scopeIn(tree, admin); err != errSnapLink {
		t.Errorf("admin through a link: %v", err)
	}
	admin.rel, admin.abs = "vol/B", "/srv/strata/vol/B"
	if sc, err := scopeIn(tree, admin); err != nil || sc.root != root || sc.rel != "B" {
		t.Errorf("admin: %+v %v", sc, err)
	}
	// A snapshot of a sub-volume inside the share holds nothing else.
	sub := storage.SnapshotTree{Name: "A_docs@x", Root: filepath.Join(root, "A/docs"), Base: "/srv/strata/vol/A/docs"}
	if sc, err := scopeIn(sub, shareA("docs")); err != nil || sc.root != sub.Root || sc.rel != "." {
		t.Errorf("sub-volume: %+v %v", sc, err)
	}
	if _, err := scopeIn(tree, target{root: "/srv/strata/other/X", rel: ".", abs: "/srv/strata/other/X"}); err == nil {
		t.Error("a share outside the snapshot")
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
