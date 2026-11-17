package api

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"strataos/internal/archive"
)

func TestWalkProps(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "sub/deep"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "a.txt"), []byte("12345"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "sub/b.bin"), []byte("1234567890"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "sub/deep/c.txt"), []byte("123"), 0o644))
	must(os.Symlink("/etc/passwd", filepath.Join(dir, "sub/link")))

	rt, err := os.OpenRoot(dir)
	must(err)
	defer rt.Close()
	files, dirs, total, truncated := walkProps(rt, ".")
	if files != 3 || dirs != 2 || total != 18 || truncated {
		t.Errorf("walkProps: files=%d dirs=%d total=%d truncated=%v, want 3 2 18 false", files, dirs, total, truncated)
	}
}

func TestZipTreeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "docs/Holidays"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "docs/notes.txt"), []byte("hello world"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "docs/Holidays/beach.jpg"), []byte("jpeg-bytes"), 0o644))
	must(os.Symlink("/etc/passwd", filepath.Join(dir, "docs/unsafe-link")))

	rt, err := os.OpenRoot(dir)
	must(err)
	defer rt.Close()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	tg := target{root: dir, rel: "docs", abs: filepath.Join(dir, "docs"), acc: access{read: true}}
	if err := zipTree(context.Background(), rt, tg, zw); err != nil {
		t.Fatal(err)
	}
	must(zw.Close())

	zipPath := filepath.Join(dir, "docs.zip")
	must(os.WriteFile(zipPath, buf.Bytes(), 0o644))

	out := t.TempDir()
	ort, err := os.OpenRoot(out)
	must(err)
	defer ort.Close()
	f, err := os.Open(zipPath)
	must(err)
	defer f.Close()
	st, err := archive.Extract(context.Background(), f, "docs.zip", ort, ".", nil, archive.Limits{MaxFiles: 1000, MaxBytes: 1 << 20}, nil)
	must(err)
	if st.Files != 2 || st.Dirs != 2 || st.Skipped != 0 {
		t.Errorf("extract: %+v, want 2 files, 2 folders (the symlink was never zipped)", st)
	}
	for _, c := range [][2]string{
		{"docs/notes.txt", "hello world"},
		{"docs/Holidays/beach.jpg", "jpeg-bytes"},
	} {
		b, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(c[0])))
		if err != nil || string(b) != c[1] {
			t.Errorf("%s: %q %v", c[0], b, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(out, "docs/unsafe-link")); err == nil {
		t.Error("symlink extracted")
	}
}
