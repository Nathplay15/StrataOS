package cluster

import (
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestValidators(t *testing.T) {
	for _, ok := range []string{"10.0.0.1", "fd00::1", "nas2.lan", "nas-2"} {
		if ValidHost(ok) != nil {
			t.Errorf("ValidHost(%q) refused", ok)
		}
	}
	for _, bad := range []string{"", "https://x", "a b", "x:7443", "a/b", "a..b", "-x"} {
		if ValidHost(bad) == nil {
			t.Errorf("ValidHost(%q) accepted", bad)
		}
	}
	if ValidNet("10.98.0.0/24", false) != nil || ValidNet("", true) != nil || ValidNet("", false) == nil || ValidNet("10.98.0.1/24", false) == nil || ValidNet("10.98.0.0", false) == nil {
		t.Error("ValidNet")
	}
	if ValidDaemonName("ceph1") != nil || ValidDaemonName("Ceph1") == nil || ValidDaemonName("nas.lan") == nil || ValidDaemonName("") == nil {
		t.Error("ValidDaemonName")
	}
	if ValidName("Home lab") != nil || ValidName("") == nil || ValidName(strings.Repeat("a", 41)) == nil || ValidName("a\nb") == nil {
		t.Error("ValidName")
	}
}

func TestAddrs(t *testing.T) {
	out := `1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever
2: enp0s2    inet 10.0.2.15/24 brd 10.0.2.255 scope global dynamic enp0s2\       valid_lft 86000sec
2: enp0s2    inet6 fe80::5054:ff:fe12:3456/64 scope link \       valid_lft forever
3: enp0s5    inet 10.98.0.2/24 scope global enp0s5\       valid_lft forever
3: enp0s5    inet6 fd00:98::2/64 scope global \       valid_lft forever
4: docker0    inet 172.17.0.1/16 brd 172.17.255.255 scope global docker0\       valid_lft forever`
	m := ParseAddrs(out)
	if len(m) != 2 || m["enp0s2"][0] != "10.0.2.15/24" || len(m["enp0s5"]) != 2 {
		t.Fatalf("ParseAddrs %v", m)
	}
	all := append(m["enp0s2"], m["enp0s5"]...)
	if AddrIn(all, "10.98.0.0/24") != "10.98.0.2" || AddrIn(all, "fd00:98::/64") != "fd00:98::2" || AddrIn(all, "192.168.1.0/24") != "" || AddrIn(all, "bad") != "" {
		t.Error("AddrIn")
	}
	if n := Networks(all); strings.Join(n, " ") != "10.0.2.0/24 10.98.0.0/24 fd00:98::/64" {
		t.Errorf("Networks %v", n)
	}
}

func TestMembers(t *testing.T) {
	c := store.Cluster{ID: "c1", Token: strings.Repeat("ab", 32), Self: "a", Members: []store.ClusterMember{{ID: "a", Name: "ceph1"}, {ID: "b", Name: "ceph2"}}}
	if o := Others(c); len(o) != 1 || o[0].ID != "b" {
		t.Errorf("Others %v", o)
	}
	if m, ok := Member(c, "b"); !ok || m.Name != "ceph2" {
		t.Error("Member")
	}
	if !TokenOK(c, strings.Repeat("ab", 32)) || TokenOK(c, "") || TokenOK(c, strings.Repeat("ab", 31)) || TokenOK(store.Cluster{}, "") {
		t.Error("TokenOK")
	}
}
