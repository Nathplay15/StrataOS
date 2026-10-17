package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var phys = []string{"eth0", "eth1", "eth2"}

func TestValidate(t *testing.T) {
	good := Config{
		Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "static", Addresses: []string{"192.168.1.10/24"}, Gateway: "192.168.1.1", DNS: []string{"1.1.1.1"}}}},
		Bonds:      []Bond{{Name: "bond0", BondMode: "active-backup", Members: []string{"eth1", "eth2"}, IP: IP{Mode: "dhcp"}}},
		VLANs:      []VLAN{{Parent: "eth0", ID: 20, IP: IP{Mode: "static", Addresses: []string{"10.20.0.2/24"}}}},
	}
	if err := Validate(&good, phys); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := map[string]Config{
		"unknown iface":     {Interfaces: []Iface{{Name: "eth9", IP: IP{Mode: "dhcp"}}}},
		"bad cidr":          {Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "static", Addresses: []string{"192.168.1.10"}}}}},
		"static no address": {Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "static"}}}},
		"member reused":     {Interfaces: []Iface{{Name: "eth1", IP: IP{Mode: "dhcp"}}}, Bonds: []Bond{{Name: "bond0", BondMode: "802.3ad", Members: []string{"eth1"}, IP: IP{Mode: "dhcp"}}}},
		"bad bond name":     {Bonds: []Bond{{Name: "lag;rm", BondMode: "802.3ad", Members: []string{"eth1"}, IP: IP{Mode: "dhcp"}}}},
		"bad bond mode":     {Bonds: []Bond{{Name: "bond0", BondMode: "magic", Members: []string{"eth1"}, IP: IP{Mode: "dhcp"}}}},
		"vlan id":           {Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "dhcp"}}}, VLANs: []VLAN{{Parent: "eth0", ID: 5000, IP: IP{Mode: "dhcp"}}}},
		"vlan unmanaged":    {VLANs: []VLAN{{Parent: "eth0", ID: 5, IP: IP{Mode: "dhcp"}}}},
		"vlan on member":    {Bonds: []Bond{{Name: "bond0", BondMode: "802.3ad", Members: []string{"eth1"}, IP: IP{Mode: "dhcp"}}}, VLANs: []VLAN{{Parent: "eth1", ID: 5, IP: IP{Mode: "dhcp"}}}},
		"newline injection": {Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "static", Addresses: []string{"10.0.0.1/24\n[Network]"}}}}},
		"bad mtu":           {Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "dhcp", MTU: 100000}}}},
	}
	for name, c := range bad {
		if Validate(&c, phys) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRender(t *testing.T) {
	files := Render(Config{
		Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "static", Addresses: []string{"192.168.1.10/24"}, Gateway: "192.168.1.1", MTU: 9000}}},
		Bonds:      []Bond{{Name: "bond0", BondMode: "802.3ad", Members: []string{"eth1", "eth2"}, IP: IP{Mode: "dhcp"}}},
		VLANs:      []VLAN{{Parent: "eth0", ID: 20, IP: IP{Mode: "dhcp"}}},
	})
	want := map[string][]string{
		"05-strata-eth0.network":    {"Name=eth0", "Address=192.168.1.10/24", "Gateway=192.168.1.1", "MTUBytes=9000", "VLAN=eth0.20"},
		"05-strata-bond0.netdev":    {"Kind=bond", "Mode=802.3ad", "LACPTransmitRate=fast"},
		"05-strata-bond0.network":   {"Name=bond0", "DHCP=yes"},
		"05-strata-eth1.network":    {"Bond=bond0"},
		"05-strata-eth2.network":    {"Bond=bond0"},
		"05-strata-eth0.20.netdev":  {"Kind=vlan", "Id=20"},
		"05-strata-eth0.20.network": {"Name=eth0.20", "DHCP=yes"},
	}
	// VLAN= must stay in [Network], before the [DHCPv4] section of a DHCP parent.
	dh := Render(Config{Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "dhcp"}}}, VLANs: []VLAN{{Parent: "eth0", ID: 7, IP: IP{Mode: "dhcp"}}}})["05-strata-eth0.network"]
	if strings.Index(dh, "VLAN=eth0.7") > strings.Index(dh, "[DHCPv4]") || !strings.Contains(dh, "ClientIdentifier=mac") {
		t.Errorf("bad section order:\n%s", dh)
	}
	if len(files) != len(want) {
		t.Errorf("got %d files, want %d: %v", len(files), len(want), files)
	}
	for f, parts := range want {
		for _, p := range parts {
			if !strings.Contains(files[f], p) {
				t.Errorf("%s: missing %q in:\n%s", f, p, files[f])
			}
		}
	}
}

func TestSnapshotRestore(t *testing.T) {
	dir := t.TempDir()
	NetworkdDir, InterfacesF, ResolvConf, StateDir = filepath.Join(dir, "nd"), filepath.Join(dir, "interfaces"), filepath.Join(dir, "resolv.conf"), filepath.Join(dir, "state")
	_ = os.MkdirAll(NetworkdDir, 0o755)
	_ = os.WriteFile(filepath.Join(NetworkdDir, "05-strata-eth0.network"), []byte("old"), 0o644)
	_ = os.WriteFile(InterfacesF, []byte("auto eth1\niface eth1 inet dhcp\n"), 0o644)
	_ = os.WriteFile(ResolvConf, []byte("nameserver 9.9.9.9\n"), 0o644)
	_ = os.MkdirAll(StateDir, 0o700)
	if err := snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := write(Config{Interfaces: []Iface{{Name: "eth1", IP: IP{Mode: "static", Addresses: []string{"10.0.0.2/24"}, DNS: []string{"1.1.1.1"}}}}}, "ifupdown"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(InterfacesF); !strings.Contains(string(b), "# strata: iface eth1") {
		t.Errorf("ifupdown stanza not released:\n%s", b)
	}
	if b, _ := os.ReadFile(ResolvConf); !strings.Contains(string(b), "1.1.1.1") {
		t.Errorf("resolv.conf not written")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(NetworkdDir, "05-strata-eth0.network")); string(b) != "old" {
		t.Errorf("old file not restored: %q", b)
	}
	if _, err := os.Stat(filepath.Join(NetworkdDir, "05-strata-eth1.network")); !os.IsNotExist(err) {
		t.Errorf("new file not removed by rollback")
	}
	if b, _ := os.ReadFile(InterfacesF); string(b) != "auto eth1\niface eth1 inet dhcp\n" {
		t.Errorf("interfaces not restored: %q", b)
	}
	if b, _ := os.ReadFile(ResolvConf); string(b) != "nameserver 9.9.9.9\n" {
		t.Errorf("resolv.conf not restored: %q", b)
	}
}

func TestConfirmWaitsForActivation(t *testing.T) {
	StateDir = t.TempDir()
	cfg := &Config{}
	pending = &Pending{Token: "abc", ReadyAt: time.Now().Add(time.Hour), Config: cfg}
	if _, err := Confirm("abc"); err != ErrNotReady {
		t.Fatalf("early confirmation accepted: %v", err)
	}
	if _, err := Confirm("nope"); err == nil || err == ErrNotReady {
		t.Fatalf("wrong token: %v", err)
	}
	pending.ReadyAt = time.Now().Add(-time.Second)
	if got, err := Confirm("abc"); err != nil || got != cfg {
		t.Fatalf("confirmation refused: %v", err)
	}
	if pending != nil {
		t.Fatalf("pending not cleared")
	}
}
