package docsearch

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/syncjournal"
)

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, p string, files map[string]string) {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for n, d := range files {
		w, _ := z.Create(n)
		w.Write([]byte(d))
	}
	z.Close()
	f.Close()
}

func extract(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	s, err := Extract(context.Background(), f, filepath.Base(p), fi.Size(), Tools{})
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return s
}

func TestExtractors(t *testing.T) {
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "a.docx"), map[string]string{
		"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t xml:space="preserve"> lighthouse</w:t></w:r></w:p><w:p><w:r><w:t>Second</w:t></w:r></w:p></w:body></w:document>`,
		"word/header1.xml":  `<w:hdr xmlns:w="w"><w:p><w:r><w:t>Header text</w:t></w:r></w:p></w:hdr>`,
	})
	writeZip(t, filepath.Join(dir, "b.xlsx"), map[string]string{
		"xl/workbook.xml":          `<workbook><sheets><sheet name="Expenses"/></sheets></workbook>`,
		"xl/sharedStrings.xml":     `<sst><si><t>Pineapple</t></si><si><r><t>Rich</t></r><r><t>Text</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c t="s"><v>0</v></c><c t="inlineStr"><is><t>Inline cell</t></is></c><c><v>12345</v></c></row></sheetData></worksheet>`,
	})
	writeZip(t, filepath.Join(dir, "c.pptx"), map[string]string{
		"ppt/slides/slide10.xml": `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>Tenth</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide2.xml":  `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>Second slide</a:t></a:r></a:p></p:sld>`,
	})
	writeZip(t, filepath.Join(dir, "d.odt"), map[string]string{
		"content.xml": `<office:document-content xmlns:office="o" xmlns:text="t"><office:font-face-decls><x>Ignored font</x></office:font-face-decls><office:body><office:text><text:p>Tarte aux<text:s/>myrtilles</text:p><text:h>Titre</text:h></office:text></office:body></office:document-content>`,
	})
	writeFile(t, filepath.Join(dir, "e.eml"), "From: Alice <a@example.org>\r\nSubject: =?utf-8?q?R=C3=A9union?=\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=XX\r\n\r\n--XX\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nCaf=E9 at the windmill\r\n--XX\r\nContent-Type: text/html\r\n\r\n<p>HTML copy</p>\r\n--XX--\r\n")
	writeFile(t, filepath.Join(dir, "f.html"), "<html><head><title>T</title><script>var secret=1</script></head><body><p>Visible <b>words</b></p></body></html>")
	writeFile(t, filepath.Join(dir, "g.txt"), "Cr\xe8me br\xfbl\xe9e") // Windows-1252

	cases := map[string][]string{
		"a.docx": {"Hello lighthouse\nSecond", "Header text"},
		"b.xlsx": {"Expenses", "Pineapple", "RichText", "Inline cell"},
		"c.pptx": {"Second slide\n\nTenth"},
		"d.odt":  {"Tarte aux myrtilles", "Titre"},
		"e.eml":  {"Réunion", "Alice", "Café at the windmill"},
		"f.html": {"Visible words"},
		"g.txt":  {"Crème brûlée"},
	}
	for name, want := range cases {
		got := extract(t, filepath.Join(dir, name))
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q not in %q", name, w, got)
			}
		}
	}
	if got := extract(t, filepath.Join(dir, "b.xlsx")); strings.Contains(got, "12345") {
		t.Errorf("numbers of worksheets are not text: %q", got)
	}
	if got := extract(t, filepath.Join(dir, "d.odt")); strings.Contains(got, "Ignored") {
		t.Errorf("only the body of OpenDocument files: %q", got)
	}
	if got := extract(t, filepath.Join(dir, "e.eml")); strings.Contains(got, "HTML copy") {
		t.Errorf("one alternative only: %q", got)
	}
	if got := extract(t, filepath.Join(dir, "f.html")); strings.Contains(got, "secret") {
		t.Errorf("scripts are not text: %q", got)
	}

	// Binary content, missing tools.
	writeFile(t, filepath.Join(dir, "bin.txt"), "abc\x00def")
	f, _ := os.Open(filepath.Join(dir, "bin.txt"))
	defer f.Close()
	if _, err := Extract(context.Background(), f, "bin.txt", 7, Tools{}); err == nil {
		t.Error("binary text file should be skipped")
	}
	if _, err := Extract(context.Background(), f, "x.pdf", 7, Tools{}); err == nil {
		t.Error("PDF without pdftotext should be skipped")
	}
	if _, err := Extract(context.Background(), f, "x.png", 7, Tools{}); err == nil {
		t.Error("image without OCR should be skipped")
	}
}

func TestKindsAndIndexable(t *testing.T) {
	for p, want := range map[string]string{"a/b/Report.PDF": KindPDF, "x.docx": KindDoc, "main.go": KindCode, "Makefile": KindText, "x.eml": KindMail, "x.bin": ""} {
		if got := KindOf(p); got != want {
			t.Errorf("KindOf(%s) = %q, want %q", p, got, want)
		}
	}
	for p, want := range map[string]bool{"a/report.pdf": true, ".git/config.txt": false, "a/.hidden/x.md": false, "a/~$lock.docx": false, "scan.png": false, "proj/node_modules/x.js": false} {
		if got := Indexable(p, false); got != want {
			t.Errorf("Indexable(%s) = %v", p, got)
		}
	}
	if !Indexable("scan.png", true) {
		t.Error("images are indexed with OCR")
	}
}

func TestMatchExpr(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"  ":                     "",
		"tax":                    `"tax"*`,
		"tax report":             `"tax" "report"*`,
		`"annual report" 2024`:   `"annual report" "2024"*`,
		`"annual report"`:        `"annual report"`,
		`a OR b NOT c`:           `"a" "OR" "b" "NOT" "c"`,
		`x*) NEAR(y`:             `"x" "NEAR" "y"`,
		`l'été`:                  `"l" "été"*`,
		`"unclosed quote`:        `"unclosed" "quote"*`,
		`report_2024-final.docx`: `"report_2024" "final" "docx"*`,
	} {
		if got := MatchExpr(in); got != want {
			t.Errorf("MatchExpr(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSnippetParts(t *testing.T) {
	got := SnippetParts("…the \x02lighthouse\x03 is\nold \x02x\x03")
	want := []Part{{Text: "…the "}, {Text: "lighthouse", Hit: true}, {Text: " is old "}, {Text: "x", Hit: true}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func openIndex(t *testing.T) *Index {
	t.Helper()
	x, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.Close() })
	return x
}

func TestSearchRightsAndFilters(t *testing.T) {
	x := openIndex(t)
	now := time.Now().Unix()
	docs := []Doc{
		{Path: "/srv/strata/v1/Team/plan.docx", Name: "plan.docx", Kind: KindDoc, Size: 1000, MTime: now, Version: "1", Text: "The lighthouse renovation plan"},
		{Path: "/srv/strata/v1/Team/old/notes.txt", Name: "notes.txt", Kind: KindText, Size: 10, MTime: now - 400*86400, Version: "1", Text: "Lighthouse keeper notes"},
		{Path: "/srv/strata/v1/TeamPrivate/secret.pdf", Name: "secret.pdf", Kind: KindPDF, Size: 5000, MTime: now, Version: "1", Text: "The secret lighthouse"},
		{Path: "/srv/strata/v1/Home/alice/lighthouse.md", Name: "lighthouse.md", Kind: KindText, Size: 5, MTime: now, Version: "1", Text: "nothing here"},
	}
	for _, d := range docs {
		if err := x.Put(d); err != nil {
			t.Fatal(err)
		}
	}
	paths := func(r Result) string {
		var p []string
		for _, h := range r.Hits {
			p = append(p, strings.TrimPrefix(h.Path, "/srv/strata/v1/"))
		}
		return strings.Join(p, ",")
	}
	ctx := context.Background()
	// Administrator: everything, the name match first (weighted).
	r, err := x.Search(ctx, Query{Text: "lighthouse"})
	if err != nil || r.Total != 4 || !strings.HasPrefix(paths(r), "Home/alice/lighthouse.md") {
		t.Fatalf("admin: %v %d %s", err, r.Total, paths(r))
	}
	// A user of Team only: "TeamPrivate" shares the prefix but not the folder.
	r, _ = x.Search(ctx, Query{Text: "lighthouse", Roots: []string{"/srv/strata/v1/Team"}})
	if r.Total != 2 || strings.Contains(paths(r), "Private") || strings.Contains(paths(r), "alice") {
		t.Fatalf("user: %d %s", r.Total, paths(r))
	}
	// No folder at all: nothing.
	if r, _ = x.Search(ctx, Query{Text: "lighthouse", Roots: []string{}}); r.Total != 0 {
		t.Fatal("no roots should find nothing")
	}
	// Filters.
	if r, _ = x.Search(ctx, Query{Text: "lighthouse", Kinds: []string{KindPDF}}); paths(r) != "TeamPrivate/secret.pdf" {
		t.Errorf("kind: %s", paths(r))
	}
	if r, _ = x.Search(ctx, Query{Text: "lighthouse", Folder: "/srv/strata/v1/Team/old"}); paths(r) != "Team/old/notes.txt" {
		t.Errorf("folder: %s", paths(r))
	}
	if r, _ = x.Search(ctx, Query{Text: "lighthouse", To: time.Now().AddDate(-1, 0, 0)}); paths(r) != "Team/old/notes.txt" {
		t.Errorf("date: %s", paths(r))
	}
	if r, _ = x.Search(ctx, Query{Text: "lighthouse", MinSize: 100, MaxSize: 2000}); paths(r) != "Team/plan.docx" {
		t.Errorf("size: %s", paths(r))
	}
	// Prefix of the last word, accents ignored, snippet highlighted.
	if r, _ = x.Search(ctx, Query{Text: "renov"}); r.Total != 1 {
		t.Errorf("prefix: %s", paths(r))
	}
	r, _ = x.Search(ctx, Query{Text: "keeper"})
	if len(r.Hits) != 1 || !hasHit(r.Hits[0].Snippet, "keeper") {
		t.Errorf("snippet: %+v", r.Hits)
	}
	x.Put(Doc{Path: "/srv/strata/v1/Team/fr.txt", Name: "fr.txt", Kind: KindText, Version: "1", Text: "Réunion à l'école"})
	if r, _ = x.Search(ctx, Query{Text: "reunion ecole"}); r.Total != 1 {
		t.Errorf("diacritics: %s", paths(r))
	}
	// Replacing a document replaces its text; deleting a folder deletes below.
	x.Put(Doc{Path: "/srv/strata/v1/Team/plan.docx", Name: "plan.docx", Kind: KindDoc, Version: "2", Text: "Budget only"})
	if r, _ = x.Search(ctx, Query{Text: "renovation"}); r.Total != 0 {
		t.Error("old text still found")
	}
	x.Delete("/srv/strata/v1/Team")
	if r, _ = x.Search(ctx, Query{Text: "lighthouse"}); paths(r) != "Home/alice/lighthouse.md,TeamPrivate/secret.pdf" && paths(r) != "TeamPrivate/secret.pdf,Home/alice/lighthouse.md" {
		t.Errorf("after delete: %s", paths(r))
	}
	if s := x.Stats(); s.Docs != 2 || s.Bytes == 0 {
		t.Errorf("stats: %+v", s)
	}
}

func hasHit(parts []Part, w string) bool {
	for _, p := range parts {
		if p.Hit && strings.EqualFold(p.Text, w) {
			return true
		}
	}
	return false
}

func TestIndexerFollowsJournal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "docs/a.txt"), "apricot jam")
	writeFile(t, filepath.Join(root, "docs/b.md"), "banana bread")
	writeFile(t, filepath.Join(root, ".hidden/c.txt"), "cherry")
	writeFile(t, filepath.Join(root, "photo.png"), "not really a png")
	j := syncjournal.New(root)
	if _, err := j.Scan("", true); err != nil {
		t.Fatal(err)
	}
	x := openIndex(t)
	ix := &Indexer{Index: x, Tools: func() Tools { return Tools{} }}
	src := JournalSource{J: j, Root: root}
	ctx := context.Background()
	n, err := ix.Sync(ctx, root, src)
	if err != nil || n != 2 {
		t.Fatalf("first pass: %d %v", n, err)
	}
	count := func(q string) int {
		r, err := x.Search(ctx, Query{Text: q})
		if err != nil {
			t.Fatal(err)
		}
		return r.Total
	}
	if count("apricot") != 1 || count("banana") != 1 || count("cherry") != 0 {
		t.Fatal("first pass content")
	}
	// Nothing changed: nothing read again.
	if n, _ = ix.Sync(ctx, root, src); n != 0 {
		t.Errorf("unchanged pass read %d files", n)
	}
	// Change, delete, add.
	time.Sleep(10 * time.Millisecond)
	writeFile(t, filepath.Join(root, "docs/a.txt"), "apple pie and more")
	os.Remove(filepath.Join(root, "docs/b.md"))
	writeFile(t, filepath.Join(root, "new/d.txt"), "damson")
	j.Scan("", true)
	if n, _ = ix.Sync(ctx, root, src); n != 2 {
		t.Errorf("second pass read %d files", n)
	}
	if count("apricot") != 0 || count("apple") != 1 || count("banana") != 0 || count("damson") != 1 {
		t.Error("second pass content")
	}
	// A lost cursor (new journal): a full listing, deleted files swept.
	os.RemoveAll(filepath.Join(root, "new"))
	j2 := syncjournal.New(root)
	j2.Scan("", true)
	n, err = ix.Sync(ctx, root, JournalSource{J: j2, Root: root})
	if err != nil || n != 0 || count("damson") != 0 || count("apple") != 1 {
		t.Errorf("reset pass: %d %v damson=%d", n, err, count("damson"))
	}
	if p := ix.Progress(); p.Running || p.Root != root {
		t.Errorf("progress: %+v", p)
	}
}

func TestJournalSourceStaysInside(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, outside, "secret")
	os.Symlink(outside, filepath.Join(root, "link.txt"))
	if f, err := (JournalSource{Root: root}).Open("link.txt"); err == nil {
		f.Close()
		t.Error("a link out of the folder was opened")
	}
	if f, err := (JournalSource{Root: root}).Open("../x"); err == nil {
		f.Close()
		t.Error("escaped the folder")
	}
}
