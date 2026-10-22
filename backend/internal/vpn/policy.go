package vpn

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"strataos/internal/store"
)

// Services a peer can be restricted to, as protocol/port.
var Services = map[string][]string{
	"smb": {"tcp/139", "tcp/445"},
	"web": {"tcp/80", "tcp/443", "tcp/7070", "tcp/7443"},
	"nfs": {"tcp/111", "udp/111", "tcp/2049", "udp/2049"},
	"ssh": {"tcp/22"},
	"dns": {"tcp/53", "udp/53"},
}

var portRe = regexp.MustCompile(`^(tcp|udp)/([0-9]{1,5})(?:-([0-9]{1,5}))?$`)

// ValidateServices normalises a list of services (smb, web…) and ports
// (tcp/8080, udp/5000-5010).
func ValidateServices(list []string) ([]string, error) {
	var out []string
	for _, s := range list {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if _, ok := Services[s]; !ok {
			m := portRe.FindStringSubmatch(s)
			if m == nil {
				return nil, fmt.Errorf("unknown service or port %q (e.g. smb, web, tcp/8080)", s)
			}
			lo, _ := strconv.Atoi(m[2])
			hi := lo
			if m[3] != "" {
				hi, _ = strconv.Atoi(m[3])
			}
			if lo < 1 || hi > 65535 || hi < lo {
				return nil, fmt.Errorf("invalid port %q", s)
			}
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) > 32 {
		return nil, fmt.Errorf("too many services")
	}
	return out, nil
}

// ports splits services into the TCP and UDP ports nftables accepts.
func ports(list []string) (tcp, udp []string) {
	add := func(p string) {
		m := portRe.FindStringSubmatch(p)
		if m == nil {
			return
		}
		v := m[2]
		if m[3] != "" {
			v += "-" + m[3]
		}
		if m[1] == "tcp" && !slices.Contains(tcp, v) {
			tcp = append(tcp, v)
		} else if m[1] == "udp" && !slices.Contains(udp, v) {
			udp = append(udp, v)
		}
	}
	for _, s := range list {
		if ps, ok := Services[s]; ok {
			for _, p := range ps {
				add(p)
			}
		} else {
			add(s)
		}
	}
	return tcp, udp
}

// Active reports whether a peer may connect: enabled, not expired, and its
// owner (when it has one) is an enabled account.
func Active(p store.VPNPeer, now time.Time, ownerOK func(string) bool) bool {
	if p.Disabled || p.Expired || (!p.Expires.IsZero() && !now.Before(p.Expires)) {
		return false
	}
	return p.Owner == "" || (ownerOK != nil && ownerOK(p.Owner))
}

// Effective returns c with the peers that may not connect disabled: what
// goes into the WireGuard and firewall configuration.
func Effective(c store.VPN, now time.Time, ownerOK func(string) bool) store.VPN {
	c.Peers = slices.Clone(c.Peers)
	for i := range c.Peers {
		if !Active(c.Peers[i], now, ownerOK) {
			c.Peers[i].Disabled = true
		}
	}
	return c
}

// Expire disables the peers whose expiry date has passed and returns them.
func Expire(c *store.VPN, now time.Time) []store.VPNPeer {
	var out []store.VPNPeer
	for i := range c.Peers {
		p := &c.Peers[i]
		if !p.Disabled && !p.Expires.IsZero() && !now.Before(p.Expires) {
			p.Disabled, p.Expired = true, true
			out = append(out, *p)
		}
	}
	return out
}

// WarnDays is how many days before its expiry a device is announced to
// the administrator (0: never).
func WarnDays(c store.VPN) int {
	switch {
	case c.ExpiryWarnDays < 0:
		return 0
	case c.ExpiryWarnDays == 0:
		return 7
	}
	return c.ExpiryWarnDays
}

// ExpiringSoon marks and returns the peers that expire within WarnDays and
// were not announced yet: once per device and expiry date.
func ExpiringSoon(c *store.VPN, now time.Time) []store.VPNPeer {
	days := WarnDays(*c)
	if days == 0 {
		return nil
	}
	var out []store.VPNPeer
	for i := range c.Peers {
		p := &c.Peers[i]
		if p.Disabled || p.Expired || p.Expires.IsZero() || !now.Before(p.Expires) || p.ExpiryWarned.Equal(p.Expires) ||
			p.Expires.Sub(now) > time.Duration(days)*24*time.Hour {
			continue
		}
		p.ExpiryWarned = p.Expires
		out = append(out, *p)
	}
	return out
}

// LastDay is the last day a peer may connect (its expiry is the start of
// the next day).
func LastDay(p store.VPNPeer) string {
	return p.Expires.Add(-time.Second).In(time.Local).Format("2006-01-02")
}

// ParseExpiry reads an expiry date: "" (never), a day (2026-12-31, the end
// of that day) or an RFC 3339 time.
func ParseExpiry(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t.AddDate(0, 0, 1), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid expiry date")
}

// lanTargets are the networks a "lan" device reaches: the local network
// and the sites.
func lanTargets(c store.VPN) []string {
	var out []string
	if c.LAN != "" {
		out = append(out, c.LAN)
	}
	for _, p := range c.Peers {
		if p.Kind == "site" && !p.Disabled {
			out = append(out, p.Subnets...)
		}
	}
	for _, l := range c.Links {
		if !l.Disabled {
			out = append(out, l.Subnets...)
		}
	}
	return out
}

func set(xs []string) string { return "{ " + strings.Join(xs, ", ") + " }" }

func quoted(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = `"` + x + `"`
	}
	return out
}

// ifaces are the WireGuard interfaces of the server and the site links.
func ifaces(c store.VPN) []string {
	var out []string
	if c.Enabled {
		out = append(out, Iface)
	}
	for _, l := range c.Links {
		if !l.Disabled {
			out = append(out, l.Iface)
		}
	}
	return out
}

// RenderFirewall returns the nftables table of the VPN (inet strata_wg):
// per peer access mode and service restrictions, site forwarding, NAT of
// the VPN networks, and the kill switch of the routed apps. "" when
// nothing needs it.
func RenderFirewall(c store.VPN) string {
	wgs := ifaces(c)
	killSwitch := ClientActive(c.Client) && c.Client.Scope == "apps"
	if len(wgs) == 0 && !killSwitch {
		return ""
	}
	var b, peers, reach, chains, nat strings.Builder
	local := lanTargets(c)
	n := 0
	if c.Enabled {
		for _, p := range c.Peers {
			if p.Disabled {
				continue
			}
			src := p.IP
			if p.Kind == "site" {
				src = set(append([]string{p.IP}, p.Subnets...))
			}
			// Access mode, enforced here and not only in the device's file.
			switch {
			case p.Kind == "site":
				dst := append([]string{c.Subnet}, slices.DeleteFunc(slices.Clone(local), func(x string) bool { return slices.Contains(p.Subnets, x) })...)
				fmt.Fprintf(&reach, "    ip saddr %s ip daddr != %s drop\n", src, set(dst))
			case p.Mode == "nas":
				fmt.Fprintf(&reach, "    ip saddr %s drop\n", src)
			case p.Mode == "lan" && len(local) == 0:
				fmt.Fprintf(&reach, "    ip saddr %s drop\n", src)
			case p.Mode == "lan":
				fmt.Fprintf(&reach, "    ip saddr %s ip daddr != %s drop\n", src, set(local))
			}
			if len(p.Services) == 0 {
				continue
			}
			tcp, udp := ports(p.Services)
			fmt.Fprintf(&peers, "    ip saddr %s jump p%d\n", src, n)
			fmt.Fprintf(&chains, "  chain p%d {\n    meta l4proto icmp accept\n", n)
			if len(tcp) > 0 {
				fmt.Fprintf(&chains, "    tcp dport %s accept\n", set(tcp))
			}
			if len(udp) > 0 {
				fmt.Fprintf(&chains, "    udp dport %s accept\n", set(udp))
			}
			chains.WriteString("    counter drop\n  }\n")
			n++
		}
	}
	b.WriteString("table inet strata_wg\ndelete table inet strata_wg\ntable inet strata_wg {\n")
	if c.Enabled {
		fmt.Fprintf(&b, "  chain input {\n    type filter hook input priority filter - 10; policy accept;\n    iifname \"%s\" jump peers\n  }\n", Iface)
	}
	b.WriteString("  chain forward {\n    type filter hook forward priority filter - 10; policy accept;\n")
	if killSwitch {
		// Before "established": a flow that started in the tunnel must not
		// carry on through the normal Internet access when the tunnel drops.
		fmt.Fprintf(&b, "    iifname \"%s\" oifname != { \"%s\", \"%s\" } ip daddr != %s counter drop\n", AppsBridge, OutIface, AppsBridge, set(privateNets4))
		// IPv6 likewise, or nowhere but the private networks when the
		// tunnel has no IPv6.
		out6 := set(quoted([]string{AppsBridge}))
		if TunnelHasIPv6(c.Client) {
			out6 = set(quoted([]string{OutIface, AppsBridge}))
		}
		fmt.Fprintf(&b, "    iifname \"%s\" oifname != %s ip6 daddr != %s counter drop\n", AppsBridge, out6, set(privateNets6))
	}
	if len(wgs) > 0 {
		b.WriteString("    ct state established,related accept\n")
	}
	if c.Enabled {
		// reach only drops: the access mode first, then the services.
		fmt.Fprintf(&b, "    iifname \"%s\" jump reach\n    iifname \"%s\" jump peers\n", Iface, Iface)
	}
	for _, l := range c.Links {
		if !l.Disabled {
			// The other NAS reaches the local network and the VPN, nothing else.
			dst := []string{}
			if c.LAN != "" {
				dst = append(dst, c.LAN)
			}
			if c.Enabled {
				dst = append(dst, c.Subnet)
			}
			if len(dst) == 0 {
				fmt.Fprintf(&b, "    iifname \"%s\" drop\n", l.Iface)
			} else {
				fmt.Fprintf(&b, "    iifname \"%s\" ip daddr != %s drop\n", l.Iface, set(dst))
			}
		}
	}
	b.WriteString("  }\n")
	if c.Enabled {
		fmt.Fprintf(&b, "  chain peers {\n    ct state established,related accept\n%s  }\n", peers.String())
		fmt.Fprintf(&b, "  chain reach {\n%s  }\n", reach.String())
		b.WriteString(chains.String())
	}
	if len(wgs) > 0 {
		// Peers reach the local network with the NAS address: no route to
		// add on the router.
		src := []string{}
		if c.Enabled {
			src = append(src, c.Subnet)
			for _, p := range c.Peers {
				if p.Kind == "site" && !p.Disabled {
					src = append(src, p.Subnets...)
				}
			}
		}
		for _, l := range c.Links {
			if !l.Disabled {
				src = append(src, l.Subnets...)
			}
		}
		if len(src) > 0 {
			fmt.Fprintf(&nat, "    ip saddr %s oifname != %s masquerade\n", set(src), set(quoted(wgs)))
		}
	}
	if killSwitch && TunnelHasIPv6(c.Client) {
		// Docker does not translate IPv6 (before 27, or ip6tables off).
		fmt.Fprintf(&nat, "    ip6 saddr %s oifname \"%s\" masquerade\n", AppsSubnet6, OutIface)
	}
	if nat.Len() > 0 {
		fmt.Fprintf(&b, "  chain postrouting {\n    type nat hook postrouting priority srcnat; policy accept;\n%s  }\n", nat.String())
	}
	b.WriteString("}\n")
	return b.String()
}

// NeedsForward reports whether the NAS must route packets for the VPN.
func NeedsForward(c store.VPN) bool {
	if ClientActive(c.Client) {
		return true
	}
	for _, l := range c.Links {
		if !l.Disabled {
			return true
		}
	}
	if !c.Enabled {
		return false
	}
	for _, p := range c.Peers {
		if !p.Disabled && (p.Kind == "site" || p.Mode != "nas") {
			return true
		}
	}
	return false
}

// siteRoutes are the remote networks routed through the server interface.
func siteRoutes(c store.VPN) []string {
	var out []string
	for _, p := range c.Peers {
		if p.Kind == "site" && !p.Disabled {
			out = append(out, p.Subnets...)
		}
	}
	sort.Strings(out)
	return out
}

// routePlan compares `ip -4 route show dev X` with the wanted networks and
// returns the routes to add and to delete. Connected routes and addresses
// of the VPN network (added by wg-quick for the devices) stay.
func routePlan(current string, want []string, vpnNet string) (add, del []string) {
	_, vn, _ := net.ParseCIDR(vpnNet)
	have := map[string]bool{}
	for _, l := range strings.Split(current, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 || strings.Contains(l, "proto kernel") || f[0] == "default" {
			continue
		}
		dst := f[0]
		if !strings.Contains(dst, "/") {
			dst += "/32"
		}
		if ip, n, err := net.ParseCIDR(dst); err == nil {
			dst = n.String()
			if vn != nil && vn.Contains(ip) {
				continue
			}
		}
		have[dst] = true
		if !slices.Contains(want, dst) {
			del = append(del, dst)
		}
	}
	for _, w := range want {
		if !have[w] {
			add = append(add, w)
		}
	}
	return add, del
}
