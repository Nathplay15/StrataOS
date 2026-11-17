// Optional OCR: images and scanned PDFs are indexed when the "Text
// recognition" component (tesseract, poppler-utils) is installed. Results are
// cached on disk so a lost index or a reindex does not recognize everything
// again. The tools run at the lowest priority with a memory cap
// (limitedCommand) and read the file from their standard input.

package ftsearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// imageExt: formats tesseract reads directly.
var imageExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".bmp": true,
	".gif": true, ".tif": true, ".tiff": true, ".webp": true,
}

const (
	// A PDF with fewer words than this is a scan: it goes through OCR.
	minPdfWords = 25
	// Pages of one PDF turned into images at most.
	maxOcrPages = 20
	// One tesseract run at most.
	maxOcrRun = 2 * time.Minute
	// One whole PDF at most (rasterizing is what takes long).
	maxOcrPdf = 10 * time.Minute
	// Results kept in the OCR cache.
	maxOcrCache = 2000
	// Memory of one tesseract or pdftoppm run.
	maxOcrMemory = "1G"
)

// ocrState remembers what the tools can do: they do not change while running.
var ocrState struct {
	once   sync.Once
	binary bool   // tesseract present
	raster bool   // pdftoppm present (scanned PDFs)
	langs  string // tesseract -l value, "" for its default language
}

// ocrAvailable reports whether images can be recognized.
func ocrAvailable() bool {
	ocrProbe()
	return ocrState.binary
}

// ocrProbe looks for the tools once and picks the languages to recognize:
// French and English when their packs are installed, otherwise tesseract's
// own default.
func ocrProbe() {
	ocrState.once.Do(func() {
		if _, err := exec.LookPath("tesseract"); err != nil {
			return
		}
		ocrState.binary = true
		_, err := exec.LookPath("pdftoppm")
		ocrState.raster = err == nil
		have := map[string]bool{}
		if out, err := exec.Command("tesseract", "--list-langs").Output(); err == nil {
			for _, l := range parseLangs(string(out)) {
				have[strings.ToLower(l)] = true
			}
		}
		var want []string
		for _, l := range []string{"eng", "fra"} {
			if have[l] {
				want = append(want, l)
			}
		}
		if len(want) > 0 {
			ocrState.langs = strings.Join(want, "+")
		}
	})
}

// parseLangs reads the output of tesseract --list-langs: a header line, then
// one language per line.
func parseLangs(out string) []string {
	var langs []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.Contains(l, ":") {
			continue
		}
		langs = append(langs, l)
	}
	return langs
}

// ocrImage recognizes the text of an open image file.
func ocrImage(f *os.File, path string, info fs.FileInfo) (string, error) {
	if !ocrAvailable() {
		return "", extractError("OCR not installed")
	}
	return ocrCached(f, path, info, func(f *os.File) (string, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		// tesseract reads the image from its standard input: it never opens
		// a path inside a share.
		return ocrRun("stdin", f)
	})
}

// ocrRun runs tesseract on one image (stdin: the image is read from in).
func ocrRun(path string, in io.Reader) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), maxOcrRun)
	defer cancel()
	args := []string{path, "stdout"}
	if ocrState.langs != "" {
		args = append(args, "-l", ocrState.langs)
	}
	cmd := limitedCommand(ctx, maxOcrMemory, "tesseract", args...)
	cmd.Stdin = in
	// One thread: OCR is background work, it must not take every core.
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, n: maxTextBytes + 4}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tesseract: %w", err)
	}
	return truncate(out.String()), nil
}

// ocrPdf recognizes a scanned PDF: its pages are rasterized with pdftoppm,
// then each image goes through tesseract.
func ocrPdf(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "strata-ocr")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), maxOcrPdf)
	defer cancel()
	cmd := limitedCommand(ctx, maxOcrMemory, "pdftoppm", "-png", "-r", "120", "-f", "1",
		"-l", strconv.Itoa(maxOcrPages), "-", filepath.Join(dir, "page"))
	cmd.Stdin = f
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdftoppm: %w", err)
	}
	pages, err := filepath.Glob(filepath.Join(dir, "page-*.png"))
	if err != nil {
		return "", err
	}
	sort.Strings(pages)
	var out strings.Builder
	for _, p := range pages {
		if ctx.Err() != nil {
			break
		}
		text, err := ocrRun(p, nil)
		if err != nil {
			continue
		}
		out.WriteString(text)
		out.WriteString("\n")
		if out.Len() > maxTextBytes {
			break
		}
	}
	return truncate(out.String()), nil
}

// ocrCached returns the saved recognition of a file, running run only when
// the file changed since the last one.
func ocrCached(f *os.File, path string, info fs.FileInfo, run func(*os.File) (string, error)) (string, error) {
	key := ocrKey(path, info)
	if b, err := os.ReadFile(filepath.Join(Dir, "ocr", key)); err == nil {
		return string(b), nil
	}
	text, err := run(f)
	if err != nil {
		return "", err
	}
	if os.MkdirAll(filepath.Join(Dir, "ocr"), 0o750) == nil {
		if os.WriteFile(filepath.Join(Dir, "ocr", key), []byte(text), 0o640) == nil {
			ocrWrites++
			if ocrWrites%128 == 0 {
				ocrPrune()
			}
		}
	}
	return text, nil
}

var ocrWrites int

// ocrKey turns the path and the stamp of a file into a cache file name
// (the same names as before 0.12.1: the cache stays valid).
func ocrKey(path string, info fs.FileInfo) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%d", path, info.Size(), info.ModTime().Unix()))
	return hex.EncodeToString(sum[:16])
}

// ocrPrune caps the OCR cache: the oldest results go first.
func ocrPrune() {
	entries, err := os.ReadDir(filepath.Join(Dir, "ocr"))
	if err != nil || len(entries) <= maxOcrCache {
		return
	}
	type item struct {
		name string
		mod  time.Time
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			items = append(items, item{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items[:len(items)-maxOcrCache] {
		_ = os.Remove(filepath.Join(Dir, "ocr", it.name))
	}
}
