package engine

import (
	"fmt"
	"path"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// namer turns paths into comparison keys: on case-insensitive file systems
// (Windows, macOS by default) "Report.txt" and "report.txt" are one file,
// and macOS also treats the composed and decomposed forms of "é" as one.
type namer struct {
	fold bool // case-insensitive
	nfc  bool // normalization-insensitive
}

func (n namer) key(p string) string {
	if n.nfc {
		p = norm.NFC.String(p)
	}
	if n.fold {
		p = strings.ToLower(p)
	}
	return p
}

// DefaultIgnore are the names never synchronized: files of file browsers,
// office lock and temporary files.
var DefaultIgnore = []string{".DS_Store", "Thumbs.db", "desktop.ini", "~$*", "*.tmp", ".~lock*", "._*", ".Spotlight-V100", ".Trashes", ".fseventsd", "$RECYCLE.BIN", "System Volume Information", ".strataos-sync*", ".strata-upload-*"}

// matcher decides which paths are left out: ignore patterns (on the name,
// or on the whole path when the pattern has a "/") and excluded subfolders.
type matcher struct {
	n        namer
	patterns []string
	exclude  []string // keys
}

func newMatcher(n namer, patterns, exclude []string) matcher {
	m := matcher{n: n}
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" {
			if n.fold {
				p = strings.ToLower(p)
			}
			m.patterns = append(m.patterns, p)
		}
	}
	for _, e := range exclude {
		if e = strings.Trim(e, "/"); e != "" {
			m.exclude = append(m.exclude, n.key(e))
		}
	}
	return m
}

// ignored tells whether rel (a slash path) is left out.
func (m matcher) ignored(rel string) bool {
	name := path.Base(rel)
	if strings.HasPrefix(name, ".strataos-sync") || strings.HasPrefix(name, ".strata-upload-") {
		return true // our own temporary files, whatever the settings
	}
	full := rel
	if m.n.fold {
		name, full = strings.ToLower(name), strings.ToLower(full)
	}
	for _, p := range m.patterns {
		target := name
		if strings.Contains(p, "/") {
			target = full
		}
		if ok, _ := path.Match(p, target); ok {
			return true
		}
	}
	k := m.n.key(rel)
	for _, e := range m.exclude {
		if k == e || strings.HasPrefix(k, e+"/") {
			return true
		}
	}
	return false
}

// localNameProblem explains why a NAS name cannot exist on this computer ("" = fine).
func localNameProblem(name string, goos string) string {
	if !utf8.ValidString(name) {
		return "invalid characters"
	}
	if goos != "windows" {
		return ""
	}
	if strings.ContainsAny(name, `<>:"|?*\`) {
		return `the name contains a character Windows refuses (< > : " | ? * \)`
	}
	for _, r := range name {
		if r < 0x20 {
			return "the name contains a control character"
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "Windows refuses names ending with a dot or a space"
	}
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "the name is reserved by Windows"
	}
	return ""
}

// remoteNameProblem explains why a local name cannot be stored on the NAS ("" = fine).
func remoteNameProblem(name string) string {
	switch {
	case !utf8.ValidString(name):
		return "invalid characters"
	case strings.Contains(name, `\`):
		return `the NAS refuses names with "\"`
	case len(name) > 255:
		return "the name is too long"
	case strings.HasPrefix(name, ".strata-upload-"):
		return "reserved name"
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "the name contains a control character"
		}
	}
	return ""
}

// pathProblem checks every part of a path with check.
func pathProblem(rel string, check func(string) string) string {
	for _, part := range strings.Split(rel, "/") {
		if p := check(part); p != "" {
			return p
		}
	}
	return ""
}

func localProblem(rel string) string {
	return pathProblem(rel, func(n string) string { return localNameProblem(n, runtime.GOOS) })
}

// safeDevice makes a device name usable inside a file name.
func safeDevice(d string) string {
	d = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 0x20 {
			return '-'
		}
		return r
	}, strings.TrimSpace(d))
	if d == "" {
		d = "computer"
	}
	if utf8.RuneCountInString(d) > 40 {
		d = string([]rune(d)[:40])
	}
	return d
}

// conflictName names the copy kept when both sides changed a file:
// "report (conflict from Laptop 2024-05-01 10-30).docx", n > 1 adding " n".
func conflictName(name, device string, t time.Time, n int) string {
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" || strings.ContainsRune(ext, ' ') || len(ext) > 12 {
		base, ext = name, "" // ".bashrc", or no real extension
	}
	suffix := ""
	if n > 1 {
		suffix = fmt.Sprintf(" %d", n)
	}
	return fmt.Sprintf("%s (conflict from %s %s%s)%s", base, safeDevice(device), t.Format("2006-01-02 15-04"), suffix, ext)
}

func parentOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// isUnder tells whether key k is strictly below folder key d.
func isUnder(k, d string) bool { return strings.HasPrefix(k, d+"/") }
