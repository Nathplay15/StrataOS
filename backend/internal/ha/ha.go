// Package ha pairs two StrataOS NAS for high availability: the primary
// replicates accounts, share definitions and data to the secondary, and a
// virtual IP address (keepalived, VRRP) moves to the secondary when the
// primary stops answering.
package ha

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

var (
	KeepalivedConf = "/etc/keepalived/keepalived.conf"
	StateFile      = "/run/strata/ha-state"
	NotifyScript   = "/usr/lib/strata/scripts/ha-notify"
	AuthorizedKeys = "/root/.ssh/authorized_keys"
	StorageRoot    = "/srv/strata"
	keyMarker      = "strata-ha"
)

func Installed() bool {
	return fileExists("/usr/sbin/keepalived") && rrsync() != ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// rrsync restricts the replication key to rsync inside the storage.
func rrsync() string {
	for _, p := range []string{"/usr/bin/rrsync", "/usr/share/doc/rsync/scripts/rrsync"} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

var (
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-:]{0,252}$`)
	ifaceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,14}$`)
)

// Validate checks the settings typed on the primary.
func Validate(c *store.HA) error {
	if !hostRe.MatchString(c.PeerHost) {
		return fmt.Errorf("invalid address of the other NAS")
	}
	ip, n, err := net.ParseCIDR(c.VIP)
	if err != nil || ip.To4() == nil {
		return fmt.Errorf("the shared address must be an IPv4 address with its prefix, e.g. 192.168.1.50/24")
	}
	if ones, _ := n.Mask.Size(); ones < 8 || ones > 30 {
		return fmt.Errorf("invalid prefix length")
	}
	if !ifaceRe.MatchString(c.Interface) {
		return fmt.Errorf("invalid network interface")
	}
	if c.VRID == 0 {
		c.VRID = 51
	}
	if c.VRID < 1 || c.VRID > 255 {
		return fmt.Errorf("the VRRP router ID must be between 1 and 255")
	}
	if c.Interval <= 0 {
		c.Interval = 15
	}
	if c.Interval > 24*60 {
		return fmt.Errorf("replication interval too long")
	}
	return nil
}

// SelfIP returns the first IPv4 address of iface that is not the VIP.
func SelfIP(iface, vip string) string {
	out, err := sys.Run("ip", "-4", "-o", "addr", "show", "dev", iface)
	if err != nil {
		return ""
	}
	v, _, _ := strings.Cut(vip, "/")
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "inet" {
				if a, _, _ := strings.Cut(f[i+1], "/"); a != v {
					return a
				}
			}
		}
	}
	return ""
}

// InterfaceFor finds the interface whose IPv4 network contains vip.
func InterfaceFor(vip string) string {
	ip, _, err := net.ParseCIDR(vip)
	if err != nil {
		return ""
	}
	out, _ := sys.Run("ip", "-4", "-o", "addr", "show")
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "inet" {
				if _, n, err := net.ParseCIDR(f[i+1]); err == nil && n.Contains(ip) {
					return f[1]
				}
			}
		}
	}
	return ""
}

// RenderKeepalived returns the VRRP configuration. Both NAS start as BACKUP
// with nopreempt: the primary (higher priority) wins the first election, and
// a primary coming back never takes the address back by itself (the
// secondary may hold newer data).
func RenderKeepalived(c store.HA, self string) string {
	prio, delay := 100, 30
	if c.Role == "primary" {
		prio, delay = 150, 0
	}
	startup := ""
	if delay > 0 {
		// The secondary waits before taking part, so that the primary (higher
		// priority) wins the first election when both start together.
		startup = fmt.Sprintf("    vrrp_startup_delay %d\n", delay)
	}
	return fmt.Sprintf(`# Managed by StrataOS — changes are overwritten.
global_defs {
    router_id strata_%s
    script_user root
    enable_script_security
%s}

vrrp_instance STRATA {
    state BACKUP
    nopreempt
    interface %s
    virtual_router_id %d
    priority %d
    advert_int 1
    authentication {
        auth_type PASS
        auth_pass %s
    }
    unicast_src_ip %s
    unicast_peer {
        %s
    }
    virtual_ipaddress {
        %s dev %s
    }
    notify %s
}
`, c.Role, startup, c.Interface, c.VRID, prio, c.AuthPass, self, c.PeerHost, c.VIP, c.Interface, NotifyScript)
}

// ApplyKeepalived writes the configuration and (re)starts keepalived, or stops
// it when the NAS is not paired.
func ApplyKeepalived(c store.HA) error {
	if c.Role == "" {
		_, _ = sys.Run("systemctl", "disable", "--now", "keepalived.service")
		_ = os.Remove(KeepalivedConf)
		_ = os.Remove(StateFile)
		return nil
	}
	self := SelfIP(c.Interface, c.VIP)
	if self == "" {
		return fmt.Errorf("interface %s has no IPv4 address", c.Interface)
	}
	if err := os.MkdirAll(filepath.Dir(KeepalivedConf), 0o755); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(KeepalivedConf, []byte(RenderKeepalived(c, self)), 0o600); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "enable", "keepalived.service")
	_, err := sys.Run("systemctl", "restart", "keepalived.service")
	return err
}

// Release gives the virtual address away (used to hand control back).
func Release() error {
	_, err := sys.Run("systemctl", "restart", "keepalived.service")
	return err
}

// State is MASTER (this NAS holds the address), BACKUP, FAULT or "".
func State() string {
	b, err := os.ReadFile(StateFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---- Trust between the two NAS ----

// CertFingerprint returns the SHA-256 of the peer's HTTPS certificate.
func CertFingerprint(ctx context.Context, host string) (string, error) {
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}, NetDialer: &net.Dialer{Timeout: 10 * time.Second}}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, "7443"))
	if err != nil {
		return "", fmt.Errorf("cannot reach %s:7443: %w", host, err)
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", fmt.Errorf("no certificate")
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

// Peer calls the other NAS's HA API, pinning its certificate.
type Peer struct {
	Host, Token, CertFP string
}

func (p Peer) client() *http.Client {
	fp := p.CertFP
	cfg := &tls.Config{InsecureSkipVerify: true, VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return fmt.Errorf("no certificate")
		}
		sum := sha256.Sum256(raw[0])
		if fp != "" && subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(fp)) != 1 {
			return fmt.Errorf("the other NAS presented an unexpected certificate (pair again if it was replaced)")
		}
		return nil
	}}
	return &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{TLSClientConfig: cfg}}
}

// Call sends a JSON request to https://peer:7443/api/ha/peer/<path>.
func (p Peer) Call(ctx context.Context, path string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://"+net.JoinHostPort(p.Host, "7443")+"/api/ha/peer/"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Strata", "1")
	if p.Token != "" {
		req.Header.Set("X-Strata-HA", p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ---- Replication key ----

var pubKeyRe = regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/=]{40,100}( [^\n]*)?$`)

// AuthorizeKey lets the other NAS run rsync into the storage as root, and
// nothing else (rrsync, no shell, no forwarding).
func AuthorizeKey(pub string) error {
	pub = strings.TrimSpace(pub)
	if !pubKeyRe.MatchString(pub) {
		return fmt.Errorf("invalid SSH public key")
	}
	f := strings.Fields(pub)
	line := fmt.Sprintf(`command="%s %s",restrict %s %s %s`, rrsync(), StorageRoot, f[0], f[1], keyMarker)
	return rewriteKeys(line)
}

// RevokeKey removes the replication key.
func RevokeKey() error { return rewriteKeys("") }

func rewriteKeys(add string) error {
	if err := os.MkdirAll(filepath.Dir(AuthorizedKeys), 0o700); err != nil {
		return err
	}
	old, _ := os.ReadFile(AuthorizedKeys)
	var lines []string
	for _, l := range strings.Split(string(old), "\n") {
		if l != "" && !strings.HasSuffix(l, " "+keyMarker) {
			lines = append(lines, l)
		}
	}
	if add != "" {
		lines = append(lines, add)
	}
	return sys.WriteFileAtomic(AuthorizedKeys, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// SyncPath mirrors a folder of the storage to the same place on the peer.
func SyncPath(ctx context.Context, key, knownHosts, peer, path string, logf func(string, ...any)) error {
	clean := filepath.Clean(path)
	rel, ok := strings.CutPrefix(clean, StorageRoot+"/")
	if !ok || rel == "" || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is not inside the storage", path)
	}
	ssh := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s -o ConnectTimeout=15", key, knownHosts)
	args := []string{"-aHAX", "--numeric-ids", "--delete", "--partial", "--mkpath", "--exclude=.recycle/", "-e", ssh, clean + "/", "root@" + bracket(peer) + ":" + rel + "/"}
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 24 * time.Hour}, "rsync", args...)
	if err != nil {
		return fmt.Errorf("rsync %s: %s", rel, lastLine(out+" "+err.Error()))
	}
	logf("%s synchronisé", rel)
	return nil
}

func bracket(h string) string {
	if strings.Contains(h, ":") {
		return "[" + h + "]"
	}
	return h
}

func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	return l[len(l)-1]
}
