package ftsearch

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenize(t *testing.T) {
	got := tokenize("Hello, World! naïve-CAFÉ test42 a")
	want := []string{"hello", "world", "naïve", "café", "test42"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tokenize: %q, want %q", got, want)
	}
	if len(tokenize("... ---")) != 0 {
		t.Fatal("punctuation only must give no term")
	}
}

func TestScanSearchPersist(t *testing.T) {
	dir := t.TempDir()
	Dir = dir
	defer func() { Dir = "/var/lib/strata/fulltext" }()
	x := &Index{docs: map[string]*doc{}, inv: map[string]map[string]int{}}

	root := filepath.Join(dir, "docs")
	if err := os.MkdirAll(filepath.Join(root, "sub", ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.md", "# Projet\n\nLe quota btrfs est activé sur le volume principal.")
	write("quota.md", "Le quota btrfs est suivi de près par le service stockage.")
	write("sub/report.docx", "") // replaced by a real docx below
	write("sub/.hidden/secret.txt", "mot de passe secret")
	write("data.csv", "name,amount\nalice,42\n")

	if err := os.WriteFile(filepath.Join(root, "image.bin"), []byte{0x00, 0x01, 0x02}, 0o640); err != nil {
		t.Fatal(err)
	}
	buildDocx(t, filepath.Join(root, "sub", "report.docx"), "Le rapport trimestriel mentionne le quota btrfs, et le quota btrfs est bien actif.")

	n, err := x.Scan(context.Background(), []string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("indexed %d documents, want 4", n)
	}
	if docs, _ := x.Stats(); docs != 4 {
		t.Fatalf("stats: %d docs", docs)
	}

	// AND search across terms, best score first.
	hits := x.Search(tokenize("quota btrfs"), 10)
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3: %+v", len(hits), hits)
	}
	if !strings.HasSuffix(hits[1].Path, "report.docx") || !strings.HasSuffix(hits[2].Path, "notes.md") {
		t.Fatalf("order: %v then %v", hits[1].Path, hits[2].Path)
	}
	// A term found in the file name boosts the score among content matches.
	if h := x.Search(tokenize("quota btrfs"), 10); len(h) != 3 || !strings.HasSuffix(h[0].Path, "quota.md") {
		t.Fatalf("name boost: %+v", h)
	}
	// One missing term: no result at all.
	if h := x.Search(tokenize("quota absentword"), 10); len(h) != 0 {
		t.Fatalf("AND: %+v", h)
	}
	// Hidden folders are not indexed.
	if h := x.Search(tokenize("secret"), 10); len(h) != 0 {
		t.Fatalf("hidden indexed: %+v", h)
	}
	// Binary files are skipped.
	if h := x.Search(tokenize("02"), 10); len(h) != 0 {
		t.Fatalf("binary indexed: %+v", h)
	}

	// Snippet around the first match.
	sn := x.Snippet(filepath.Join(root, "notes.md"), tokenize("quota"))
	if !strings.Contains(sn, "quota") {
		t.Fatalf("snippet %q", sn)
	}

	// Persistence: a fresh index loads the same documents.
	if err := os.Remove(filepath.Join(root, "notes.md")); err != nil {
		t.Fatal(err)
	}
	x2 := &Index{docs: map[string]*doc{}, inv: map[string]map[string]int{}}
	x2.Load()
	if h := x2.Search(tokenize("quota"), 10); len(h) != 3 {
		t.Fatalf("after load: %+v", h)
	}
	// A new scan drops the deleted file.
	if _, err := x2.Scan(context.Background(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if h := x2.Search(tokenize("activé"), 10); len(h) != 0 {
		t.Fatalf("deleted file still indexed: %+v", h)
	}
}

func TestScanIncremental(t *testing.T) {
	dir := t.TempDir()
	x := &Index{docs: map[string]*doc{}, inv: map[string]map[string]int{}}
	root := filepath.Join(dir, "docs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "a.txt")
	if err := os.WriteFile(p, []byte("first content here"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Scan(context.Background(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	// Same file, unchanged: nothing to do, same results.
	if _, err := x.Scan(context.Background(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if h := x.Search(tokenize("first"), 10); len(h) != 1 {
		t.Fatalf("unchanged: %+v", h)
	}
	// Changed content: re-indexed.
	if err := os.WriteFile(p, []byte("second content now"), 0o640); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Scan(context.Background(), []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	if h := x.Search(tokenize("second"), 10); len(h) != 1 {
		t.Fatalf("updated: %+v", h)
	}
	if h := x.Search(tokenize("first"), 10); len(h) != 0 {
		t.Fatalf("old content kept: %+v", h)
	}
}

func TestScanCancel(t *testing.T) {
	x := &Index{docs: map[string]*doc{}, inv: map[string]map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.Scan(ctx, []string{t.TempDir()}, nil); err == nil {
		t.Fatal("cancelled scan must return the context error")
	}
}

// buildDocx writes a minimal .docx (zip + word/document.xml).
func buildDocx(t *testing.T, path, text string) {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="x"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestExtractOffice(t *testing.T) {
	dir := t.TempDir()
	docx := filepath.Join(dir, "d.docx")
	buildDocx(t, docx, "Le rapport trimestriel mentionne le quota btrfs.")
	text, err := extract(docx, ".docx", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "rapport trimestriel") {
		t.Fatalf("docx text %q", text)
	}
	// ODT: content.xml.
	odt := filepath.Join(dir, "d.odt")
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("content.xml")
	w.Write([]byte(`<?xml version="1.0"?><office:document-content xmlns:office="x"><office:body><office:text><text:p>Contenu open document</text:p></office:text></office:body></office:document-content>`))
	z.Close()
	if err := os.WriteFile(odt, buf.Bytes(), 0o640); err != nil {
		t.Fatal(err)
	}
	if text, err = extract(odt, ".odt", 0); err != nil || !strings.Contains(text, "open document") {
		t.Fatalf("odt: %q %v", text, err)
	}
	// Corrupted zip: error, no panic.
	if err := os.WriteFile(filepath.Join(dir, "bad.docx"), []byte("not a zip"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := extract(filepath.Join(dir, "bad.docx"), ".docx", 0); err == nil {
		t.Fatal("corrupted docx must fail")
	}
	// Binary plain-text file rejected.
	bin := filepath.Join(dir, "b.log")
	if err := os.WriteFile(bin, bytes.Repeat([]byte{0x00, 0x01}, 64), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := extract(bin, ".log", 0); err != errBinary {
		t.Fatalf("binary: %v", err)
	}
}
