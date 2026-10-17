// Package vpn runs the WireGuard server of the NAS (interface "strata") and
// keeps a dynamic DNS name pointing at the public address.
package vpn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"

	"golang.org/x/crypto/curve25519"
)

var (
	ConfPath = "/etc/wireguard/strata.conf"
	Iface    = "strata"
)

func Installed() bool { return sys.Has("wg") && sys.Has("wg-quick") }

// GenKey returns a WireGuard private key and its public key (base64).
func GenKey() (priv, pub string, err error) {
	var k [32]byte
	if _, err = rand.Read(k[:]); err != nil {
		return "", "", err
	}
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	p, err := curve25519.X25519(k[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k[:]), base64.StdEncoding.EncodeToString(p), nil
}

var (
	nameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._'-]{0,39}$`)
	hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-:\[\]]{0,252}$`)
)

// Validate normalises the server settings.
func Validate(c *store.VPN) error {
	if c.Port == 0 {
		c.Port = 51820
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port")
	}
	if c.Subnet == "" {
		c.Subnet = "10.8.0.0/24"
	}
	ip, n, err := net.ParseCIDR(c.Subnet)
	if err != nil || ip.To4() == nil {
		return fmt.Errorf("the VPN network must be an IPv4 network, e.g. 10.8.0.0/24")
	}
	if ones, _ := n.Mask.Size(); ones < 16 || ones > 28 {
		return fmt.Errorf("the VPN network must be between /16 and /28")
	}
	c.Subnet = n.String()
	if c.Endpoint != "" && !hostRe.MatchString(c.Endpoint) {
		return fmt.Errorf("invalid public address")
	}
	if c.LAN != "" {
		if _, _, err := net.ParseCIDR(c.LAN); err != nil {
			return fmt.Errorf("invalid local network")
		}
	}
	return nil
}

// addr returns the n-th address of the VPN network (1 = the NAS).
func addr(subnet string, n int) string {
	_, nw, _ := net.ParseCIDR(subnet)
	ip := nw.IP.To4()
	v := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
	v += uint32(n)
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String()
}

// ServerIP is the address of the NAS inside the VPN.
func ServerIP(c store.VPN) string { return addr(c.Subnet, 1) }

// NextIP returns the first free client address.
func NextIP(c store.VPN) (string, error) {
	_, nw, _ := net.ParseCIDR(c.Subnet)
	ones, bits := nw.Mask.Size()
	used := map[string]bool{ServerIP(c): true}
	for _, p := range c.Peers {
		used[p.IP] = true
	}
	for i := 2; i < (1<<(bits-ones))-1; i++ {
		if a := addr(c.Subnet, i); !used[a] {
			return a, nil
		}
	}
	return "", fmt.Errorf("the VPN network is full")
}

// NewPeer validates a device name and access mode and creates its keys.
func NewPeer(c store.VPN, name, mode string) (store.VPNPeer, error) {
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return store.VPNPeer{}, fmt.Errorf("invalid device name")
	}
	for _, p := range c.Peers {
		if strings.EqualFold(p.Name, name) {
			return store.VPNPeer{}, fmt.Errorf("a device with this name already exists")
		}
	}
	switch mode {
	case "nas", "lan", "all":
	default:
		return store.VPNPeer{}, fmt.Errorf("invalid access")
	}
	ip, err := NextIP(c)
	if err != nil {
		return store.VPNPeer{}, err
	}
	priv, pub, err := GenKey()
	if err != nil {
		return store.VPNPeer{}, err
	}
	psk := make([]byte, 32)
	_, _ = rand.Read(psk)
	return store.VPNPeer{ID: strconv.FormatInt(time.Now().UnixNano(), 36), Name: name, Mode: mode, IP: ip, PrivateKey: priv, PublicKey: pub, PresharedKey: base64.StdEncoding.EncodeToString(psk), CreatedAt: time.Now()}, nil
}

func needsNAT(c store.VPN) bool {
	for _, p := range c.Peers {
		if p.Mode != "nas" {
			return true
		}
	}
	return false
}

// RenderServer returns the wg-quick configuration of the NAS.
func RenderServer(c store.VPN) string {
	var b strings.Builder
	_, nw, _ := net.ParseCIDR(c.Subnet)
	ones, _ := nw.Mask.Size()
	fmt.Fprintf(&b, "# Managed by StrataOS — changes are overwritten.\n[Interface]\nAddress = %s/%d\nListenPort = %d\nPrivateKey = %s\n", ServerIP(c), ones, c.Port, c.PrivateKey)
	if needsNAT(c) {
		// Devices allowed beyond the NAS go out with the NAS address.
		fmt.Fprintf(&b, "PostUp = sysctl -q -w net.ipv4.ip_forward=1; nft add table ip strata_vpn; nft add chain ip strata_vpn post '{ type nat hook postrouting priority 100 ; }'; nft add rule ip strata_vpn post ip saddr %s oifname != \"%s\" masquerade\n", c.Subnet, Iface)
		b.WriteString("PostDown = nft delete table ip strata_vpn\n")
	}
	for _, p := range c.Peers {
		if p.Disabled {
			continue
		}
		fmt.Fprintf(&b, "\n# %s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s/32\n", strings.ReplaceAll(p.Name, "\n", " "), p.PublicKey, p.PresharedKey, p.IP)
	}
	return b.String()
}

// RenderClient returns the configuration to import on a device.
func RenderClient(c store.VPN, p store.VPNPeer) string {
	allowed := ServerIP(c) + "/32"
	switch p.Mode {
	case "lan":
		allowed = c.Subnet
		if c.LAN != "" {
			allowed += ", " + c.LAN
		}
	case "all":
		allowed = "0.0.0.0/0"
	}
	dns := ""
	if p.Mode == "all" {
		dns = "DNS = 1.1.1.1, 9.9.9.9\n"
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "<adresse-publique>"
	}
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/32\n%s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nEndpoint = %s:%d\nAllowedIPs = %s\nPersistentKeepalive = 25\n", p.PrivateKey, p.IP, dns, c.PublicKey, p.PresharedKey, endpoint, c.Port, allowed)
}

// Apply writes the configuration and starts, reloads or stops the server.
func Apply(c store.VPN) error {
	unit := "wg-quick@" + Iface + ".service"
	if !c.Enabled {
		if Installed() {
			_, _ = sys.Run("systemctl", "disable", "--now", unit)
		}
		return nil
	}
	if !Installed() {
		return fmt.Errorf("WireGuard is not installed")
	}
	if err := os.MkdirAll("/etc/wireguard", 0o700); err != nil {
		return err
	}
	old, _ := os.ReadFile(ConfPath)
	conf := RenderServer(c)
	if err := sys.WriteFileAtomic(ConfPath, []byte(conf), 0o600); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "enable", unit)
	active, _ := sys.Run("systemctl", "is-active", unit)
	switch {
	case strings.TrimSpace(active) != "active":
		_, err := sys.Run("systemctl", "start", unit)
		return err
	case needsNAT(c) != strings.Contains(string(old), "PostUp"):
		// NAT rules change only on restart.
		_, err := sys.Run("systemctl", "restart", unit)
		return err
	default:
		// Peers change without cutting the other devices.
		stripped, err := sys.Run("wg-quick", "strip", Iface)
		if err != nil {
			return err
		}
		_, err = sys.RunInput(stripped, "wg", "syncconf", Iface, "/dev/stdin")
		return err
	}
}

// PeerStatus is the live state of a device.
type PeerStatus struct {
	Handshake int64  `json:"handshake"` // Unix time of the last handshake, 0 = never
	Endpoint  string `json:"endpoint"`
	Rx        int64  `json:"rx"`
	Tx        int64  `json:"tx"`
}

// Status reads `wg show strata dump`, keyed by public key.
func Status() map[string]PeerStatus {
	out := map[string]PeerStatus{}
	dump, err := sys.Run("wg", "show", Iface, "dump")
	if err != nil {
		return out
	}
	for i, l := range strings.Split(strings.TrimSpace(dump), "\n") {
		f := strings.Split(l, "\t")
		if i == 0 || len(f) < 7 {
			continue
		}
		h, _ := strconv.ParseInt(f[4], 10, 64)
		rx, _ := strconv.ParseInt(f[5], 10, 64)
		tx, _ := strconv.ParseInt(f[6], 10, 64)
		ep := f[2]
		if ep == "(none)" {
			ep = ""
		}
		out[f[0]] = PeerStatus{Handshake: h, Endpoint: ep, Rx: rx, Tx: tx}
	}
	return out
}

// ---- Dynamic DNS ----

// IPLookupURL returns the public IPv4 address as plain text.
var IPLookupURL = "https://api.ipify.org"

var httpClient = &http.Client{Timeout: 20 * time.Second}

func get(ctx context.Context, u string, header map[string]string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, "", err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, strings.TrimSpace(string(b)), nil
}

// PublicIP asks an external service for the address the Internet sees.
func PublicIP(ctx context.Context) (string, error) {
	code, body, err := get(ctx, IPLookupURL, nil)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(body); code != 200 || ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("could not read the public address")
	}
	return body, nil
}

var ddnsHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)*[A-Za-z0-9-]{1,63}$`)

// ValidateDDNS checks the dynamic DNS settings.
func ValidateDDNS(d *store.DDNS) error {
	d.Hostname = strings.ToLower(strings.TrimSpace(d.Hostname))
	switch d.Provider {
	case "":
		return nil
	case "duckdns":
		d.Hostname = strings.TrimSuffix(d.Hostname, ".duckdns.org")
		if !ddnsHostRe.MatchString(d.Hostname) || d.Token == "" {
			return fmt.Errorf("DuckDNS needs the sub-domain and the token")
		}
	case "dynv6", "cloudflare":
		if !ddnsHostRe.MatchString(d.Hostname) || d.Token == "" {
			return fmt.Errorf("enter the host name and the token")
		}
		if d.Provider == "cloudflare" && !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(d.ZoneID) {
			return fmt.Errorf("enter the Cloudflare zone ID (32 hexadecimal characters)")
		}
	case "custom":
		u, err := url.Parse(d.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !strings.Contains(d.URL, "{ip}") {
			return fmt.Errorf("the update URL must be http(s) and contain {ip}")
		}
	default:
		return fmt.Errorf("unknown dynamic DNS provider")
	}
	if strings.ContainsAny(d.Token+d.ZoneID, "\n\r") {
		return fmt.Errorf("invalid token")
	}
	return nil
}

// FullHostname is the name devices connect to.
func FullHostname(d store.DDNS) string {
	if d.Provider == "duckdns" {
		return d.Hostname + ".duckdns.org"
	}
	if d.Provider == "custom" {
		return d.Hostname
	}
	return d.Hostname
}

// CloudflareAPI is replaced in tests.
var CloudflareAPI = "https://api.cloudflare.com/client/v4"

// UpdateDDNS points the name at ip.
func UpdateDDNS(ctx context.Context, d store.DDNS, ip string) error {
	switch d.Provider {
	case "duckdns":
		_, body, err := get(ctx, "https://www.duckdns.org/update?domains="+url.QueryEscape(d.Hostname)+"&token="+url.QueryEscape(d.Token)+"&ip="+ip, nil)
		if err != nil {
			return fmt.Errorf("DuckDNS unreachable")
		}
		if body != "OK" {
			return fmt.Errorf("DuckDNS refused the update (check the sub-domain and the token)")
		}
	case "dynv6":
		code, body, err := get(ctx, "https://dynv6.com/api/update?hostname="+url.QueryEscape(d.Hostname)+"&token="+url.QueryEscape(d.Token)+"&ipv4="+ip, nil)
		if err != nil {
			return fmt.Errorf("dynv6 unreachable")
		}
		if code != 200 {
			return fmt.Errorf("dynv6: %s", body)
		}
	case "custom":
		code, body, err := get(ctx, strings.ReplaceAll(d.URL, "{ip}", ip), nil)
		if err != nil {
			return err
		}
		if code >= 300 {
			return fmt.Errorf("HTTP %d: %.120s", code, body)
		}
	case "cloudflare":
		return cloudflareUpdate(ctx, d, ip)
	}
	return nil
}
