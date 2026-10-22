package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"a.txt": "a.txt", "dir/b.txt": "dir/b.txt", "./c": "c", "d/./e": "d/e", "win\\f.txt": "win/f.txt", "dir/": "dir",
		"../evil": "", "a/../../evil": "", "/etc/passwd": "", "..\\evil": "", "C:\\x": "", "c:/x": "", "": "", ".": "", "a/..": "", "nul\x00": "",
	} {
		got, ok := SafeName(in)
		if ok != (want != "") || got != want {
			t.Errorf("SafeName(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
}

func TestFormat(t *testing.T) {
	for in, want := range map[string]string{"a.ZIP": "zip", "b.tar.gz": "tar.gz", "c.tgz": "tar.gz", "d.tar.xz": "tar.xz", "e.7z": "7z", "f.rar": "rar", "g.txt.gz": "gz", "h.txt": "", ".zip": "", "i.tar.zst": "tar.zst"} {
		if got := Format(in); got != want {
			t.Errorf("Format(%q) = %q, want %q", in, got, want)
		}
	}
}

type file struct {
	name, body string
	link       bool
}

func makeZip(t *testing.T, dir string, files []file) string {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		if f.link {
			h.SetMode(os.ModeSymlink | 0o777)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	zw.Close()
	p := filepath.Join(dir, "test.zip")
	os.WriteFile(p, buf.Bytes(), 0o644)
	return p
}

func extract(t *testing.T, archive string, only map[string]bool, lim Limits) (string, Stats, error) {
	t.Helper()
	dst := t.TempDir()
	os.Mkdir(filepath.Join(dst, "out"), 0o755)
	root, err := os.OpenRoot(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if lim.MaxFiles == 0 {
		lim = Limits{MaxFiles: 100, MaxBytes: 1 << 20}
	}
	st, err := Extract(context.Background(), f, archive, root, "out", only, lim, nil)
	return dst, st, err
}

func TestZipTraversalRefused(t *testing.T) {
	dir := t.TempDir()
	z := makeZip(t, dir, []file{{name: "ok/a.txt", body: "a"}, {name: "../evil.txt", body: "x"}, {name: "/abs.txt", body: "x"}, {name: "..\\win.txt", body: "x"},
		{name: "link", body: "/etc/passwd", link: true}, {name: "ok/b.txt", body: "b"}})
	dst, st, err := extract(t, z, nil, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 2 || st.Skipped != 4 {
		t.Errorf("stats %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "out/ok/b.txt")); string(b) != "b" {
		t.Error("b.txt not extracted")
	}
	for _, p := range []string{filepath.Join(dst, "evil.txt"), filepath.Join(filepath.Dir(dst), "evil.txt"), filepath.Join(dst, "out/link"), filepath.Join(dst, "abs.txt")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s written", p)
		}
	}
	f, _ := os.Open(z)
	defer f.Close()
	l, err := List(context.Background(), f, z)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := 0
	for _, e := range l.Entries {
		if e.Skipped != "" {
			unsafe++
		}
	}
	if l.Files != 2 || unsafe != 4 {
		t.Errorf("listing %+v", l)
	}
}

func TestSymlinkedFolderInDestinationRefused(t *testing.T) {
	dir := t.TempDir()
	z := makeZip(t, dir, []file{{name: "esc/x.txt", body: "x"}})
	dst := t.TempDir()
	outside := t.TempDir()
	os.Mkdir(filepath.Join(dst, "out"), 0o755)
	os.Symlink(outside, filepath.Join(dst, "out", "esc"))
	root, _ := os.OpenRoot(dst)
	defer root.Close()
	f, _ := os.Open(z)
	defer f.Close()
	_, err := Extract(context.Background(), f, z, root, "out", nil, Limits{MaxFiles: 10, MaxBytes: 1 << 20}, nil)
	if err == nil {
		t.Error("extraction through a symlinked folder succeeded")
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); err == nil {
		t.Error("file written outside the destination")
	}
}

func TestBombLimits(t *testing.T) {
	dir := t.TempDir()
	z := makeZip(t, dir, []file{{name: "zeros", body: strings.Repeat("\x00", 4<<20)}})
	dst, _, err := extract(t, z, nil, Limits{MaxFiles: 10, MaxBytes: 1 << 20})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("size limit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "out/zeros")); err == nil {
		t.Error("partial file left")
	}
	many := []file{}
	for i := 0; i < 20; i++ {
		many = append(many, file{name: strings.Repeat("d/", 1) + string(rune('a'+i)), body: "x"})
	}
	z = makeZip(t, t.TempDir(), many)
	if _, _, err := extract(t, z, nil, Limits{MaxFiles: 5, MaxBytes: 1 << 20}); !errors.Is(err, ErrLimit) {
		t.Errorf("file count limit: %v", err)
	}
}

func TestRatioLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 1 GiB")
	}
	// The ratio is only checked past 1 GiB: simulate with a tiny declared archive size.
	z := makeZip(t, t.TempDir(), []file{{name: "a", body: "x"}})
	if _, _, err := extract(t, z, nil, Limits{MaxFiles: 10, MaxBytes: 1 << 20, MaxRatio: 2, ArchiveSize: 1}); err != nil {
		t.Errorf("small file stopped by the ratio: %v", err)
	}
}

func TestTarGzAndSelection(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		h.Mode = 0o644
		tw.WriteHeader(h)
		tw.Write([]byte(body))
	}
	add(&tar.Header{Name: "docs/", Typeflag: tar.TypeDir}, "")
	add(&tar.Header{Name: "docs/readme.txt"}, "hello")
	add(&tar.Header{Name: "../../etc/cron.d/evil"}, "x")
	add(&tar.Header{Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "/etc"}, "")
	add(&tar.Header{Name: "dev", Typeflag: tar.TypeChar}, "")
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	os.WriteFile(p, buf.Bytes(), 0o644)
	dst, st, err := extract(t, p, nil, Limits{})
	if err != nil || st.Files != 1 || st.Skipped != 3 {
		t.Fatalf("all: %+v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "out/docs/readme.txt")); string(b) != "hello" {
		t.Error("readme not extracted")
	}
	// One member: lands directly in the folder; a second time under a new name.
	dst2 := t.TempDir()
	root, _ := os.OpenRoot(dst2)
	defer root.Close()
	for i := 0; i < 2; i++ {
		f, _ := os.Open(p)
		st, err = Extract(context.Background(), f, p, root, ".", map[string]bool{"docs/readme.txt": true}, Limits{MaxFiles: 10, MaxBytes: 1 << 20}, nil)
		f.Close()
		if err != nil || st.Files != 1 {
			t.Fatalf("selection: %+v %v", st, err)
		}
	}
	for _, n := range []string{"readme.txt", "readme (2).txt"} {
		if _, err := os.Stat(filepath.Join(dst2, n)); err != nil {
			t.Errorf("%s missing", n)
		}
	}
}

func TestParseSevenZipList(t *testing.T) {
	out := `7-Zip 25.01

Listing archive: /x/archive
--
Path = /x/archive
Type = 7z
Physical Size = 400

----------
Path = docs
Size = 0
Modified = 2026-10-01 10:00:00.1234567
Attributes = D drwxr-xr-x
Folder = +

Path = docs/a.txt
Size = 12
Packed Size = 8
Modified = 2026-10-01 10:00:00
Attributes = A -rw-r--r--
Encrypted = -

Path = docs/link
Size = 4
Attributes = A lrwxrwxrwx

Path = secret.bin
Size = 9
Encrypted = +
`
	l := parseSevenZipList(out)
	if len(l) != 4 || !l[0].Dir || l[1].Size != 12 || l[1].Packed != 8 || l[1].ModTime.Year() != 2026 || l[2].Skipped != "link" || l[3].Skipped != "encrypted" {
		t.Errorf("parsed %+v", l)
	}
}

// TestSevenZipAndXz runs the external tools when they are installed.
func TestSevenZipAndXz(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "d"), 0o755)
	os.WriteFile(filepath.Join(src, "d", "a*.txt"), []byte("star"), 0o644)
	os.Symlink("/etc", filepath.Join(src, "d", "lnk"))
	out := t.TempDir()
	if sevenZip() != "" {
		SevenZipCopy = func(f *os.File) (string, func(), error) {
			dir := t.TempDir()
			p, err := TempCopy(f, dir, -1, -1)
			return p, func() {}, err
		}
		z := filepath.Join(out, "t.7z")
		cmd := exec.Command(sevenZip(), "a", "-snl", z, "d")
		cmd.Dir = src
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("7z: %v %s", err, b)
		}
		dst, st, err := extract(t, z, nil, Limits{})
		if err != nil || st.Files != 1 || st.Skipped != 1 {
			t.Fatalf("7z extraction: %+v %v", st, err)
		}
		// The wildcard character is taken literally.
		if b, _ := os.ReadFile(filepath.Join(dst, "out/d/a*.txt")); string(b) != "star" {
			t.Error("7z member not extracted")
		}
	}
	if _, err := exec.LookPath("xz"); err == nil {
		tx := filepath.Join(out, "t.tar.xz")
		cmd := exec.Command("tar", "-cJf", tx, "d")
		cmd.Dir = src
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tar: %v %s", err, b)
		}
		f, _ := os.Open(tx)
		defer f.Close()
		l, err := List(context.Background(), f, tx)
		if err != nil || l.Files != 1 || l.Format != "tar.xz" {
			t.Fatalf("tar.xz listing: %+v %v", l, err)
		}
	}
}
