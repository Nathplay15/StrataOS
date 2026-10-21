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

var (
	sectionRe = regexp.MustCompile(`^\s*([a-zA-Z_]+)\s*(?:"[^"]*"\s*)?\{`)
	dirsRe    = regexp.MustCompile(`^\s*#?\s*directories\s*=`)
	trustedRe = regexp.MustCompile(`^\s*#?\s*trusted_networks\s*=`)
)

func confList(vals []string) string {
	q := make([]string, len(vals))
	for i, v := range vals {
		q[i] = `"` + v + `"`
	}
	return "{ " + strings.Join(q, ", ") + " }"
}

// PatchOwnToneConf sets the music folders of the library and limits the
// JSON API and web interface to local clients (StrataOS proxies them with its
// own authentication) in the configuration written by the container on its
// first start. The rest of the file is kept as it is.
func PatchOwnToneConf(conf string, dirs []string) (string, error) {
	for _, d := range dirs {
		if !strings.HasPrefix(d, "/") || strings.ContainsAny(d, "\"\\\n\r\x00") {
			return "", errors.New("invalid music folder " + d)
		}
	}
	if len(dirs) == 0 {
		// An empty list would make OwnTone scan nothing; a folder that does
		// not exist in the container keeps the library empty instead.
		dirs = []string{"/music"}
	}
	trusted := "\ttrusted_networks = { \"localhost\" }"
	dirLine := "\tdirectories = " + confList(dirs)
	var out []string
	section := ""
	start := map[string]int{} // section → index of its opening line in out
	done := map[string]bool{}
	skipping := false // inside a multi-line value being replaced
	for _, l := range strings.Split(conf, "\n") {
		if skipping {
			if strings.Contains(l, "}") {
				skipping = false
			}
			continue
		}
		t := strings.TrimSpace(l)
		if m := sectionRe.FindStringSubmatch(l); m != nil && !strings.Contains(l, "=") {
			section = m[1]
			if _, ok := start[section]; !ok {
				start[section] = len(out)
			}
			out = append(out, l)
			continue
		}
		if t == "}" {
			section = ""
			out = append(out, l)
			continue
		}
		replace := ""
		switch {
		case section == "general" && !done["general"] && trustedRe.MatchString(l):
			replace, done["general"] = trusted, true
		case section == "library" && !done["library"] && dirsRe.MatchString(l):
			replace, done["library"] = dirLine, true
		}
		if replace == "" {
			out = append(out, l)
			continue
		}
		out = append(out, replace)
		if strings.Contains(l, "{") && !strings.Contains(l, "}") && !strings.HasPrefix(t, "#") {
			skipping = true
		}
	}
	// Settings missing from their section are added at its top, the later
	// section first so that the index of the other one stays right.
	adds := []struct{ name, line string }{{"library", dirLine}, {"general", trusted}}
	if start["general"] > start["library"] {
		adds[0], adds[1] = adds[1], adds[0]
	}
	for _, sec := range adds {
		if done[sec.name] {
			continue
		}
		i, ok := start[sec.name]
		if !ok {
			return "", errors.New("unexpected OwnTone configuration file (no " + sec.name + " section)")
		}
		out = append(out[:i+1], append([]string{sec.line}, out[i+1:]...)...)
	}
	return strings.Join(out, "\n"), nil
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
