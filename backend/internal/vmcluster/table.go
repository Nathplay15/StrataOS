package vmcluster

import (
	"slices"

	"strataos/internal/store"
)

// MinHAMembers is the size of cluster that high availability needs: with
// two members, neither can tell its partner's failure from its own isolation.
const MinHAMembers = 3

// Clone deep-copies a table.
func Clone(t store.VMCluster) store.VMCluster {
	t.HA = slices.Clone(t.HA)
	t.Maintenance = slices.Clone(t.Maintenance)
	return t
}

// Accept reports whether a member holding local takes the proposed table:
// only a newer version. Two members proposing the same next version are
// thus never both accepted by the same member, and only one can gather a
// majority.
func Accept(local, proposed store.VMCluster) bool { return proposed.Version > local.Version }

// Find returns the index of a VM in the HA list, or -1.
func Find(t store.VMCluster, name string) int {
	return slices.IndexFunc(t.HA, func(h store.ClusterHAVM) bool { return h.Name == name })
}

// InMaintenance reports whether a member is in maintenance.
func InMaintenance(t store.VMCluster, id string) bool { return slices.Contains(t.Maintenance, id) }

// Owned lists the HA VMs of a member.
func Owned(t store.VMCluster, id string) []store.ClusterHAVM {
	var out []store.ClusterHAVM
	for _, h := range t.HA {
		if h.Owner == id {
			out = append(out, h)
		}
	}
	return out
}

// Stale lists the HA VMs defined on member self that belong to another
// member and are not on their way here: their copy here must not run (it
// was restarted elsewhere while this member was cut off or down).
func Stale(t store.VMCluster, self string, defined []string) []string {
	var out []string
	for _, h := range t.HA {
		if h.Owner != self && h.Migrating != self && slices.Contains(defined, h.Name) {
			out = append(out, h.Name)
		}
	}
	return out
}
