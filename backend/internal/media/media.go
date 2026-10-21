// Package media manages the DLNA media server (ReadyMedia/minidlna), the
// configuration of the OwnTone music server and the signed stream URLs given
// to media receivers.
package media

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"strataos/internal/sys"
)

// ---- Signed stream URLs ----

var errBadToken = errors.New("invalid or expired link")

func streamMAC(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("strata-stream|" + payload))
	return m.Sum(nil)
}

// SignStream returns a token that lets a receiver read one file as a user
// until exp, for one cast session (receivers fetch without a session cookie).
func SignStream(key []byte, session, user, path string, exp time.Time) string {
	payload := strconv.FormatInt(exp.Unix(), 10) + "\x00" + session + "\x00" + user + "\x00" + path
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(streamMAC(key, payload))
}

// StreamGrant is what a valid token allows.
type StreamGrant struct{ Session, User, Path string }

// VerifyStream checks a token and returns what it grants.
func VerifyStream(key []byte, token string, now time.Time) (StreamGrant, error) {
	p64, s64, ok := strings.Cut(token, ".")
	if !ok {
		return StreamGrant{}, errBadToken
	}
	pb, err1 := base64.RawURLEncoding.DecodeString(p64)
	sig, err2 := base64.RawURLEncoding.DecodeString(s64)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, streamMAC(key, string(pb))) {
		return StreamGrant{}, errBadToken
	}
	parts := strings.SplitN(string(pb), "\x00", 4)
	if len(parts) != 4 {
		return StreamGrant{}, errBadToken
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || now.Unix() > exp {
		return StreamGrant{}, errBadToken
	}
	return StreamGrant{Session: parts[1], User: parts[2], Path: parts[3]}, nil
}

// ---- DLNA server (minidlna) ----

const (
	DLNAUnit     = "strata-minidlna.service"
	DLNAUnitPath = "/etc/systemd/system/" + DLNAUnit
	DLNAConfPath = "/etc/strata-minidlna.conf"
	DLNADBDir    = "/var/cache/strata-minidlna"
	DLNAPort     = 8200
)

// DLNAFolder is a folder published by the DLNA server.
type DLNAFolder struct {
	Path string
	Type string // "" (everything), video, audio, pictures
}

// DLNAConfig is what MiniDLNAConf needs.
type DLNAConfig struct {
	Name      string
	Interface string
	Folders   []DLNAFolder
}

var ifaceRe = regexp.MustCompile(`^[a-zA-Z0-9_.@-]{1,15}$`)

// ValidName checks the name TVs show for the server.
func ValidName(n string) error {
	if n == "" || len(n) > 64 {
		return errors.New("the server name must have 1 to 64 characters")
	}
	for _, r := range n {
		if !unicode.IsPrint(r) {
			return errors.New("the server name contains forbidden characters")
		}
	}
	return nil
}

// ValidInterface checks a network interface name ("" = all).
func ValidInterface(n string) error {
	if n != "" && !ifaceRe.MatchString(n) {
		return errors.New("invalid network interface")
	}
	return nil
}

// ValidFolderType checks the content type of a published folder.
func ValidFolderType(t string) error {
	switch t {
	case "", "video", "audio", "pictures":
		return nil
	}
	return errors.New("invalid content type (video, audio, pictures or empty for all)")
}

// MiniDLNAConf writes the configuration of the server.
func MiniDLNAConf(c DLNAConfig) (string, error) {
	if err := ValidName(c.Name); err != nil {
		return "", err
	}
	if err := ValidInterface(c.Interface); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Written by StrataOS (Media → DLNA server): changes made here are overwritten.\n")
	fmt.Fprintf(&b, "port=%d\nfriendly_name=%s\n", DLNAPort, c.Name)
	if c.Interface != "" {
		fmt.Fprintf(&b, "network_interface=%s\n", c.Interface)
	}
	for _, f := range c.Folders {
		if err := ValidFolderType(f.Type); err != nil {
			return "", err
		}
		if !strings.HasPrefix(f.Path, "/") || strings.ContainsAny(f.Path, "\n\r\x00") {
			return "", fmt.Errorf("invalid folder %q", f.Path)
		}
		prefix := map[string]string{"video": "V,", "audio": "A,", "pictures": "P,"}[f.Type]
		fmt.Fprintf(&b, "media_dir=%s%s\n", prefix, f.Path)
	}
	b.WriteString("db_dir=" + DLNADBDir + "\nlog_dir=/var/log/strata-minidlna\n")
	b.WriteString("log_level=general,artwork,database,inotify,scanner,metadata,http,ssdp,tivo=warn\n")
	b.WriteString("inotify=yes\nnotify_interval=300\nroot_container=.\nstrict_dlna=no\nenable_subtitles=yes\nwide_links=no\n")
	b.WriteString("album_art_names=Cover.jpg/cover.jpg/AlbumArtSmall.jpg/albumartsmall.jpg/AlbumArt.jpg/albumart.jpg/Album.jpg/album.jpg/Folder.jpg/folder.jpg/Thumb.jpg/thumb.jpg\n")
	return b.String(), nil
}

// DLNAUnitFile runs minidlnad as its own unprivileged user. Shared folders
// are usually private (ACLs for their users only): CAP_DAC_READ_SEARCH lets
// it read them without running as root, and the system is read-only for it.
func DLNAUnitFile() string {
	return `[Unit]
Description=DLNA media server (StrataOS)
After=network-online.target local-fs.target remote-fs.target
Wants=network-online.target

[Service]
User=minidlna
Group=minidlna
ExecStart=/usr/sbin/minidlnad -S -f ` + DLNAConfPath + ` -P /run/strata-minidlna/minidlna.pid
RuntimeDirectory=strata-minidlna
CacheDirectory=strata-minidlna
LogsDirectory=strata-minidlna
AmbientCapabilities=CAP_DAC_READ_SEARCH
CapabilityBoundingSet=CAP_DAC_READ_SEARCH
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
InaccessiblePaths=-/var/lib/strata -/etc/strata -/etc/shadow -/etc/gshadow -/root
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
`
}

// DLNAInstalled reports whether ReadyMedia is installed.
func DLNAInstalled() bool {
	_, err := os.Stat("/usr/sbin/minidlnad")
	return err == nil
}

// ApplyDLNA writes the configuration and starts, restarts or stops the server.
func ApplyDLNA(enabled bool, c DLNAConfig) error {
	if !enabled {
		if _, err := os.Stat(DLNAUnitPath); err == nil {
			_, _ = sys.Run("systemctl", "disable", "--now", DLNAUnit)
		}
		return nil
	}
	if !DLNAInstalled() {
		return errors.New("install the DLNA server first (Settings → Components)")
	}
	conf, err := MiniDLNAConf(c)
	if err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(DLNAConfPath, []byte(conf), 0o644); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(DLNAUnitPath, []byte(DLNAUnitFile()), 0o644); err != nil {
		return err
	}
	if _, err := sys.Run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := sys.Run("systemctl", "enable", DLNAUnit); err != nil {
		return err
	}
	_, err = sys.Run("systemctl", "restart", DLNAUnit)
	return err
}

// RescanDLNA rebuilds the media database from scratch.
func RescanDLNA() error {
	if _, err := sys.Run("systemctl", "stop", DLNAUnit); err != nil {
		return err
	}
	_ = os.Remove(DLNADBDir + "/files.db")
	_, err := sys.Run("systemctl", "start", DLNAUnit)
	return err
}

// DLNAStatus is the state of the server.
type DLNAStatus struct {
	Installed bool   `json:"installed"`
	Service   string `json:"service"`
	Audio     int    `json:"audio"`
	Video     int    `json:"video"`
	Images    int    `json:"images"`
}

var countRe = regexp.MustCompile(`(?i)(Audio|Video|Image) files\s*</td>\s*<td>\s*(\d+)`)

// ParseDLNACounts reads the file counts on the status page of minidlna.
func ParseDLNACounts(page string) (audio, video, images int) {
	for _, m := range countRe.FindAllStringSubmatch(page, -1) {
		n, _ := strconv.Atoi(m[2])
		switch strings.ToLower(m[1]) {
		case "audio":
			audio = n
		case "video":
			video = n
		case "image":
			images = n
		}
	}
	return
}

// GetDLNAStatus reads the service state and the indexed file counts.
func GetDLNAStatus(ctx context.Context) DLNAStatus {
	st := DLNAStatus{Installed: DLNAInstalled(), Service: "inactive"}
	if !st.Installed {
		return st
	}
	out, _ := sys.RunOpts(ctx, sys.Opts{AllowCodes: []int{-2}, Timeout: 5 * time.Second}, "systemctl", "is-active", DLNAUnit)
	if s := strings.TrimSpace(out); s != "" {
		st.Service = s
	}
	if st.Service != "active" {
		return st
	}
	cl := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", DLNAPort), nil)
	if resp, err := cl.Do(req); err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		st.Audio, st.Video, st.Images = ParseDLNACounts(string(b))
	}
	return st
}
