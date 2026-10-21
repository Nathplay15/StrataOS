// Package nvr is the built-in video recorder of the Surveillance app: camera
// stream validation, ffmpeg recording and live snapshots, segment timelines,
// retention, and the configuration of Frigate (the advanced engine).
package nvr

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	// Path and query of a stream: no quotes, braces, spaces or backslashes, so
	// the URL is safe in an ffconcat file, YAML and Frigate's {FRIGATE_} syntax.
	pathRe = regexp.MustCompile(`^[A-Za-z0-9/_.~%&=?+,;:@!$()*-]*$`)
	// credRe finds the user information of a URL in ffmpeg messages.
	credRe = regexp.MustCompile(`://[^/\s'"@]*@`)
)

// Schemes accepted for camera streams.
var schemes = map[string]bool{"rtsp": true, "rtsps": true, "http": true, "https": true}

// ParseURL validates a camera stream URL and splits the credentials off: the
// returned URL has none, user and pass are those written in it (if any).
func ParseURL(raw string) (clean, user, pass string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", errors.New("the stream URL is empty")
	}
	if len(raw) > 1024 {
		return "", "", "", errors.New("the stream URL is too long")
	}
	for _, r := range raw {
		if r < 0x21 || r > 0x7e {
			return "", "", "", errors.New("the stream URL contains spaces or invalid characters")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", errors.New("invalid stream URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if !schemes[u.Scheme] {
		return "", "", "", errors.New("the stream URL must start with rtsp://, rtsps://, http:// or https://")
	}
	if u.Opaque != "" || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", "", "", errors.New("invalid stream URL")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip == nil && !hostRe.MatchString(host) {
		return "", "", "", errors.New("invalid camera address")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", "", "", errors.New("invalid camera port")
		}
	}
	path := u.EscapedPath()
	if !pathRe.MatchString(path) || !pathRe.MatchString(u.RawQuery) {
		return "", "", "", errors.New("the stream path contains invalid characters")
	}
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	clean = u.Scheme + "://" + u.Host + path
	if u.RawQuery != "" {
		clean += "?" + u.RawQuery
	}
	return clean, user, pass, nil
}

// ValidCredential checks a user name or password typed for a camera.
func ValidCredential(s string) error {
	if len(s) > 128 {
		return errors.New("camera user name or password too long")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return errors.New("camera user name or password contains invalid characters")
		}
	}
	return nil
}

// Escape percent-encodes everything but the unreserved characters of RFC 3986:
// the result is safe inside a URL user information, an ffconcat file, YAML and
// a secret file. ffmpeg (and Frigate's go2rtc) decode it before authenticating.
func Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// WithCredentials inserts the escaped credentials into a clean stream URL.
func WithCredentials(clean, user, pass string) string {
	return withUserinfo(clean, userinfo(Escape(user), Escape(pass), user != "", pass != ""))
}

func userinfo(user, pass string, hasUser, hasPass bool) string {
	switch {
	case hasUser && hasPass:
		return user + ":" + pass
	case hasUser:
		return user
	case hasPass:
		return ":" + pass
	}
	return ""
}

func withUserinfo(clean, info string) string {
	if info == "" {
		return clean
	}
	scheme, rest, ok := strings.Cut(clean, "://")
	if !ok {
		return clean
	}
	return scheme + "://" + info + "@" + rest
}

// Redact hides the credentials of any URL found in s (ffmpeg messages).
func Redact(s string) string { return credRe.ReplaceAllString(s, "://***@") }

// Scheme returns the scheme of a clean stream URL.
func Scheme(clean string) string {
	s, _, _ := strings.Cut(clean, "://")
	return s
}

// HTTPStream reports whether a stream is fetched over HTTP (MJPEG, MPEG-TS…).
func HTTPStream(clean string) bool { s := Scheme(clean); return s == "http" || s == "https" }

// ConcatList is an ffconcat file pointing at the stream. ffmpeg opens the
// camera through it, so the URL and its credentials never appear on the
// command line (/proc/<pid>/cmdline is readable by every local user). Each
// stream only gets the network protocols of its own scheme.
func ConcatList(full string) string {
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	b.WriteString("file '" + strings.ReplaceAll(full, "'", `'\''`) + "'\n")
	switch Scheme(full) {
	case "rtsp":
		b.WriteString("option protocol_whitelist rtsp,rtp,udp,tcp\noption rtsp_transport tcp\noption timeout 10000000\n")
	case "rtsps":
		b.WriteString("option protocol_whitelist rtsps,rtsp,rtp,srtp,udp,tcp,tls\noption rtsp_transport tcp\noption timeout 10000000\n")
	case "http":
		b.WriteString("option protocol_whitelist http,tcp\noption rw_timeout 10000000\noption use_wallclock_as_timestamps 1\n")
	case "https":
		b.WriteString("option protocol_whitelist https,http,tcp,tls\noption rw_timeout 10000000\noption use_wallclock_as_timestamps 1\n")
	}
	return b.String()
}
