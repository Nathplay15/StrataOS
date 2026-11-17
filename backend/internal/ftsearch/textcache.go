// Saved text for search snippets: the beginning of each indexed document's
// text is kept, compressed, under Dir/text when it is indexed. Searches build
// their snippets from it, so they never open a file of a share (which a user
// may have swapped for a symbolic link) nor run a parser again.

package ftsearch

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Text kept per document for snippets.
const maxSnippetText = 64 << 10

func textDir() string { return filepath.Join(Dir, "text") }

// textName names the saved text of a path in a given state (size and
// modification time): a changed file never shows the snippet of its old text.
func textName(path string, size, mtime int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%d", path, size, mtime))
	return hex.EncodeToString(sum[:16]) + ".gz"
}

func textKey(d *doc) string { return textName(d.Path, d.Size, d.Mtime) }

// saveText keeps the beginning of a document's text, whitespace collapsed.
func saveText(d *doc, text string) {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxSnippetText {
		text = text[:maxSnippetText]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	if err := os.MkdirAll(textDir(), 0o750); err != nil {
		return
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(text))
	if zw.Close() != nil {
		return
	}
	name := filepath.Join(textDir(), textKey(d))
	if os.WriteFile(name+".tmp", buf.Bytes(), 0o640) == nil {
		_ = os.Rename(name+".tmp", name)
	}
}

// hasText tells whether the text of a document was saved.
func hasText(d *doc) bool {
	_, err := os.Stat(filepath.Join(textDir(), textKey(d)))
	return err == nil
}

// loadText returns the saved text of a document, "" when there is none.
func loadText(path string, size, mtime int64) string {
	f, err := os.Open(filepath.Join(textDir(), textName(path, size, mtime)))
	if err != nil {
		return ""
	}
	defer f.Close()
	zr, err := gzip.NewReader(io.LimitReader(f, 2*maxSnippetText))
	if err != nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(zr, maxSnippetText))
	return string(b)
}

// pruneTexts removes the saved texts of documents no longer indexed.
func pruneTexts(live map[string]bool) {
	entries, err := os.ReadDir(textDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !live[e.Name()] {
			_ = os.Remove(filepath.Join(textDir(), e.Name()))
		}
	}
}
