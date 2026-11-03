package health

import (
	"math"
	"time"
)

// Wear sums up the wear of an SSD from its SMART history.
type Wear struct {
	PercentUsed *int64 `json:"percentUsed,omitempty"`
	Spare       *int64 `json:"spare,omitempty"`
	Written     *int64 `json:"written,omitempty"` // bytes written by the host
	// Endurance is the rated amount of writes, estimated from the bytes
	// written and the percentage used (SMART does not give the TBW).
	Endurance *int64 `json:"endurance,omitempty"`
	// WriteRate: bytes written per day, over the last 30 days (or the
	// RateDays the history covers, at least 3).
	WriteRate *float64 `json:"writeRate,omitempty"`
	RateDays  float64  `json:"rateDays,omitempty"`
	// RemainingDays at the current rate before the rated endurance is used up.
	RemainingDays *int64 `json:"remainingDays,omitempty"`
}

const day = 86400

// WearOf estimates the wear of an SSD; points are in time order.
func WearOf(pts []Point) Wear {
	var w Wear
	if len(pts) == 0 {
		return w
	}
	last := pts[len(pts)-1]
	w.PercentUsed, w.Spare, w.Written = last.PercentUsed, last.Spare, last.Written
	if w.Written != nil && w.PercentUsed != nil && *w.PercentUsed >= 1 {
		e := *w.Written * 100 / *w.PercentUsed
		w.Endurance = &e
	}
	// Write rate over the last 30 days.
	if w.Written != nil {
		for _, p := range pts {
			if p.Written == nil || p.T < last.T-30*day {
				continue
			}
			span := float64(last.T-p.T) / day
			if span >= 3 && *last.Written >= *p.Written {
				r := float64(*last.Written-*p.Written) / span
				w.WriteRate, w.RateDays = &r, math.Round(span*10)/10
			}
			break
		}
	}
	if w.PercentUsed != nil && *w.PercentUsed >= 100 {
		zero := int64(0)
		w.RemainingDays = &zero
		return w
	}
	switch {
	case w.Endurance != nil && w.WriteRate != nil && *w.WriteRate > 0:
		d := int64(float64(*w.Endurance-*w.Written) / *w.WriteRate)
		w.RemainingDays = &d
	case w.PercentUsed != nil && w.Written == nil:
		// No byte counter: the percentage itself, when it moved by 2
		// points or more over at least 30 days (it is a whole number).
		for _, p := range pts {
			if p.PercentUsed == nil || p.T < last.T-365*day {
				continue
			}
			span := float64(last.T-p.T) / day
			if dp := *last.PercentUsed - *p.PercentUsed; span >= 30 && dp >= 2 {
				d := int64(float64(100-*last.PercentUsed) / (float64(dp) / span))
				w.RemainingDays = &d
			}
			break
		}
	}
	return w
}

// CacheAdvice gives the recommendations for an SSD (mode: the cache mode
// when it is an LVM cache, "l2arc"/"slog" for ZFS, "" otherwise; hitRatio
// the share of reads it served over 30 days, -1 when unknown).
func CacheAdvice(w Wear, mode string, hitRatio float64, reads int64) []string {
	var out []string
	if u := w.PercentUsed; u != nil {
		switch {
		case *u >= 90:
			out = append(out, sub("Replace the SSD now: it has used {0}% of its rated endurance", *u))
		case *u >= 80:
			out = append(out, sub("Plan to replace the SSD: it has used {0}% of its rated endurance", *u))
		}
	}
	if d := w.RemainingDays; d != nil && *d > 0 {
		switch m := *d / 30; {
		case m < 24:
			out = append(out, sub("At the current write rate, the SSD reaches its rated endurance in about {0} months", max(m, 1)))
		case m >= 120:
			out = append(out, "At the current write rate, the SSD lasts more than 10 years")
		default:
			out = append(out, sub("At the current write rate, the SSD lasts about {0} more years", m/12))
		}
	}
	switch mode {
	case "writeback":
		out = append(out, "Write-back: every write lands on the SSD first. A consumer SSD wears faster this way, and data not yet copied to the disks is lost if it fails: write-through is safer, or use a mirrored or data-center SSD")
	case "slog":
		out = append(out, "Log device (SLOG): every synchronous write goes through it. Prefer an SSD with power-loss protection and high endurance")
	}
	if hitRatio >= 0 && hitRatio < 20 && reads >= 10000 {
		out = append(out, sub("Only {0}% of reads come from the SSD: the cache brings little for its wear", int(math.Round(hitRatio))))
	}
	if len(out) == 0 {
		if w.PercentUsed == nil && w.Written == nil {
			return []string{"This SSD reports no wear counter"}
		}
		if w.RemainingDays == nil {
			return []string{"Wear is low. The remaining life is estimated after a few days of history"}
		}
		return []string{"Wear is low: nothing to do"}
	}
	return out
}

// Since returns the first point not older than t (nil when none).
func Since(pts []Point, t time.Time) *Point {
	for i := range pts {
		if pts[i].T >= t.Unix() {
			return &pts[i]
		}
	}
	return nil
}
