package network

import (
	"strings"
	"testing"
)

func vnetConfig() Config {
	return Config{
		Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "dhcp"}}},
		Virtual: []VNet{
			{ID: "iot", Kind: "nat", Bridge: "vn0", Address: "10.20.0.1/24", DHCP: true, DNS: []string{"1.1.1.1"}, Isolate: true, NASPorts: "445", Docker: true},
			{ID: "lab", Kind: "isolated", Bridge: "vn1", Address: "10.21.0.1/24", DHCP: true},
			{ID: "cams", Kind: "vlan", Bridge: "vn2", Parent: "eth0", VLAN: 42, Subnet: "10.42.0.7/24", Gateway: "10.42.0.1"},
			{ID: "site", Kind: "vxlan", Bridge: "vn3", VNI: 100, Peers: []string{"192.168.1.11", " 192.168.1.12"}, Address: "10.100.0.1/24", DHCP: true},
		},
	}
}

func TestValidateVNets(t *testing.T) {
	c := vnetConfig()
	if err := Validate(&c, phys); err != nil {
		t.Fatal(err)
	}
	if c.Virtual[2].Subnet != "10.42.0.0/24" || c.Virtual[3].Peers[1] != "192.168.1.12" {
		t.Errorf("not normalised: %+v", c.Virtual)
	}
	bad := map[string]func(*Config){
		"bad id":           func(c *Config) { c.Virtual[0].ID = "IoT!" },
		"same id":          func(c *Config) { c.Virtual[1].ID = "iot" },
		"bad bridge":       func(c *Config) { c.Virtual[0].Bridge = "br0" },
		"same bridge":      func(c *Config) { c.Virtual[1].Bridge = "vn0" },
		"bad kind":         func(c *Config) { c.Virtual[0].Kind = "mesh" },
		"nat no address":   func(c *Config) { c.Virtual[0].Address = ""; c.Virtual[0].DHCP = false; c.Virtual[0].Docker = false },
		"network address":  func(c *Config) { c.Virtual[0].Address = "10.20.0.0/24" },
		"overlap":          func(c *Config) { c.Virtual[1].Address = "10.20.0.129/25" },
		"dhcp no address":  func(c *Config) { c.Virtual[2].DHCP = true },
		"vlan id":          func(c *Config) { c.Virtual[2].VLAN = 0 },
		"vlan unmanaged":   func(c *Config) { c.Virtual[2].Parent = "eth1" },
		"vlan taken":       func(c *Config) { c.VLANs = []VLAN{{Parent: "eth0", ID: 42, IP: IP{Mode: "dhcp"}}} },
		"gateway outside":  func(c *Config) { c.Virtual[2].Gateway = "10.43.0.1" },
		"vni":              func(c *Config) { c.Virtual[3].VNI = 1 << 24 },
		"no peer":          func(c *Config) { c.Virtual[3].Peers = nil },
		"bad peer":         func(c *Config) { c.Virtual[3].Peers = []string{"nas.local"} },
		"twice the peer":   func(c *Config) { c.Virtual[3].Peers = []string{"192.168.1.11", "192.168.1.11"} },
		"mtu":              func(c *Config) { c.Virtual[3].MTU = 100 },
		"ports":            func(c *Config) { c.Virtual[0].NASPorts = "445;rm" },
		"dns":              func(c *Config) { c.Virtual[0].DNS = []string{"dns.google"} },
		"docker no subnet": func(c *Config) { c.Virtual[2].Subnet = ""; c.Virtual[2].Gateway = ""; c.Virtual[2].Docker = true },
		"docker range":     func(c *Config) { c.Virtual[0].Address = "10.20.0.200/24" },
		"label newline":    func(c *Config) { c.Virtual[0].Label = strings.Repeat("x", 100) },
	}
	for name, f := range bad {
		c := vnetConfig()
		f(&c)
		if Validate(&c, phys) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRenderVNets(t *testing.T) {
	c := vnetConfig()
	if err := Validate(&c, phys); err != nil {
		t.Fatal(err)
	}
	files := Render(c)
	want := map[string][]string{
		"05-strata-vn0.netdev":      {"Name=vn0", "Kind=bridge", "STP=no"},
		"05-strata-vn0.network":     {"RequiredForOnline=no", "ConfigureWithoutCarrier=yes", "Address=10.20.0.1/24", "IPMasquerade=ipv4", "DHCPServer=yes", "PoolOffset=64\nPoolSize=128", "EmitRouter=yes", "EmitDNS=yes\nDNS=1.1.1.1"},
		"05-strata-vn0dk.netdev":    {"Kind=veth", "[Peer]\nName=vn0dkp"},
		"05-strata-vn0dk.network":   {"Bridge=vn0"},
		"05-strata-vn0dkp.network":  {"Name=vn0dkp", "LinkLocalAddressing=no"},
		"05-strata-vn1.network":     {"Address=10.21.0.1/24", "EmitRouter=no", "EmitDNS=no"},
		"05-strata-eth0.network":    {"VLAN=eth0.42"},
		"05-strata-eth0.42.netdev":  {"Kind=vlan", "Id=42"},
		"05-strata-eth0.42.network": {"Bridge=vn2"},
		"05-strata-vn2.network":     {"LinkLocalAddressing=no"},
		"05-strata-vn3.netdev":      {"MTUBytes=1450"},
		"05-strata-vn3vx.netdev":    {"Kind=vxlan", "VNI=100", "DestinationPort=4789", "Independent=yes", "MTUBytes=1450"},
		"05-strata-vn3vx.network":   {"Bridge=vn3", "MTUBytes=1450", "[BridgeFDB]\nMACAddress=00:00:00:00:00:00\nDestination=192.168.1.11", "Destination=192.168.1.12"},
	}
	for f, subs := range want {
		for _, s := range subs {
			if !strings.Contains(files[f], s) {
				t.Errorf("%s: missing %q in\n%s", f, s, files[f])
			}
		}
	}
	for _, f := range []string{"05-strata-vn1.network", "05-strata-vn3.network"} {
		if strings.Contains(files[f], "IPMasquerade") {
			t.Errorf("%s: only NAT networks masquerade", f)
		}
	}
	if strings.Contains(files["05-strata-vn2.network"], "Address=") {
		t.Error("the NAS has no address on vn2")
	}
	// Activation covers every link, the VLAN parent included.
	l := strings.Join(links(c), " ")
	for _, n := range []string{"vn0", "vn0dk", "vn0dkp", "vn2", "eth0.42", "eth0", "vn3vx"} {
		if !strings.Contains(" "+l+" ", " "+n+" ") {
			t.Errorf("%s not activated: %s", n, l)
		}
	}
}

func TestVNetHelpers(t *testing.T) {
	c := vnetConfig()
	_ = Validate(&c, phys)
	off, size, d := c.Virtual[0].Pools()
	if off != 64 || size != 128 || d.String() != "10.20.0.192/26" {
		t.Error(off, size, d)
	}
	f := c.Virtual[2].FW()
	if f.Bridge != "vn2" || f.Uplinks[0] != "eth0.42" || f.Subnets[0] != "10.42.0.0/24" {
		t.Errorf("%+v", f)
	}
	if NextBridge(c, []string{"vn4"}) != "vn5" {
		t.Error(NextBridge(c, []string{"vn4"}))
	}
	if err := CheckVNetAddrs(c.Virtual[0], []string{"10.20.0.50/16"}); err == nil {
		t.Error("overlap with the LAN accepted")
	}
	if err := CheckVNetAddrs(VNet{Kind: "vxlan", Peers: []string{"192.168.1.11"}}, []string{"192.168.1.11/24"}); err == nil {
		t.Error("own address as peer accepted")
	}
	if err := CheckVNetAddrs(c.Virtual[0], []string{"192.168.1.10/24"}); err != nil {
		t.Error(err)
	}
	if c.Virtual[3].EffectiveMTU() != 1450 || c.Virtual[0].GatewayAddr() != "10.20.0.1" || c.Virtual[2].GatewayAddr() != "10.42.0.1" {
		t.Error("mtu or gateway")
	}
}
