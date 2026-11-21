package health

import (
	"time"

	"strataos/internal/storage"
)

// failCounters are the counters whose own growth predicts a failure: dead
// sectors and errors never heal, a disk that racks them up is wearing out.
var failCounters = []struct {
	field string
	get   func(storage.SmartSample) *int64
}{
	{"re", func(s storage.SmartSample) *int64 { return s.Reallocated }},
	{"pe", func(s storage.SmartSample) *int64 { return s.Pending }},
	{"un", func(s storage.SmartSample) *int64 { return s.Uncorrectable }},
}

// FailIn projects when a disk becomes likely to fail: a least-squares line
// over the points of the last 30 days of a counter predicts the days left
// before it doubles. ok is false without a usable history (too short),
// without growth, or for counts that say nothing yet: a SMART reset makes
// them jump back to 0, and the first sectors alone are not a trend.
func FailIn(pts []Point, now time.Time) (days float64, field string, ok bool) {
	if len(pts) < 2 {
		return 0, "", false
	}
	since := now.Add(-30 * 24 * time.Hour).Unix()
	recent := pts[:0:0]
	for _, p := range pts {
		if p.T >= since {
			recent = append(recent, p)
		}
	}
	if len(recent) >= 2 {
		pts = recent
	}
	if pts[len(pts)-1].T-pts[0].T < 3*86400 {
		return 0, "", false
	}
	best := 0.0
	for _, c := range failCounters {
		// The counter must be traced over the whole window: a reading that
		// never saw the counter is a gap, not a zero.
		var trace int
		for _, p := range pts {
			if c.get(p.SmartSample) != nil {
				trace++
			}
		}
		if trace < 2 {
			continue
		}
		var n float64
		var sx, sy, sxx, sxy float64
		for _, p := range pts {
			if v := c.get(p.SmartSample); v != nil {
				x, y := float64(p.T)/86400, float64(*v)
				n++
				sx, sy, sxx, sxy = sx+x, sy+y, sxx+x*x, sxy+x*y
			}
		}
		den := n*sxx - sx*sx
		if den == 0 {
			continue
		}
		slope := (n*sxy - sx*sy) / den
		// The count is the last value actually reported, not the line.
		cur := 0.0
		for i := len(pts) - 1; i >= 0; i-- {
			if v := c.get(pts[i].SmartSample); v != nil {
				cur = float64(*v)
				break
			}
		}
		if slope <= 0 || cur < 10 {
			continue
		}
		d := cur / slope // days before the count doubles
		if best == 0 || d < best {
			best, field, ok = d, c.field, true
		}
	}
	return best, field, ok
}
