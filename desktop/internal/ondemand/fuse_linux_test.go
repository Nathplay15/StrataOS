package ondemand

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// TestFUSE mounts the fake NAS and uses it like a program would. It needs
// /dev/fuse and fusermount3 (no root); it is skipped without them.
func TestFUSE(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil && os.Getuid() != 0 {
		if _, err := exec.LookPath("fusermount"); err != nil {
			t.Skip("no fusermount")
		}
	}
	nas := newNAS()
	big := bytes.Repeat([]byte("StrataOS"), 1<<18) // 2 MiB
	nas.put("Videos/film.bin", big)
	nas.put("readme.txt", []byte("hello\n"))
	s := newService(t, nas)
	waitFor(t, "listing", s.Ready)
	mnt := filepath.Join(t.TempDir(), "StrataOS - NAS")
	p, err := StartProvider(s, ProviderOptions{Root: mnt})
	if err != nil {
		t.Skip("cannot mount here: ", err)
	}
	defer p.Close()
	docs := filepath.Join(mnt, "Docs")

	names := func(dir string) []string {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var l []string
		for _, e := range ents {
			l = append(l, e.Name())
		}
		sort.Strings(l)
		return l
	}
	if got := strings.Join(names(mnt), ","); got != "Docs" {
		t.Fatal("root:", got)
	}
	if got := strings.Join(names(docs), ","); got != "Videos,readme.txt" {
		t.Fatal("Docs:", got)
	}
	// Sizes without downloading; no space used.
	st, err := os.Stat(filepath.Join(docs, "Videos/film.bin"))
	if err != nil || st.Size() != int64(len(big)) || st.Sys().(*syscall.Stat_t).Blocks != 0 || nas.read != 0 {
		t.Fatal("stat", err, nas.read)
	}
	attr := func(p, name string) string {
		b := make([]byte, 64)
		n, err := syscall.Getxattr(p, name, b)
		if err != nil {
			t.Fatal(err)
		}
		return string(b[:n])
	}
	if a := attr(filepath.Join(docs, "readme.txt"), xattrStatus); a != "cloud" {
		t.Fatal(a)
	}
	// Reading part of a file downloads part of it.
	f, err := os.Open(filepath.Join(docs, "Videos/film.bin"))
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 16)
	if _, err := f.ReadAt(b, 1<<20); err != nil || !bytes.Equal(b, big[1<<20:1<<20+16]) {
		t.Fatal("read", err)
	}
	f.Close()
	if nas.read >= int64(len(big)) {
		t.Fatal("whole file downloaded for 16 bytes", nas.read)
	}
	if a := attr(filepath.Join(docs, "Videos/film.bin"), xattrStatus); a != "partial" {
		t.Fatal(a)
	}
	// Keep on this device through the attribute.
	if err := syscall.Setxattr(filepath.Join(docs, "Videos"), xattrPinned, []byte("1"), 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pinned", func() bool { return attr(filepath.Join(docs, "Videos/film.bin"), xattrStatus) == "pinned" })
	got, _ := os.ReadFile(filepath.Join(docs, "Videos/film.bin"))
	if !bytes.Equal(got, big) {
		t.Fatal("contents")
	}
	if err := syscall.Setxattr(filepath.Join(docs, "Videos"), xattrPinned, []byte("0"), 0); err != nil {
		t.Fatal(err)
	}
	if a := attr(filepath.Join(docs, "Videos/film.bin"), xattrStatus); a != "cloud" {
		t.Fatal("after free up:", a)
	}

	// Writing: a new file, an edit, an editor's save through a temporary file.
	if err := os.WriteFile(filepath.Join(docs, "new.txt"), []byte("bonjour"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "new file sent", func() bool { return string(nas.data("new.txt")) == "bonjour" })
	fh, err := os.OpenFile(filepath.Join(docs, "readme.txt"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString("world\n")
	fh.Close()
	waitFor(t, "edit sent", func() bool { return string(nas.data("readme.txt")) == "hello\nworld\n" })
	waitFor(t, "edit clean", func() bool { return attr(filepath.Join(docs, "readme.txt"), xattrStatus) == "local" })
	tmp := filepath.Join(docs, ".readme.txt.swp")
	if err := os.WriteFile(tmp, []byte("rewritten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(docs, "readme.txt")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replace sent", func() bool { return string(nas.data("readme.txt")) == "rewritten\n" })
	if nas.data(".readme.txt.swp") != nil {
		t.Fatal("temporary file sent")
	}

	// Folders, moves, deletions.
	if err := os.MkdirAll(filepath.Join(docs, "a/b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(docs, "new.txt"), filepath.Join(docs, "a/b/moved.txt")); err != nil {
		t.Fatal(err)
	}
	if string(nas.data("a/b/moved.txt")) != "bonjour" {
		t.Fatal("move")
	}
	if err := os.Remove(filepath.Join(docs, "a")); err == nil {
		t.Fatal("removed a full folder")
	}
	if err := os.RemoveAll(filepath.Join(docs, "a")); err != nil {
		t.Fatal(err)
	}
	if nas.data("a/b/moved.txt") != nil {
		t.Fatal("not deleted on the NAS")
	}
	if err := os.Mkdir(filepath.Join(mnt, "Other"), 0o755); err == nil {
		t.Fatal("made a folder next to the shares")
	}

	// A change on the NAS shows.
	nas.put("Videos/new.mkv", []byte("x"))
	waitFor(t, "NAS change", func() bool { _, err := os.Stat(filepath.Join(docs, "Videos/new.mkv")); return err == nil })
}

func TestThumbnailer(t *testing.T) {
	for _, c := range []string{"gnome-thumbnail\n", "tracker-extract", "ffmpegthumbnailer"} {
		if !Thumbnailer(c) {
			t.Error(c)
		}
	}
	for _, c := range []string{"vlc", "nautilus", "cat"} {
		if Thumbnailer(c) {
			t.Error(c)
		}
	}
}
