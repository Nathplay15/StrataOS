package ha

import (
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestKeepalived(t *testing.T) {
	c := store.HA{Role: "primary", PeerHost: "10.0.0.2", VIP: "10.0.0.100/24", Interface: "eth0", AuthPass: "abcdefgh"}
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	p := RenderKeepalived(c, "10.0.0.1")
	for _, want := range []string{"priority 150", "nopreempt", "state BACKUP", "unicast_peer {\n        10.0.0.2", "10.0.0.100/24 dev eth0", "virtual_router_id 51", "vrrp_startup_delay 0"} {
		if !strings.Contains(p, want) {
			t.Errorf("primary config lacks %q", want)
		}
	}
	c.Role = "secondary"
	if s := RenderKeepalived(c, "10.0.0.2"); !strings.Contains(s, "priority 100") || !strings.Contains(s, "vrrp_startup_delay 30") {
		t.Errorf("secondary config: %s", s)
	}
	for _, bad := range []store.HA{{PeerHost: "-oProxy", VIP: "10.0.0.1/24", Interface: "eth0"}, {PeerHost: "h", VIP: "10.0.0.1", Interface: "eth0"}, {PeerHost: "h", VIP: "10.0.0.1/24", Interface: "eth0;x"}} {
		if Validate(&bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
