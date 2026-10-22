package api

// Grouped updates from the Multi-NAS console: the pure part (permissions,
// order of the NAS, progress), kept apart from the HTTP calls to test it.

import (
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/store"
)

// fleetAllowed checks what a console may do on this NAS: "" (read the
// status), "updates", or "reboot" (needs both permissions).
func fleetAllowed(m store.FleetManager, perm string) error {
	switch perm {
	case "":
		return nil
	case "updates":
		if m.AllowUpdates {
			return nil
		}
		return forbidden("this NAS does not allow the console to install updates")
	case "reboot":
		if m.AllowUpdates && m.AllowReboot {
			return nil
		}
		return forbidden("this NAS does not allow the console to restart it")
	}
	return forbidden("action not allowed")
}

func forbidden(msg string) error { return &Error{Status: 403, Msg: msg} }

// fleetRate is a sliding-window rate limit per console and kind of call.
type fleetRate struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (f *fleetRate) allow(key string, max int, window time.Duration, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hits == nil {
		f.hits = map[string][]time.Time{}
	}
	recent := slices.DeleteFunc(f.hits[key], func(t time.Time) bool { return now.Sub(t) >= window })
	if len(recent) >= max {
		f.hits[key] = recent
		return false
	}
	f.hits[key] = append(recent, now)
	return true
}

// fleetNode is what the scheduler knows of a selected NAS.
type fleetNode struct {
	Hostname string
	Host     string
	HARole   string   // primary | secondary | ""
	HAPeers  []string // names and addresses of its HA partners, as it reports them
}

func (n fleetNode) knows(o fleetNode) bool {
	for _, p := range n.HAPeers {
		if p != "" && (strings.EqualFold(p, o.Hostname) || strings.EqualFold(p, o.Host)) {
			return true
		}
	}
	return false
}

// fleetGroups puts the NAS that report each other as HA partners in the same
// group: group[i] is the index of the first member of i's group, or -1 when
// i is not paired with another selected NAS.
func fleetGroups(nodes []fleetNode) []int {
	parent := make([]int, len(nodes))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	paired := make([]bool, len(nodes))
	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			a, b := nodes[i], nodes[j]
			if a.HARole == "" || b.HARole == "" || !(a.knows(b) || b.knows(a)) {
				continue
			}
			paired[i], paired[j] = true, true
			if ri, rj := find(i), find(j); ri != rj {
				parent[max(ri, rj)] = min(ri, rj)
			}
		}
	}
	out := make([]int, len(nodes))
	for i := range nodes {
		out[i] = -1
		if paired[i] {
			out[i] = find(i)
		}
	}
	return out
}

// fleetLanes orders the update: lanes run side by side, the NAS of a lane
// one after the other. Rolling is a single lane. The members of an HA pair
// always share a lane (never both down at once), secondaries first so the
// primary keeps serving until its partner is back with the new version.
func fleetLanes(nodes []fleetNode, rolling bool) [][]int {
	groups := fleetGroups(nodes)
	members := func(g int) []int {
		var sec, prim []int
		for i := range nodes {
			if groups[i] != g {
				continue
			}
			if nodes[i].HARole == "primary" {
				prim = append(prim, i)
			} else {
				sec = append(sec, i)
			}
		}
		return append(sec, prim...)
	}
	var lanes [][]int
	placed := make([]bool, len(nodes))
	for i := range nodes {
		if placed[i] {
			continue
		}
		block := []int{i}
		if groups[i] >= 0 {
			block = members(groups[i])
		}
		for _, k := range block {
			placed[k] = true
		}
		if rolling && len(lanes) == 1 {
			lanes[0] = append(lanes[0], block...)
		} else {
			lanes = append(lanes, block)
		}
	}
	return lanes
}

func fleetTerminal(state string) bool {
	return state == "success" || state == "failed" || state == "skipped"
}

// laneNext returns the next item of a lane to work on (-1 when the lane is
// over) and the items to skip: a failure stops the rest of its lane.
func laneNext(items []store.FleetUpdateItem, lane []int, cancelled bool) (next int, skip []int) {
	for k, i := range lane {
		switch items[i].State {
		case "success":
			continue
		case "failed", "skipped":
			for _, j := range lane[k+1:] {
				if !fleetTerminal(items[j].State) {
					skip = append(skip, j)
				}
			}
			return -1, skip
		case "pending":
			if cancelled {
				for _, j := range lane[k:] {
					if !fleetTerminal(items[j].State) {
						skip = append(skip, j)
					}
				}
				return -1, skip
			}
		}
		return i, nil
	}
	return -1, nil
}

type fleetTotals struct {
	Success  int     `json:"success"`
	Failed   int     `json:"failed"`
	Skipped  int     `json:"skipped"`
	Active   int     `json:"active"`
	Pending  int     `json:"pending"`
	Progress float64 `json:"progress"`
}

// fleetAggregate sums up the items of a run; finished items count as 100 %.
func fleetAggregate(items []store.FleetUpdateItem) fleetTotals {
	var t fleetTotals
	for _, it := range items {
		p := it.Progress
		switch it.State {
		case "success":
			t.Success++
		case "failed":
			t.Failed++
		case "skipped":
			t.Skipped++
		case "pending":
			t.Pending++
		default:
			t.Active++
		}
		if fleetTerminal(it.State) {
			p = 100
		}
		t.Progress += max(p, 0)
	}
	if len(items) > 0 {
		t.Progress /= float64(len(items))
	}
	return t
}

// fleetRunStatus is the final status of a run.
func fleetRunStatus(t fleetTotals, cancelled bool) string {
	switch {
	case t.Failed > 0:
		return "failed"
	case cancelled && t.Skipped > 0:
		return "cancelled"
	}
	return "success"
}

// fleetItemProgress maps an item state to a share of its work.
func fleetItemProgress(state string, jobProgress float64) float64 {
	switch state {
	case "running":
		if jobProgress < 0 {
			return 30
		}
		return 10 + jobProgress*0.6
	case "restarting":
		return 75
	case "rebooting":
		return 85
	case "verifying":
		return 90
	case "pending":
		return 0
	}
	return 100
}
