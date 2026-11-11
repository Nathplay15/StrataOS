// Package docsearch indexes the text inside the documents of the folders an
// administrator chooses (PDF, Office and OpenDocument files, text and code,
// e-mails, and pictures through OCR) into an SQLite FTS5 database, kept up to
// date from the change journal of each folder, and answers searches with
// highlighted snippets. Rights are not stored in the index: the caller passes
// the folders the user may read and only hits below them come back.
package docsearch

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"encoding/base64"
	"net/mail"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/htmlindex"

	"strataos/internal/sys"
)

// Kinds of documents (the type filter of the search).
const (
	KindPDF    = "pdf"
	KindDoc    = "doc"
	KindSheet  = "sheet"
	KindSlides = "slides"
	KindText   = "text"
	KindCode   = "code"
	KindMail   = "mail"
	KindImage  = "image"
)

var kinds = map[string]string{
	".pdf":  KindPDF,
	".docx": KindDoc, ".docm": KindDoc, ".odt": KindDoc,
	".xlsx": KindSheet, ".xlsm": KindSheet, ".ods": KindSheet, ".csv": KindSheet, ".tsv": KindSheet,
	".pptx": KindSlides, ".pptm": KindSlides, ".odp": KindSlides,
	".txt": KindText, ".md": KindText, ".markdown": KindText, ".rst": KindText, ".org": KindText, ".tex": KindText,
	".log": KindText, ".html": KindText, ".htm": KindText, ".srt": KindText, ".vtt": KindText, ".adoc": KindText,
	".eml": KindMail,
	".jpg": KindImage, ".jpeg": KindImage, ".png": KindImage, ".tif": KindImage, ".tiff": KindImage, ".webp": KindImage, ".bmp": KindImage,
}

func init() {
	for _, e := range strings.Fields(".go .py .js .mjs .cjs .ts .tsx .jsx .c .h .cpp .hpp .cc .cs .java .kt .kts .rs .rb .php .sh .bash .zsh .fish .ps1 .bat .sql .css .scss .less .json .yaml .yml .toml .ini .conf .cfg .xml .swift .lua .pl .r .vue .svelte .dart .scala .ex .exs .erl .hs .ml .clj .gradle .properties .proto .graphql .cmake .mk .dockerfile .tf .nix .zig .jl .m") {
		kinds[e] = KindCode
	}
}

// KindOf returns the kind of a file name, "" when it is not indexed.
func KindOf(name string) string {
	base := strings.ToLower(path.Base(name))
	switch base {
	case "makefile", "dockerfile", "readme", "license", "changelog":
		return KindText
	}
	return kinds[path.Ext(base)]
}

// Kinds lists the kinds, for the type filter.
func Kinds() []string {
	return []string{KindPDF, KindDoc, KindSheet, KindSlides, KindText, KindCode, KindMail, KindImage}
}

// Limits of the extraction.
const (
	MaxText      = 1 << 20 // characters of text kept per document
	maxPlain     = 8 << 20 // bytes read from a text file
	maxFileSize  = 200 << 20
	maxImageSize = 40 << 20
	maxZipPart   = 64 << 20 // uncompressed bytes read from one member of an Office file
	toolTimeout  = 2 * time.Minute
)

// ErrSkipped is returned for files that are not indexed (too large, OCR off,
// no tool): they are listed without content.
var ErrSkipped = errors.New("not indexed")

// Tools tells which optional extractors can run.
type Tools struct {
	PDF bool // pdftotext (poppler-utils)
	OCR bool // tesseract, and OCR enabled by the administrator
	// OCRLangs: tesseract languages ("eng+fra"); "" = eng.
	OCRLangs string
}

// Available reports the installed tools (OCR as installed, not as enabled).
func Available() Tools {
	return Tools{PDF: sys.Has("pdftotext"), OCR: sys.Has("tesseract")}
}

// Extract returns the text of the file f (name gives its type). It never
// returns more than MaxText characters.
func Extract(ctx context.Context, f *os.File, name string, size int64, t Tools) (string, error) {
	kind := KindOf(name)
	ext := strings.ToLower(path.Ext(name))
	if kind == "" {
		return "", ErrSkipped
	}
	if size > maxFileSize || (kind == KindImage && size > maxImageSize) {
		return "", fmt.Errorf("%w: file too large", ErrSkipped)
	}
	var text string
	var err error
	switch {
	case kind == KindPDF:
		if !t.PDF {
			return "", fmt.Errorf("%w: pdftotext is not installed", ErrSkipped)
		}
		text, err = runTool(ctx, f, "pdftotext", "-q", "-enc", "UTF-8", "-", "-")
	case kind == KindImage:
		if !t.OCR {
			return "", fmt.Errorf("%w: OCR is off", ErrSkipped)
		}
		langs := t.OCRLangs
		if langs == "" {
			langs = "eng"
		}
		text, err = runTool(ctx, f, "tesseract", "stdin", "stdout", "-l", langs, "--psm", "3")
	case kind == KindMail:
		text, err = mailText(io.LimitReader(f, 64<<20))
	case ext == ".docx" || ext == ".docm" || ext == ".xlsx" || ext == ".xlsm" || ext == ".pptx" || ext == ".pptm" || ext == ".odt" || ext == ".ods" || ext == ".odp":
		text, err = officeText(f, size, ext)
	case ext == ".html" || ext == ".htm":
		b, rerr := io.ReadAll(io.LimitReader(f, maxPlain))
		if rerr != nil {
			return "", rerr
		}
		text = htmlText(string(toUTF8(b, "")))
	default:
		b, rerr := io.ReadAll(io.LimitReader(f, maxPlain))
		if rerr != nil {
			return "", rerr
		}
		if bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 {
			return "", fmt.Errorf("%w: binary file", ErrSkipped)
		}
		text = string(toUTF8(b, ""))
	}
	if err != nil {
		return "", err
	}
	return clip(normalize(text), MaxText), nil
}

var spaces = regexp.MustCompile(`[ \t\r\f\v]+`)
var blankLines = regexp.MustCompile(`\n{3,}`)

// normalize collapses runs of spaces and blank lines (PDF layouts are full of them).
func normalize(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\x00", "")
	s = spaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(blankLines.ReplaceAllString(s, "\n\n"))
}

// clip cuts s to n characters at most.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for k := range s {
		if i == n {
			return s[:k]
		}
		i++
	}
	return s
}

// toUTF8 converts text in charset (or guessed: UTF-8, else Windows-1252).
func toUTF8(b []byte, charset string) []byte {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if charset != "" && !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "us-ascii") {
		if enc, err := htmlindex.Get(charset); err == nil {
			if out, err := enc.NewDecoder().Bytes(b); err == nil {
				return out
			}
		}
	}
	if utf8.Valid(b) {
		return b
	}
	if bytes.HasPrefix(b, []byte("\xff\xfe")) || bytes.HasPrefix(b, []byte("\xfe\xff")) {
		if enc, err := htmlindex.Get("utf-16"); err == nil {
			if out, err := enc.NewDecoder().Bytes(b); err == nil {
				return out
			}
		}
	}
	enc, _ := htmlindex.Get("windows-1252")
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return []byte(strings.ToValidUTF8(string(b), " "))
	}
	return out
}

// htmlText keeps the visible text of an HTML document.
func htmlText(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken:
			n, _ := z.TagName()
			switch string(n) {
			case "script", "style", "head", "noscript":
				skip++
			case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6":
				b.WriteByte('\n')
			}
		case html.EndTagToken:
			n, _ := z.TagName()
			switch string(n) {
			case "script", "style", "head", "noscript":
				if skip > 0 {
					skip--
				}
			case "td", "th":
				b.WriteByte(' ')
			}
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		}
		if b.Len() > MaxText*2 {
			return b.String()
		}
	}
}

// ---- Office Open XML and OpenDocument ----

// officeText reads the text parts of a zipped Office document.
func officeText(f *os.File, size int64, ext string) (string, error) {
	z, err := zip.NewReader(f, size)
	if err != nil {
		return "", fmt.Errorf("not a valid document: %w", err)
	}
	var parts []*zip.File
	numbered := func(prefix string) {
		var list []*zip.File
		for _, zf := range z.File {
			if strings.HasPrefix(zf.Name, prefix) && strings.HasSuffix(zf.Name, ".xml") && !strings.Contains(zf.Name[len(prefix):], "/") {
				list = append(list, zf)
			}
		}
		// slide2 before slide10.
		sort.Slice(list, func(i, j int) bool {
			a, b := list[i].Name, list[j].Name
			if len(a) != len(b) {
				return len(a) < len(b)
			}
			return a < b
		})
		parts = append(parts, list...)
	}
	byName := func(names ...string) {
		for _, n := range names {
			for _, zf := range z.File {
				if zf.Name == n {
					parts = append(parts, zf)
				}
			}
		}
	}
	odf := false
	switch ext {
	case ".docx", ".docm":
		byName("word/document.xml", "word/footnotes.xml", "word/endnotes.xml", "word/comments.xml")
		numbered("word/header")
		numbered("word/footer")
	case ".xlsx", ".xlsm":
		byName("xl/workbook.xml", "xl/sharedStrings.xml")
		numbered("xl/worksheets/sheet") // inline strings
	case ".pptx", ".pptm":
		numbered("ppt/slides/slide")
		numbered("ppt/notesSlides/notesSlide")
	default:
		odf = true
		byName("content.xml", "meta.xml")
	}
	if len(parts) == 0 {
		return "", errors.New("not a valid document")
	}
	var b strings.Builder
	for i, zf := range parts {
		if i >= 2000 || b.Len() > MaxText*2 {
			break
		}
		if zf.UncompressedSize64 > maxZipPart {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		if odf {
			odfText(io.LimitReader(rc, maxZipPart), &b)
		} else {
			ooxmlText(io.LimitReader(rc, maxZipPart), &b, strings.HasPrefix(zf.Name, "xl/worksheets/"))
		}
		rc.Close()
		b.WriteString("\n")
	}
	return b.String(), nil
}

// ooxmlText appends the runs of text of an Office Open XML part: <w:t>,
// <a:t> and <t> (shared and inline strings of spreadsheets), with line
// breaks at paragraphs. In worksheets only inline strings are text (other
// values are numbers or indexes into the shared strings).
func ooxmlText(r io.Reader, b *strings.Builder, sheet bool) {
	d := xml.NewDecoder(r)
	d.Strict = false
	inText := 0
	inInline := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText++
			case "is":
				inInline++
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			case "sheet": // workbook.xml: sheet names
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						b.WriteString(a.Value + "\n")
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				if inText > 0 {
					inText--
				}
			case "is":
				if inInline > 0 {
					inInline--
				}
			case "p", "si", "tc":
				b.WriteByte('\n')
			case "c":
				if sheet {
					b.WriteByte(' ')
				}
			}
		case xml.CharData:
			if inText > 0 && (!sheet || inInline > 0) {
				b.Write(t)
			}
		}
		if b.Len() > MaxText*2 {
			return
		}
	}
}

// odfText appends the text of an OpenDocument content.xml (or meta.xml).
func odfText(r io.Reader, b *strings.Builder) {
	d := xml.NewDecoder(r)
	d.Strict = false
	inBody := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "body", "meta":
				inBody++
			case "tab", "s":
				b.WriteByte(' ')
			case "line-break":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "body", "meta":
				inBody--
			case "p", "h", "table-cell", "title", "subject", "keyword", "description":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inBody > 0 {
				b.Write(t)
			}
		}
		if b.Len() > MaxText*2 {
			return
		}
	}
}

// ---- E-mails ----

// mailText returns the headers people search for and the text of an e-mail.
func mailText(r io.Reader) (string, error) {
	m, err := mail.ReadMessage(r)
	if err != nil {
		return "", fmt.Errorf("not a valid e-mail: %w", err)
	}
	var b strings.Builder
	dec := new(mime.WordDecoder)
	dec.CharsetReader = func(charset string, in io.Reader) (io.Reader, error) {
		raw, err := io.ReadAll(io.LimitReader(in, 1<<20))
		if err != nil {
			return nil, err
		}
		return bytes.NewReader(toUTF8(raw, charset)), nil
	}
	for _, h := range []string{"Subject", "From", "To", "Cc", "Date"} {
		if v := m.Header.Get(h); v != "" {
			if d, err := dec.DecodeHeader(v); err == nil {
				v = d
			}
			b.WriteString(v + "\n")
		}
	}
	b.WriteByte('\n')
	mailPart(m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Body, &b, 0)
	return b.String(), nil
}

// mailPart appends the text of a MIME part: text/plain as is, text/html
// without tags (only when no plain alternative came first), nested parts.
func mailPart(ctype, cte string, body io.Reader, b *strings.Builder, depth int) {
	if depth > 8 || b.Len() > MaxText*2 {
		return
	}
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil || ctype == "" {
		mt, params = "text/plain", map[string]string{}
	}
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, &newlineSkipper{r: body})
	}
	switch {
	case strings.HasPrefix(mt, "multipart/"):
		mr := multipart.NewReader(body, params["boundary"])
		alt := mt == "multipart/alternative"
		for {
			p, err := mr.NextPart()
			if err != nil {
				return
			}
			pt := p.Header.Get("Content-Type")
			disp := p.Header.Get("Content-Disposition")
			if strings.HasPrefix(strings.ToLower(disp), "attachment") {
				continue
			}
			before := b.Len()
			mailPart(pt, p.Header.Get("Content-Transfer-Encoding"), p, b, depth+1)
			if alt && b.Len() > before {
				return // one alternative is enough
			}
		}
	case mt == "text/plain":
		raw, _ := io.ReadAll(io.LimitReader(body, maxPlain))
		b.Write(toUTF8(raw, params["charset"]))
		b.WriteByte('\n')
	case mt == "text/html":
		raw, _ := io.ReadAll(io.LimitReader(body, maxPlain))
		b.WriteString(htmlText(string(toUTF8(raw, params["charset"]))))
		b.WriteByte('\n')
	case mt == "message/rfc822":
		if t, err := mailText(body); err == nil {
			b.WriteString(t)
		}
	}
}

// newlineSkipper drops line breaks, which the base64 decoder refuses.
type newlineSkipper struct{ r io.Reader }

func (n *newlineSkipper) Read(p []byte) (int, error) {
	k, err := n.r.Read(p)
	j := 0
	for _, c := range p[:k] {
		if c != '\r' && c != '\n' {
			p[j] = c
			j++
		}
	}
	return j, err
}

// ---- External tools ----

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.Len(); room > 0 {
		l.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// runTool feeds f to a converter run as the sandbox user, at the lowest
// priority, and returns its output.
func runTool(ctx context.Context, f *os.File, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "nice", append([]string{"-n", "19", name}, args...)...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C.UTF-8", "OMP_THREAD_LIMIT=1"}
	cmd.Stdin = f
	out := &limitedBuffer{max: MaxText * 4}
	var stderr limitedBuffer
	stderr.max = 2000
	cmd.Stdout, cmd.Stderr = out, &stderr
	cmd.SysProcAttr = sys.Sandboxed()
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s: timed out", name)
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", name, msg)
	}
	return out.String(), nil
}
