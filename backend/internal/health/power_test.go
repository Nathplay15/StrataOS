package health

import (
	"testing"
	"time"
)

func TestPowerHistory(t *testing.T) {
	s := NewStore(t.TempDir())
	now := time.Now()
	s.RecordPower(42.5, now)
	s.RecordPower(43, now.Add(time.Minute)) // too soon: dropped (min interval)
	s.RecordPower(50, now.Add(2*time.Hour))
	pts := s.Power()
	if len(pts) != 2 || pts[0].Watts != 42.5 || pts[1].Watts != 50 {
		t.Fatalf("power history = %+v", pts)
	}
	// Reloaded from disk.
	s2 := NewStore(s.dir)
	if pts := s2.Power(); len(pts) != 2 || pts[1].Watts != 50 {
		t.Fatalf("reloaded power = %+v", pts)
	}
}
