package preview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestKey(t *testing.T) {
	t0 := time.Unix(1700000000, 5)
	k := Key("/srv/strata/main/a.docx", t0, 100, "pdf")
	if len(k) != 40 || k != Key("/srv/strata/main/a.docx", t0, 100, "pdf") {
		t.Fatal("key not stable")
	}
	for _, other := range []string{
		Key("/srv/strata/main/b.docx", t0, 100, "pdf"),
		Key("/srv/strata/main/a.docx", t0.Add(time.Nanosecond), 100, "pdf"),
		Key("/srv/strata/main/a.docx", t0, 101, "pdf"),
		Key("/srv/strata/main/a.docx", t0, 100, "img-2560.jpg"),
		// The separator keeps fields apart.
		Key("/srv/strata/main/a.docx1", t0, 0, "pdf"),
	} {
		if other == k {
			t.Error("different versions share a key")
		}
	}
}

func TestCacheEviction(t *testing.T) {
	c := &Cache{Dir: t.TempDir(), Max: 250}
	put := func(name string, size int, age time.Duration) {
		src := filepath.Join(t.TempDir(), "x")
		os.WriteFile(src, make([]byte, size), 0o600)
		if _, err := c.Put(name, ".pdf", src); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		os.Chtimes(filepath.Join(c.Dir, name+".pdf"), at, at)
	}
	put("old", 100, 3*time.Hour)
	put("mid", 100, 2*time.Hour)
	if _, ok := c.Get("old", ".pdf"); !ok { // a hit makes it recent
		t.Fatal("old missing")
	}
	os.Mkdir(filepath.Join(c.Dir, ".work-1"), 0o700) // never evicted nor counted
	put("new", 100, 0)
	if _, ok := c.Get("mid", ".pdf"); ok {
		t.Error("least recently used entry kept")
	}
	for _, k := range []string{"old", "new"} {
		if _, ok := c.Get(k, ".pdf"); !ok {
			t.Errorf("%s evicted", k)
		}
	}
	c.CleanWork()
	if _, err := os.Stat(filepath.Join(c.Dir, ".work-1")); err == nil {
		t.Error("work folder left")
	}
}

func TestImagePlan(t *testing.T) {
	all := Tools{Vips: true, FFmpeg: true, Exiftool: true}
	for _, tc := range []struct {
		ext    string
		tools  Tools
		steps  []string
		format string
	}{
		{".heic", all, []string{"vips"}, "jpeg"},
		{".HEIF", all, []string{"vips"}, "jpeg"},
		{".tiff", all, []string{"vips", "ffmpeg"}, "webp"},
		{".psd", all, []string{"vips", "ffmpeg"}, "webp"},
		{".psd", Tools{FFmpeg: true}, []string{"ffmpeg"}, "webp"},
		{".cr2", all, []string{"exiftool"}, "jpeg"},
		{".dng", all, []string{"exiftool", "ffmpeg"}, "jpeg"},
		{".nef", Tools{Vips: true}, nil, "jpeg"},
		{".heic", Tools{FFmpeg: true}, nil, "jpeg"},
		{".jpg", all, nil, ""},
		{".txt", all, nil, ""},
	} {
		steps, format := ImagePlan(tc.ext, tc.tools)
		if !reflect.DeepEqual(steps, tc.steps) || format != tc.format {
			t.Errorf("%s %+v: %v %s, want %v %s", tc.ext, tc.tools, steps, format, tc.steps, tc.format)
		}
	}
	if !ImageConvertible(".ARW") || !ImageConvertible(".heic") || ImageConvertible(".png") {
		t.Error("ImageConvertible")
	}
}

// TestImageWithFFmpeg converts a TIFF when ffmpeg is installed.
func TestImageWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "pic.tiff")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=640x480", "-frames:v", "1", "-y", src).CombinedOutput(); err != nil {
		t.Skipf("cannot make a TIFF: %v %s", err, b)
	}
	f, _ := os.Open(src)
	defer f.Close()
	fi, _ := f.Stat()
	c := &Cache{Dir: filepath.Join(dir, "cache"), Max: 1 << 30}
	p, ctype, err := c.Image(context.Background(), f, src, fi, 320)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if ctype != "image/webp" || len(data) < 100 || string(data[8:12]) != "WEBP" {
		t.Errorf("got %s, %d bytes", ctype, len(data))
	}
	// Second call: from the cache.
	if p2, _, err := c.Image(context.Background(), f, src, fi, 320); err != nil || p2 != p {
		t.Errorf("not cached: %s %v", p2, err)
	}
}
