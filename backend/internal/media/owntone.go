package media

import (
	"errors"
	"regexp"
	"strings"
)

// OwnToneApp is the App Center id of the music server, and OwnToneAddr the
// address of its JSON API (host networking, needed for mDNS discovery of the
// speakers).
const (
	OwnToneApp  = "owntone"
	OwnToneAddr = "127.0.0.1:3689"
)

var sectionRe = regexp.MustCompile(`^\s*([a-zA-Z_]+)\s*(?:"[^"]*"\s*)?\{`)

func confList(vals []string) string {
	q := make([]string, len(vals))
	for i, v := range vals {
		q[i] = `"` + v + `"`
	}
	return "{ " + strings.Join(q, ", ") + " }"
}

// confSetting is one value StrataOS imposes in owntone.conf.
type confSetting struct {
	section, key, value string
	required            bool // the section must exist (else it is appended)
}

// setConf replaces (or uncomments) key in section, or adds it at the top of
// the section; a multi-line value ({ … } over several lines) is replaced as
// a whole.
func setConf(lines []string, c confSetting) ([]string, bool) {
	keyRe := regexp.MustCompile(`^\s*#?\s*` + c.key + `\s*=`)
	line := "\t" + c.key + " = " + c.value
	section, start := "", -1
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if m := sectionRe.FindStringSubmatch(l); m != nil && !strings.Contains(l, "=") {
			section = m[1]
			if section == c.section && start < 0 {
				start = i
			}
			continue
		}
		if t == "}" {
			section = ""
			continue
		}
		if section != c.section || !keyRe.MatchString(l) {
			continue
		}
		end := i + 1
		if strings.Contains(l, "{") && !strings.Contains(l, "}") && !strings.HasPrefix(t, "#") {
			for end < len(lines) && !strings.Contains(lines[end-1], "}") {
				end++
			}
		}
		out := append(append(append([]string{}, lines[:i]...), line), lines[end:]...)
		return out, true
	}
	if start < 0 {
		return lines, false
	}
	out := append(append(append([]string{}, lines[:start+1]...), line), lines[start+1:]...)
	return out, true
}

// PatchOwnToneConf adapts the configuration the OwnTone container writes on
// its first start (the rest of the file is kept as it is):
//   - the library reads the chosen shared folders;
//   - the JSON API and web interface only trust local clients (the image
//     trusts "any"): StrataOS proxies them behind its own sign-in;
//   - OwnTone keeps running as root after starting, so that it can read
//     private shares (mounted read-only in the container);
//   - the MPD server, which has no authentication, is turned off.
func PatchOwnToneConf(conf string, dirs []string) (string, error) {
	for _, d := range dirs {
		if !strings.HasPrefix(d, "/") || strings.ContainsAny(d, "\"\\\n\r\x00") {
			return "", errors.New("invalid music folder " + d)
		}
	}
	if len(dirs) == 0 {
		// An empty list would be invalid; a folder missing from the container
		// keeps the library empty instead.
		dirs = []string{"/srv/media"}
	}
	lines := strings.Split(conf, "\n")
	for _, c := range []confSetting{
		{"general", "uid", `"root"`, true},
		{"general", "trusted_networks", `{ "localhost" }`, true},
		{"library", "directories", confList(dirs), true},
		{"mpd", "port", "0", false},
	} {
		var ok bool
		lines, ok = setConf(lines, c)
		if !ok {
			if c.required {
				return "", errors.New("unexpected OwnTone configuration file (no " + c.section + " section)")
			}
			lines = append(lines, c.section+" {", "\t"+c.key+" = "+c.value, "}")
		}
	}
	return strings.Join(lines, "\n"), nil
}

// OwnToneAllowed filters the OwnTone API paths reachable through the
// StrataOS proxy. Regular users may play music, edit the queue and choose
// speakers; the server settings, pairing and library changes stay with
// administrators.
func OwnToneAllowed(method, path string, admin bool) bool {
	if !strings.HasPrefix(path, "api/") && !strings.HasPrefix(path, "artwork/") {
		return false
	}
	if strings.Contains(path, "..") {
		return false
	}
	if admin {
		return true
	}
	for _, p := range []string{"api/spotify", "api/lastfm", "api/pairing", "api/verification", "api/library/backup", "api/update", "api/rescan"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return false
		}
	}
	if strings.HasPrefix(path, "api/settings") || strings.HasPrefix(path, "api/library") {
		return method == "GET"
	}
	return true
}
