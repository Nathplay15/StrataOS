package firewall

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Binding is where a guest is on the network right now.
type Binding struct {
	MACs   []string `json:"macs"`   // its MAC addresses
	Ifaces []string `json:"ifaces"` // host side of its interfaces on a bridge (tap, veth)
}

// VNet is the isolation of a virtual network: its guests reach neither the
// local network nor the NAS, apart from DHCP, DNS and the ports listed.
type VNet struct {
	ID       string
	Bridge   string
	Uplinks  []string // ports leading out of the NAS (VLAN or VXLAN link)
	Subnets  []string // the network's own subnets: guests talk freely there
	DNS      []string // DNS servers the guests may use
	NASPorts string   // ports of the NAS open to the guests (TCP and UDP)
	Log      bool
}

// Env is what the rules depend on besides the configuration.
type Env struct {
	NAS      []string           // addresses of the NAS
	Bindings map[string]Binding // by Guest.Key()
	WebPorts []int              // web interface (and SSH): never closed to the LAN
	Peers    []string           // other NAS (cluster, VXLAN): always let in
	VNets    []VNet
}

// Ruleset is the rendered firewall: each table is empty when it has
// nothing to do.
type Ruleset struct {
	Bridge string `json:"bridge"`
	Host   string `json:"host"`
}

const (
	BridgeTable = "strata_fw"   // bridge family: guests and virtual networks
	HostTable   = "strata_host" // inet family: the NAS itself
	LogPrefix   = "strata-fw:"
)

// Private networks: "lan" in the rules.
var lanPrefixes = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16",
	"fc00::/7", "fe80::/10",
}

var (
	macRe   = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)
	ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
	tagRe   = regexp.MustCompile(`[^A-Za-z0-9_.:-]`)
)

// ValidIface reports whether an interface name can be written in a rule.
func ValidIface(n string) bool { return ifaceRe.MatchString(n) }

type renderer struct {
	sets   map[string]IPSet
	groups map[string]Group
}

func newRenderer(c Config) *renderer {
	r := &renderer{sets: map[string]IPSet{}, groups: map[string]Group{}}
	for _, s := range c.IPSets {
		r.sets[s.Name] = s
	}
	for _, g := range c.Groups {
		r.groups[g.Name] = g
	}
	return r
}

func prefixes(list []string) (v4, v6 []netip.Prefix) {
	for _, s := range list {
		p, err := parsePrefix(s)
		if err != nil {
			continue
		}
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	return mergePrefixes(v4), mergePrefixes(v6)
}

// hostAddrs keeps the addresses of "192.168.1.10/24" (an interface address,
// not its network).
func hostAddrs(list []string) []string {
	var out []string
	for _, s := range list {
		if a, _, ok := strings.Cut(s, "/"); ok {
			s = a
		}
		out = append(out, s)
	}
	return out
}

func elem(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

func elems(ps []netip.Prefix) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = elem(p)
	}
	return strings.Join(out, ", ")
}

func quoted(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strconv.Quote(n)
	}
	return strings.Join(out, ", ")
}

// set writes an interval set of addresses.
func set(b *strings.Builder, name string, v6 bool, ps []netip.Prefix) {
	typ := "ipv4_addr"
	if v6 {
		typ = "ipv6_addr"
	}
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n", name, typ)
	if len(ps) > 0 {
		fmt.Fprintf(b, "\t\telements = { %s }\n", elems(ps))
	}
	b.WriteString("\t}\n")
}

func (r *renderer) commonSets(b *strings.Builder, nas []string) {
	l4, l6 := prefixes(lanPrefixes)
	set(b, "lan4", false, l4)
	set(b, "lan6", true, l6)
	n4, n6 := prefixes(hostAddrs(nas))
	set(b, "nas4", false, n4)
	set(b, "nas6", true, n6)
	names := make([]string, 0, len(r.sets))
	for n := range r.sets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v4, v6 := prefixes(r.sets[n].Entries)
		set(b, "s_"+n+"4", false, v4)
		set(b, "s_"+n+"6", true, v6)
	}
}

// side renders one address side for a family ("" = no condition; ok false:
// the side cannot match that family).
func side(a addr, field string, v6 bool) (string, bool) {
	l3 := "ip"
	fam := "4"
	list := a.v4
	if v6 {
		l3, fam, list = "ip6", "6", a.v6
	}
	switch {
	case a.any:
		return "", true
	case a.keyset != "":
		return fmt.Sprintf("%s %s @%s%s", l3, field, a.keyset, fam), true
	case len(list) > 0:
		return fmt.Sprintf("%s %s { %s }", l3, field, elems(list)), true
	}
	return "", false
}

// matches returns the match expressions of a rule, one per address family
// it applies to (a single one when no address is involved).
func (r *renderer) matches(ru Rule) []string {
	src, err1 := parseAddr(ru.Source, r.sets)
	dst, err2 := parseAddr(ru.Dest, r.sets)
	if err1 != nil || err2 != nil {
		return nil
	}
	var proto []string
	dports, _ := parsePorts(ru.Ports)
	sports, _ := parsePorts(ru.SPorts)
	switch ru.Proto {
	case "tcp", "udp":
		if len(sports) > 0 {
			proto = append(proto, fmt.Sprintf("%s sport { %s }", ru.Proto, strings.Join(sports, ", ")))
		}
		if len(dports) > 0 {
			proto = append(proto, fmt.Sprintf("%s dport { %s }", ru.Proto, strings.Join(dports, ", ")))
		}
		if len(proto) == 0 {
			proto = append(proto, "meta l4proto "+ru.Proto)
		}
	case "tcpudp":
		proto = append(proto, "meta l4proto { tcp, udp }")
		if len(sports) > 0 {
			proto = append(proto, fmt.Sprintf("th sport { %s }", strings.Join(sports, ", ")))
		}
		if len(dports) > 0 {
			proto = append(proto, fmt.Sprintf("th dport { %s }", strings.Join(dports, ", ")))
		}
	case "icmp":
		proto = append(proto, "meta l4proto icmp")
	case "icmpv6":
		proto = append(proto, "meta l4proto ipv6-icmp")
	}
	tail := strings.Join(proto, " ")
	join := func(parts ...string) string {
		var keep []string
		for _, p := range parts {
			if p != "" {
				keep = append(keep, p)
			}
		}
		return strings.Join(keep, " ")
	}
	if src.any && dst.any {
		return []string{tail}
	}
	var out []string
	for _, v6 := range []bool{false, true} {
		if (ru.Proto == "icmp" && v6) || (ru.Proto == "icmpv6" && !v6) {
			continue
		}
		s, ok1 := side(src, "saddr", v6)
		d, ok2 := side(dst, "daddr", v6)
		if ok1 && ok2 {
			out = append(out, join(s, d, tail))
		}
	}
	return out
}

func logStmt(tag string) string {
	tag = tagRe.ReplaceAllString(tag, "_")
	if len(tag) > 100 {
		tag = tag[:100]
	}
	return fmt.Sprintf("limit rate 10/minute burst 5 packets log prefix %q level info", LogPrefix+tag+" ")
}

// verdict maps an action to nftables; reject only exists where packets enter.
func verdict(action string, canReject bool) string {
	if action == "reject" && !canReject {
		return "drop"
	}
	return action
}

// rules writes the rules of one direction into a chain.
func (r *renderer) rules(b *strings.Builder, rules []Rule, dir string, canReject bool, tag string) {
	for _, ru := range expand(rules, r.groups) {
		if ru.Dir != dir {
			continue
		}
		v := verdict(ru.Action, canReject)
		for _, m := range r.matches(ru) {
			if ru.Log {
				fmt.Fprintf(b, "\t\t%s\n", strings.TrimSpace(m+" "+logStmt(tag)))
			}
			fmt.Fprintf(b, "\t\t%s\n", strings.TrimSpace(m+" "+v))
		}
	}
}

func policy(b *strings.Builder, p string, canReject, log bool, tag string) {
	if log && p != "accept" {
		fmt.Fprintf(b, "\t\t%s\n", logStmt(tag))
	}
	fmt.Fprintf(b, "\t\t%s\n", verdict(p, canReject))
}

func orDefault(p, def string) string {
	if p == "" {
		return def
	}
	return p
}

// Render turns a configuration into the nftables tables.
func Render(c Config, env Env) Ruleset {
	r := newRenderer(c)
	return Ruleset{Bridge: r.bridge(c, env), Host: r.host(c, env)}
}

func cleanList(in []string, ok func(string) bool) []string {
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if ok(s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// bridge renders the bridge table: the guests' firewalls (classified by
// interface, then by MAC address, so a guest is filtered from its first frame
// even before its interface is known) and the isolation of virtual networks.
func (r *renderer) bridge(c Config, env Env) string {
	var guests []Guest
	for _, g := range c.Guests {
		if g.Enabled {
			guests = append(guests, g)
		}
	}
	slices.SortFunc(guests, func(a, b Guest) int { return strings.Compare(a.Key(), b.Key()) })
	var vnets []VNet
	for _, v := range env.VNets {
		if ValidIface(v.Bridge) {
			vnets = append(vnets, v)
		}
	}
	if len(guests) == 0 && len(vnets) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table bridge %s {\n", BridgeTable)
	r.commonSets(&b, env.NAS)

	type entry struct{ key, chain string }
	var outIf, inIf, outMAC, inMAC []entry
	usedIf, usedMAC := map[string]bool{}, map[string]bool{}
	var chains strings.Builder
	for i, g := range guests {
		bd := env.Bindings[g.Key()]
		macs := cleanList(bd.MACs, macRe.MatchString)
		var ifaces []string
		for _, n := range bd.Ifaces {
			if ifaceRe.MatchString(n) && !slices.Contains(ifaces, n) {
				ifaces = append(ifaces, n)
			}
		}
		out, in := fmt.Sprintf("g%d_out", i), fmt.Sprintf("g%d_in", i)
		for _, n := range ifaces {
			if !usedIf[n] {
				usedIf[n] = true
				outIf = append(outIf, entry{strconv.Quote(n), out})
				inIf = append(inIf, entry{strconv.Quote(n), in})
			}
		}
		for _, m := range macs {
			if !usedMAC[m] {
				usedMAC[m] = true
				outMAC = append(outMAC, entry{m, out})
				inMAC = append(inMAC, entry{m, in})
			}
		}
		tag := strings.ReplaceAll(g.Key(), ":", ".")

		// From the guest.
		fmt.Fprintf(&chains, "\tchain %s {\n", out)
		if g.MACFilter && len(ifaces) > 0 && len(macs) > 0 {
			fmt.Fprintf(&chains, "\t\tiifname { %s } ether saddr != { %s } %s\n", quoted(ifaces), strings.Join(macs, ", "), "drop")
		}
		chains.WriteString(guestHead)
		chains.WriteString("\t\ticmpv6 type { nd-router-solicit, nd-neighbor-solicit, nd-neighbor-advert } accept\n")
		chains.WriteString("\t\tudp sport 68 udp dport 67 accept\n\t\tudp sport 546 udp dport 547 accept\n")
		r.rules(&chains, g.Rules, "out", true, tag+":out")
		if g.Isolate {
			chains.WriteString("\t\tmeta l4proto { tcp, udp } th dport 53 accept\n")
			for _, s := range []string{"ip daddr @nas4", "ip6 daddr @nas6", "ip daddr @lan4", "ip6 daddr @lan6"} {
				if g.LogDrops {
					fmt.Fprintf(&chains, "\t\t%s %s\n", s, logStmt(tag+":out"))
				}
				fmt.Fprintf(&chains, "\t\t%s drop\n", s)
			}
		}
		policy(&chains, orDefault(g.PolicyOut, "accept"), true, g.LogDrops, tag+":out")
		chains.WriteString("\t}\n")

		// To the guest.
		fmt.Fprintf(&chains, "\tchain %s {\n", in)
		chains.WriteString(guestHead)
		chains.WriteString("\t\ticmpv6 type { nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert, nd-redirect } accept\n")
		chains.WriteString("\t\tudp sport 67 udp dport 68 accept\n\t\tudp sport 547 udp dport 546 accept\n")
		r.rules(&chains, g.Rules, "in", false, tag+":in")
		policy(&chains, orDefault(g.PolicyIn, "drop"), false, g.LogDrops, tag+":in")
		chains.WriteString("\t}\n")
	}

	vmap := func(sel string, es []entry) {
		if len(es) == 0 {
			return
		}
		parts := make([]string, len(es))
		for i, e := range es {
			parts[i] = e.key + " : jump " + e.chain
		}
		fmt.Fprintf(&b, "\t\t%s vmap { %s }\n", sel, strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "\tchain guests_pre {\n\t\ttype filter hook prerouting priority 0; policy accept;\n")
	vmap("iifname", outIf)
	vmap("ether saddr", outMAC)
	b.WriteString("\t}\n")
	fmt.Fprintf(&b, "\tchain guests_post {\n\t\ttype filter hook postrouting priority 0; policy accept;\n")
	vmap("oifname", inIf)
	vmap("ether daddr", inMAC)
	b.WriteString("\t}\n")

	// Virtual networks: what leaves through the NAS (input: the NAS itself
	// and what it routes) or through an uplink (forward).
	var in, fwd strings.Builder
	for i, v := range vnets {
		ch := fmt.Sprintf("n%d", i)
		var up []string
		for _, u := range v.Uplinks {
			if ifaceRe.MatchString(u) {
				up = append(up, u)
			}
		}
		notUp := ""
		if len(up) > 0 {
			notUp = fmt.Sprintf(" iifname != { %s }", quoted(up))
			fmt.Fprintf(&fwd, "\t\tmeta ibrname %q%s oifname { %s } jump %s\n", v.Bridge, notUp, quoted(up), ch)
		}
		fmt.Fprintf(&in, "\t\tmeta ibrname %q%s jump %s\n", v.Bridge, notUp, ch)

		tag := "vnet." + v.ID
		fmt.Fprintf(&chains, "\tchain %s {\n", ch)
		chains.WriteString("\t\tct state established,related return\n\t\tudp dport 67 return\n")
		if d4, d6 := prefixes(v.DNS); len(d4)+len(d6) > 0 {
			if len(d4) > 0 {
				fmt.Fprintf(&chains, "\t\tip daddr { %s } meta l4proto { tcp, udp } th dport 53 return\n", elems(d4))
			}
			if len(d6) > 0 {
				fmt.Fprintf(&chains, "\t\tip6 daddr { %s } meta l4proto { tcp, udp } th dport 53 return\n", elems(d6))
			}
		}
		chains.WriteString("\t\tip daddr @nas4 icmp type echo-request return\n")
		if ports, err := parsePorts(v.NASPorts); err == nil && len(ports) > 0 {
			fmt.Fprintf(&chains, "\t\tip daddr @nas4 meta l4proto { tcp, udp } th dport { %s } return\n", strings.Join(ports, ", "))
			fmt.Fprintf(&chains, "\t\tip6 daddr @nas6 meta l4proto { tcp, udp } th dport { %s } return\n", strings.Join(ports, ", "))
		}
		drop := func(m string) {
			if v.Log {
				fmt.Fprintf(&chains, "\t\t%s %s\n", m, logStmt(tag))
			}
			fmt.Fprintf(&chains, "\t\t%s drop\n", m)
		}
		drop("ip daddr @nas4")
		drop("ip6 daddr @nas6")
		if s4, s6 := prefixes(v.Subnets); len(s4)+len(s6) > 0 {
			if len(s4) > 0 {
				fmt.Fprintf(&chains, "\t\tip daddr { %s } return\n", elems(s4))
			}
			if len(s6) > 0 {
				fmt.Fprintf(&chains, "\t\tip6 daddr { %s } return\n", elems(s6))
			}
		}
		drop("ip daddr @lan4")
		drop("ip6 daddr @lan6")
		chains.WriteString("\t}\n")
	}
	if in.Len() > 0 {
		fmt.Fprintf(&b, "\tchain vnets_in {\n\t\ttype filter hook input priority 0; policy accept;\n%s\t}\n", in.String())
	}
	if fwd.Len() > 0 {
		fmt.Fprintf(&b, "\tchain vnets_fwd {\n\t\ttype filter hook forward priority 0; policy accept;\n%s\t}\n", fwd.String())
	}
	b.WriteString(chains.String())
	b.WriteString("}\n")
	return b.String()
}

// guestHead starts each guest chain. ARP comes first: a frame without
// connection tracking (anything but IP) counts as "ct state invalid".
const guestHead = "\t\tether type arp accept\n\t\tct state established,related accept\n\t\tmeta protocol { ip, ip6 } ct state invalid drop\n"

// Internal bridges of guests: their traffic to the NAS is the business of
// each guest's firewall and of the virtual networks' isolation.
var guestBridges = []string{"lxcbr*", "virbr*", "docker*", "br-*", "vn*"}

// host renders the firewall of the NAS (incoming traffic).
func (r *renderer) host(c Config, env Env) string {
	h := c.Host
	if !h.Enabled {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\n", HostTable)
	r.commonSets(&b, env.NAS)
	p4, p6 := prefixes(env.Peers)
	set(&b, "peers4", false, p4)
	set(&b, "peers6", true, p6)
	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter + 5; policy accept;\n")
	b.WriteString("\t\tiif \"lo\" accept\n\t\tct state established,related accept\n\t\tct state invalid drop\n")
	for _, g := range guestBridges {
		fmt.Fprintf(&b, "\t\tiifname %q accept\n", g)
	}
	// Anti-lockout: the web interface and SSH from the local network.
	var web []string
	for _, p := range env.WebPorts {
		if p > 0 && p < 65536 && !slices.Contains(web, strconv.Itoa(p)) {
			web = append(web, strconv.Itoa(p))
		}
	}
	if len(web) > 0 {
		fmt.Fprintf(&b, "\t\ttcp dport { %s } ip saddr @lan4 accept\n", strings.Join(web, ", "))
		fmt.Fprintf(&b, "\t\ttcp dport { %s } ip6 saddr @lan6 accept\n", strings.Join(web, ", "))
	}
	b.WriteString("\t\tip saddr @peers4 accept\n\t\tip6 saddr @peers6 accept\n")
	b.WriteString("\t\ticmp type { echo-request, destination-unreachable, time-exceeded, parameter-problem } accept\n")
	b.WriteString("\t\ticmpv6 type { echo-request, destination-unreachable, packet-too-big, time-exceeded, parameter-problem, nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert, mld-listener-query } accept\n")
	b.WriteString("\t\tudp sport 67 udp dport 68 accept\n\t\tudp dport 546 accept\n")
	r.rules(&b, h.Rules, "in", true, "host:in")
	policy(&b, orDefault(h.PolicyIn, "drop"), true, h.LogDrops, "host:in")
	b.WriteString("\t}\n}\n")
	return b.String()
}

// Script is the nftables input that replaces both tables in one transaction
// (a table is declared before its deletion so the deletion never fails).
func Script(rs Ruleset) string {
	var b strings.Builder
	for _, t := range []struct{ fam, name, body string }{{"bridge", BridgeTable, rs.Bridge}, {"inet", HostTable, rs.Host}} {
		fmt.Fprintf(&b, "table %s %s\ndelete table %s %s\n", t.fam, t.name, t.fam, t.name)
		b.WriteString(t.body)
	}
	return b.String()
}
