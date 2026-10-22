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
		_, n, err := net.ParseCIDR(c.LAN)
		if err != nil || n.IP.To4() == nil {
			return fmt.Errorf("invalid local network")
		}
		c.LAN = n.String()
	}
	if c.ExpiryWarnDays < -1 || c.ExpiryWarnDays > 365 {
		return fmt.Errorf("invalid expiry warning delay")
	}
	switch c.DefaultMode {
	case "":
		c.DefaultMode = "lan"
	case "nas", "lan", "all":
	default:
		return fmt.Errorf("invalid access")
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

// CheckName validates a device name, unique per owner (skip: the ID of the
// peer being renamed).
func CheckName(c store.VPN, owner, name, skip string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid device name")
	}
	for _, p := range c.Peers {
		if p.ID != skip && p.Owner == owner && strings.EqualFold(p.Name, name) {
			return fmt.Errorf("a device with this name already exists")
		}
	}
	return nil
}

// NewPeer validates a device name and access mode and creates its keys.
// Names are unique per owner; mode "" is the default mode of the server.
func NewPeer(c store.VPN, owner, name, mode string) (store.VPNPeer, error) {
	name = strings.TrimSpace(name)
	if err := CheckName(c, owner, name, ""); err != nil {
		return store.VPNPeer{}, err
	}
	if mode == "" {
		mode = c.DefaultMode
	}
	switch mode {
	case "nas", "lan", "all":
	case "":
		mode = "lan"
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
	p := store.VPNPeer{ID: strconv.FormatInt(time.Now().UnixNano(), 36), Name: name, Mode: mode, IP: ip, PrivateKey: priv, PublicKey: pub, PresharedKey: base64.StdEncoding.EncodeToString(psk), CreatedAt: time.Now()}
	p.Owner = owner
	return p, nil
}

// RenderServer returns the wg-quick configuration of the NAS (firewall and
// NAT: RenderFirewall).
func RenderServer(c store.VPN) string {
	var b strings.Builder
	_, nw, _ := net.ParseCIDR(c.Subnet)
	ones, _ := nw.Mask.Size()
	fmt.Fprintf(&b, "# Managed by StrataOS — changes are overwritten.\n[Interface]\nAddress = %s/%d\nListenPort = %d\nPrivateKey = %s\nFwMark = %#x\n", ServerIP(c), ones, c.Port, c.PrivateKey, FwMark)
	for _, p := range c.Peers {
		if p.Disabled {
			continue
		}
		allowed := append([]string{p.IP + "/32"}, p.Subnets...)
		fmt.Fprintf(&b, "\n# %s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s\n", strings.ReplaceAll(p.Name, "\n", " "), p.PublicKey, p.PresharedKey, strings.Join(allowed, ", "))
		if p.Kind == "site" && p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
		if p.Kind == "site" && p.Keepalive > 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.Keepalive)
		}
	}
	return b.String()
}

// RenderClient returns the configuration to import on a device.
func RenderClient(c store.VPN, p store.VPNPeer) string {
	allowed := ServerIP(c) + "/32"
	switch p.Mode {
	case "lan":
		allowed = strings.Join(append([]string{c.Subnet}, lanTargets(c)...), ", ")
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
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/32\n%s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = 25\n", p.PrivateKey, p.IP, dns, c.PublicKey, p.PresharedKey, net.JoinHostPort(endpoint, strconv.Itoa(c.Port)), allowed)
}

// PeerStatus is the live state of a device.
type PeerStatus struct {
	Handshake int64  `json:"handshake"` // Unix time of the last handshake, 0 = never
	Endpoint  string `json:"endpoint"`
	Rx        int64  `json:"rx"`
	Tx        int64  `json:"tx"`
}

// Status reads `wg show strata dump`, keyed by public key.
func Status() map[string]PeerStatus { return StatusOf(Iface) }

// StatusOf reads the peers of a WireGuard interface.
func StatusOf(iface string) map[string]PeerStatus {
	out := map[string]PeerStatus{}
	dump, err := sys.Run("wg", "show", iface, "dump")
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
