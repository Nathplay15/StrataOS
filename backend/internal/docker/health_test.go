package docker

import (
	"strings"
	"testing"
	"time"
)

func TestCrashReason(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	h := func(s string) *HealthState {
		return &HealthState{Status: s}
	}
	cases := []struct {
		name     string
		st       InspectState
		restarts int    // in the last hour, from the tracker
		want     string // "" = healthy
	}{
		{"running fine", InspectState{Status: "running", Running: true, StartedAt: now.Add(-time.Hour)}, 0, ""},
		{"restarting now", InspectState{Status: "restarting", StartedAt: now.Add(-time.Minute)}, 0, "loop"},
		{"health unhealthy", InspectState{Status: "running", Running: true, Health: h("unhealthy"), StartedAt: now.Add(-time.Hour)}, 0, "health"},
		{"health starting is fine", InspectState{Status: "running", Running: true, Health: h("starting"), StartedAt: now.Add(-time.Minute)}, 0, ""},
		{"5 restarts in the hour", InspectState{Status: "running", RestartCount: 9, StartedAt: now.Add(-30 * time.Minute)}, 5, "5 restarts"},
		{"many restarts long ago", InspectState{Status: "running", RestartCount: 50, StartedAt: now.Add(-1 * time.Minute)}, 0, ""},
		{"4 restarts only", InspectState{Status: "running", RestartCount: 4, StartedAt: now.Add(-10 * time.Minute)}, 4, ""},
		{"start error", InspectState{Status: "exited", Error: "OCI runtime create failed"}, 0, "failed to start"},
		{"oom killed", InspectState{Status: "exited", OOMKilled: true}, 0, "out of memory"},
	}
	for _, c := range cases {
		got := CrashReason(c.st, c.restarts)
		if c.want == "" && got != "" {
			t.Errorf("%s: CrashReason = %q, want healthy", c.name, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: CrashReason = %q, want it to mention %q", c.name, got, c.want)
		}
	}
}

func TestRestartTracker(t *testing.T) {
	var tr RestartTracker
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	// First seen with a long history: nothing in the last hour.
	if n := tr.Restarts("c", 40, t0); n != 0 {
		t.Fatalf("first observation: %d", n)
	}
	if n := tr.Restarts("c", 43, t0.Add(10*time.Minute)); n != 3 {
		t.Fatalf("+3: %d", n)
	}
	if n := tr.Restarts("c", 46, t0.Add(30*time.Minute)); n != 6 {
		t.Fatalf("+6: %d", n)
	}
	// 70 minutes on: the restarts before t0+10min left the window.
	if n := tr.Restarts("c", 46, t0.Add(80*time.Minute)); n != 3 {
		t.Fatalf("window: %d", n)
	}
	// Recreated container: its count starts again.
	if n := tr.Restarts("c", 1, t0.Add(90*time.Minute)); n != 0 {
		t.Fatalf("recreated: %d", n)
	}
}
