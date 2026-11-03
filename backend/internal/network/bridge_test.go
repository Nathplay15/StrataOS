package network

import (
	"reflect"
	"testing"
)

func TestPlanBridgeObservedDHCP(t *testing.T) {
	out, name, err := PlanBridge(Config{}, "eth0", Observed{Addresses: []string{"192.168.1.20/24"}, Dynamic: true, MTU: 1500}, []string{"eth0", "lo"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Bridge{{Name: "br0", Members: []string{"eth0"}, IP: IP{Mode: "dhcp"}}}
	if name != "br0" || !reflect.DeepEqual(out.Bridges, want) {
		t.Fatalf("got %s %+v", name, out.Bridges)
	}
	if err := Validate(&out, []string{"eth0"}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanBridgeObservedStatic(t *testing.T) {
	obs := Observed{Addresses: []string{"192.168.1.20/24", "fe80::1/64", "2001:db8::5/64"}, Gateway: "192.168.1.1", DNS: []string{"127.0.0.53", "9.9.9.9"}, MTU: 9000}
	out, name, err := PlanBridge(Config{}, "enp1s0", obs, []string{"enp1s0", "br0"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "br1" {
		t.Fatalf("name %s (br0 exists on the machine)", name)
	}
	ip := out.Bridges[0].IP
	want := IP{Mode: "static", Addresses: []string{"192.168.1.20/24", "2001:db8::5/64"}, Gateway: "192.168.1.1", DNS: []string{"9.9.9.9"}, MTU: 9000}
	if !reflect.DeepEqual(ip, want) {
		t.Fatalf("got %+v", ip)
	}
	if err := Validate(&out, []string{"enp1s0"}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanBridgeManaged(t *testing.T) {
	cfg := Config{
		Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "static", Addresses: []string{"10.0.0.2/24"}, Gateway: "10.0.0.1"}}, {Name: "eth2", IP: IP{Mode: "dhcp"}}},
		Bridges:    []Bridge{{Name: "br0", Members: []string{"eth1"}, IP: IP{Mode: "none"}}},
		VLANs:      []VLAN{{Parent: "eth0", ID: 10, IP: IP{Mode: "none"}}},
	}
	out, name, err := PlanBridge(cfg, "eth0", Observed{Dynamic: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if name != "br1" || len(out.Interfaces) != 1 || out.Interfaces[0].Name != "eth2" {
		t.Fatalf("got %s %+v", name, out.Interfaces)
	}
	// The managed configuration wins over what is observed.
	if br := out.Bridges[1]; br.Mode != "static" || br.Gateway != "10.0.0.1" || br.Members[0] != "eth0" {
		t.Fatalf("bridge %+v", br)
	}
	if out.VLANs[0].Parent != "br1" {
		t.Fatalf("VLAN not moved: %+v", out.VLANs)
	}
	// The input is not modified.
	if len(cfg.Interfaces) != 2 || cfg.VLANs[0].Parent != "eth0" || len(cfg.Bridges) != 1 {
		t.Fatal("input changed")
	}
	if err := Validate(&out, []string{"eth0", "eth1", "eth2"}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanBridgeBond(t *testing.T) {
	cfg := Config{Bonds: []Bond{{Name: "bond0", BondMode: "active-backup", Members: []string{"eth0", "eth1"}, IP: IP{Mode: "dhcp", MTU: 9000}}}}
	out, name, err := PlanBridge(cfg, "bond0", Observed{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if name != "br0" || out.Bonds[0].Mode != "none" || out.Bonds[0].MTU != 9000 || out.Bridges[0].Mode != "dhcp" {
		t.Fatalf("got %+v %+v", out.Bonds, out.Bridges)
	}
	if err := Validate(&out, []string{"eth0", "eth1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PlanBridge(cfg, "eth0", Observed{Dynamic: true}, nil); err == nil {
		t.Fatal("bond member accepted")
	}
}

func TestPlanBridgeRefused(t *testing.T) {
	cfg := Config{Bridges: []Bridge{{Name: "br0", Members: []string{"eth0"}, IP: IP{Mode: "dhcp"}}}}
	for _, link := range []string{"eth0", "br0"} {
		if _, _, err := PlanBridge(cfg, link, Observed{Dynamic: true}, nil); err == nil {
			t.Errorf("%s accepted", link)
		}
	}
	if _, _, err := PlanBridge(Config{}, "eth1", Observed{Addresses: []string{"fe80::1/64"}}, nil); err == nil {
		t.Error("link without address accepted")
	}
	if _, _, err := PlanBridge(Config{Interfaces: []Iface{{Name: "eth1", IP: IP{Mode: "none"}}}}, "eth1", Observed{}, nil); err == nil {
		t.Error("managed link without address accepted")
	}
}
