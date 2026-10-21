// Package hardware drives the fans (hwmon PWM outputs) and front-panel LEDs.
package hardware

import (
	"fmt"
	"math"
	"sort"

	"strataos/internal/store"
)

// Fan profiles. ProfileBIOS leaves the fans to the firmware (StrataOS writes nothing).
const (
	ProfileBIOS     = "bios"
	ProfileQuiet    = "quiet"
	ProfileBalanced = "balanced"
	ProfileCool     = "cool"
	ProfileCustom   = "custom"
)

type pt = store.FanPoint

// curve builds a curve from temperature, speed pairs.
func curve(v ...int) []pt {
	out := make([]pt, 0, len(v)/2)
	for i := 0; i+1 < len(v); i += 2 {
		out = append(out, pt{Temp: v[i], PWM: v[i+1]})
	}
	return out
}

// Preset curves: one for the processor, one for the disks. Fans run at the
// highest of both, never below MinPWM.
var presets = map[string][2][]pt{
	ProfileQuiet:    {curve(50, 20, 70, 40, 85, 100), curve(35, 20, 42, 35, 47, 60, 52, 100)},
	ProfileBalanced: {curve(45, 30, 65, 55, 80, 100), curve(32, 30, 38, 45, 45, 75, 50, 100)},
	ProfileCool:     {curve(40, 45, 60, 75, 75, 100), curve(28, 45, 35, 65, 42, 90, 46, 100)},
}

// MinPWM keeps the fans spinning whatever the curve says.
const MinPWM = 15

// Curves returns the processor and disk curves of a profile.
func Curves(h store.Hardware) (cpu, disk []store.FanPoint) {
	if h.FanProfile == ProfileCustom {
		return h.CPUCurve, h.DiskCurve
	}
	if p, ok := presets[h.FanProfile]; ok {
		return p[0], p[1]
	}
	return nil, nil
}

// Presets exposes the preset curves to the interface.
func Presets() map[string][2][]store.FanPoint { return presets }

// ValidateCurve checks a custom curve: 2 to 8 points, temperatures increasing
// within 0–100 °C, speeds within MinPWM–100 % and never decreasing.
func ValidateCurve(c []store.FanPoint) error {
	if len(c) < 2 || len(c) > 8 {
		return fmt.Errorf("a fan curve needs 2 to 8 points")
	}
	for i, p := range c {
		if p.Temp < 0 || p.Temp > 100 {
			return fmt.Errorf("temperature out of range (0–100 °C)")
		}
		if p.PWM < MinPWM || p.PWM > 100 {
			return fmt.Errorf("fan speed must be between %d and 100 %%", MinPWM)
		}
		if i > 0 && p.Temp <= c[i-1].Temp {
			return fmt.Errorf("curve temperatures must increase")
		}
		if i > 0 && p.PWM < c[i-1].PWM {
			return fmt.Errorf("fan speed must not decrease when the temperature rises")
		}
	}
	return nil
}

// Interpolate returns the speed (percent) for a temperature: linear between
// points, flat before the first and after the last.
func Interpolate(c []store.FanPoint, t float64) float64 {
	if len(c) == 0 {
		return 100
	}
	pts := append([]store.FanPoint{}, c...)
	sort.Slice(pts, func(i, j int) bool { return pts[i].Temp < pts[j].Temp })
	if t <= float64(pts[0].Temp) {
		return clampPWM(float64(pts[0].PWM))
	}
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		if t <= float64(b.Temp) {
			f := (t - float64(a.Temp)) / float64(b.Temp-a.Temp)
			return clampPWM(float64(a.PWM) + f*float64(b.PWM-a.PWM))
		}
	}
	return clampPWM(float64(pts[len(pts)-1].PWM))
}

func clampPWM(v float64) float64 { return math.Max(MinPWM, math.Min(100, v)) }

// Temps is a reading of the sensors: NaN when a source is unavailable.
type Temps struct {
	CPU  float64
	Disk float64
}

// Target is the speed wanted for a reading. ok is false when no temperature
// could be read: the caller must then run the fans at full speed.
func Target(cpuCurve, diskCurve []store.FanPoint, t Temps) (pct float64, ok bool) {
	pct = 0
	if !math.IsNaN(t.CPU) && len(cpuCurve) > 0 {
		pct, ok = math.Max(pct, Interpolate(cpuCurve, t.CPU)), true
	}
	if !math.IsNaN(t.Disk) && len(diskCurve) > 0 {
		pct, ok = math.Max(pct, Interpolate(diskCurve, t.Disk)), true
	}
	if !ok {
		return 100, false
	}
	return pct, true
}

// Hysteresis avoids fans hunting around a curve point: the speed rises as soon
// as the curve asks for more, but only drops to what the curve gives for
// temperatures Degrees higher than the current ones, i.e. once the sources have
// cooled down by Degrees. Works with several sources (processor and disks).
type Hysteresis struct {
	Degrees float64
	cur     float64
	set     bool
}

// Next returns the speed to apply: up is the target for the current
// temperatures, down the target for temperatures raised by Degrees.
func (h *Hysteresis) Next(up, down float64) float64 {
	switch {
	case !h.set || up > h.cur:
		h.cur, h.set = up, true
	case down < h.cur:
		h.cur = math.Max(down, up)
	}
	return h.cur
}

// Step computes the next speed for a reading. ok is false when no temperature
// could be read: the speed is then 100 % and the hysteresis is reset.
func (h *Hysteresis) Step(cpuCurve, diskCurve []store.FanPoint, t Temps) (pct float64, ok bool) {
	up, ok := Target(cpuCurve, diskCurve, t)
	if !ok {
		h.Reset()
		return 100, false
	}
	down, _ := Target(cpuCurve, diskCurve, Temps{CPU: t.CPU + h.Degrees, Disk: t.Disk + h.Degrees})
	return h.Next(up, down), true
}

// Reset forgets the current speed (after a full-speed fallback, or when control resumes).
func (h *Hysteresis) Reset() { h.set = false }
