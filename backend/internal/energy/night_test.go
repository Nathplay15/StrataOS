package energy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNightPlanValidate(t *testing.T) {
	ok := NightPlan{Enabled: true, From: "22:00", To: "07:00", SpindownMin: 20}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	for _, bad := range []NightPlan{
		{Enabled: true, From: "22:00", To: "22:00", SpindownMin: 20},
		{Enabled: true, From: "25:00", To: "07:00", SpindownMin: 20},
		{Enabled: true, From: "22:00", To: "07:00", SpindownMin: 300},
		{Enabled: true, From: "22:00", To: "07:00", SpindownMin: -1},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid plan accepted: %+v", bad)
		}
	}
	if err := (NightPlan{}).Validate(); err != nil {
		t.Fatalf("disabled plan: %v", err)
	}
}

func TestNightPlanActive(t *testing.T) {
	n := NightPlan{Enabled: true, From: "22:00", To: "07:00", SpindownMin: 20}
	at := func(s string) time.Time {
		tm, _ := time.Parse("2006-01-02 15:04", s)
		return tm
	}
	cases := []struct {
		now  time.Time
		want bool
	}{
		{at("2026-11-23 21:59"), false},
		{at("2026-11-23 22:00"), true},
		{at("2026-11-23 23:30"), true},
		{at("2026-11-24 00:00"), true}, // across midnight
		{at("2026-11-24 06:59"), true},
		{at("2026-11-24 07:00"), false},
		{at("2026-11-24 12:00"), false},
	}
	for _, c := range cases {
		if got := n.Active(c.now); got != c.want {
			t.Errorf("Active(%s) = %v, want %v", c.now.Format("15:04"), got, c.want)
		}
	}
	// Same-day window.
	d := NightPlan{Enabled: true, From: "12:00", To: "14:00"}
	if !d.Active(at("2026-11-23 13:00")) || d.Active(at("2026-11-23 15:00")) {
		t.Error("same-day window wrong")
	}
	// Disabled: never active.
	if (NightPlan{Enabled: false, From: "22:00", To: "07:00"}).Active(at("2026-11-23 23:00")) {
		t.Error("disabled plan must be inactive")
	}
}

func TestPowerNow(t *testing.T) {
	old := SysRoot
	SysRoot = t.TempDir()
	t.Cleanup(func() { SysRoot = old })
	dir := filepath.Join(SysRoot, "sys/class/hwmon/hwmon0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "power1_average"), []byte("12500000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir2 := filepath.Join(SysRoot, "sys/class/hwmon/hwmon1")
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "power1_average"), []byte("7500000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A broken sensor is skipped.
	dir3 := filepath.Join(SysRoot, "sys/class/hwmon/hwmon2")
	if err := os.MkdirAll(dir3, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir3, "power1_average"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := PowerNow(); w != 20 {
		t.Fatalf("PowerNow = %v, want 20 W", w)
	}
}
