package api

import (
	"strings"
	"testing"
)

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }
func texts(rs []fleetReason) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Level+":"+r.Text)
	}
	return strings.Join(out, "|")
}

func TestFleetReasons(t *testing.T) {
	if rs := fleetReasons(false, nil); fleetLevel(rs) != levelCritical || rs[0].Kind != "online" {
		t.Fatalf("offline: %v", rs)
	}
	healthy := &fleetHealth{Updates: &fleetUpdateInfo{}, Advisor: &fleetAdvisor{Score: 92}}
	if rs := fleetReasons(true, healthy); len(rs) != 0 || fleetLevel(rs) != levelOK {
		t.Fatalf("healthy: %v", rs)
	}
	h := &fleetHealth{
		Disks:   fleetDisks{Failing: 1, Warning: 2, MaxWear: i64(93)},
		Arrays:  fleetArrays{Degraded: 1, Rebuilding: 1},
		Space:   fleetSpace{Fullest: "volume1", FullestPct: 94.6, Unmounted: 1},
		Backups: fleetBackups{Failed: 1, Stale: []string{"Daily"}},
		Advisor: &fleetAdvisor{Score: 40, High: 2},
		Temps:   fleetTemps{CPU: f64(91), Disk: i64(58), DiskName: "/dev/sda"},
		Alerts:  fleetAlerts{Errors: 3},
		Updates: &fleetUpdateInfo{Candidate: "0.10.0", RebootRequired: true},
	}
	got := texts(fleetReasons(true, h))
	for _, want := range []string{
		"critical:1 disk(s) failing", "critical:1 degraded or failed array(s)", "warning:1 backup task(s) failed",
		"warning:1 backup task(s) without success for 8 days", "warning:2 disk(s) with SMART warnings",
		"warning:An SSD has used 93% of its endurance", "info:1 array(s) rebuilding", "warning:Volume volume1 is 95% full",
		"warning:1 volume(s) not mounted", "warning:2 high-risk security finding(s)", "warning:Processor at 91 °C",
		"warning:Disk /dev/sda at 58 °C", "info:StrataOS 0.10.0 available", "info:Restart required",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	// Error alerts count only when no failing disk or array explains them.
	if strings.Contains(got, "error alert") {
		t.Errorf("error alerts counted twice: %s", got)
	}
	if fleetLevel(fleetReasons(true, h)) != levelCritical {
		t.Error("level should be critical")
	}
	only := &fleetHealth{Alerts: fleetAlerts{Errors: 2}, Advisor: &fleetAdvisor{Score: 50}}
	if got := texts(fleetReasons(true, only)); got != "critical:2 error alert(s)|warning:Security score 50/100" {
		t.Errorf("alerts and score: %s", got)
	}
	if got := fleetLevel(fleetReasons(true, &fleetHealth{Partial: true})); got != levelInfo {
		t.Errorf("partial: %s", got)
	}
}

func TestFleetHealthSum(t *testing.T) {
	up := &fleetHealth{Updates: &fleetUpdateInfo{System: 3}, Space: fleetSpace{Total: 100, Used: 40}}
	t1 := fleetHealthSum([]fleetHealthItem{
		{Online: true, Level: levelOK, Health: up},
		{Online: true, Level: levelWarning, Health: &fleetHealth{Space: fleetSpace{Total: 50, Used: 10}}},
		{Online: false, Level: levelCritical, Health: &fleetHealth{Space: fleetSpace{Total: 1000, Used: 1000}}},
	})
	if t1.Total != 3 || t1.Online != 2 || t1.Offline != 1 || t1.Attention != 2 || t1.Critical != 1 || t1.Updates != 1 || t1.SpaceTotal != 150 || t1.SpaceUsed != 50 {
		t.Fatalf("%+v", t1)
	}
}

func TestFleetSpaceOf(t *testing.T) {
	sp := fleetSpaceOf([]fleetVol{{Name: "a", Total: 1000, Used: 500, Health: "healthy"}, {Name: "b", Total: 100, Used: 91, Health: "degraded"}, {Name: "c", Health: "unmounted"}})
	if sp.Total != 1100 || sp.Used != 591 || sp.Fullest != "b" || sp.FullestPct != 91 || sp.Unmounted != 1 || len(sp.Volumes) != 3 {
		t.Fatalf("%+v", sp)
	}
	if sp := fleetSpaceOf(nil); sp.Volumes == nil {
		t.Fatal("volumes must be a list")
	}
}

func TestFleetFromSummary(t *testing.T) {
	sum := map[string]any{
		"hostname": "old-nas", "version": "0.9.1", "uptimeDays": 12.0, "disks": 4.0, "raids": 1.0, "pools": 0.0,
		"volumes": []any{map[string]any{"name": "v1", "total": 1000.0, "used": 950.0, "health": "degraded"}},
		"backups": map[string]any{"ok": 2.0, "failed": 1.0, "lastFailure": "Photos"},
		"alerts": []any{
			map[string]any{"level": "error", "message": "Disk /dev/sdb (X Y) fails its SMART health check: replace it"},
			map[string]any{"level": "warning", "message": "Disk /dev/sdc (X Z) is getting weak: 8 reallocated sectors. Check your backups and plan its replacement"},
		},
		"updates": map[string]any{"version": "0.9.1", "candidate": "0.10.0", "system": 2.0, "rebootRequired": false},
		"access":  map[string]any{"updates": true, "reboot": false},
	}
	h := fleetFromSummary(sum)
	if !h.Partial || h.Hostname != "old-nas" || h.UptimeDays != 12 || h.Disks.Total != 4 || h.Disks.Failing != 1 || h.Disks.Warning != 1 {
		t.Fatalf("%+v", h)
	}
	if h.Arrays.Degraded != 1 || h.Space.Fullest != "v1" || h.Backups.Failed != 1 || h.Backups.Total != 3 || h.Backups.LastFailureName != "Photos" {
		t.Fatalf("%+v", h)
	}
	if h.Updates == nil || h.Updates.Candidate != "0.10.0" || h.Updates.System != 2 || h.Access == nil || !h.Access.Updates {
		t.Fatalf("%+v", h)
	}
	if fleetLevel(fleetReasons(true, h)) != levelCritical {
		t.Fatal("a failing disk is critical")
	}
}
