// Package vmcluster holds the decisions of the VM cluster, kept apart from
// libvirt and HTTP to test them: membership and quorum from heartbeats, when
// a member is declared dead, where its high-availability VMs restart, which
// Ceph clients are fenced first, migration pre-checks and the CPU model the
// members have in common. The API package (api/h_vmcluster*.go) drives them.
package vmcluster

import (
	"slices"
	"time"
)

// Timing of the membership. A member is unreachable when no heartbeat came
// for UnreachableAfter; a member without quorum stops its HA VMs after
// SelfFenceAfter, before the others may declare it dead (DeadAfter), so a VM
// cut off from the cluster is stopped before its copy starts elsewhere.
const (
	HeartbeatEvery   = 5 * time.Second
	UnreachableAfter = 15 * time.Second
	SelfFenceAfter   = 30 * time.Second
	DeadAfter        = 45 * time.Second
)

// Majority is the number of members that make a quorum.
func Majority(total int) int { return total/2 + 1 }

// HasQuorum reports whether reachable members (this one included) are a
// majority of total.
func HasQuorum(total, reachable int) bool { return total > 0 && reachable >= Majority(total) }

// Peer is what this member knows of another one from its heartbeats.
type Peer struct {
	LastSeen time.Time // last heartbeat answered (zero: never since Start)
	// Sees: the members that peer reached recently (itself included), as it
	// said in its last heartbeat.
	Sees []string
}

// View is the membership as this member sees it.
type View struct {
	Self    string
	Members []string // IDs of every member, this one included
	Peers   map[string]Peer
	Start   time.Time // when this member started watching
	Now     time.Time
}

// Reachable reports whether member id answered recently (always true for self).
func (v View) Reachable(id string) bool {
	if id == v.Self {
		return true
	}
	p, ok := v.Peers[id]
	return ok && !p.LastSeen.IsZero() && v.Now.Sub(p.LastSeen) < UnreachableAfter
}

// Online lists the reachable members, sorted.
func (v View) Online() []string {
	var out []string
	for _, m := range v.Members {
		if v.Reachable(m) {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// Quorum reports whether this member reaches a majority of the members.
func (v View) Quorum() bool { return HasQuorum(len(v.Members), len(v.Online())) }

// Leader is the member that takes the recovery decisions: the lowest ID
// among the reachable members, when they form a quorum ("" otherwise).
// Two members could each believe they lead in an odd partition; their
// decisions still need a majority to be committed (see Table), so only one
// of them can act.
func (v View) Leader() string {
	if !v.Quorum() {
		return ""
	}
	return v.Online()[0]
}

// unreachableSince is when member id stopped answering, as far as we know.
func (v View) unreachableSince(id string) time.Time {
	p := v.Peers[id]
	if p.LastSeen.IsZero() || p.LastSeen.Before(v.Start) {
		return v.Start
	}
	return p.LastSeen
}

// Dead lists the members this one may declare dead: unreachable for
// DeadAfter, with quorum, and seen as unreachable by a majority of all the
// members (this one and the reachable peers whose last report leaves it out).
func (v View) Dead() []string {
	if !v.Quorum() {
		return nil
	}
	total := len(v.Members)
	var out []string
	for _, x := range v.Members {
		if x == v.Self || v.Reachable(x) || v.Now.Sub(v.unreachableSince(x)) < DeadAfter {
			continue
		}
		agree := 1
		for _, p := range v.Members {
			if p == v.Self || p == x || !v.Reachable(p) {
				continue
			}
			if !slices.Contains(v.Peers[p].Sees, x) {
				agree++
			}
		}
		if agree >= Majority(total) {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return out
}

// ShouldSelfFence reports whether this member, cut from the majority for
// SelfFenceAfter, must stop its HA VMs. lostSince is when it last had quorum.
func ShouldSelfFence(v View, lostSince time.Time) bool {
	return !v.Quorum() && !lostSince.IsZero() && v.Now.Sub(lostSince) >= SelfFenceAfter
}
