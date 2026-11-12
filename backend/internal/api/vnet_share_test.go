package api

import (
	"net/netip"
	"slices"
	"testing"

	"strataos/internal/network"
)

func TestVNetPlan(t *testing.T) {
	v := network.VNet{ID: "lab", Kind: "vxlan", VNI: 77}
	parts := []vnetParticipant{{Addr: "192.168.1.10"}, {Member: "fleet:a", Addr: "192.168.1.20"}, {Member: "cluster:b", Addr: "192.168.1.30"}}
	plan := vnetPlan(v, netip0(), parts)
	if len(plan) != 3 || !slices.Equal(plan[0].Peers, []string{"192.168.1.20", "192.168.1.30"}) || !slices.Equal(plan[2].Peers, []string{"192.168.1.10", "192.168.1.20"}) || plan[1].WG != nil {
		t.Fatalf("plain plan %+v", plan)
	}
	// Encrypted: mesh addresses, WireGuard peers; a NAS without key is left out.
	v.Encrypt = true
	k1, k2 := "YJqTsXV5pr6hSGBcIPbq1HqzaZhFzZ6vCk1cCUoT5lE=", "hDnD1sW3gxN3Wl4Sxd6ZtJbS4LZjYB4yR4ox6M1T8Fo="
	parts[0].PubKey, parts[0].Port = k1, 51900
	parts[1].PubKey, parts[1].Port = k2, 51901
	mesh := vnetMesh("machine", "lab")
	if mesh.Bits() != 64 || mesh.Addr().As16()[0] != 0xfd || mesh != vnetMesh("machine", "lab") || mesh == vnetMesh("machine", "lab2") {
		t.Fatal("mesh", mesh)
	}
	plan = vnetPlan(v, mesh, parts)
	if len(plan) != 2 {
		t.Fatalf("NAS without key planned: %+v", plan)
	}
	o, m := plan[0], plan[1]
	if o.Local != meshAddr(mesh, 1).String() || m.Local != meshAddr(mesh, 2).String() || !slices.Equal(o.Peers, []string{m.Local}) || !slices.Equal(m.Peers, []string{o.Local}) {
		t.Fatalf("mesh addresses %+v %+v", o, m)
	}
	if len(m.WG.Peers) != 1 || m.WG.Peers[0].PublicKey != k1 || m.WG.Peers[0].Endpoint != "192.168.1.10:51900" || m.WG.Port != 51901 || m.WG.Address != m.Local+"/64" {
		t.Fatalf("member WireGuard %+v", m.WG)
	}
	// What a member applies is a valid network of its own.
	sp := m
	sp.ID, sp.VNI, sp.Subnet, sp.Gateway, sp.Encrypt, sp.Origin = "lab", 77, "10.77.0.0/24", "10.77.0.1", true, "nas-a"
	rv := receivedVNet(sp, "fleet:mgr1", nil, "vn4")
	if rv.Bridge != "vn4" || rv.OriginKey != "fleet:mgr1" || rv.Kind != "vxlan" {
		t.Fatal(rv)
	}
	c := network.Config{Virtual: []network.VNet{rv}}
	if err := network.Validate(&c, nil); err != nil {
		t.Fatal(err)
	}
	cur := rv
	cur.Bridge = "vn9"
	if receivedVNet(sp, "fleet:mgr1", &cur, "vn4").Bridge != "vn9" {
		t.Fatal("bridge of the existing copy not kept")
	}
}

func TestSameButVirtual(t *testing.T) {
	a := network.Config{Virtual: []network.VNet{{ID: "x"}}}
	b := network.Config{Interfaces: []network.Iface{}}
	if !sameButVirtual(a, b) {
		t.Fatal("virtual only")
	}
	b.Bridges = []network.Bridge{{Name: "br0"}}
	if sameButVirtual(a, b) {
		t.Fatal("bridge change accepted")
	}
}

func TestWGPort(t *testing.T) {
	c := network.Config{Virtual: []network.VNet{{ID: "a", WG: &network.VNetWG{Port: 51900}}, {ID: "b", WG: &network.VNetWG{Port: 51901}}}}
	if wgPort(c, "b") != 51901 || wgPort(c, "c") != 51902 {
		t.Fatal(wgPort(c, "b"), wgPort(c, "c"))
	}
}

func netip0() netip.Prefix { return vnetMesh("s", "x") }
