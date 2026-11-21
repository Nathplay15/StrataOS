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

// fakeGPSJPEG builds a JPEG header with only a GPS IFD.
func fakeGPSJPEG(lat, lon [3][2]uint32, ns, ew byte) []byte {
	t := make([]byte, 200)
	copy(t, "MM\x00*")
	binary.BigEndian.PutUint32(t[4:], 8)
	binary.BigEndian.PutUint16(t[8:], 1)
	binary.BigEndian.PutUint16(t[10:], 0x8825)
	binary.BigEndian.PutUint16(t[12:], 4)
	binary.BigEndian.PutUint32(t[14:], 1)
	binary.BigEndian.PutUint32(t[18:], 26)
	// GPS IFD at 26: 4 entries; rationals at 100 and 124.
	binary.BigEndian.PutUint16(t[26:], 4)
	e := 28
	entry := func(tag, typ uint16, count uint32, val []byte) {
		binary.BigEndian.PutUint16(t[e:], tag)
		binary.BigEndian.PutUint16(t[e+2:], typ)
		binary.BigEndian.PutUint32(t[e+4:], count)
		copy(t[e+8:e+12], val)
		e += 12
	}
	off := func(o uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, o); return b }
	entry(1, 2, 2, []byte{ns, 0})
	entry(2, 5, 3, off(100))
	entry(3, 2, 2, []byte{ew, 0})
	entry(4, 5, 3, off(124))
	for k := range 3 {
		binary.BigEndian.PutUint32(t[100+8*k:], lat[k][0])
		binary.BigEndian.PutUint32(t[104+8*k:], lat[k][1])
		binary.BigEndian.PutUint32(t[124+8*k:], lon[k][0])
		binary.BigEndian.PutUint32(t[128+8*k:], lon[k][1])
	}
	app1 := append([]byte("Exif\x00\x00"), t...)
	b := []byte{0xFF, 0xD8, 0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(b[4:], uint16(len(app1)+2))
	b = append(b, app1...)
	return append(b, 0xFF, 0xDA, 0, 2)
}

func TestGPSAndPlaces(t *testing.T) {
	// 48°51'24" N, 2°21'8" W
	m, ok := exifOf(fakeGPSJPEG([3][2]uint32{{48, 1}, {51, 1}, {2400, 100}}, [3][2]uint32{{2, 1}, {21, 1}, {8, 1}}, 'N', 'W'))
	if !ok || m.lat < 48.856 || m.lat > 48.857 || m.lon > -2.35 || m.lon < -2.36 || !m.date.IsZero() {
		t.Fatalf("gps %+v %v", m, ok)
	}
	m, _ = exifOf(fakeGPSJPEG([3][2]uint32{{33, 1}, {52, 1}, {0, 0}}, [3][2]uint32{{151, 1}, {12, 1}, {0, 1}}, 'S', 'E'))
	if m.lat != 0 {
		t.Errorf("a zero denominator is no position: %+v", m)
	}
	p, ok := Nearest(48.8566, 2.3522)
	if !ok || p.City != "Paris" || p.Country != "FR" {
		t.Errorf("Paris: %+v", p)
	}
	if p, ok = Nearest(-33.87, 151.21); !ok || p.City != "Sydney" || p.Region == "" {
		t.Errorf("Sydney: %+v", p)
	}
	if p2, ok := PlaceByID(p.ID); !ok || p2 != p {
		t.Error("PlaceByID")
	}
	if _, ok = Nearest(0, -30); ok {
		t.Error("the middle of the Atlantic has no town")
	}
	if _, ok := PlaceByID(-1); ok {
		t.Error("invalid id")
	}
}

func TestMemories(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	at := func(y int, m time.Month, d int) Item {
		return Item{Path: "x", Date: time.Date(y, m, d, 12, 0, 0, 0, time.Local).Unix()}
	}
	got := Memories([]Item{at(2026, 11, 11), at(2024, 11, 11), at(2024, 11, 11), at(2024, 11, 12), at(2019, 11, 11), at(2020, 10, 11)}, now)
	if len(got) != 2 || got[0].Year != 2024 || got[0].Count != 2 || got[0].YearsAgo != 2 || got[1].Year != 2019 {
		t.Errorf("memories: %+v", got)
	}
}
