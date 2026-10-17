// Package scheduler runs periodic maintenance tasks: snapshots with retention, scrubs and SMART tests.
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed 5-field cron expression (minute hour day-of-month month day-of-week).
type Cron struct {
	min, hour, dom, month, dow uint64 // bitsets
	domAny, dowAny             bool
}

var fieldBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

var macros = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 0 * * *",
	"@weekly":  "0 0 * * 0",
	"@monthly": "0 0 1 * *",
	"@yearly":  "0 0 1 1 *",
}

// ParseCron parses expressions such as "0 3 * * 0", "*/15 * * * *", "30 2 1-7 * 6" or "@daily".
func ParseCron(expr string) (*Cron, error) {
	expr = strings.TrimSpace(expr)
	if m, ok := macros[expr]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields (minute hour day month weekday)")
	}
	var sets [5]uint64
	for i, field := range f {
		set, err := parseField(field, fieldBounds[i][0], fieldBounds[i][1])
		if err != nil {
			return nil, fmt.Errorf("field %d (%q): %w", i+1, field, err)
		}
		sets[i] = set
	}
	// Sunday may be written 0 or 7.
	if sets[4]&(1<<7) != 0 {
		sets[4] |= 1
	}
	return &Cron{min: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4], domAny: f[2] == "*", dowAny: f[4] == "*"}, nil
}

func parseField(s string, lo, hi int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(s, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid step")
			}
			step, part = n, part[:i]
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			x, err1 := strconv.Atoi(a)
			y, err2 := strconv.Atoi(b)
			if err1 != nil || err2 != nil || x > y {
				return 0, fmt.Errorf("invalid range")
			}
			from, to = x, y
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return 0, fmt.Errorf("invalid value")
			}
			from = n
			if step == 1 {
				to = n
			}
		}
		if from < lo || to > hi {
			return 0, fmt.Errorf("value out of range %d-%d", lo, hi)
		}
		for v := from; v <= to; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

func has(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

// Match reports whether t (truncated to the minute) satisfies the expression.
func (c *Cron) Match(t time.Time) bool {
	if !has(c.min, t.Minute()) || !has(c.hour, t.Hour()) || !has(c.month, int(t.Month())) {
		return false
	}
	return c.dayOK(t)
}

// Next returns the first matching minute strictly after t (zero time if none within ~4 years).
func (c *Cron) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(4, 0, 0)
	for t.Before(limit) {
		if !has(c.month, int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.dayOK(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !has(c.hour, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if has(c.min, t.Minute()) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// dayOK applies the standard cron rule: when both day fields are restricted, either may match.
func (c *Cron) dayOK(t time.Time) bool {
	domOK := has(c.dom, t.Day())
	dowOK := has(c.dow, int(t.Weekday()))
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dowOK
	case c.dowAny:
		return domOK
	default:
		return domOK || dowOK
	}
}
