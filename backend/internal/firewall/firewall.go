// Package firewall is the distributed firewall of StrataOS: rules attached to
// each guest (VM, LXC container, Docker container), security groups, named
// address sets, the isolation of virtual networks and a firewall for the NAS
// itself. Everything is rendered as two nftables tables of their own (bridge
// strata_fw for the guests and virtual networks, inet strata_host for the
// NAS) and loaded in one transaction.
package firewall

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Rule is one line of a rule list. A rule naming a Group stands for the
// rules of that security group, in place.
type Rule struct {
	Disabled bool   `json:"disabled,omitempty"`
	Dir      string `json:"dir"`              // in | out (seen from the guest; the NAS: in only)
	Action   string `json:"action"`           // accept | drop | reject
	Proto    string `json:"proto,omitempty"`  // "" (any) | tcp | udp | tcpudp | icmp | icmpv6
	Ports    string `json:"ports,omitempty"`  // destination ports: 80,443,8000-8100
	SPorts   string `json:"sports,omitempty"` // source ports
	Source   string `json:"source,omitempty"` // "" | lan | nas | set:<name> | addresses and CIDRs
	Dest     string `json:"dest,omitempty"`
	Group    string `json:"group,omitempty"`
	Log      bool   `json:"log,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// Group is a security group: a named rule list several guests (or the NAS) use.
type Group struct {
	Name    string `json:"name"`
	Comment string `json:"comment,omitempty"`
	Rules   []Rule `json:"rules"`
}

// IPSet is a named list of addresses and networks (an alias), used in rules
// as set:<name>.
type IPSet struct {
	Name    string   `json:"name"`
	Comment string   `json:"comment,omitempty"`
	Entries []string `json:"entries"`
}

// Guest is the firewall of a VM or container. Its rules apply to every
// network interface it has on a bridge of the NAS.
type Guest struct {
	Kind    string `json:"kind"` // vm | lxc | docker
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// PolicyIn / PolicyOut: what happens to traffic no rule matched
	// (accept | drop | reject; "" means drop in, accept out).
	PolicyIn  string `json:"policyIn,omitempty"`
	PolicyOut string `json:"policyOut,omitempty"`
	// Isolate: the guest reaches the Internet but neither the local network
	// nor the NAS (DNS and DHCP excepted); out rules come first, so an
	// accept rule opens a port of the NAS or a LAN address.
	Isolate bool `json:"isolate,omitempty"`
	// MACFilter: frames leaving the guest with another MAC address are dropped.
	MACFilter bool   `json:"macFilter,omitempty"`
	LogDrops  bool   `json:"logDrops,omitempty"`
	Rules     []Rule `json:"rules"`
}

// Key identifies a guest: vm:web, lxc:ha, docker:nginx.
func (g Guest) Key() string { return g.Kind + ":" + g.Name }

// Host is the firewall of the NAS itself (incoming traffic). The web
// interface stays reachable from the local network whatever the rules say.
type Host struct {
	Enabled  bool   `json:"enabled"`
	PolicyIn string `json:"policyIn,omitempty"` // drop | reject | accept ("" = drop)
	LogDrops bool   `json:"logDrops,omitempty"`
	Rules    []Rule `json:"rules"`
}

// Config is the whole firewall (State.Firewall).
type Config struct {
	Host   Host    `json:"host"`
	Guests []Guest `json:"guests,omitempty"`
	Groups []Group `json:"groups,omitempty"`
	IPSets []IPSet `json:"ipsets,omitempty"`
}

// DefaultHost is the NAS firewall proposed when it is first switched on:
// everything from the local network, the web interface from anywhere.
func DefaultHost() Host {
	return Host{PolicyIn: "drop", LogDrops: true, Rules: []Rule{
		{Dir: "in", Action: "accept", Source: "lan", Comment: "Local network"},
		{Dir: "in", Action: "accept", Proto: "tcp", Ports: "7070,7443", Comment: "Web interface"},
	}}
}

// Guest returns the firewall of a guest (zero when it has none).
func (c Config) Guest(kind, name string) Guest {
	for _, g := range c.Guests {
		if g.Kind == kind && g.Name == name {
			return g
		}
	}
	return Guest{Kind: kind, Name: name, Rules: []Rule{}}
}

// SetGuest replaces (or adds) the firewall of a guest; a guest with nothing
// set is removed from the list.
func (c *Config) SetGuest(g Guest) {
	c.Guests = slices.DeleteFunc(c.Guests, func(x Guest) bool { return x.Kind == g.Kind && x.Name == g.Name })
	if g.Enabled || len(g.Rules) > 0 || g.Isolate || g.MACFilter {
		c.Guests = append(c.Guests, g)
		slices.SortFunc(c.Guests, func(a, b Guest) int { return strings.Compare(a.Key(), b.Key()) })
	}
}

// RenameGuest follows a guest renamed or removed (to == "").
func (c *Config) RenameGuest(kind, from, to string) {
	for i := range c.Guests {
		if c.Guests[i].Kind == kind && c.Guests[i].Name == from {
			if to == "" {
				c.Guests = slices.Delete(c.Guests, i, i+1)
			} else {
				c.Guests[i].Name = to
			}
			return
		}
	}
}

var (
	setNameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	guestNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	actions     = []string{"accept", "drop", "reject"}
	protos      = []string{"", "tcp", "udp", "tcpudp", "icmp", "icmpv6"}
)

// ValidPorts checks a port list: 80,443,8000-8100 (32 items at most).
func ValidPorts(s string) error {
	_, err := parsePorts(s)
	return err
}

func parsePorts(s string) ([]string, error) {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		lo, hi, isRange := strings.Cut(f, "-")
		a, err1 := strconv.Atoi(lo)
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(hi)
		}
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b || (isRange && a == b) {
			return nil, fmt.Errorf("invalid port %q (e.g. 80,443,8000-8100)", f)
		}
		if isRange {
			out = append(out, fmt.Sprintf("%d-%d", a, b))
		} else {
			out = append(out, strconv.Itoa(a))
		}
	}
	if len(out) > 32 {
		return nil, fmt.Errorf("at most 32 ports or ranges per rule")
	}
	return out, nil
}

// addr is a parsed source or destination.
type addr struct {
	any    bool
	keyset string // lan | nas | s_<name>: named sets, one per family
	v4, v6 []netip.Prefix
}

func parseAddr(s string, sets map[string]IPSet) (addr, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || s == "any":
		return addr{any: true}, nil
	case s == "lan" || s == "nas":
		return addr{keyset: s}, nil
	case strings.HasPrefix(s, "set:"):
		n := strings.TrimPrefix(s, "set:")
		if _, ok := sets[n]; !ok {
			return addr{}, fmt.Errorf("unknown address set %q", n)
		}
		return addr{keyset: "s_" + n}, nil
	}
	var a addr
	items := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	if len(items) > 64 {
		return addr{}, fmt.Errorf("at most 64 addresses per rule (use an address set)")
	}
	for _, f := range items {
		p, err := parsePrefix(f)
		if err != nil {
			return addr{}, err
		}
		if p.Addr().Is4() {
			a.v4 = append(a.v4, p)
		} else {
			a.v6 = append(a.v6, p)
		}
	}
	a.v4, a.v6 = mergePrefixes(a.v4), mergePrefixes(a.v6)
	return a, nil
}

// parsePrefix accepts an address or a network (CIDR).
func parsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if p, err := netip.ParsePrefix(s); err == nil {
		return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked(), nil
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	return netip.Prefix{}, fmt.Errorf("invalid address %q (e.g. 192.168.1.20, 192.168.1.0/24, fd00::/8)", s)
}

// mergePrefixes sorts and drops the networks another one of the list
// contains (nftables refuses overlapping intervals).
func mergePrefixes(in []netip.Prefix) []netip.Prefix {
	ps := slices.Clone(in)
	slices.SortFunc(ps, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	var out []netip.Prefix
	for _, p := range ps {
		if len(out) > 0 {
			last := out[len(out)-1]
			if last.Bits() <= p.Bits() && last.Contains(p.Addr()) {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

func validRule(r Rule, host, inGroup bool, sets map[string]IPSet, groups map[string]Group) error {
	if r.Group != "" {
		if inGroup {
			return fmt.Errorf("a security group cannot include another group")
		}
		if _, ok := groups[r.Group]; !ok {
			return fmt.Errorf("unknown security group %q", r.Group)
		}
		return nil
	}
	if r.Dir != "in" && r.Dir != "out" {
		return fmt.Errorf("the direction of a rule is in or out")
	}
	if host && r.Dir != "in" && !inGroup {
		return fmt.Errorf("the NAS firewall filters incoming traffic only")
	}
	if !slices.Contains(actions, r.Action) {
		return fmt.Errorf("the action of a rule is accept, drop or reject")
	}
	if !slices.Contains(protos, r.Proto) {
		return fmt.Errorf("unknown protocol %q", r.Proto)
	}
	if r.Ports != "" || r.SPorts != "" {
		if r.Proto != "tcp" && r.Proto != "udp" && r.Proto != "tcpudp" {
			return fmt.Errorf("ports need the TCP or UDP protocol")
		}
		if err := ValidPorts(r.Ports); err != nil {
			return err
		}
		if err := ValidPorts(r.SPorts); err != nil {
			return err
		}
	}
	for _, s := range []string{r.Source, r.Dest} {
		if _, err := parseAddr(s, sets); err != nil {
			return err
		}
	}
	if len(r.Comment) > 200 {
		return fmt.Errorf("comment too long")
	}
	return nil
}

func validPolicy(p string) bool { return p == "" || slices.Contains(actions, p) }

// Validate checks the whole configuration.
func (c *Config) Validate() error {
	sets := map[string]IPSet{}
	for _, s := range c.IPSets {
		if !setNameRe.MatchString(s.Name) {
			return fmt.Errorf("invalid set name %q (lower case letters, digits, - and _)", s.Name)
		}
		if _, dup := sets[s.Name]; dup {
			return fmt.Errorf("two address sets are named %s", s.Name)
		}
		if len(s.Entries) > 4096 {
			return fmt.Errorf("set %s: at most 4096 entries", s.Name)
		}
		for _, e := range s.Entries {
			if _, err := parsePrefix(e); err != nil {
				return fmt.Errorf("set %s: %v", s.Name, err)
			}
		}
		sets[s.Name] = s
	}
	groups := map[string]Group{}
	for _, g := range c.Groups {
		if !setNameRe.MatchString(g.Name) {
			return fmt.Errorf("invalid group name %q (lower case letters, digits, - and _)", g.Name)
		}
		if _, dup := groups[g.Name]; dup {
			return fmt.Errorf("two security groups are named %s", g.Name)
		}
		groups[g.Name] = g
	}
	for _, g := range c.Groups {
		if len(g.Rules) > 500 {
			return fmt.Errorf("group %s: too many rules", g.Name)
		}
		for i, r := range g.Rules {
			if err := validRule(r, false, true, sets, groups); err != nil {
				return fmt.Errorf("group %s, rule %d: %v", g.Name, i+1, err)
			}
		}
	}
	if !validPolicy(c.Host.PolicyIn) {
		return fmt.Errorf("invalid policy of the NAS firewall")
	}
	if len(c.Host.Rules) > 500 {
		return fmt.Errorf("NAS firewall: too many rules")
	}
	for i, r := range c.Host.Rules {
		if err := validRule(r, true, false, sets, groups); err != nil {
			return fmt.Errorf("NAS firewall, rule %d: %v", i+1, err)
		}
	}
	seen := map[string]bool{}
	for _, g := range c.Guests {
		if g.Kind != "vm" && g.Kind != "lxc" && g.Kind != "docker" {
			return fmt.Errorf("unknown guest kind %q", g.Kind)
		}
		if !guestNameRe.MatchString(g.Name) {
			return fmt.Errorf("invalid guest name %q", g.Name)
		}
		if seen[g.Key()] {
			return fmt.Errorf("%s has two firewalls", g.Key())
		}
		seen[g.Key()] = true
		if !validPolicy(g.PolicyIn) || !validPolicy(g.PolicyOut) {
			return fmt.Errorf("%s: invalid policy", g.Name)
		}
		if len(g.Rules) > 500 {
			return fmt.Errorf("%s: too many rules", g.Name)
		}
		for i, r := range g.Rules {
			if err := validRule(r, false, false, sets, groups); err != nil {
				return fmt.Errorf("%s, rule %d: %v", g.Name, i+1, err)
			}
		}
	}
	return nil
}

// expand replaces the group references by the rules of their groups and
// drops the disabled rules.
func expand(rules []Rule, groups map[string]Group) []Rule {
	var out []Rule
	for _, r := range rules {
		if r.Disabled {
			continue
		}
		if r.Group != "" {
			for _, gr := range groups[r.Group].Rules {
				if !gr.Disabled && gr.Group == "" {
					out = append(out, gr)
				}
			}
			continue
		}
		out = append(out, r)
	}
	return out
}
