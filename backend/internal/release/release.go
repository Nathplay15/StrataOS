// Package release knows StrataOS release versions and the APT suites they
// are published to.
//
// VERSION holds the Debian version of the package: 0.8.0 for a final
// release, 0.8.0~beta.1 or 0.8.0~rc.1 for a pre-release ("~" sorts before
// anything, so 0.8.0~beta.1 < 0.8.0~rc.1 < 0.8.0 for apt). Git tags cannot
// hold "~": the tag, the release notes and the files of the release page use
// "-" instead (v0.8.0-beta.1, packaging/release-notes/0.8.0-beta.1.md,
// strataos_0.8.0-beta.1_amd64.deb).
//
// Final releases go to the "stable" and "testing" suites of the APT
// repository, pre-releases to "testing" only. packaging/make-repo.sh applies
// the same rule (a version with "~" is a pre-release).
package release

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	Stable  = "stable"
	Testing = "testing"
)

var versionRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(~(alpha|beta|rc)\.[1-9][0-9]*)?$`)

// Valid checks a version as VERSION holds it.
func Valid(v string) error {
	if !versionRe.MatchString(v) {
		return fmt.Errorf("invalid version %q: expected 1.2.3, or 1.2.3~beta.1 (alpha, beta, rc) for a pre-release", v)
	}
	return nil
}

// Prerelease tells whether a (Debian) version is a pre-release.
func Prerelease(v string) bool { return strings.Contains(v, "~") }

// FileVersion is the version as tags and file names write it.
func FileVersion(v string) string { return strings.ReplaceAll(v, "~", "-") }

// Tag is the git tag of a version.
func Tag(v string) string { return "v" + FileVersion(v) }

// FromTag returns the version a tag stands for.
func FromTag(tag string) (string, error) {
	t, ok := strings.CutPrefix(tag, "v")
	if !ok || strings.Contains(t, "~") {
		return "", fmt.Errorf("invalid tag %q: expected v1.2.3 or v1.2.3-beta.1", tag)
	}
	v := strings.Replace(t, "-", "~", 1)
	if err := Valid(v); err != nil {
		return "", fmt.Errorf("invalid tag %q: expected v1.2.3 or v1.2.3-beta.1", tag)
	}
	return v, nil
}

// Suites lists the APT suites a version is published to.
func Suites(v string) []string {
	if Prerelease(v) {
		return []string{Testing}
	}
	return []string{Stable, Testing}
}

// Compare compares two Debian package versions (epoch, upstream version,
// revision) the way dpkg does: <0, 0 or >0.
func Compare(a, b string) int {
	ea, ua, ra := splitVersion(a)
	eb, ub, rb := splitVersion(b)
	if c := compareNum(ea, eb); c != 0 {
		return c
	}
	if c := compareFragment(ua, ub); c != 0 {
		return c
	}
	return compareFragment(ra, rb)
}

func splitVersion(v string) (epoch, upstream, revision string) {
	v = strings.TrimSpace(v)
	if e, rest, ok := strings.Cut(v, ":"); ok {
		epoch, v = e, rest
	}
	if i := strings.LastIndex(v, "-"); i >= 0 {
		v, revision = v[:i], v[i+1:]
	}
	return epoch, v, revision
}

// order is dpkg's weight of a non-digit character: "~" before the end of
// the string, letters before other characters.
func order(c byte) int {
	switch {
	case c == '~':
		return -1
	case unicode.IsDigit(rune(c)):
		return 0
	case unicode.IsLetter(rune(c)):
		return int(c)
	}
	return int(c) + 256
}

func compareFragment(a, b string) int {
	for a != "" || b != "" {
		// Non-digit prefixes, character by character.
		for (a != "" && !isDigit(a[0])) || (b != "" && !isDigit(b[0])) {
			var ca, cb int
			if a != "" && !isDigit(a[0]) {
				ca = order(a[0])
			}
			if b != "" && !isDigit(b[0]) {
				cb = order(b[0])
			}
			if ca != cb {
				if ca < cb {
					return -1
				}
				return 1
			}
			if a != "" && !isDigit(a[0]) {
				a = a[1:]
			}
			if b != "" && !isDigit(b[0]) {
				b = b[1:]
			}
		}
		// Then the runs of digits, as numbers.
		var da, db string
		da, a = digits(a)
		db, b = digits(b)
		if c := compareNum(da, db); c != 0 {
			return c
		}
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digits(s string) (string, string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

func compareNum(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}
