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
	for _, want := range []string{"priority 150", "nopreempt", "state BACKUP", "unicast_peer {\n        10.0.0.2", "10.0.0.100/24 dev eth0", "virtual_router_id 51"} {
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

func TestMapPath(t *testing.T) {
	m := map[string]string{"/srv/strata/volume1": "/srv/strata/disque2", "/srv/strata/volume1/big": "/srv/strata/gros"}
	for in, want := range map[string]string{
		"/srv/strata/volume1/Docs":    "/srv/strata/disque2/Docs",
		"/srv/strata/volume1":         "/srv/strata/disque2",
		"/srv/strata/volume1/big/x":   "/srv/strata/gros/x",
		"/srv/strata/volume10/Docs":   "/srv/strata/volume10/Docs",
		"/srv/strata/other/Documents": "/srv/strata/other/Documents",
	} {
		if got := MapPath(in, m); got != want {
			t.Errorf("MapPath(%s) = %s, want %s", in, got, want)
		}
	}
	if MapPath(MapPath("/srv/strata/volume1/Docs", m), Reverse(m)) != "/srv/strata/volume1/Docs" {
		t.Error("reverse mapping")
	}
}
