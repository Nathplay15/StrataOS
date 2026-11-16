// Text extraction for the full-text index: plain text and code are read
// directly, Office and OpenDocument files are unzipped and their XML text is
// collected, PDFs are parsed with a pure-Go reader, images and scanned PDFs
// go through OCR when the component is installed. Everything is capped so a
// single file can never slow the scan or grow the index without bound.

package ftsearch

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"strings"
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

// supported reports whether a file type is indexed.
func supported(ext string) bool {
	return plainExt[ext] || officeExt[ext] || ext == ".pdf" || imageExt[ext]
}

// extract reads the text of a file, capped to maxTextBytes.
func extract(path, ext string, size int64) (string, error) {
	switch {
	case plainExt[ext]:
		return readCapped(path)
	case officeExt[ext]:
		return officeText(path, ext)
	case ext == ".pdf":
		return pdfTextAny(path)
	case imageExt[ext]:
		return ocrImage(path)
	}
	return "", nil
}

// readCapped reads at most maxTextBytes of a file as UTF-8 text. Binary
// content (NUL byte) is rejected: it would only pollute the index.
func readCapped(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTextBytes+1))
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
func officeText(path, ext string) (string, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer z.Close()
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
			n, err := xmlText(rc, &out)
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
	dec := xml.NewDecoder(io.LimitReader(r, maxTextBytes))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	before := out.Len()
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			// A malformed part still leaves the text collected so far usable.
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

// pdfText extracts the plain text of a PDF.
func pdfText(path string) (string, error) {
	f, r, err := pdflib.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
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

// pdfNeedsOCR tells whether a PDF text layer is too thin: it is a scan.
func pdfNeedsOCR(text string) bool {
	return len(strings.Fields(text)) < minPdfWords
}

// pdfTextAny falls back to OCR when a PDF holds (almost) no text.
func pdfTextAny(path string) (string, error) {
	text, err := pdfText(path)
	if err != nil {
		return "", err
	}
	if !pdfNeedsOCR(text) || !ocrAvailable() || !ocrState.raster {
		return text, nil
	}
	ocrText, err := ocrCached(path, ocrPdf)
	if err != nil {
		return text, nil // the digital text is better than nothing
	}
	if len(strings.Fields(ocrText)) > len(strings.Fields(text)) {
		return ocrText, nil
	}
	return text, nil
}
