package api

import (
	"slices"
	"testing"
)

func TestParseDomiflist(t *testing.T) {
	out := ` Interface   Type      Source    Model    MAC
-------------------------------------------------------------
 vnet3       bridge    br0       virtio   52:54:00:AA:bb:01
 -           network   default   virtio   52:54:00:aa:bb:02
`
	b := parseDomiflist(out)
	if !slices.Equal(b.MACs, []string{"52:54:00:aa:bb:01", "52:54:00:aa:bb:02"}) || !slices.Equal(b.Ifaces, []string{"vnet3"}) {
		t.Fatalf("%+v", b)
	}
}
