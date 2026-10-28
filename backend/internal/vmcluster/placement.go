package vmcluster

import (
	"slices"

	"strataos/internal/store"
)

// Node is a member as placement sees it.
type Node struct {
	ID     string
	FreeMB int // memory available for new VMs
	// OK: online, not in maintenance, virtualisation installed.
	OK bool
}

// marginMB is kept free on a member after placing a VM.
const marginMB = 256

// ChooseTarget picks the member to run a VM needing memMB: the preferred
// one when it can, else the one with the most free memory (lowest ID on a
// tie), never exclude. "" when none fits.
func ChooseTarget(nodes []Node, preferred, exclude string, memMB int) string {
	fits := func(n Node) bool { return n.OK && n.ID != exclude && n.FreeMB >= memMB+marginMB }
	for _, n := range nodes {
		if n.ID == preferred && fits(n) {
			return n.ID
		}
	}
	best := -1
	for i, n := range nodes {
		if !fits(n) {
			continue
		}
		if best < 0 || n.FreeMB > nodes[best].FreeMB || (n.FreeMB == nodes[best].FreeMB && n.ID < nodes[best].ID) {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return nodes[best].ID
}

// Recovery is the restart of an HA VM of a dead member on another one.
type Recovery struct {
	VM, From, To string
}

// PlanRecovery places the HA VMs that ran on dead members. memOf gives the
// memory of each VM (from its definition); VMs that fit nowhere are listed
// in stuck. Only VMs whose owner reported them running are restarted.
func PlanRecovery(t store.VMCluster, dead []string, nodes []Node, memOf func(store.ClusterHAVM) int) (plan []Recovery, stuck []string) {
	nodes = slices.Clone(nodes)
	for i := range nodes {
		if slices.Contains(dead, nodes[i].ID) || InMaintenance(t, nodes[i].ID) {
			nodes[i].OK = false
		}
	}
	for _, h := range t.HA {
		if !slices.Contains(dead, h.Owner) || !(h.Running || h.Recover) {
			continue
		}
		mem := memOf(h)
		to := ChooseTarget(nodes, h.Preferred, h.Owner, mem)
		if to == "" {
			stuck = append(stuck, h.Name)
			continue
		}
		plan = append(plan, Recovery{VM: h.Name, From: h.Owner, To: to})
		for i := range nodes {
			if nodes[i].ID == to {
				nodes[i].FreeMB -= mem
			}
		}
	}
	return plan, stuck
}
