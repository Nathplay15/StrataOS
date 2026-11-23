package energy

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// NightPlan is the "night energy plan" of roadmap 0.15: during the window
// (From..To, possibly across midnight) the rotating data disks spin down
// after SpindownMin minutes; outside, the global spindownMin applies again.
type NightPlan struct {
	Enabled     bool   `json:"enabled"`
	From        string `json:"from"`        // HH:MM
	To          string `json:"to"`          // HH:MM
	SpindownMin int    `json:"spindownMin"` // minutes of idle before standby, 0 = never
}

// Validate checks the window and the spin-down delay.
func (n NightPlan) Validate() error {
	if !n.Enabled {
		return nil
	}
	if !hhmm.MatchString(n.From) || !hhmm.MatchString(n.To) {
		return fmt.Errorf("times must be HH:MM")
	}
	if n.From == n.To {
		return fmt.Errorf("the start and end times must differ")
	}
	if n.SpindownMin < 0 || n.SpindownMin > 240 {
		return fmt.Errorf("spin-down must be between 0 and 240 minutes")
	}
	return nil
}

// Active reports whether now is inside the window (a window across midnight
// is active from From in the evening until To the next morning).
func (n NightPlan) Active(now time.Time) bool {
	if !n.Enabled || !hhmm.MatchString(n.From) || !hhmm.MatchString(n.To) {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	from := mustHHMM(n.From)
	to := mustHHMM(n.To)
	if from < to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to // across midnight
}

func mustHHMM(s string) int {
	h, _ := strconv.Atoi(s[:2])
	m, _ := strconv.Atoi(s[3:])
	return h*60 + m
}

// PowerNow returns the consumption of the NAS in watts, when the hardware
// reports it (hwmon power1_average, summed over the sensors), else 0.
func PowerNow() float64 {
	files, _ := filepath.Glob(filepath.Join(SysRoot, "sys/class/hwmon/hwmon*/power1_average"))
	total := 0.0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		uw, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil || uw <= 0 {
			continue
		}
		total += uw / 1e6
	}
	return total
}
