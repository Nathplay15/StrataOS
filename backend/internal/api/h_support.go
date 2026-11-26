package api

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// Diagnostic report (System → Updates → Support): an archive for a bug
// report, with the version, components, hardware, recent logs, services and
// disk health. Every secret is masked (redact): passwords, tokens, keys,
// hashes, Wi-Fi keys, and e-mail addresses unless asked to keep them.

var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	// Private keys and certificates' keys, whole blocks.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), "[private key]"},
	// Authorization headers and bearer tokens.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 [redacted]"},
	// key = value pairs whose name says secret (INI, env, JSON, URLs).
	{regexp.MustCompile(`(?i)("?[a-z0-9_.-]*(?:pass(?:word|wd|phrase)?|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|psk|credential|auth)[a-z0-9_.-]*"?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&}]+)`), "${1}[redacted]"},
	// Tokens of StrataOS (devices, API, kiosk) and JWTs.
	{regexp.MustCompile(`\b(stdev|stt|sthdmi)_[A-Za-z0-9_-]{8,}`), "${1}_[redacted]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), "[jwt]"},
	// Password hashes (crypt, bcrypt, argon2, Samba NT).
	{regexp.MustCompile(`\$(?:2[aby]|argon2(?:id|i|d)?|[156y])\$[^\s:"]+`), "[hash]"},
	// User info in URLs (scheme://user:password@host).
	{regexp.MustCompile(`([a-z][a-z0-9+.-]*://[^/\s:@]+):[^/\s@]+@`), "${1}:[redacted]@"},
	// Long hexadecimal or base64 strings (keys, hashes).
	{regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`), "[hex]"},
	{regexp.MustCompile(`\b[A-Za-z0-9+/]{40,}={0,2}`), "[base64]"},
}

var emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// redact masks the secrets of a text (and the e-mail addresses unless keepEmails).
func redact(s string, keepEmails bool) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}
	if !keepEmails {
		s = emailRe.ReplaceAllString(s, "[e-mail]")
	}
	return s
}

func (s *Server) supportRoutes() {
	s.adminRaw("GET /api/system/support/report", func(w http.ResponseWriter, r *http.Request) {
		keep := r.URL.Query().Get("emails") == "1"
		s.audit(r, "system", "Diagnostic report downloaded")
		name := "strataos-report-" + time.Now().Format("20060102-1504") + ".tar.gz"
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		gz := gzip.NewWriter(w)
		tw := tar.NewWriter(gz)
		add := func(file, text string) {
			b := []byte(redact(text, keep))
			_ = tw.WriteHeader(&tar.Header{Name: "strataos-report/" + file, Mode: 0o644, Size: int64(len(b)), ModTime: time.Now()})
			_, _ = tw.Write(b)
		}
		run := func(name string, args ...string) string {
			out, err := sys.Run(name, args...)
			if err != nil {
				out += "\n(" + err.Error() + ")"
			}
			return "$ " + name + " " + strings.Join(args, " ") + "\n" + out + "\n\n"
		}
		add("version.txt", "StrataOS "+s.version+"\n"+run("uname", "-a")+run("cat", "/etc/debian_version")+run("dpkg-query", "-W", "strataos"))
		if b, err := json.MarshalIndent(componentsInstalled(), "", "  "); err == nil {
			add("components.json", string(b))
		}
		add("hardware.txt", run("lscpu")+run("free", "-h")+run("lsblk", "-o", "NAME,SIZE,TYPE,FSTYPE,MODEL,TRAN,MOUNTPOINTS")+run("lspci", "-nn")+run("lsusb"))
		add("services.txt", run("systemctl", "--failed", "--no-pager")+run("systemctl", "is-active", "strata", "smbd", "nmbd", "nfs-server", "docker", "libvirtd", "ssh", "avahi-daemon"))
		disks := run("cat", "/proc/mdstat") + run("df", "-h", "-x", "tmpfs", "-x", "devtmpfs")
		if sys.Has("zpool") {
			disks += run("zpool", "status")
		}
		if sys.Has("btrfs") {
			disks += run("btrfs", "filesystem", "show")
		}
		smartCache.Range(func(k, v any) bool {
			e := v.(smartEntry)
			disks += fmt.Sprintf("%v: %s, passed %v, %d °C, %d h, warnings %v\n", k, e.model, e.info.Passed != nil && *e.info.Passed, e.info.Temperature, e.info.PowerOnHours, e.info.Warnings)
			return true
		})
		add("disks.txt", disks)
		add("journal.txt", run("journalctl", "-u", "strata", "-n", "3000", "--no-pager", "-o", "short-iso"))
		var ev strings.Builder
		s.store.Read(func(st *store.State) {
			from := max(0, len(st.Events)-1000)
			for _, e := range st.Events[from:] {
				fmt.Fprintf(&ev, "%s %s %s %s %s\n", e.Time.Format(time.RFC3339), e.Level, e.Category, e.User, e.Message)
			}
		})
		add("events.txt", ev.String())
		_ = tw.Close()
		_ = gz.Close()
	})
}
