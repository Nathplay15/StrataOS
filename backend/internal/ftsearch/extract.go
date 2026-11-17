// Text extraction for the full-text index: plain text and code are read
// directly, Office and OpenDocument files are unzipped and their XML text is
// collected, PDFs are parsed with a pure-Go reader (in a helper process when
// the daemon set one up: a malformed PDF can crash or exhaust the parser),
// images and scanned PDFs go through OCR when the component is installed.
// Extraction always works on a file already opened by the scan, never on a
// path, so a file swapped for a symbolic link cannot make the root daemon read
// elsewhere. Everything is capped so a single file can never slow the scan or
// grow the index without bound.

package ftsearch

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	pdflib "github.com/ledongthuc/pdf"
)

// plainExt: files whose bytes are the text itself.
var plainExt = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".log": true, ".csv": true, ".tsv": true,
	".json": true, ".yml": true, ".yaml": true, ".toml": true, ".ini": true, ".conf": true, ".cfg": true,
	".xml": true, ".html": true, ".htm": true, ".css": true, ".sql": true,
	".sh": true, ".bash": true, ".py": true, ".js": true, ".mjs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".go": true, ".rs": true, ".java": true, ".kt": true, ".kts": true, ".c": true, ".h": true, ".cpp": true,
	".hpp": true, ".cs": true, ".rb": true, ".php": true, ".lua": true, ".pl": true, ".r": true, ".swift": true,
	".dart": true, ".vue": true, ".svelte": true, ".gradle": true,
	".properties": true, ".env": true, ".tex": true, ".rtf": true,
	".srt": true, ".vtt": true, ".nfo": true,
}

// officeExt: zip archives whose text lives in XML parts.
var officeExt = map[string]bool{
	".docx": true, ".pptx": true, ".odt": true, ".ods": true, ".odp": true,
}

// One PDF parse at most (helper process).
var maxPdfRun = 2 * time.Minute

const (
	// Memory of the PDF helper process.
	maxPdfMemory = "768M"
	// Uncompressed size of one Office XML part read at most.
	maxOfficePart = 64 << 20
)

// supported reports whether a file type is indexed.
func supported(ext string) bool {
	return plainExt[ext] || officeExt[ext] || ext == ".pdf" || imageExt[ext]
}

// extract reads the text of an open regular file (path is where the scan
// found it, for the OCR cache), capped to maxTextBytes.
// A panic of a parser is turned into an error: the file is then skipped.
func extract(f *os.File, path, ext string, info fs.FileInfo) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("extractor crashed: %v", r)
		}
	}()
	switch {
	case plainExt[ext]:
		return readCapped(f)
	case officeExt[ext]:
		return officeText(f, info.Size(), ext)
	case ext == ".pdf":
		return pdfTextAny(f, path, info)
	case imageExt[ext]:
		return ocrImage(f, path, info)
	}
	return "", nil
}

// readCapped reads at most maxTextBytes of a file as UTF-8 text. Binary
// content (NUL byte) is rejected: it would only pollute the index.
func readCapped(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxTextBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxTextBytes {
		b = b[:maxTextBytes]
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return "", errBinary
	}
	if !utf8.Valid(b) {
		return "", errBinary
	}
	return string(b), nil
}

type extractError string

func (e extractError) Error() string { return string(e) }

const errBinary = extractError("binary file")

// officeText collects the character data of the XML parts holding the text of
// an Office or OpenDocument file.
func officeText(f io.ReaderAt, size int64, ext string) (string, error) {
	z, err := zip.NewReader(f, size)
	if err != nil {
		return "", err
	}
	var parts []string
	switch ext {
	case ".docx":
		parts = []string{"word/document.xml"}
	case ".pptx":
		for _, f := range z.File {
			if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
				parts = append(parts, f.Name)
			}
		}
	default: // .odt, .ods, .odp
		parts = []string{"content.xml"}
	}
	var out strings.Builder
	for _, name := range parts {
		for _, f := range z.File {
			if f.Name != name {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			n, err := xmlText(io.LimitReader(rc, maxOfficePart), &out)
			rc.Close()
			if err != nil {
				return "", err
			}
			if out.Len() > maxTextBytes || n > maxTextBytes {
				break
			}
		}
		if out.Len() > maxTextBytes {
			break
		}
	}
	return out.String(), nil
}

// xmlText writes the character data of an XML stream to out and returns how
// many bytes were written.
func xmlText(r io.Reader, out *strings.Builder) (int, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	before := out.Len()
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			// EOF, or a malformed part: the text collected so far is usable.
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			// Spreadsheet cells are separated by nothing in the XML: add a
			// space so words of adjacent cells do not merge.
			if t.Name.Local == "p" || t.Name.Local == "tab" || t.Name.Local == "br" {
				out.WriteByte(' ')
			}
		case xml.EndElement:
			if depth > 0 {
				depth--
			}
			if t.Name.Local == "p" || t.Name.Local == "br" {
				out.WriteByte(' ')
			}
		case xml.CharData:
			out.Write(t)
		}
		if out.Len()-before > maxTextBytes {
			break
		}
	}
	return out.Len() - before, nil
}

// truncate caps extracted text to maxTextBytes on a rune boundary.
func truncate(s string) string {
	if len(s) <= maxTextBytes {
		return s
	}
	cut := s[:maxTextBytes]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// helperExe is the program run as "<exe> ftsearch-pdf" to parse PDFs out of
// the daemon (SetHelper). Empty: PDFs are parsed in-process (tests).
var helperExe string

// HelperArg is the first argument that makes the strata binary run HelperMain.
const HelperArg = "ftsearch-pdf"

// SetHelper makes PDF parsing run in a separate, limited process: a malformed
// PDF can panic, recurse without end or exhaust memory in the parser, which
// must never take the root daemon down.
func SetHelper(exe string) { helperExe = exe }

// HelperMain is the PDF helper process: the PDF on stdin, its text on stdout.
func HelperMain() {
	debug.SetMaxStack(64 << 20)
	debug.SetMemoryLimit(512 << 20)
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "parser crashed:", r)
			os.Exit(1)
		}
	}()
	info, err := os.Stdin.Stat()
	if err != nil || !info.Mode().IsRegular() {
		fmt.Fprintln(os.Stderr, "ftsearch-pdf: stdin must be a regular file")
		os.Exit(2)
	}
	text, err := pdfParse(os.Stdin, info.Size())
	if err != nil {
		fmt.Fprintln(os.Stderr, "ftsearch-pdf:", err)
		os.Exit(1)
	}
	_, _ = os.Stdout.WriteString(text)
}

// pdfParse extracts the plain text of a PDF.
func pdfParse(f io.ReaderAt, size int64) (string, error) {
	r, err := pdflib.NewReader(f, size)
	if err != nil {
		return "", err
	}
	text, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(text, maxTextBytes+utf8.UTFMax))
	if err != nil {
		return "", err
	}
	return truncate(string(b)), nil
}

// pdfText extracts the plain text of a PDF, through the helper process when
// there is one.
func pdfText(f *os.File, info fs.FileInfo) (string, error) {
	if helperExe == "" {
		return pdfParse(f, info.Size())
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxPdfRun)
	defer cancel()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	cmd := limitedCommand(ctx, maxPdfMemory, helperExe, HelperArg)
	cmd.Stdin = f // the open file itself: the helper seeks in it
	var out, errb bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, n: maxTextBytes + utf8.UTFMax}
	cmd.Stderr = &limitedWriter{w: &errb, n: 4096}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", extractError("PDF parsing timed out")
		}
		msg, _, _ := strings.Cut(strings.TrimSpace(errb.String()), "\n")
		return "", fmt.Errorf("PDF parsing failed: %v: %s", err, msg)
	}
	return truncate(out.String()), nil
}

// limitedWriter keeps the first n bytes written and drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// limitedCommand runs a command at the lowest CPU and disk priority, with a
// memory cap: in its own systemd scope (MemoryMax, one CPU at most) when the
// daemon runs as root under systemd, otherwise with an address-space limit.
func limitedCommand(ctx context.Context, memory, name string, args ...string) *exec.Cmd {
	_, errRun := exec.LookPath("systemd-run")
	_, errSd := os.Stat("/run/systemd/system")
	_, errPr := exec.LookPath("prlimit")
	argv := limitArgv(os.Geteuid() == 0 && errRun == nil && errSd == nil, errPr == nil, memory, name, args...)
	return exec.CommandContext(ctx, argv[0], argv[1:]...)
}

// limitArgv builds the command line of limitedCommand.
func limitArgv(systemd, prlimit bool, memory, name string, args ...string) []string {
	switch {
	case systemd:
		return append([]string{"systemd-run", "--scope", "--quiet", "--collect",
			"-p", "MemoryMax=" + memory, "-p", "MemorySwapMax=0", "-p", "CPUQuota=100%",
			"--nice=19", "-p", "IOSchedulingClass=idle", "--", name}, args...)
	case prlimit:
		// Address space, not resident memory: give four times the budget
		// (thread stacks and the Go runtime reserve more than they use).
		return append([]string{"prlimit", "--as=" + scaleSize(memory, 4), "--", "nice", "-n", "19", name}, args...)
	}
	return append([]string{"nice", "-n", "19", name}, args...)
}

// scaleSize multiplies a size written like "768M" or "2G" into bytes.
func scaleSize(s string, k int64) string {
	var n, unit int64 = 0, 1
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			n = n*10 + int64(c-'0')
		case c == 'K':
			unit = 1 << 10
		case c == 'M':
			unit = 1 << 20
		case c == 'G':
			unit = 1 << 30
		}
	}
	return fmt.Sprint(n * unit * k)
}

// pdfNeedsOCR tells whether a PDF text layer is too thin: it is a scan.
func pdfNeedsOCR(text string) bool {
	return len(strings.Fields(text)) < minPdfWords
}

// pdfTextAny falls back to OCR when a PDF holds (almost) no text.
func pdfTextAny(f *os.File, path string, info fs.FileInfo) (string, error) {
	text, err := pdfText(f, info)
	if err != nil {
		return "", err
	}
	if !pdfNeedsOCR(text) || !ocrAvailable() || !ocrState.raster {
		return text, nil
	}
	ocrText, err := ocrCached(f, path, info, ocrPdf)
	if err != nil {
		return text, nil // the digital text is better than nothing
	}
	if len(strings.Fields(ocrText)) > len(strings.Fields(text)) {
		return ocrText, nil
	}
	return text, nil
}
