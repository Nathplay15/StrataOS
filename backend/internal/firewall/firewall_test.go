package firewall

import (
	"os"
	"strings"
	"testing"
)

func sample() (Config, Env) {
	c := Config{
		Host: Host{Enabled: true, PolicyIn: "drop", LogDrops: true, Rules: []Rule{
			{Dir: "in", Action: "accept", Source: "lan"},
			{Dir: "in", Action: "accept", Proto: "tcp", Ports: "80,443"},
			{Dir: "in", Action: "reject", Proto: "tcp", Ports: "445", Source: "192.168.1.66"},
			{Dir: "in", Action: "accept", Proto: "udp", Ports: "51820", Disabled: true},
			{Group: "admin"},
		}},
		Groups: []Group{{Name: "admin", Rules: []Rule{
			{Dir: "in", Action: "accept", Proto: "tcp", Ports: "22", Source: "set:office"},
		}}, {Name: "web", Rules: []Rule{
			{Dir: "in", Action: "accept", Proto: "tcp", Ports: "80,443,8000-8100"},
			{Dir: "out", Action: "accept", Proto: "tcpudp", Ports: "123"},
		}}},
		IPSets: []IPSet{{Name: "office", Entries: []string{"192.168.10.0/24", "192.168.10.5", "fd00:1::/64"}}},
		Guests: []Guest{
			{Kind: "lxc", Name: "ha", Enabled: true, Isolate: true, LogDrops: true, Rules: []Rule{
				{Dir: "out", Action: "accept", Proto: "tcp", Ports: "445", Dest: "nas"},
				{Group: "web"},
				{Dir: "in", Action: "reject", Proto: "icmp"},
			}},
			{Kind: "vm", Name: "web", Enabled: true, PolicyIn: "accept", PolicyOut: "reject", MACFilter: true, Rules: []Rule{
				{Dir: "out", Action: "accept", Dest: "10.0.0.0/8, 2001:db8::/32", Log: true},
			}},
			{Kind: "docker", Name: "off", Enabled: false, Rules: []Rule{{Dir: "in", Action: "drop"}}},
		},
	}
	env := Env{
		NAS:      []string{"192.168.1.10/24", "10.0.3.1", "fe80::1"},
		WebPorts: []int{7070, 7443, 22},
		Peers:    []string{"192.168.1.11"},
		Bindings: map[string]Binding{
			"lxc:ha":   {MACs: []string{"00:16:3E:AA:BB:CC"}, Ifaces: []string{"veth1A2B3C"}},
			"vm:web":   {MACs: []string{"52:54:00:11:22:33"}, Ifaces: []string{"vnet4", "bad name"}},
			"docker:x": {MACs: []string{"02:42:ac:11:00:02"}},
		},
		VNets: []VNet{
			{ID: "iot", Bridge: "vn0", Uplinks: []string{"enp1s0.42"}, Subnets: []string{"10.42.0.0/24"}, DNS: []string{"1.1.1.1"}, NASPorts: "445", Log: true},
			{ID: "pub", Bridge: "vn1", Subnets: []string{"10.50.0.0/24"}},
		},
	}
	return c, env
}

func TestValidate(t *testing.T) {
	c, _ := sample()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(*Config){
		func(c *Config) { c.Host.Rules[1].Ports = "0" },
		func(c *Config) { c.Host.Rules[1].Ports = "80-80" },
		func(c *Config) { c.Host.Rules[1].Proto = "" },
		func(c *Config) { c.Host.Rules[0].Dir = "out" },
		func(c *Config) { c.Host.Rules[0].Source = "set:nope" },
		func(c *Config) { c.Host.Rules[0].Source = "192.168.1.300" },
		func(c *Config) { c.Host.Rules[0].Action = "allow" },
		func(c *Config) { c.Host.Rules[4].Group = "nope" },
		func(c *Config) { c.Groups[0].Rules = append(c.Groups[0].Rules, Rule{Group: "web"}) },
		func(c *Config) { c.Groups[1].Name = "admin" },
		func(c *Config) { c.IPSets[0].Name = "Bad Name" },
		func(c *Config) { c.IPSets[0].Entries = []string{"x"} },
		func(c *Config) { c.Guests[0].Kind = "pod" },
		func(c *Config) { c.Guests[0].Name = "a b" },
		func(c *Config) { c.Guests[1].Name = "ha"; c.Guests[1].Kind = "lxc" },
		func(c *Config) { c.Guests[0].PolicyIn = "maybe" },
	}
	for i, f := range bad {
		c, _ := sample()
		f(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: invalid configuration accepted", i)
		}
	}
}

func has(t *testing.T, text string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(text, l) {
			t.Errorf("missing %q in:\n%s", l, text)
		}
	}
}

func hasNot(t *testing.T, text string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(text, l) {
			t.Errorf("unexpected %q", l)
		}
	}
}

func TestRenderHost(t *testing.T) {
	c, env := sample()
	h := Render(c, env).Host
	has(t, h,
		"table inet strata_host {",
		"type filter hook input priority filter + 5; policy accept;",
		"ct state established,related accept",
		"iifname \"lxcbr*\" accept",
		// anti-lockout before any user rule
		"tcp dport { 7070, 7443, 22 } ip saddr @lan4 accept",
		"ip saddr @peers4 accept",
		"elements = { 192.168.1.11 }",
		"ip saddr @lan4 accept\n\t\tip6 saddr @lan6 accept",
		"tcp dport { 80, 443 } accept",
		"ip saddr { 192.168.1.66 } tcp dport { 445 } reject",
		// group expanded, set split by family and merged
		"ip saddr @s_office4 tcp dport { 22 } accept",
		"ip6 saddr @s_office6 tcp dport { 22 } accept",
		"set s_office4 {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\telements = { 192.168.10.0/24 }",
		"elements = { fd00:1::/64 }",
		"log prefix \"strata-fw:host:in \" level info\n\t\tdrop\n\t}",
		"elements = { 10.0.3.1, 192.168.1.10 }",
	)
	hasNot(t, h, "51820")
	if i, j := strings.Index(h, "tcp dport { 7070"), strings.Index(h, "tcp dport { 80, 443 }"); i < 0 || j < i {
		t.Error("anti-lockout must come before the user rules")
	}
	c.Host.Enabled = false
	if Render(c, env).Host != "" {
		t.Error("host table rendered while off")
	}
}

func TestRenderGuests(t *testing.T) {
	c, env := sample()
	b := Render(c, env).Bridge
	has(t, b,
		"table bridge strata_fw {",
		"type filter hook prerouting priority 0; policy accept;",
		// lxc:ha is g0, vm:web is g1 (sorted keys); docker:off is disabled
		`iifname vmap { "veth1A2B3C" : jump g0_out, "vnet4" : jump g1_out }`,
		"ether saddr vmap { 00:16:3e:aa:bb:cc : jump g0_out, 52:54:00:11:22:33 : jump g1_out }",
		`oifname vmap { "veth1A2B3C" : jump g0_in, "vnet4" : jump g1_in }`,
		"ether daddr vmap { 00:16:3e:aa:bb:cc : jump g0_in, 52:54:00:11:22:33 : jump g1_in }",
		// lxc:ha: its rules, then the isolation, then the policy
		"chain g0_out {",
		"ip daddr @nas4 tcp dport { 445 } accept",
		"meta l4proto { tcp, udp } th dport { 123 } accept",
		"meta l4proto { tcp, udp } th dport 53 accept",
		"ip daddr @lan4 limit rate 10/minute burst 5 packets log prefix \"strata-fw:lxc.ha:out \" level info",
		"ip daddr @lan4 drop",
		"ip6 daddr @nas6 drop",
		// web group in rule in g0_in, icmp reject becomes a drop where it leaves the bridge
		"chain g0_in {",
		"tcp dport { 80, 443, 8000-8100 } accept",
		"meta l4proto icmp drop",
		// vm:web: MAC filter, logged rule split by family, reject policy out
		`iifname { "vnet4" } ether saddr != { 52:54:00:11:22:33 } drop`,
		"ip daddr { 10.0.0.0/8 } limit rate 10/minute",
		"ip daddr { 10.0.0.0/8 } accept",
		"ip6 daddr { 2001:db8::/32 } accept",
		"chain g1_in {",
	)
	hasNot(t, b, "bad name", "g2_", "02:42:ac:11:00:02")
	// reject stays a reject going out (prerouting), never in the in chains
	g1out := b[strings.Index(b, "chain g1_out"):strings.Index(b, "chain g1_in")]
	if !strings.Contains(g1out, "\t\treject\n") {
		t.Error("policy out reject lost: " + g1out)
	}
	g0in := b[strings.Index(b, "chain g0_in"):strings.Index(b, "chain g1_out")]
	if strings.Contains(g0in, "reject") {
		t.Error("reject in an in chain: " + g0in)
	}
	if !strings.Contains(g0in, "\t\tdrop\n") {
		t.Error("default policy in should be drop")
	}
	if i, j := strings.Index(b, "ip daddr @nas4 tcp dport { 445 } accept"), strings.Index(b, "ip daddr @nas4 drop"); j < i {
		t.Error("guest rules must come before the isolation")
	}
}

func TestRenderVNets(t *testing.T) {
	c, env := sample()
	c.Guests = nil
	b := Render(c, env).Bridge
	has(t, b,
		"chain vnets_in {\n\t\ttype filter hook input priority 0; policy accept;",
		`meta ibrname "vn0" iifname != { "enp1s0.42" } jump n0`,
		`meta ibrname "vn1" jump n1`,
		"chain vnets_fwd {\n\t\ttype filter hook forward priority 0; policy accept;",
		`meta ibrname "vn0" iifname != { "enp1s0.42" } oifname { "enp1s0.42" } jump n0`,
		"chain n0 {",
		"ip daddr { 1.1.1.1 } meta l4proto { tcp, udp } th dport 53 return",
		"ip daddr @nas4 meta l4proto { tcp, udp } th dport { 445 } return",
		"ip daddr { 10.42.0.0/24 } return",
		"log prefix \"strata-fw:vnet.iot \"",
		"ip daddr @lan4 drop",
	)
	hasNot(t, b, "chain guests_pre {\n\t\ttype filter hook prerouting priority 0; policy accept;\n\t\tiifname")
	if strings.Count(b, `meta ibrname "vn1"`) != 1 {
		t.Error("a network without uplink has no forward rule")
	}
	// Nothing to filter: no table.
	c.Guests = nil
	env.VNets = nil
	if Render(c, env).Bridge != "" {
		t.Error("empty bridge table rendered")
	}
}

func TestScript(t *testing.T) {
	s := Script(Ruleset{Host: "table inet strata_host {\n}\n"})
	has(t, s, "table bridge strata_fw\ndelete table bridge strata_fw\ntable inet strata_host\ndelete table inet strata_host\ntable inet strata_host {")
	// Optional check against the real nft (NFT_CHECK=1, needs nft and CAP_NET_ADMIN).
	if os.Getenv("NFT_CHECK_OUT") != "" {
		c, env := sample()
		_ = os.WriteFile(os.Getenv("NFT_CHECK_OUT"), []byte(Script(Render(c, env))), 0o644)
	}
}

func TestMergePrefixes(t *testing.T) {
	a, _ := parseAddr("10.0.0.0/8, 10.1.2.3, 192.168.1.0/24, 192.168.1.0/25, ::ffff:192.168.2.1", nil)
	if got := elems(a.v4); got != "10.0.0.0/8, 192.168.1.0/24, 192.168.2.1" {
		t.Error(got)
	}
}

func TestParseLog(t *testing.T) {
	text := `2026-11-05T10:00:00+0100 nas kernel: other line
2026-11-05T10:00:01+0100 nas kernel: strata-fw:lxc.ha:out IN=veth1 OUT= MACSRC=00:16:3e:aa:bb:cc SRC=10.0.3.50 DST=192.168.1.20 LEN=60 PROTO=TCP SPT=40000 DPT=7070 SYN
2026-11-05T10:00:02+0100 nas kernel: strata-fw:host:in IN=enp1s0 OUT= SRC=203.0.113.9 DST=192.168.1.10 PROTO=UDP SPT=5353 DPT=161`
	l := ParseLog(text, 10)
	if len(l) != 2 || l[0].Scope != "host" || l[0].DPort != 161 || l[1].Scope != "lxc.ha" || l[1].Dir != "out" || l[1].Src != "10.0.3.50" || l[1].Proto != "TCP" || l[1].Time != "2026-11-05T10:00:01+0100" {
		t.Fatalf("%+v", l)
	}
	if len(ParseLog(text, 1)) != 1 {
		t.Error("limit")
	}
}

func TestSetGuest(t *testing.T) {
	var c Config
	c.SetGuest(Guest{Kind: "vm", Name: "b", Enabled: true})
	c.SetGuest(Guest{Kind: "lxc", Name: "a", Enabled: true})
	c.SetGuest(Guest{Kind: "vm", Name: "b"})
	if len(c.Guests) != 1 || c.Guests[0].Name != "a" {
		t.Fatalf("%+v", c.Guests)
	}
	c.RenameGuest("lxc", "a", "z")
	if c.Guest("lxc", "z").Enabled != true {
		t.Error("rename")
	}
	c.RenameGuest("lxc", "z", "")
	if len(c.Guests) != 0 {
		t.Error("removal")
	}
}
