package hardware

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestInterpolate(t *testing.T) {
	c := curve(30, 20, 40, 60, 50, 100)
	for _, tc := range []struct{ t, want float64 }{{10, 20}, {30, 20}, {35, 40}, {40, 60}, {45, 80}, {50, 100}, {80, 100}} {
		if got := Interpolate(c, tc.t); got != tc.want {
			t.Errorf("%v °C: %v, want %v", tc.t, got, tc.want)
		}
	}
	// Below the floor, clamped to MinPWM; no curve: full speed.
	if got := Interpolate(curve(30, 5, 40, 10), 35); got != MinPWM {
		t.Errorf("floor: %v", got)
	}
	if Interpolate(nil, 20) != 100 {
		t.Error("empty curve must give full speed")
	}
}

func TestValidateCurve(t *testing.T) {
	for _, tc := range []struct {
		c  []store.FanPoint
		ok bool
	}{
		{curve(30, 20, 50, 100), true},
		{curve(30, 20), false},
		{curve(30, 20, 30, 50), false},
		{curve(50, 20, 30, 50), false},
		{curve(30, 50, 40, 30), false},
		{curve(30, 5, 50, 100), false},
		{curve(30, 20, 120, 100), false},
		{curve(1, 20, 2, 20, 3, 20, 4, 20, 5, 20, 6, 20, 7, 20, 8, 20, 9, 20), false},
	} {
		if err := ValidateCurve(tc.c); (err == nil) != tc.ok {
			t.Errorf("%v: %v", tc.c, err)
		}
	}
	for name, p := range presets {
		if ValidateCurve(p[0]) != nil || ValidateCurve(p[1]) != nil {
			t.Errorf("preset %s invalid", name)
		}
	}
}

func TestTargetFallback(t *testing.T) {
	cpu, disk := curve(40, 20, 80, 100), curve(30, 20, 50, 100)
	if p, ok := Target(cpu, disk, Temps{CPU: math.NaN(), Disk: math.NaN()}); ok || p != 100 {
		t.Fatalf("no temperature: %v %v", p, ok)
	}
	if p, ok := Target(cpu, disk, Temps{CPU: 60, Disk: math.NaN()}); !ok || p != 60 {
		t.Fatalf("cpu only: %v", p)
	}
	// The hotter source (relative to its curve) wins.
	if p, _ := Target(cpu, disk, Temps{CPU: 60, Disk: 45}); p != 80 {
		t.Fatalf("max: %v", p)
	}
}

func TestHysteresis(t *testing.T) {
	cpu, disk := curve(40, 20, 80, 100), curve(30, 20, 50, 100)
	h := Hysteresis{Degrees: 3}
	step := func(d float64) float64 {
		p, ok := h.Step(cpu, disk, Temps{CPU: math.NaN(), Disk: d})
		if !ok {
			t.Fatal("fallback")
		}
		return p
	}
	if p := step(40); p != 60 {
		t.Fatalf("start: %v", p)
	}
	if p := step(42); p != 68 {
		t.Fatalf("rise is immediate: %v", p)
	}
	// Small drops keep the speed.
	if p := step(41); p != 68 {
		t.Fatalf("1° drop: %v", p)
	}
	if p := step(39.5); p != 68 {
		t.Fatalf("2.5° drop: %v", p)
	}
	// A larger drop lowers it, to the speed of 3° more than now.
	if p := step(38); p != 64 {
		t.Fatalf("4° drop: %v", p)
	}
	// Oscillating by less than the hysteresis does not move the speed.
	for _, d := range []float64{39, 38, 40, 38.5, 40.9} {
		if p := step(d); p != 64 {
			t.Fatalf("hunting at %v: %v", d, p)
		}
	}
	// No reading: full speed, then the curve again.
	if p, ok := h.Step(cpu, disk, Temps{CPU: math.NaN(), Disk: math.NaN()}); ok || p != 100 {
		t.Fatalf("fallback %v %v", p, ok)
	}
	if p := step(30); p != 20 {
		t.Fatalf("after reset: %v", p)
	}
}

func put(t *testing.T, p, v string, mode os.FileMode) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(v+"\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

func TestScanAndController(t *testing.T) {
	root := t.TempDir()
	HwmonRoot = root
	defer func() { HwmonRoot = "/sys/class/hwmon" }()
	it := filepath.Join(root, "hwmon2")
	put(t, it+"/name", "it8613", 0o444)
	put(t, it+"/pwm1", "120", 0o644)
	put(t, it+"/pwm1_enable", "2", 0o644)
	put(t, it+"/fan1_input", "900", 0o444)
	put(t, it+"/pwm2", "255", 0o444) // read-only output
	put(t, it+"/fan3_input", "1200", 0o444)
	ct := filepath.Join(root, "hwmon1")
	put(t, ct+"/name", "coretemp", 0o444)
	put(t, ct+"/temp1_input", "47000", 0o444)
	put(t, ct+"/temp2_input", "52500", 0o444)
	acpi := filepath.Join(root, "hwmon0")
	put(t, acpi+"/name", "acpitz", 0o444)
	put(t, acpi+"/temp1_input", "90000", 0o444)

	pwms, sensors := Scan()
	if len(pwms) != 2 || !pwms[0].Controllable || pwms[0].RPM != 900 || pwms[0].Enable != 2 || pwms[1].Controllable {
		t.Fatalf("pwms: %+v", pwms)
	}
	if len(sensors) != 1 || sensors[0].RPM != 1200 {
		t.Fatalf("sensors: %+v", sensors)
	}
	if got := CPUTemp(); got != 52.5 {
		t.Fatalf("cpu temp: %v", got)
	}

	state := filepath.Join(t.TempDir(), "orig.json")
	c := NewController(state)
	if c.Active() {
		t.Fatal("active before use")
	}
	if err := c.Drive(pwms, 50); err != nil {
		t.Fatal(err)
	}
	if get(t, it+"/pwm1_enable") != "1" || get(t, it+"/pwm1") != "128" || get(t, it+"/pwm2") != "255" {
		t.Fatalf("drive: enable %s pwm %s", get(t, it+"/pwm1_enable"), get(t, it+"/pwm1"))
	}
	// A restart (or a crash) finds the originals again.
	c2 := NewController(state)
	if !c2.Active() {
		t.Fatal("originals not saved")
	}
	if err := c2.Release(); err != nil {
		t.Fatal(err)
	}
	if get(t, it+"/pwm1_enable") != "2" || c2.Active() {
		t.Fatal("original mode not restored")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("state file left")
	}
}

func TestLEDs(t *testing.T) {
	root := t.TempDir()
	LEDRoot = root
	defer func() { LEDRoot = "/sys/class/leds" }()
	put(t, root+"/sys:green/brightness", "0", 0o644)
	put(t, root+"/sys:green/max_brightness", "255", 0o444)
	put(t, root+"/sys:green/trigger", "[none] timer disk-activity", 0o644)
	put(t, root+"/input3::capslock/brightness", "0", 0o644)
	leds := LEDs()
	if len(leds) != 1 || leds[0].Name != "sys:green" || !leds[0].Disk || leds[0].Trigger != "none" {
		t.Fatalf("%+v", leds)
	}
	if err := SetLED("sys:green", "on"); err != nil || get(t, root+"/sys:green/brightness") != "255" {
		t.Fatalf("on: %v", err)
	}
	if err := SetLED("sys:green", "disk"); err != nil || get(t, root+"/sys:green/trigger") != "disk-activity" {
		t.Fatalf("disk: %v", err)
	}
	for _, bad := range []string{"../x", "input3::capslock", "nope"} {
		if SetLED(bad, "on") == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if SetLED("sys:green", "blink") == nil {
		t.Error("bad mode accepted")
	}
}

func TestTerraMaster(t *testing.T) {
	if !(DMI{Vendor: "TerraMaster", Product: "F2-223"}).TerraMaster() || (DMI{Vendor: "QEMU"}).TerraMaster() {
		t.Fatal("dmi")
	}
}
