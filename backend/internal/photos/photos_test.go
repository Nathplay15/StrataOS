package photos

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeJPEG builds the header of a JPEG with an EXIF DateTimeOriginal.
func fakeJPEG(date string) []byte {
	t := make([]byte, 64)
	copy(t, "II*\x00")
	binary.LittleEndian.PutUint32(t[4:], 8)
	// IFD0 at 8: one entry, ExifIFD pointer -> 26
	binary.LittleEndian.PutUint16(t[8:], 1)
	binary.LittleEndian.PutUint16(t[10:], 0x8769)
	binary.LittleEndian.PutUint16(t[12:], 4)
	binary.LittleEndian.PutUint32(t[14:], 1)
	binary.LittleEndian.PutUint32(t[18:], 26)
	// Exif IFD at 26: DateTimeOriginal ASCII(20) at 44
	binary.LittleEndian.PutUint16(t[26:], 1)
	binary.LittleEndian.PutUint16(t[28:], 0x9003)
	binary.LittleEndian.PutUint16(t[30:], 2)
	binary.LittleEndian.PutUint32(t[32:], 20)
	binary.LittleEndian.PutUint32(t[36:], 44)
	copy(t[44:], date+"\x00")
	app1 := append([]byte("Exif\x00\x00"), t...)
	b := []byte{0xFF, 0xD8, 0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(b[4:], uint16(len(app1)+2))
	b = append(b, app1...)
	return append(b, 0xFF, 0xDA, 0, 2)
}

func TestExifAndScan(t *testing.T) {
	d, ok := exifDate(fakeJPEG("2019:07:14 10:30:00"))
	if !ok || d.Year() != 2019 || d.Month() != 7 || d.Hour() != 10 {
		t.Fatalf("date %v %v", d, ok)
	}
	if _, ok := exifDate([]byte("not a jpeg")); ok {
		t.Error("garbage parsed")
	}
	root := t.TempDir()
	Dir = t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "2019", ".hidden"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "2019", "plage.jpg"), fakeJPEG("2019:07:14 10:30:00"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "video.mp4"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "2019", ".hidden", "x.jpg"), []byte("x"), 0o644)
	_ = os.Symlink("/etc/passwd", filepath.Join(root, "evil.jpg"))
	x := &Index{byPath: map[string]int{}}
	n, err := x.Scan(context.Background(), []string{root}, func(string, ...any) {})
	if err != nil || n != 2 {
		t.Fatalf("scan: %d %v", n, err)
	}
	list := x.List(func(string) bool { return true })
	if !list[1].Video && !list[0].Video {
		t.Error("video not recognised")
	}
	it, ok := x.Get(filepath.Join(root, "2019", "plage.jpg"))
	if !ok || time.Unix(it.Date, 0).Year() != 2019 {
		t.Errorf("photo date: %+v", it)
	}
	y := &Index{byPath: map[string]int{}}
	y.Load()
	if _, ok := y.Get(it.Path); !ok {
		t.Error("index not saved")
	}
}

// An index made before hashes existed (or whose hash failed) gets them at
// the next scans, failures being retried only after hashRetry.
func TestScanFillsMissingHashes(t *testing.T) {
	Dir = t.TempDir()
	root := t.TempDir()
	for _, n := range []string{"a.jpg", "b.jpg", "broken.jpg", "clip.mp4"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	calls := map[string]int{}
	hashImage = func(_ context.Context, p string) uint64 {
		calls[filepath.Base(p)]++
		if filepath.Base(p) == "broken.jpg" {
			return 0
		}
		return 42
	}
	oldCan := canHash
	defer func() { hashImage, canHash = dhash, oldCan }()
	// First scan without the tools: no hash, as in an old index.
	canHash = func() bool { return false }
	x := &Index{byPath: map[string]int{}}
	if _, err := x.Scan(context.Background(), []string{root}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if it, _ := x.Get(filepath.Join(root, "a.jpg")); it.Hash != 0 {
		t.Fatal("hashed without the tools")
	}
	canHash = func() bool { return true }
	if _, err := x.Scan(context.Background(), []string{root}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.jpg", "b.jpg"} {
		if it, _ := x.Get(filepath.Join(root, n)); it.Hash != 42 {
			t.Errorf("%s: hash %d after the second scan", n, it.Hash)
		}
	}
	if it, _ := x.Get(filepath.Join(root, "broken.jpg")); it.Hash != 0 || it.HashFailed == 0 {
		t.Errorf("broken.jpg: %+v", it)
	}
	if calls["clip.mp4"] != 0 {
		t.Error("a video was hashed")
	}
	// A failure is not retried at every scan…
	if _, err := x.Scan(context.Background(), []string{root}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if calls["broken.jpg"] != 1 || calls["a.jpg"] != 1 {
		t.Errorf("calls %v", calls)
	}
	// …but once hashRetry has passed.
	if !needsHash(Item{HashFailed: time.Now().Add(-hashRetry - time.Hour).Unix()}, time.Now()) {
		t.Error("an old failure is not retried")
	}
}
