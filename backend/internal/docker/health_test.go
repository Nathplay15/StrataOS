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
		name string
		st   InspectState
		want string // "" = healthy
	}{
		{"running fine", InspectState{Status: "running", Running: true, StartedAt: now.Add(-time.Hour)}, ""},
		{"restarting now", InspectState{Status: "restarting", StartedAt: now.Add(-time.Minute)}, "loop"},
		{"health unhealthy", InspectState{Status: "running", Running: true, Health: h("unhealthy"), StartedAt: now.Add(-time.Hour)}, "health"},
		{"health starting is fine", InspectState{Status: "running", Running: true, Health: h("starting"), StartedAt: now.Add(-time.Minute)}, ""},
		{"5 restarts in the hour", InspectState{Status: "running", RestartCount: 5, StartedAt: now.Add(-30 * time.Minute)}, "5 restarts"},
		{"5 restarts long ago", InspectState{Status: "running", RestartCount: 5, StartedAt: now.Add(-48 * time.Hour)}, ""},
		{"4 restarts only", InspectState{Status: "running", RestartCount: 4, StartedAt: now.Add(-10 * time.Minute)}, ""},
		{"start error", InspectState{Status: "exited", Error: "OCI runtime create failed"}, "failed to start"},
		{"oom killed", InspectState{Status: "exited", OOMKilled: true}, "out of memory"},
	}
	for _, c := range cases {
		got := CrashReason(c.st, now)
		if c.want == "" && got != "" {
			t.Errorf("%s: CrashReason = %q, want healthy", c.name, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: CrashReason = %q, want it to mention %q", c.name, got, c.want)
		}
	}
}
