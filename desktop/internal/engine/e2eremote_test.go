package engine

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos-sync/internal/e2e"
)

const encFolder = "/srv/strata/main/Secret"

// e2eHarness is a computer synchronizing an encrypted folder of the shared fake NAS.
func e2eHarness(t *testing.T, fake *fakeRemote, k *e2e.Keys) *harness {
	t.Helper()
	h := &harness{t: t, local: t.TempDir(), fake: fake}
	rem := &E2ERemote{Inner: fake}
	rem.SetFolder(encFolder, strings.Repeat("ab", 16), k)
	o := Options{StateFile: filepath.Join(t.TempDir(), "state.db"), Device: "Laptop", Now: func() time.Time { return t0 },
		Logf: func(string, ...any) {}, Trash: func(p string) error { return os.Remove(p) },
		OnEvent: func(e Event) { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() }}
	e, err := New(rem, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	h.eng = e
	if err := e.SetPairs([]Pair{{ID: "p1", Local: h.local, Remote: encFolder}}); err != nil {
		t.Fatal(err)
	}
	return h
}

func e2eKeys(t *testing.T) *e2e.Keys {
	t.Helper()
	mk := make([]byte, e2e.KeySize)
	rand.Read(mk)
	k, err := e2e.Derive(mk)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptedFolderTwoComputers(t *testing.T) {
	fake := newFake()
	k := e2eKeys(t)
	a, b := e2eHarness(t, fake, k), e2eHarness(t, fake, k)
	big := make([]byte, 3*e2e.ChunkSize+777)
	rand.Read(big)
	a.write("notes/plan.txt", "secret plan", t0)
	a.write("big.bin", string(big), t0)
	a.write("empty.txt", "", t0)
	a.sync()

	// The NAS has only ciphertext: no plaintext name nor content.
	listing := fake.list()
	if strings.Contains(listing, "plan") || strings.Contains(listing, "notes") || strings.Contains(listing, "big") || strings.Count(listing, " ") != 3 {
		t.Fatalf("NAS tree shows names: %s", listing)
	}
	fake.mu.Lock()
	for p, f := range fake.files {
		if bytes.Contains(f.data, []byte("secret plan")) {
			t.Errorf("%s holds plaintext", p)
		}
	}
	fake.mu.Unlock()

	b.sync()
	if got := b.tree(); got != "big.bin empty.txt notes/ notes/plan.txt" {
		t.Fatalf("other computer: %s", got)
	}
	if b.read("notes/plan.txt") != "secret plan" || b.read("big.bin") != string(big) || b.read("empty.txt") != "" {
		t.Fatal("decrypted contents differ")
	}

	// Changes, a move and a deletion travel back.
	b.write("notes/plan.txt", "new plan!", t0.Add(time.Hour))
	if err := os.Rename(filepath.Join(b.local, "big.bin"), filepath.Join(b.local, "notes", "big-moved.bin")); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(b.local, "empty.txt"))
	b.sync()
	a.sync()
	if got := a.tree(); got != "notes/ notes/big-moved.bin notes/plan.txt" {
		t.Fatalf("after changes: %s", got)
	}
	if a.read("notes/plan.txt") != "new plan!" || a.read("notes/big-moved.bin") != string(big) {
		t.Fatal("changed contents")
	}

	// The same file changed on both sides with the same content: compared
	// through the encrypted metadata, no conflict copy.
	a.write("same.txt", "identical", t0)
	b.write("same.txt", "identical", t0.Add(time.Minute))
	a.sync()
	b.sync()
	if got := b.tree(); strings.Contains(got, "conflict") || !strings.Contains(got, "same.txt") {
		t.Errorf("identical files made a conflict: %s", got)
	}
}

func TestEncryptedFolderIgnoresForeignItems(t *testing.T) {
	fake := newFake()
	k := e2eKeys(t)
	a := e2eHarness(t, fake, k)
	var skipped []string
	a.eng.remote.(*E2ERemote).Skipped = func(_, p string) { skipped = append(skipped, p) }
	// Something put a plaintext file there (SMB, another app): not ours.
	fake.put("readme.txt", "hello", t0)
	// A file of another folder (other key) is not readable either.
	other := e2eKeys(t)
	name, _ := other.EncryptName("x.txt")
	fake.put(name, "whatever", t0)
	a.sync()
	if got := a.tree(); got != "" {
		t.Errorf("foreign items synchronized: %q", got)
	}
	if len(skipped) != 2 {
		t.Errorf("skipped %v", skipped)
	}
}

func TestEncryptedFolderTamperedFile(t *testing.T) {
	fake := newFake()
	k := e2eKeys(t)
	a, b := e2eHarness(t, fake, k), e2eHarness(t, fake, k)
	a.write("doc.txt", strings.Repeat("important ", 100), t0)
	a.sync()
	fake.mu.Lock()
	for _, f := range fake.files {
		if !f.dir {
			f.data[len(f.data)-20] ^= 1
		}
	}
	fake.mu.Unlock()
	_ = b.eng.SyncOnce(t.Context())
	if got := b.read("doc.txt"); got != "<missing>" {
		t.Errorf("a tampered file was written: %q", got)
	}
}

func TestEncryptedFolderResumedDownload(t *testing.T) {
	fake := newFake()
	k := e2eKeys(t)
	a, b := e2eHarness(t, fake, k), e2eHarness(t, fake, k)
	big := make([]byte, 4*e2e.ChunkSize+5)
	rand.Read(big)
	a.write("big.bin", string(big), t0)
	a.sync()
	fake.mu.Lock()
	fake.failDownloadAt = e2e.ChunkOffset(2) + 100
	fake.mu.Unlock()
	_ = b.eng.SyncOnce(t.Context())
	if b.read("big.bin") != "<missing>" {
		t.Fatal("an interrupted download was put in place")
	}
	before := fake.calls["download"]
	b.sync()
	if b.read("big.bin") != string(big) {
		t.Fatal("resumed download differs")
	}
	// Resumed (header, then the rest), not started again from zero.
	if fake.calls["download"]-before != 2 {
		t.Errorf("%d downloads to resume", fake.calls["download"]-before)
	}
}
