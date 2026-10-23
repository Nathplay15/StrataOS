package system

// APT sources for the update channel and the security-only updates.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"strataos/internal/release"
	"strataos/internal/sys"
)

const (
	// StrataSources is the repository of the package (a dpkg conffile, never
	// edited: dpkg would ask about it at each update).
	StrataSources = "/etc/apt/sources.list.d/strataos.sources"
	// BetaSources adds the "testing" suite of the same repository. StrataOS
	// writes and removes it (Updates → Update channel); with both files, apt
	// takes the newest version of either suite, and testing holds every release.
	BetaSources = "/etc/apt/sources.list.d/strataos-beta.sources"
	// SecurityDir holds the sources and package lists of the security-only
	// updates, apart from the system ones (readable by apt's _apt user).
	SecurityDir = "/var/cache/strata-apt-security"
)

// stanza is one paragraph of a deb822 .sources file, keys as written.
type stanza struct {
	keys   []string
	values map[string]string
}

func (s stanza) get(k string) string {
	for _, x := range s.keys {
		if strings.EqualFold(x, k) {
			return s.values[x]
		}
	}
	return ""
}

// parseDeb822 reads the paragraphs of a .sources file (comments dropped,
// continuation lines joined).
func parseDeb822(text string) []stanza {
	var out []stanza
	cur := stanza{values: map[string]string{}}
	last := ""
	flush := func() {
		if len(cur.keys) > 0 {
			out = append(out, cur)
		}
		cur, last = stanza{values: map[string]string{}}, ""
	}
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.TrimSpace(l) == "":
			flush()
		case strings.HasPrefix(l, "#"):
		case l[0] == ' ' || l[0] == '\t':
			if last != "" {
				cur.values[last] += " " + strings.TrimSpace(l)
			}
		default:
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			if _, dup := cur.values[k]; !dup {
				cur.keys = append(cur.keys, k)
			}
			cur.values[k], last = strings.TrimSpace(v), k
		}
	}
	flush()
	return out
}

var (
	sourceValueRe = regexp.MustCompile(`^[A-Za-z0-9:/._~+@%=,-]+$`)
	suiteRe       = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// safeValue keeps a value of a generated sources file to one plain line.
func safeValue(v string) bool {
	for _, f := range strings.Fields(v) {
		if !sourceValueRe.MatchString(f) {
			return false
		}
	}
	return v != ""
}

// BetaSourcesFor writes the beta channel's sources from the package's own:
// the same repository and key, suite "testing".
func BetaSourcesFor(main string) (string, error) {
	for _, s := range parseDeb822(main) {
		if !slices.Contains(strings.Fields(s.get("Types")), "deb") || strings.EqualFold(s.get("Enabled"), "no") {
			continue
		}
		uris, comps, key := s.get("URIs"), s.get("Components"), s.get("Signed-By")
		if !safeValue(uris) || !safeValue(comps) || (key != "" && !safeValue(key)) {
			return "", fmt.Errorf("unexpected content in %s", StrataSources)
		}
		var b strings.Builder
		b.WriteString("# StrataOS beta channel (pre-releases). Written by StrataOS (Updates →\n# Update channel): choose \"Stable\" there to remove it.\n")
		fmt.Fprintf(&b, "Types: deb\nURIs: %s\nSuites: %s\nComponents: %s\n", uris, release.Testing, comps)
		if key != "" {
			fmt.Fprintf(&b, "Signed-By: %s\n", key)
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("no repository in %s", StrataSources)
}

// Channel is the update channel the sources files select.
func Channel() string {
	if _, err := os.Stat(BetaSources); err == nil {
		return "beta"
	}
	return "stable"
}

// SetChannel writes or removes the beta sources.
func SetChannel(channel string) error {
	switch channel {
	case "stable":
		if err := os.Remove(BetaSources); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	case "beta":
		main, err := os.ReadFile(StrataSources)
		if err != nil {
			return fmt.Errorf("no StrataOS update repository is configured on this NAS")
		}
		text, err := BetaSourcesFor(string(main))
		if err != nil {
			return err
		}
		return sys.WriteFileAtomic(BetaSources, []byte(text), 0o644)
	}
	return fmt.Errorf("invalid update channel")
}

// isSecuritySuite: trixie-security, or the older stable/updates form.
func isSecuritySuite(s string) bool {
	return strings.HasSuffix(s, "-security") || strings.HasSuffix(s, "/updates")
}

// SecuritySourcesFor writes the sources of the security updates (deb822)
// from the system's: files maps a path to its content (one-line .list or
// deb822 .sources). Without any security entry, Debian's security archive
// for the codename is used.
func SecuritySourcesFor(files map[string]string, codename string) string {
	type entry struct{ uris, suites, comps, key, arch string }
	var entries []entry
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		text := files[p]
		if strings.HasSuffix(p, ".sources") {
			for _, s := range parseDeb822(text) {
				if !slices.Contains(strings.Fields(s.get("Types")), "deb") || strings.EqualFold(s.get("Enabled"), "no") {
					continue
				}
				var suites []string
				for _, x := range strings.Fields(s.get("Suites")) {
					if isSecuritySuite(x) {
						suites = append(suites, x)
					}
				}
				if len(suites) > 0 {
					entries = append(entries, entry{s.get("URIs"), strings.Join(suites, " "), s.get("Components"), s.get("Signed-By"), s.get("Architectures")})
				}
			}
			continue
		}
		for _, l := range strings.Split(text, "\n") {
			l, _, _ = strings.Cut(l, "#")
			f := strings.Fields(l)
			if len(f) < 3 || f[0] != "deb" {
				continue
			}
			var key, arch string
			f = f[1:]
			if strings.HasPrefix(f[0], "[") {
				// Options: [arch=amd64 signed-by=/usr/share/keyrings/x.gpg]
				i := 0
				for ; i < len(f); i++ {
					o := strings.Trim(f[i], "[]")
					if k, v, ok := strings.Cut(o, "="); ok {
						switch k {
						case "signed-by":
							key = v
						case "arch":
							arch = strings.ReplaceAll(v, ",", " ")
						}
					}
					if strings.HasSuffix(f[i], "]") {
						break
					}
				}
				f = f[min(i+1, len(f)):]
			}
			if len(f) >= 3 && isSecuritySuite(f[1]) {
				entries = append(entries, entry{f[0], f[1], strings.Join(f[2:], " "), key, arch})
			}
		}
	}
	var b strings.Builder
	b.WriteString("# Security updates only (StrataOS automatic updates). Generated: do not edit.\n")
	n := 0
	for _, e := range entries {
		if !safeValue(e.uris) || !suiteRe.MatchString(strings.ReplaceAll(e.suites, " ", "")) || !safeValue(e.comps) || (e.key != "" && !safeValue(e.key)) || (e.arch != "" && !safeValue(e.arch)) {
			continue
		}
		fmt.Fprintf(&b, "\nTypes: deb\nURIs: %s\nSuites: %s\nComponents: %s\n", e.uris, e.suites, e.comps)
		if e.key != "" {
			fmt.Fprintf(&b, "Signed-By: %s\n", e.key)
		}
		if e.arch != "" {
			fmt.Fprintf(&b, "Architectures: %s\n", e.arch)
		}
		n++
	}
	if n == 0 && suiteRe.MatchString(codename) && codename != "" {
		fmt.Fprintf(&b, "\nTypes: deb\nURIs: http://security.debian.org/debian-security\nSuites: %s-security\nComponents: main non-free-firmware\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n", codename)
	}
	return b.String()
}

// SecurityAptOptions prepares the security-only sources and returns the
// apt options that make apt see them alone, with their own package lists
// (the system's stay as they are).
func SecurityAptOptions() (opts []string, sources string, err error) {
	files := map[string]string{}
	if b, err := os.ReadFile("/etc/apt/sources.list"); err == nil {
		files["/etc/apt/sources.list"] = string(b)
	}
	for _, pat := range []string{"/etc/apt/sources.list.d/*.list", "/etc/apt/sources.list.d/*.sources"} {
		m, _ := filepath.Glob(pat)
		for _, p := range m {
			if b, err := os.ReadFile(p); err == nil {
				files[p] = string(b)
			}
		}
	}
	sources = SecuritySourcesFor(files, osCodename())
	parts, lists := filepath.Join(SecurityDir, "sources.d"), filepath.Join(SecurityDir, "lists")
	for _, d := range []string{SecurityDir, parts, lists} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, "", err
		}
	}
	if err := sys.WriteFileAtomic(filepath.Join(parts, "security.sources"), []byte(sources), 0o644); err != nil {
		return nil, "", err
	}
	return SecurityOptions(), sources, nil
}

// SecurityOptions are the apt options of the security-only updates.
func SecurityOptions() []string {
	return []string{
		"-o", "Dir::Etc::SourceList=/dev/null",
		"-o", "Dir::Etc::SourceParts=" + filepath.Join(SecurityDir, "sources.d"),
		"-o", "Dir::State::Lists=" + filepath.Join(SecurityDir, "lists"),
		"-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=",
	}
}

func osCodename() string {
	b, _ := os.ReadFile("/etc/os-release")
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "VERSION_CODENAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// MadisonVersions lists the versions `apt-cache madison` offers from a
// suite ("stable", "testing"), whatever the repository URL.
func MadisonVersions(out, suite string) []string {
	var vs []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "|")
		if len(f) != 3 {
			continue
		}
		src := strings.Fields(f[2])
		if len(src) >= 2 && strings.HasPrefix(src[1], suite+"/") {
			vs = append(vs, strings.TrimSpace(f[1]))
		}
	}
	return vs
}

// Newest returns the highest version of a list ("" when empty).
func Newest(vs []string) string {
	best := ""
	for _, v := range vs {
		if best == "" || release.Compare(v, best) > 0 {
			best = v
		}
	}
	return best
}
