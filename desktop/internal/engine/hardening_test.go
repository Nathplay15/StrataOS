package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A name from the NAS never leads out of the sync folder.
func TestNamesNeverEscape(t *testing.T) {
	for _, n := range []string{"..", ".", "", "a\x00b"} {
		for _, goos := range []string{"linux", "darwin", "windows"} {
			if localNameProblem(n, goos) == "" {
				t.Errorf("%q accepted on %s", n, goos)
			}
		}
	}
	h := newHarness(t)
	h.fake.put("../evil.txt", "x", t0)
	h.fake.put("ok.txt", "x", t0)
	h.sync()
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.local), "evil.txt")); err == nil {
		t.Fatal("written outside the sync folder")
	}
	if h.read("ok.txt") != "x" {
		t.Fatal("the other file was not synchronized")
	}
}

// Deletions spread over rounds still trip the brake.
func TestBrakeCountsRecentRounds(t *testing.T) {
	h := newHarness(t)
	for i := range 100 {
		h.fake.put("f"+strconv.Itoa(i)+".txt", "x", t0)
	}
	h.sync()
	braked := false
	for round := range 5 {
		for i := range 19 {
			h.fake.remove("f" + strconv.Itoa(round*19+i) + ".txt")
		}
		h.sync()
		if h.eng.Status().Pairs[0].Brake != nil {
			braked = true
			break
		}
	}
	if !braked {
		t.Fatalf("95 files deleted 19 at a time without the brake (%d trashed)", len(h.trash))
	}
	if len(h.trash) >= 95 {
		t.Fatalf("everything trashed: %d", len(h.trash))
	}
}

// A synchronized folder replaced by a link to elsewhere: nothing is written
// there, and its files are not taken as deleted.
func TestSymlinkedFolderNotFollowed(t *testing.T) {
	h := newHarness(t)
	h.fake.put("docs/a.txt", "a", t0)
	h.sync()
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(h.local, "docs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(h.local, "docs")); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	h.fake.put("docs/new.txt", "n", t0)
	h.sync()
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("written through the link: %v", ents)
	}
	if _, ok := h.fake.get("docs/a.txt"); !ok {
		t.Fatal("the NAS copy was deleted")
	}
}

// An encrypted folder without its key does not sync (in plaintext).
func TestEncryptedPairWithoutKey(t *testing.T) {
	fake := newFake()
	h := &harness{t: t, local: t.TempDir(), fake: fake}
	e, err := New(&E2ERemote{Inner: fake}, Options{StateFile: filepath.Join(t.TempDir(), "state.db"), Now: func() time.Time { return t0 }, Logf: func(string, ...any) {}, Trash: os.Remove})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	if err := e.SetPairs([]Pair{{ID: "p1", Local: h.local, Remote: encFolder, Encrypted: true}}); err != nil {
		t.Fatal(err)
	}
	h.write("secret.txt", "plaintext", t0)
	if err := e.SyncOnce(context.Background()); err == nil {
		t.Fatal("synchronized without the key")
	}
	if got := fake.list(); strings.Contains(got, "secret") {
		t.Fatalf("plaintext on the NAS: %s", got)
	}
}

// A wrong key does not make the local files look deleted on the NAS.
func TestEncryptedWrongKeyDeletesNothing(t *testing.T) {
	fake := newFake()
	k := e2eKeys(t)
	a := e2eHarness(t, fake, k)
	for i := range 3 {
		a.write("f"+strconv.Itoa(i)+".txt", "x", t0)
	}
	a.sync()
	// The same folder opened with another key.
	rem := a.eng.remote.(*E2ERemote)
	rem.SetFolder(encFolder, strings.Repeat("ab", 16), e2eKeys(t))
	if err := a.eng.SetPairs([]Pair{{ID: "p1", Local: a.local, Remote: encFolder, Encrypted: true}}); err != nil {
		t.Fatal(err)
	}
	_ = a.eng.SyncOnce(context.Background())
	if got := a.tree(); strings.Count(got, ".txt") != 3 {
		t.Fatalf("local files deleted: %q", got)
	}
}
