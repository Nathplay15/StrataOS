package vpn

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"strataos/internal/store"
)

// The outgoing tunnel: interface, routing table and firewall mark (the
// encrypted packets of every StrataOS WireGuard interface carry the mark,
// so they never go into the tunnel themselves), and the Docker network of
// the apps routed through it.
const (
	OutIface          = "strata-out"
	RouteTable        = 52000
	FwMark            = 0xcb20 // 52000
	AppsNetwork       = "strata-vpn"
	AppsBridge        = "br-strata-vpn"
	DefaultAppsSubnet = "172.31.250.0/24"
	// AppsSubnet6 is the IPv6 network of the routed apps: it gives them
	// their IPv6 default route, so it goes into the tunnel or nowhere.
	AppsSubnet6 = "fd5f:cb20:250::/64"
)

// Rule priorities, before the main table (32766).
const (
	prioLocal     = 5298 // whole NAS: local and private networks keep their routes
	prioMain      = 5300 // routed apps: local networks keep their routes
	prioTunnelSrc = 5301 // traffic from the tunnel address
	prioTunnel    = 5302 // what goes through the tunnel
	prioKill      = 5303 // kill switch: the routed apps, tunnel down
	prioUpdates   = 5304 // whole NAS, tunnel down: APT when allowed
	prioKillAll   = 5305 // kill switch of the whole NAS
)

// private ranges never count as the Internet for the kill switch.
var (
	privateNets4 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16"}
	privateNets6 = []string{"fc00::/7", "fe80::/10"}
)

var (
	linkRe      = regexp.MustCompile(`^strata-l[0-9]{1,2}$`)
	containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	dnsNameRe   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)*[A-Za-z0-9-]{1,63}$`)
)

// ClientActive reports whether the outgoing tunnel is configured and on.
func ClientActive(cl store.VPNClient) bool { return cl.Enabled && cl.PrivateKey != "" }

// ParseClientConf reads the WireGuard file of a VPN provider. Only what
// StrataOS needs is kept (the file is written again, never used as is):
// scripts and routing settings are ignored, with a warning.
func ParseClientConf(text string) (store.VPNClient, []string, error) {
	var cl store.VPNClient
	var warn []string
	if len(text) > 16<<10 {
		return cl, nil, fmt.Errorf("the file is too large")
	}
	section, peers := "", 0
	for n, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[] "))
			if section == "peer" {
				peers++
			} else if section != "interface" {
				return cl, nil, fmt.Errorf("line %d: unknown section %s", n+1, line)
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			return cl, nil, fmt.Errorf("line %d: not a WireGuard configuration", n+1)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 }) {
			return cl, nil, fmt.Errorf("line %d: invalid value", n+1)
		}
		list := func() []string {
			var out []string
			for _, x := range strings.Split(v, ",") {
				if x = strings.TrimSpace(x); x != "" {
					out = append(out, x)
				}
			}
			return out
		}
		switch section + "." + k {
		case "interface.privatekey":
			if !ValidKey(v) {
				return cl, nil, fmt.Errorf("invalid private key")
			}
			cl.PrivateKey = v
		case "interface.address":
			for _, a := range list() {
				ip, n, err := net.ParseCIDR(a)
				if err != nil {
					if ip = net.ParseIP(a); ip == nil {
						return cl, nil, fmt.Errorf("invalid address %q", a)
					}
					n = &net.IPNet{IP: ip, Mask: net.CIDRMask(len(ip)*8, len(ip)*8)}
				}
				ones, _ := n.Mask.Size()
				if ip.To4() == nil {
					cl.Addresses = append(cl.Addresses, fmt.Sprintf("%s/%d", ip, ones))
					continue
				}
				cl.Addresses = append(cl.Addresses, fmt.Sprintf("%s/%d", ip.To4(), ones))
			}
		case "interface.dns":
			for _, d := range list() {
				if net.ParseIP(d) == nil && !dnsNameRe.MatchString(d) {
					return cl, nil, fmt.Errorf("invalid DNS server %q", d)
				}
				cl.DNS = append(cl.DNS, d)
			}
		case "interface.mtu":
			m, err := strconv.Atoi(v)
			if err != nil || m < 1280 || m > 9000 {
				return cl, nil, fmt.Errorf("invalid MTU")
			}
			cl.MTU = m
		case "peer.publickey":
			if !ValidKey(v) {
				return cl, nil, fmt.Errorf("invalid public key of the server")
			}
			cl.PeerPublicKey = v
		case "peer.presharedkey":
			if !ValidKey(v) {
				return cl, nil, fmt.Errorf("invalid pre-shared key")
			}
			cl.PresharedKey = v
		case "peer.endpoint":
			if !ValidEndpoint(v) {
				return cl, nil, fmt.Errorf("invalid server address %q (host:port)", v)
			}
			cl.Endpoint = v
		case "peer.allowedips":
			for _, a := range list() {
				_, n, err := net.ParseCIDR(a)
				if err != nil {
					return cl, nil, fmt.Errorf("invalid allowed IPs %q", a)
				}
				cl.AllowedIPs = append(cl.AllowedIPs, n.String())
			}
		case "peer.persistentkeepalive":
			if strings.EqualFold(v, "off") {
				break
			}
			ka, err := strconv.Atoi(v)
			if err != nil || ka < 0 || ka > 3600 {
				return cl, nil, fmt.Errorf("invalid keepalive interval")
			}
			cl.Keepalive = ka
		case "interface.listenport", "interface.table", "interface.fwmark", "interface.saveconfig",
			"interface.preup", "interface.postup", "interface.predown", "interface.postdown":
			warn = append(warn, "ignored: "+k)
		default:
			warn = append(warn, "unknown setting ignored: "+k)
		}
	}
	switch {
	case peers != 1:
		return cl, nil, fmt.Errorf("the file must have exactly one [Peer] section")
	case cl.PrivateKey == "":
		return cl, nil, fmt.Errorf("the file has no private key")
	case TunnelIP(cl) == "":
		return cl, nil, fmt.Errorf("the file has no IPv4 address")
	case cl.PeerPublicKey == "":
		return cl, nil, fmt.Errorf("the file has no server public key")
	case cl.Endpoint == "":
		return cl, nil, fmt.Errorf("the file has no server address (Endpoint)")
	case !slices.ContainsFunc(cl.AllowedIPs, is4):
		return cl, nil, fmt.Errorf("the file has no IPv4 allowed IPs")
	}
	if !TunnelHasIPv6(cl) {
		warn = append(warn, "the tunnel has no IPv6: IPv6 is blocked for what goes through it")
	}
	return cl, warn, nil
}

func is4(cidr string) bool {
	ip, _, err := net.ParseCIDR(cidr)
	return err == nil && ip.To4() != nil
}

// TunnelIP6 is the IPv6 address of the NAS in the outgoing tunnel.
func TunnelIP6(cl store.VPNClient) string {
	for _, a := range cl.Addresses {
		if ip, _, err := net.ParseCIDR(a); err == nil && ip.To4() == nil {
			return ip.String()
		}
	}
	return ""
}

// TunnelHasIPv6 reports whether IPv6 can go through the tunnel: an IPv6
// address and IPv6 allowed IPs. Otherwise IPv6 is blocked, never sent
// around the tunnel.
func TunnelHasIPv6(cl store.VPNClient) bool {
	return TunnelIP6(cl) != "" && slices.ContainsFunc(cl.AllowedIPs, func(a string) bool { return !is4(a) })
}

// WithoutIPv6 is the tunnel of a NAS without IPv6.
func WithoutIPv6(cl store.VPNClient) store.VPNClient {
	cl.Addresses = slices.DeleteFunc(slices.Clone(cl.Addresses), func(a string) bool { return !is4(a) })
	cl.AllowedIPs = slices.DeleteFunc(slices.Clone(cl.AllowedIPs), func(a string) bool { return !is4(a) })
	return cl
}

// TunnelIP is the IPv4 address of the NAS in the outgoing tunnel.
func TunnelIP(cl store.VPNClient) string {
	for _, a := range cl.Addresses {
		if ip, _, err := net.ParseCIDR(a); err == nil && ip.To4() != nil {
			return ip.String()
		}
	}
	return ""
}

// ValidateClient checks the routing choices of the outgoing tunnel.
func ValidateClient(c store.VPN, cl *store.VPNClient) error {
	switch cl.Scope {
	case "":
		cl.Scope = "apps"
	case "apps", "all":
	default:
		return fmt.Errorf("invalid scope")
	}
	if cl.Name = strings.TrimSpace(cl.Name); cl.Name != "" && !nameRe.MatchString(cl.Name) {
		return fmt.Errorf("invalid name")
	}
	if cl.AppsSubnet == "" {
		cl.AppsSubnet = DefaultAppsSubnet
	}
	n, err := network(cl.AppsSubnet)
	if err != nil {
		return err
	}
	if ones, _ := n.Mask.Size(); ones < 16 || ones > 28 || !n.IP.IsPrivate() {
		return fmt.Errorf("the network of the routed apps must be a private IPv4 network between /16 and /28")
	}
	cl.AppsSubnet = n.String()
	for _, u := range usedNets(store.VPN{Enabled: c.Enabled, Subnet: c.Subnet, LAN: c.LAN, Peers: c.Peers, Links: c.Links}, "") {
		if overlap(u, cl.AppsSubnet) {
			return fmt.Errorf("the network of the routed apps overlaps %s", u)
		}
	}
	if len(cl.Apps) > 64 {
		return fmt.Errorf("too many apps")
	}
	var apps []string
	for _, a := range cl.Apps {
		a = strings.TrimPrefix(strings.TrimSpace(a), "/")
		if !containerRe.MatchString(a) {
			return fmt.Errorf("invalid container name %q", a)
		}
		if !slices.Contains(apps, a) {
			apps = append(apps, a)
		}
	}
	cl.Apps = apps
	if cl.CheckURL != "" {
		u, err := url.Parse(cl.CheckURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(cl.CheckURL) > 512 {
			return fmt.Errorf("the address check URL must be http(s)")
		}
	}
	if cl.Enabled && cl.PrivateKey == "" {
		return fmt.Errorf("import the configuration file of the VPN provider first")
	}
	return nil
}

// RenderClientTunnel returns the wg-quick configuration of strata-out. The
// routes are StrataOS's (Table = off): see ClientRules.
func RenderClientTunnel(cl store.VPNClient) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by StrataOS — changes are overwritten.\n[Interface]\nPrivateKey = %s\nAddress = %s\nTable = off\nFwMark = %#x\n", cl.PrivateKey, strings.Join(cl.Addresses, ", "), FwMark)
	if cl.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", cl.MTU)
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\n", cl.PeerPublicKey)
	if cl.PresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", cl.PresharedKey)
	}
	ka := cl.Keepalive
	if ka == 0 {
		ka = 25
	}
	fmt.Fprintf(&b, "Endpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = %d\n", cl.Endpoint, strings.Join(cl.AllowedIPs, ", "), ka)
	return b.String()
}

// ClientRules returns the `ip` commands of the policy routing, in order
// (v6: the NAS has IPv6). Apps: the Docker network of the routed apps uses
// the tunnel table. All: everything but the marked WireGuard packets. When
// the tunnel is down its route disappears and the kill switch rules
// (KillRules) take the traffic; IPv6 goes into the tunnel only when it has
// IPv6, else straight to the kill switch.
func ClientRules(cl store.VPNClient, v6 bool) [][]string {
	t, p := strconv.Itoa(RouteTable), func(n int) string { return strconv.Itoa(n) }
	fams := []string{"-4"}
	if v6 && TunnelHasIPv6(cl) {
		fams = append(fams, "-6")
	}
	var cmds [][]string
	for _, f := range fams {
		src, apps := TunnelIP(cl), cl.AppsSubnet
		if f == "-6" {
			src, apps = TunnelIP6(cl), AppsSubnet6
		}
		cmds = append(cmds, []string{f, "route", "replace", "default", "dev", OutIface, "table", t})
		if src != "" {
			cmds = append(cmds, []string{f, "rule", "add", "pref", p(prioTunnelSrc), "from", src, "lookup", t})
		}
		if cl.Scope == "all" {
			cmds = append(cmds, []string{f, "rule", "add", "pref", p(prioTunnel), "not", "fwmark", mark(FwMark), "lookup", t})
		} else {
			cmds = append(cmds,
				[]string{f, "rule", "add", "pref", p(prioMain), "from", apps, "lookup", "main", "suppress_prefixlength", "0"},
				[]string{f, "rule", "add", "pref", p(prioTunnel), "from", apps, "lookup", t})
		}
	}
	return cmds
}

func mark(m int) string { return fmt.Sprintf("%#x", m) }

// KillRules are the kill switch rules, kept while the tunnel is on (down
// or not), in the order they must be added. Apps: a blackhole for the
// network of the routed apps, IPv4 and IPv6. All: everything goes into a
// blackhole but the local and private networks (the administrator never
// loses the NAS on the LAN), the packets of the WireGuard interfaces
// (server, sites and the tunnel itself, marked) and, when allowed, APT
// (aptUID, -1: none).
func KillRules(cl store.VPNClient, v6 bool, aptUID int) [][]string {
	p := strconv.Itoa
	fams := []string{"-4"}
	if v6 {
		fams = append(fams, "-6")
	}
	var cmds [][]string
	for _, f := range fams {
		if cl.Scope != "all" {
			apps := cl.AppsSubnet
			if f == "-6" {
				apps = AppsSubnet6
			}
			cmds = append(cmds, []string{f, "rule", "add", "pref", p(prioKill), "from", apps, "blackhole"})
			continue
		}
		private := privateNets4
		if f == "-6" {
			private = privateNets6
		}
		cmds = append(cmds, []string{f, "rule", "add", "pref", p(prioLocal), "lookup", "main", "suppress_prefixlength", "0"})
		for _, n := range private {
			cmds = append(cmds, []string{f, "rule", "add", "pref", p(prioLocal), "to", n, "lookup", "main"})
		}
		if cl.AllowUpdates && aptUID >= 0 {
			u := p(aptUID)
			cmds = append(cmds, []string{f, "rule", "add", "pref", p(prioUpdates), "uidrange", u + "-" + u, "lookup", "main"})
		}
		cmds = append(cmds, []string{f, "rule", "add", "pref", p(prioKillAll), "not", "fwmark", mark(FwMark), "blackhole"})
	}
	return cmds
}

// DockerDropIn puts the kill switch rules of the routed apps in place
// before Docker starts its containers at boot, before StrataOS has set up
// the tunnel.
func DockerDropIn(cl store.VPNClient, v6 bool) string {
	var b strings.Builder
	b.WriteString("# Managed by StrataOS: the apps routed through the outgoing VPN never\n# reach the Internet another way, even before StrataOS has started.\n[Service]\n")
	for _, r := range KillRules(cl, v6, -1) {
		fmt.Fprintf(&b, "ExecStartPre=-/usr/sbin/ip %s rule del pref %d\n", r[0], prioKill)
		ign := ""
		if r[0] == "-6" {
			ign = "-" // IPv6 may be off at boot: IPv4 still protected
		}
		fmt.Fprintf(&b, "ExecStartPre=%s/usr/sbin/ip %s\n", ign, strings.Join(r, " "))
	}
	return b.String()
}

// KillSwitchUnit blocks the Internet of the whole NAS from boot, until
// StrataOS has brought the tunnel up. The rules come in order and systemd
// stops at the first failure: the blackhole never comes without the local
// networks exempted.
func KillSwitchUnit(cl store.VPNClient, v6 bool, aptUID int) string {
	var b strings.Builder
	b.WriteString("# Managed by StrataOS: whole NAS routed through the outgoing VPN.\n[Unit]\nDescription=StrataOS outgoing VPN kill switch\nDefaultDependencies=no\nBefore=network-pre.target\nWants=network-pre.target\n\n[Service]\nType=oneshot\nRemainAfterExit=yes\n")
	for _, r := range KillRules(cl, v6, aptUID) {
		fmt.Fprintf(&b, "ExecStart=/usr/sbin/ip %s\n", strings.Join(r, " "))
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

// Gateway is the first address of a network (the Docker bridge).
func Gateway(subnet string) string { return addr(subnet, 1) }

// Gateway6 is the address of the Docker bridge in AppsSubnet6.
func Gateway6() string {
	p := netip.MustParsePrefix(AppsSubnet6)
	return p.Addr().Next().String()
}

// TunnelPublicIP asks the address check URL, through the tunnel, for the
// address the Internet sees.
func TunnelPublicIP(ctx context.Context, cl store.VPNClient) (string, error) {
	u := cl.CheckURL
	if u == "" {
		u = IPLookupURL
	}
	ip := net.ParseIP(TunnelIP(cl))
	if ip == nil {
		return "", fmt.Errorf("the tunnel has no address")
	}
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}, Timeout: 10 * time.Second}
	hc := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: d.DialContext}}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("the address check failed through the tunnel")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(string(b)), "[]"))
	if err != nil || resp.StatusCode != 200 {
		return "", fmt.Errorf("could not read the public address")
	}
	// Some echo services answer an IPv4 client in IPv4-mapped IPv6 form.
	return addr.Unmap().String(), nil
}
