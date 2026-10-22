package api

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestFleetAllowed(t *testing.T) {
	ro := store.FleetManager{}
	upd := store.FleetManager{AllowUpdates: true}
	rebootOnly := store.FleetManager{AllowReboot: true} // reboot without updates means nothing
	all := store.FleetManager{AllowUpdates: true, AllowReboot: true}
	for _, c := range []struct {
		m    store.FleetManager
		perm string
		ok   bool
	}{
		{ro, "", true}, {ro, "updates", false}, {ro, "reboot", false},
		{upd, "updates", true}, {upd, "reboot", false},
		{rebootOnly, "updates", false}, {rebootOnly, "reboot", false},
		{all, "updates", true}, {all, "reboot", true},
		{all, "shell", false},
	} {
		err := fleetAllowed(c.m, c.perm)
		if (err == nil) != c.ok {
			t.Errorf("%+v %q: err = %v, want ok=%v", c.m, c.perm, err, c.ok)
		}
		if e, isErr := err.(*Error); err != nil && (!isErr || e.Status != 403) {
			t.Errorf("%q: want a 403, got %v", c.perm, err)
		}
	}
}

func TestFleetRate(t *testing.T) {
	var f fleetRate
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !f.allow("a", 3, time.Minute, now) {
			t.Fatalf("call %d refused", i)
		}
	}
	if f.allow("a", 3, time.Minute, now) {
		t.Fatal("4th call allowed")
	}
	if !f.allow("b", 3, time.Minute, now) {
		t.Fatal("another console was limited")
	}
	if !f.allow("a", 3, time.Minute, now.Add(61*time.Second)) {
		t.Fatal("window did not slide")
	}
}

func TestFleetLanes(t *testing.T) {
	plain := func(n string) fleetNode { return fleetNode{Hostname: n, Host: n + ".lan"} }
	ha := func(n, role string, peers ...string) fleetNode {
		return fleetNode{Hostname: n, Host: n + ".lan", HARole: role, HAPeers: peers}
	}
	for _, c := range []struct {
		name    string
		nodes   []fleetNode
		rolling bool
		want    [][]int
	}{
		{"parallel", []fleetNode{plain("a"), plain("b"), plain("c")}, false, [][]int{{0}, {1}, {2}}},
		{"rolling keeps the order", []fleetNode{plain("a"), plain("b"), plain("c")}, true, [][]int{{0, 1, 2}}},
		{"HA pair shares a lane, secondary first",
			[]fleetNode{ha("p", "primary", "s"), plain("x"), ha("s", "secondary", "p.lan")}, false, [][]int{{2, 0}, {1}}},
		{"HA pair in rolling, secondary first where the pair starts",
			[]fleetNode{plain("x"), ha("p", "primary", "s"), plain("y"), ha("s", "secondary")}, true, [][]int{{0, 3, 1, 2}}},
		{"matched by address, one side enough",
			[]fleetNode{ha("p", "primary", "10.0.0.2"), {Hostname: "s", Host: "10.0.0.2", HARole: "secondary"}}, false, [][]int{{1, 0}}},
		{"partner not selected: on its own", []fleetNode{ha("p", "primary", "s"), plain("x")}, false, [][]int{{0}, {1}}},
		{"named but not in HA any more", []fleetNode{ha("p", "primary", "s"), plain("s")}, false, [][]int{{0}, {1}}},
		{"primary with two secondaries",
			[]fleetNode{ha("p", "primary", "s1", "s2"), ha("s1", "secondary", "p"), ha("s2", "secondary", "p"), plain("x")}, false, [][]int{{1, 2, 0}, {3}}},
		{"two pairs side by side",
			[]fleetNode{ha("p1", "primary", "s1"), ha("p2", "primary", "s2"), ha("s1", "secondary", "p1"), ha("s2", "secondary", "p2")}, false, [][]int{{2, 0}, {3, 1}}},
	} {
		if got := fleetLanes(c.nodes, c.rolling); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: lanes %v, want %v", c.name, got, c.want)
		}
	}
}

func items(states ...string) []store.FleetUpdateItem {
	out := make([]store.FleetUpdateItem, len(states))
	for i, s := range states {
		out[i] = store.FleetUpdateItem{Name: string(rune('a' + i)), State: s}
	}
	return out
}

func TestLaneNext(t *testing.T) {
	for _, c := range []struct {
		name      string
		states    []string
		lane      []int
		cancelled bool
		next      int
		skip      []int
	}{
		{"first pending", []string{"pending", "pending"}, []int{0, 1}, false, 0, nil},
		{"after a success", []string{"success", "pending"}, []int{0, 1}, false, 1, nil},
		{"lane order, not item order", []string{"pending", "success"}, []int{1, 0}, false, 0, nil},
		{"resumes a running item", []string{"success", "restarting", "pending"}, []int{0, 1, 2}, false, 1, nil},
		{"stops on failure", []string{"success", "failed", "pending", "pending"}, []int{0, 1, 2, 3}, false, -1, []int{2, 3}},
		{"done", []string{"success", "success"}, []int{0, 1}, false, -1, nil},
		{"cancel skips what has not started", []string{"success", "pending", "pending"}, []int{0, 1, 2}, true, -1, []int{1, 2}},
		{"cancel lets a running update finish", []string{"running", "pending"}, []int{0, 1}, true, 0, nil},
	} {
		next, skip := laneNext(items(c.states...), c.lane, c.cancelled)
		if next != c.next || !reflect.DeepEqual(skip, c.skip) {
			t.Errorf("%s: got %d %v, want %d %v", c.name, next, skip, c.next, c.skip)
		}
	}
}

func TestFleetAggregate(t *testing.T) {
	it := items("success", "failed", "skipped", "running", "pending")
	it[3].Progress = 50
	got := fleetAggregate(it)
	want := fleetTotals{Success: 1, Failed: 1, Skipped: 1, Active: 1, Pending: 1, Progress: (100 + 100 + 100 + 50 + 0) / 5.0}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if s := fleetRunStatus(got, false); s != "failed" {
		t.Errorf("status %q", s)
	}
	if s := fleetRunStatus(fleetAggregate(items("success", "skipped")), true); s != "cancelled" {
		t.Errorf("cancelled run: %q", s)
	}
	if s := fleetRunStatus(fleetAggregate(items("success", "success")), false); s != "success" {
		t.Errorf("successful run: %q", s)
	}
	if p := fleetAggregate(nil).Progress; p != 0 {
		t.Errorf("empty run progress %v", p)
	}
	if p := fleetItemProgress("running", -1); p <= 0 || p >= 100 {
		t.Errorf("indeterminate job progress %v", p)
	}
	if fleetItemProgress("running", 100) >= fleetItemProgress("restarting", -1) {
		t.Error("progress must grow along the states")
	}
}

func TestFleetSummaryText(t *testing.T) {
	run := store.FleetUpdateRun{Items: items("success", "failed", "skipped")}
	run.Items[0].From, run.Items[0].Version = "0.6.0", "0.6.1"
	run.Items[1].Error = "boom"
	level, subject, lines := fleetSummaryText(run)
	if level != "warning" || subject != "Grouped update failed" {
		t.Errorf("level %q subject %q", level, subject)
	}
	want := "Grouped update finished: 1 succeeded, 1 failed, 1 skipped\n• a: updated to StrataOS 0.6.1\n• b: failed — boom\n• c: skipped"
	if got := strings.Join(lines, "\n"); got != want {
		t.Errorf("got %q", got)
	}
	run = store.FleetUpdateRun{DryRun: true, Items: items("success")}
	if level, subject, lines = fleetSummaryText(run); level != "info" || subject != "Grouped update finished" || lines[1] != "• a: simulation succeeded" {
		t.Errorf("dry run: %q %q %v", level, subject, lines)
	}
	if level, _, _ = fleetSummaryText(store.FleetUpdateRun{Items: items("failed")}); level != "error" {
		t.Errorf("all failed: %q", level)
	}
}
