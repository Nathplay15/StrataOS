package ftsearch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary act as the PDF helper process.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		HelperMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func newIndex() *Index {
	return &Index{docs: map[string]*doc{}, inv: map[string]map[string]int{}, failed: map[string]*doc{}}
}

// tempDir sets Dir to a fresh folder for the test.
func tempDir(t *testing.T) string {
	t.Helper()
	old := Dir
	Dir = t.TempDir()
	t.Cleanup(func() { Dir = old })
	return Dir
}

func hitOf(t *testing.T, x *Index, p string) Hit {
	t.Helper()
	x.mu.RLock()
	defer x.mu.RUnlock()
	d := x.docs[p]
	if d == nil {
		t.Fatalf("%s not indexed", p)
	}
	return Hit{Path: d.Path, Name: d.Name, Size: d.Size, Mtime: d.Mtime}
}

func extractPath(t *testing.T, p string) (string, error) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return extract(f, p, strings.ToLower(filepath.Ext(p)), info)
}

// buildPdf writes a PDF made of the given objects (1 is the catalog), with a
// correct cross-reference table.
func buildPdf(objs ...string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, 0, len(objs))
	for i, o := range objs {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

// pdfPage is a one-page PDF whose page dictionary ends with extra.
func pdfPage(text, extra string) []byte {
	content := "BT /F1 12 Tf (" + text + ") Tj ET"
	return buildPdf(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> "+extra+">>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	)
}

// A malformed PDF makes the parser panic: the scan survives it, records the
// file as failed and does not try it again until it changes (review 0.12.0 #3).
func TestScanMalformedPDF(t *testing.T) {
	tempDir(t)
	root := t.TempDir()
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(root, name), b, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("good.pdf", pdfPage("Bonjour facture", ""))
	write("bad.pdf", pdfPage("crash", ") "))
	write("notes.txt", []byte("facture du mois"))

	x := newIndex()
	var skipped []string
	logf := func(msg string, args ...any) { skipped = append(skipped, fmt.Sprint(args...)) }
	if _, err := x.Scan(context.Background(), []string{root}, logf); err != nil {
		t.Fatal(err)
	}
	if h := x.Search(tokenize("facture"), 10); len(h) != 2 {
		t.Fatalf("hits: %+v", h)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "bad.pdf") {
		t.Fatalf("skipped: %q", skipped)
	}
	if x.failed[filepath.Join(root, "bad.pdf")] == nil {
		t.Fatal("bad.pdf must be marked as failed")
	}
	// Saved with the index, and not parsed again by the next scans.
	x2 := newIndex()
	x2.Load()
	if x2.failed[filepath.Join(root, "bad.pdf")] == nil {
		t.Fatal("failure not persisted")
	}
	skipped = nil
	if _, err := x2.Scan(context.Background(), []string{root}, logf); err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("failed file parsed again: %q", skipped)
	}
	// Once the file changes it is tried again.
	write("bad.pdf", pdfPage("repare", ""))
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(filepath.Join(root, "bad.pdf"), future, future)
	if _, err := x2.Scan(context.Background(), []string{root}, logf); err != nil {
		t.Fatal(err)
	}
	if h := x2.Search(tokenize("repare"), 10); len(h) != 1 || x2.failed[filepath.Join(root, "bad.pdf")] != nil {
		t.Fatalf("repaired file: %+v", h)
	}
}

// In the daemon, PDFs are parsed by a helper process with a time limit: a PDF
// that sends the parser into an endless loop is abandoned.
func TestPdfHelperProcess(t *testing.T) {
	tempDir(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	SetHelper(exe)
	oldRun := maxPdfRun
	maxPdfRun = 5 * time.Second
	t.Cleanup(func() { SetHelper(""); maxPdfRun = oldRun })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "good.pdf"), pdfPage("Bonjour helper", ""), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.pdf"), pdfPage("crash", ") "), 0o640); err != nil {
		t.Fatal(err)
	}
	// The page tree points to itself: the parser loops forever.
	loop := buildPdf("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [2 0 R] /Count 1 >>")
	if err := os.WriteFile(filepath.Join(root, "loop.pdf"), loop, 0o640); err != nil {
		t.Fatal(err)
	}
	x := newIndex()
	start := time.Now()
	if _, err := x.Scan(context.Background(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("scan took %v", d)
	}
	if h := x.Search(tokenize("helper"), 10); len(h) != 1 {
		t.Fatalf("hits: %+v", h)
	}
	for _, n := range []string{"bad.pdf", "loop.pdf"} {
		if x.failed[filepath.Join(root, n)] == nil {
			t.Fatalf("%s must be marked as failed", n)
		}
	}
}

// A file swapped for a symbolic link is never followed: neither when it is
// indexed nor when a snippet is shown (review 0.12.0 #2).
func TestSymlinkSwap(t *testing.T) {
	tempDir(t)
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "shadow.txt")
	if err := os.WriteFile(secret, []byte("root hash motdepasse quota"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(p, []byte("le quota du volume"), 0o640); err != nil {
		t.Fatal(err)
	}
	x := newIndex()
	if _, err := x.Scan(context.Background(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	h := hitOf(t, x, p)

	// The user replaces the indexed file by a link to a system file.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, p); err != nil {
		t.Fatal(err)
	}
	if sn := x.Snippet(h, tokenize("quota")); strings.Contains(sn, "motdepasse") || !strings.Contains(sn, "quota du volume") {
		t.Fatalf("snippet %q", sn)
	}

	// The same swap between the listing and the extraction.
	listed, err := os.Stat(filepath.Join(root, "other.txt"))
	if err == nil {
		t.Fatal("unexpected file")
	}
	other := filepath.Join(root, "other.txt")
	if err := os.WriteFile(other, []byte("texte normal"), 0o640); err != nil {
		t.Fatal(err)
	}
	if listed, err = os.Lstat(other); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(other)
	if err := os.Symlink(secret, other); err != nil {
		t.Fatal(err)
	}
	rt, err := openRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if text, err := extractAt(rt, root, other, ".txt", listed); err == nil || strings.Contains(text, "motdepasse") {
		t.Fatalf("followed the link: %q %v", text, err)
	}
	// A link inside the root to another file is not followed either.
	inner := filepath.Join(root, "inner.txt")
	if err := os.WriteFile(inner, []byte("autre"), 0o640); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(other)
	if err := os.Symlink("inner.txt", other); err != nil {
		t.Fatal(err)
	}
	if _, err := extractAt(rt, root, other, ".txt", listed); err == nil {
		t.Fatal("followed an inner link")
	}
	// A rescan does not index the link.
	if _, err := x.Scan(context.Background(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if h := x.Search(tokenize("motdepasse"), 10); len(h) != 0 {
		t.Fatalf("link indexed: %+v", h)
	}
}

// Documents of a removed share are dropped; those of a share that cannot be
// reached (unmounted volume) are kept (review 0.12.0, minor).
func TestScanDropsRemovedRoots(t *testing.T) {
	tempDir(t)
	a, b := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(a, "a.txt"), []byte("alpha commun"), 0o640)
	_ = os.WriteFile(filepath.Join(b, "b.txt"), []byte("beta commun"), 0o640)
	x := newIndex()
	if _, err := x.Scan(context.Background(), []string{a, b}, nil); err != nil {
		t.Fatal(err)
	}
	if h := x.Search(tokenize("commun"), 10); len(h) != 2 {
		t.Fatalf("hits: %+v", h)
	}
	// b unreachable: kept.
	gone := b + "-moved"
	if err := os.Rename(b, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Scan(context.Background(), []string{a, b}, nil); err != nil {
		t.Fatal(err)
	}
	if h := x.Search(tokenize("beta"), 10); len(h) != 1 {
		t.Fatalf("unreachable share dropped: %+v", h)
	}
	_ = os.Rename(gone, b)
	// b no longer a share: dropped, with its saved text.
	if _, err := x.Scan(context.Background(), []string{a}, nil); err != nil {
		t.Fatal(err)
	}
	if h := x.Search(tokenize("beta"), 10); len(h) != 0 {
		t.Fatalf("removed share kept: %+v", h)
	}
	entries, _ := os.ReadDir(textDir())
	if len(entries) != 1 {
		t.Fatalf("saved texts: %d", len(entries))
	}
}

func TestPostingsCap(t *testing.T) {
	x := newIndex()
	d := indexText("/a", "a", "un deux trois", 3, 1)
	x.add(d)
	if x.postings != 3 {
		t.Fatalf("postings %d", x.postings)
	}
	x.remove("/a")
	if x.postings != 0 {
		t.Fatalf("postings %d", x.postings)
	}
}

// OCR tools and the PDF helper run with a memory cap at the lowest priority
// (review 0.12.0: tesseract and pdftoppm ran as root without limits).
func TestLimitArgv(t *testing.T) {
	got := strings.Join(limitArgv(true, true, "1G", "tesseract", "stdin", "stdout"), " ")
	for _, want := range []string{"systemd-run --scope", "MemoryMax=1G", "CPUQuota=100%", "--nice=19", "IOSchedulingClass=idle", "-- tesseract stdin stdout"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
	got = strings.Join(limitArgv(false, true, "768M", "pdftoppm", "-"), " ")
	if got != "prlimit --as=3221225472 -- nice -n 19 pdftoppm -" {
		t.Fatalf("prlimit: %q", got)
	}
	if got = strings.Join(limitArgv(false, false, "1G", "x"), " "); got != "nice -n 19 x" {
		t.Fatalf("plain: %q", got)
	}
}

// An index made before the saved texts existed gets them at the next scan,
// so its snippets come back.
func TestScanFillsMissingTexts(t *testing.T) {
	tempDir(t)
	root := t.TempDir()
	p := filepath.Join(root, "notes.txt")
	_ = os.WriteFile(p, []byte("le quota du volume"), 0o640)
	x := newIndex()
	if _, err := x.Scan(context.Background(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(textDir()); err != nil {
		t.Fatal(err)
	}
	if sn := x.Snippet(hitOf(t, x, p), tokenize("quota")); sn != "" {
		t.Fatalf("snippet without saved text: %q", sn)
	}
	if _, err := x.Scan(context.Background(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if sn := x.Snippet(hitOf(t, x, p), tokenize("quota")); !strings.Contains(sn, "quota") {
		t.Fatalf("snippet after rescan: %q", sn)
	}
}
