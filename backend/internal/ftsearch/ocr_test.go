package ftsearch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLangs(t *testing.T) {
	langs := parseLangs("List of available languages (3):\neng\nfra\nosd\n")
	if len(langs) != 3 || langs[0] != "eng" || langs[1] != "fra" || langs[2] != "osd" {
		t.Fatalf("parseLangs = %v", langs)
	}
	if got := parseLangs(""); got != nil {
		t.Fatalf("parseLangs(\"\") = %v", got)
	}
}

func TestPdfNeedsOCR(t *testing.T) {
	if !pdfNeedsOCR("") || !pdfNeedsOCR("a few words only") {
		t.Fatal("a thin text layer must go through OCR")
	}
	long := strings.Repeat("word ", 30)
	if pdfNeedsOCR(long) {
		t.Fatal("a digital PDF must not go through OCR")
	}
}

func TestOcrKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, ok := ocrKey(p)
	if !ok || key == "" {
		t.Fatal("ocrKey failed on an existing file")
	}
	again, _ := ocrKey(p)
	if key != again {
		t.Fatal("ocrKey must be stable for an unchanged file")
	}
	if err := os.Chtimes(p, time.Now(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if changed, _ := ocrKey(p); changed == key {
		t.Fatal("ocrKey must change when the file changes")
	}
	if _, ok := ocrKey(filepath.Join(p, "missing")); ok {
		t.Fatal("ocrKey must fail on a missing file")
	}
}

func TestOcrCached(t *testing.T) {
	old := Dir
	Dir = t.TempDir()
	defer func() { Dir = old }()
	p := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	run := func(string) (string, error) { calls++; return "recognized text", nil }
	a, err := ocrCached(p, run)
	if err != nil || a != "recognized text" {
		t.Fatalf("ocrCached = %q, %v", a, err)
	}
	b, err := ocrCached(p, run)
	if err != nil || b != "recognized text" {
		t.Fatalf("ocrCached = %q, %v", b, err)
	}
	if calls != 1 {
		t.Fatalf("the recognition ran %d times, want 1 (cached)", calls)
	}
}

func TestOcrPrune(t *testing.T) {
	old := Dir
	Dir = t.TempDir()
	defer func() { Dir = old }()
	dir := filepath.Join(Dir, "ocr")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for i := range maxOcrCache + 10 {
		name := filepath.Join(dir, fmt.Sprintf("%032d", i))
		if err := os.WriteFile(name, []byte("t"), 0o640); err != nil {
			t.Fatal(err)
		}
		if i < 10 {
			stamp := time.Now().Add(-time.Duration(maxOcrCache+10-i) * time.Minute)
			if err := os.Chtimes(name, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
	}
	ocrPrune()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxOcrCache {
		t.Fatalf("cache holds %d entries, want %d", len(entries), maxOcrCache)
	}
}

func TestImageExt(t *testing.T) {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".tif", ".tiff", ".webp"} {
		if !imageExt[ext] || !supported(ext) {
			t.Fatalf("%s must be supported", ext)
		}
	}
	if supported(".bin") || imageExt[".bin"] {
		t.Fatal(".bin must stay out of the index")
	}
}
