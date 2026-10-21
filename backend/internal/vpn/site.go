package vpn

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"strataos/internal/store"
)

// Policy is what an administrator sets on a peer.
type Policy struct {
	Mode      string   `json:"mode"`
	Services  []string `json:"services"`
	Expires   string   `json:"expires"` // "" | 2026-12-31 | RFC 3339
	Subnets   []string `json:"subnets"`
	Endpoint  string   `json:"endpoint"`
	Keepalive int      `json:"keepalive"`
}

// ValidEndpoint checks a host:port ([v6]:port included).
func ValidEndpoint(s string) bool {
	h, p, err := net.SplitHostPort(s)
	if err != nil || h == "" {
		return false
	}
	if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
		return false
	}
	return net.ParseIP(h) != nil || ddnsHostRe.MatchString(h)
}

// network parses an IPv4 network (an address alone is a /32).
func network(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		s += "/32"
	}
	ip, n, err := net.ParseCIDR(s)
	if err != nil || ip.To4() == nil {
		return nil, fmt.Errorf("invalid IPv4 network %q", s)
	}
	return n, nil
}

func overlap(a, b string) bool {
	x, err1 := network(a)
	y, err2 := network(b)
	return err1 == nil && err2 == nil && (x.Contains(y.IP) || y.Contains(x.IP))
}

// usedNets are the networks of this NAS a new site must not overlap, but
// those of the peer or link with the given ID.
func usedNets(c store.VPN, skip string) []string {
	var out []string
	if c.Enabled || len(c.Peers) > 0 {
		out = append(out, c.Subnet)
	}
	if c.LAN != "" {
		out = append(out, c.LAN)
	}
	for _, p := range c.Peers {
		if p.Kind == "site" && p.ID != skip {
			out = append(out, p.Subnets...)
		}
	}
	for _, l := range c.Links {
		if l.ID != skip {
			out = append(out, l.Subnets...)
		}
	}
	if c.Client.Scope == "apps" && c.Client.AppsSubnet != "" {
		out = append(out, c.Client.AppsSubnet)
	}
	return out
}

// checkSubnets normalises the networks of a site and checks they do not
// collide with the networks of this NAS.
func checkSubnets(c store.VPN, list []string, skip string) ([]string, error) {
	var out []string
	for _, s := range list {
		if strings.TrimSpace(s) == "" {
			continue
		}
		n, err := network(s)
		if err != nil {
			return nil, err
		}
		if ones, _ := n.Mask.Size(); ones < 8 {
			return nil, fmt.Errorf("the network %s is too large", n)
		}
		for _, u := range usedNets(c, skip) {
			if overlap(n.String(), u) {
				return nil, fmt.Errorf("the network %s overlaps %s, already used on this NAS", n, u)
			}
		}
		for _, o := range out {
			if overlap(o, n.String()) {
				return nil, fmt.Errorf("the networks %s and %s overlap", o, n)
			}
		}
		out = append(out, n.String())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("enter the networks of the site, e.g. 192.168.2.0/24")
	}
	if len(out) > 16 {
		return nil, fmt.Errorf("too many networks")
	}
	return out, nil
}

// SetPolicy validates pol and puts it on p.
func SetPolicy(c store.VPN, p *store.VPNPeer, pol Policy) error {
	svc, err := ValidateServices(pol.Services)
	if err != nil {
		return err
	}
	exp, err := ParseExpiry(pol.Expires)
	if err != nil {
		return err
	}
	if pol.Keepalive < 0 || pol.Keepalive > 3600 {
		return fmt.Errorf("invalid keepalive interval")
	}
	if p.Kind == "site" {
		subnets, err := checkSubnets(c, pol.Subnets, p.ID)
		if err != nil {
			return err
		}
		if pol.Endpoint != "" && !ValidEndpoint(pol.Endpoint) {
			return fmt.Errorf("the address of the site must be host:port")
		}
		if pol.Keepalive == 0 {
			pol.Keepalive = 25
		}
		p.Mode, p.Subnets, p.Endpoint, p.Keepalive = "lan", subnets, pol.Endpoint, pol.Keepalive
	} else {
		switch pol.Mode {
		case "nas", "lan", "all":
			p.Mode = pol.Mode
		case "":
		default:
			return fmt.Errorf("invalid access")
		}
		p.Subnets, p.Endpoint, p.Keepalive = nil, "", 0
	}
	p.Services = svc
	if !exp.Equal(p.Expires) {
		p.Expires = exp
		if p.Expired && (exp.IsZero() || time.Now().Before(exp)) {
			// A new date gives an expired peer its access back.
			p.Expired, p.Disabled = false, false
		}
	}
	return nil
}

// localNets are the networks of this NAS a site reaches.
func localNets(c store.VPN) []string {
	out := []string{c.Subnet}
	if c.LAN != "" {
		out = append(out, c.LAN)
	}
	return out
}

// RenderRouter returns the configuration of the other side of a site for
// a router (OPNsense, pfSense, MikroTik, Fritz!Box…) or any WireGuard
// system: the values one by one, then the wg-quick file most of them
// import as is.
func RenderRouter(c store.VPN, p store.VPNPeer, hostname string) string {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "<public-address>"
	}
	ep := net.JoinHostPort(endpoint, strconv.Itoa(c.Port))
	nets := strings.Join(localNets(c), ", ")
	ka := max(p.Keepalive, 25)
	var b strings.Builder
	fmt.Fprintf(&b, "# WireGuard site-to-site tunnel to %s (StrataOS), site \"%s\"\n#\n", hostname, p.Name)
	b.WriteString("# Values for a router (OPNsense, pfSense, MikroTik, Fritz!Box...):\n")
	fmt.Fprintf(&b, "#   Tunnel address of the router:   %s/32\n", p.IP)
	fmt.Fprintf(&b, "#   Private key of the router:      %s\n", p.PrivateKey)
	fmt.Fprintf(&b, "#   Peer public key (the NAS):      %s\n", c.PublicKey)
	fmt.Fprintf(&b, "#   Pre-shared key:                 %s\n", p.PresharedKey)
	fmt.Fprintf(&b, "#   Peer endpoint:                  %s\n", ep)
	fmt.Fprintf(&b, "#   Allowed IPs (NAS side):         %s\n", nets)
	fmt.Fprintf(&b, "#   Persistent keepalive:           %d s\n", ka)
	if p.Endpoint != "" {
		_, port, _ := net.SplitHostPort(p.Endpoint)
		fmt.Fprintf(&b, "#   Listen port of the router:      %s (the NAS connects to %s)\n", port, p.Endpoint)
	}
	fmt.Fprintf(&b, "#   Networks of this site:          %s\n", strings.Join(p.Subnets, ", "))
	b.WriteString("# Route the NAS side networks through the tunnel, and allow them in the\n# firewall of the router (rules on the WireGuard interface).\n#\n")
	b.WriteString("# MikroTik RouterOS:\n")
	port := ""
	if p.Endpoint != "" {
		_, pp, _ := net.SplitHostPort(p.Endpoint)
		port = " listen-port=" + pp
	}
	fmt.Fprintf(&b, "#   /interface wireguard add name=wg-strata private-key=\"%s\"%s\n", p.PrivateKey, port)
	fmt.Fprintf(&b, "#   /interface wireguard peers add interface=wg-strata public-key=\"%s\" preshared-key=\"%s\" endpoint-address=%s endpoint-port=%d allowed-address=%s persistent-keepalive=%ds\n", c.PublicKey, p.PresharedKey, endpoint, c.Port, strings.Join(localNets(c), ","), ka)
	fmt.Fprintf(&b, "#   /ip address add address=%s/32 interface=wg-strata\n", p.IP)
	for _, n := range localNets(c) {
		fmt.Fprintf(&b, "#   /ip route add dst-address=%s gateway=wg-strata\n", n)
	}
	b.WriteString("#\n# Generic WireGuard file (Fritz!Box, OPNsense import, wg-quick):\n")
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s/32\n", p.PrivateKey, p.IP)
	if p.Endpoint != "" {
		_, pp, _ := net.SplitHostPort(p.Endpoint)
		fmt.Fprintf(&b, "ListenPort = %s\n", pp)
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = %d\n", c.PublicKey, p.PresharedKey, ep, nets, ka)
	return b.String()
}

// Pairing is what another StrataOS needs to create the other side of a site.
type Pairing struct {
	V            int      `json:"v"`
	Name         string   `json:"name"`    // the NAS that made the code
	Address      string   `json:"address"` // of the importing NAS in the tunnel
	PrivateKey   string   `json:"privateKey"`
	PublicKey    string   `json:"publicKey"` // of the NAS that made the code
	PresharedKey string   `json:"presharedKey"`
	Endpoint     string   `json:"endpoint,omitempty"` // host:port of that NAS
	Subnets      []string `json:"subnets"`            // its networks
	ListenPort   int      `json:"listenPort,omitempty"`
	Keepalive    int      `json:"keepalive,omitempty"`
}

const pairingPrefix = "strataos-site:"

// PairingCode returns the code another StrataOS imports to connect to the
// site p of this NAS. It holds a private key: it is a secret.
func PairingCode(c store.VPN, p store.VPNPeer, hostname string) (string, error) {
	pr := Pairing{V: 1, Name: hostname, Address: p.IP + "/32", PrivateKey: p.PrivateKey, PublicKey: c.PublicKey, PresharedKey: p.PresharedKey, Subnets: localNets(c), Keepalive: max(p.Keepalive, 25)}
	if c.Endpoint != "" {
		pr.Endpoint = net.JoinHostPort(c.Endpoint, strconv.Itoa(c.Port))
	}
	if p.Endpoint != "" {
		_, port, _ := net.SplitHostPort(p.Endpoint)
		pr.ListenPort, _ = strconv.Atoi(port)
	}
	if pr.Endpoint == "" && pr.ListenPort == 0 {
		return "", fmt.Errorf("set the public address of this NAS or the address of the site first")
	}
	js, _ := json.Marshal(pr)
	return pairingPrefix + base64.RawURLEncoding.EncodeToString(js), nil
}

// ParsePairing reads and checks a pairing code.
func ParsePairing(code string) (Pairing, error) {
	var pr Pairing
	code = strings.Join(strings.Fields(code), "")
	raw, ok := strings.CutPrefix(code, pairingPrefix)
	if !ok || len(raw) > 8192 {
		return pr, fmt.Errorf("this is not a StrataOS site code")
	}
	js, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(js, &pr) != nil || pr.V != 1 {
		return pr, fmt.Errorf("this is not a StrataOS site code")
	}
	if !ValidKey(pr.PrivateKey) || !ValidKey(pr.PublicKey) || (pr.PresharedKey != "" && !ValidKey(pr.PresharedKey)) {
		return pr, fmt.Errorf("invalid keys in the site code")
	}
	if n, err := network(pr.Address); err != nil || n.Mask.String() != net.CIDRMask(32, 32).String() {
		return pr, fmt.Errorf("invalid tunnel address in the site code")
	}
	if pr.Endpoint != "" && !ValidEndpoint(pr.Endpoint) {
		return pr, fmt.Errorf("invalid address in the site code")
	}
	if pr.ListenPort < 0 || pr.ListenPort > 65535 || pr.Keepalive < 0 || pr.Keepalive > 3600 || (pr.Endpoint == "" && pr.ListenPort == 0) {
		return pr, fmt.Errorf("invalid port in the site code")
	}
	if len(pr.Subnets) == 0 || len(pr.Subnets) > 16 {
		return pr, fmt.Errorf("invalid networks in the site code")
	}
	for i, s := range pr.Subnets {
		n, err := network(s)
		if err != nil {
			return pr, err
		}
		pr.Subnets[i] = n.String()
	}
	pr.Name = strings.TrimSpace(pr.Name)
	if !nameRe.MatchString(pr.Name) {
		pr.Name = "Site"
	}
	return pr, nil
}

// NewLink creates the tunnel to the NAS that made a pairing code.
func NewLink(c store.VPN, pr Pairing, name string) (store.VPNLink, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = pr.Name
	}
	if !nameRe.MatchString(name) {
		return store.VPNLink{}, fmt.Errorf("invalid site name")
	}
	for _, l := range c.Links {
		if l.PeerPublicKey == pr.PublicKey && l.Address == pr.Address {
			return store.VPNLink{}, fmt.Errorf("this site is already imported")
		}
	}
	subnets, err := checkSubnets(c, pr.Subnets, "")
	if err != nil {
		return store.VPNLink{}, fmt.Errorf("%v: change the network of one of the two NAS", err)
	}
	if pr.ListenPort != 0 && c.Enabled && pr.ListenPort == c.Port {
		return store.VPNLink{}, fmt.Errorf("the port %d is already used by the VPN server", pr.ListenPort)
	}
	iface := ""
	for i := 1; i < 100 && iface == ""; i++ {
		n := "strata-l" + strconv.Itoa(i)
		if !slices.ContainsFunc(c.Links, func(l store.VPNLink) bool { return l.Iface == n }) {
			iface = n
		}
	}
	if iface == "" {
		return store.VPNLink{}, fmt.Errorf("too many sites")
	}
	return store.VPNLink{ID: strconv.FormatInt(time.Now().UnixNano(), 36), Name: name, Iface: iface, Address: pr.Address, PrivateKey: pr.PrivateKey, PeerPublicKey: pr.PublicKey, PresharedKey: pr.PresharedKey, Endpoint: pr.Endpoint, Subnets: subnets, ListenPort: pr.ListenPort, Keepalive: pr.Keepalive, CreatedAt: time.Now()}, nil
}

// ValidateLink checks a link restored from a configuration backup.
func ValidateLink(c store.VPN, l store.VPNLink) error {
	if !nameRe.MatchString(l.Name) || !linkRe.MatchString(l.Iface) {
		return fmt.Errorf("invalid site")
	}
	if !ValidKey(l.PrivateKey) || !ValidKey(l.PeerPublicKey) || (l.PresharedKey != "" && !ValidKey(l.PresharedKey)) {
		return fmt.Errorf("invalid site keys")
	}
	if _, err := network(l.Address); err != nil {
		return err
	}
	if (l.Endpoint != "" && !ValidEndpoint(l.Endpoint)) || l.ListenPort < 0 || l.ListenPort > 65535 || l.Keepalive < 0 || l.Keepalive > 3600 {
		return fmt.Errorf("invalid site address")
	}
	if slices.ContainsFunc(c.Links, func(x store.VPNLink) bool { return x.Iface == l.Iface }) {
		return fmt.Errorf("the interface %s is already used", l.Iface)
	}
	_, err := checkSubnets(c, l.Subnets, l.ID)
	return err
}

// RenderLink returns the wg-quick configuration of a link.
func RenderLink(l store.VPNLink) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by StrataOS — changes are overwritten.\n# Site %s\n[Interface]\nPrivateKey = %s\nAddress = %s\n", strings.ReplaceAll(l.Name, "\n", " "), l.PrivateKey, l.Address)
	if l.ListenPort > 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", l.ListenPort)
	}
	fmt.Fprintf(&b, "FwMark = %#x\n\n[Peer]\nPublicKey = %s\n", FwMark, l.PeerPublicKey)
	if l.PresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", l.PresharedKey)
	}
	if l.Endpoint != "" {
		fmt.Fprintf(&b, "Endpoint = %s\n", l.Endpoint)
	}
	fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(l.Subnets, ", "))
	if l.Keepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", l.Keepalive)
	}
	return b.String()
}
