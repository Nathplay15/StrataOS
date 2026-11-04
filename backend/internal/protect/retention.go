// Package protect holds the pure logic of data protection: snapshot
// retention, the 3-2-1 assessment, restore previews and restore test samples.
// internal/restore and internal/api gather the facts and run the commands.
package protect

import (
	"fmt"
	"sort"
	"time"
)

// Policy says which snapshots to keep. Each rule keeps some snapshots and a
// snapshot kept by any rule stays; the newest one is always kept.
type Policy struct {
	Last    int // the newest N
	Days    int // all those younger than N days
	Hourly  int // the newest of each of the last N hours that have one
	Daily   int // ... days
	Weekly  int // ... ISO weeks
	Monthly int // ... months
}

// Empty reports whether the policy keeps everything (no rule set).
func (p Policy) Empty() bool {
	return p.Last <= 0 && p.Days <= 0 && p.Hourly <= 0 && p.Daily <= 0 && p.Weekly <= 0 && p.Monthly <= 0
}

// Validate refuses negative values and absurd ones.
func (p Policy) Validate() error {
	for _, n := range []int{p.Last, p.Days, p.Hourly, p.Daily, p.Weekly, p.Monthly} {
		if n < 0 || n > 100000 {
			return fmt.Errorf("retention values must be between 0 and 100000")
		}
	}
	return nil
}

// String describes the policy shortly ("last 7, 24 hourly, 7 daily").
func (p Policy) String() string {
	var out string
	add := func(n int, what string) {
		if n > 0 {
			if out != "" {
				out += ", "
			}
			out += fmt.Sprintf("%d %s", n, what)
		}
	}
	add(p.Last, "last")
	add(p.Hourly, "hourly")
	add(p.Daily, "daily")
	add(p.Weekly, "weekly")
	add(p.Monthly, "monthly")
	add(p.Days, "days")
	return out
}

// bucket keys of a time, in its own location.
func hourKey(t time.Time) string  { return t.Format("2006-01-02T15") }
func dayKey(t time.Time) string   { return t.Format("2006-01-02") }
func monthKey(t time.Time) string { return t.Format("2006-01") }
func weekKey(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// Keep returns, for each time (any order), whether its snapshot is kept.
// An empty policy keeps everything.
func Keep(times []time.Time, p Policy, now time.Time) []bool {
	keep := make([]bool, len(times))
	if len(times) == 0 {
		return keep
	}
	if p.Empty() {
		for i := range keep {
			keep[i] = true
		}
		return keep
	}
	// Newest first; equal times keep their order.
	idx := make([]int, len(times))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return times[idx[a]].After(times[idx[b]]) })
	keep[idx[0]] = true
	for rank, i := range idx {
		if p.Last > 0 && rank < p.Last {
			keep[i] = true
		}
		if p.Days > 0 && now.Sub(times[i]) <= time.Duration(p.Days)*24*time.Hour {
			keep[i] = true
		}
	}
	calendar := func(n int, key func(time.Time) string) {
		if n <= 0 {
			return
		}
		seen := map[string]bool{}
		for _, i := range idx {
			k := key(times[i])
			if seen[k] {
				continue
			}
			if len(seen) == n {
				return
			}
			seen[k] = true
			keep[i] = true // newest snapshot of this period
		}
	}
	calendar(p.Hourly, hourKey)
	calendar(p.Daily, dayKey)
	calendar(p.Weekly, weekKey)
	calendar(p.Monthly, monthKey)
	return keep
}

// Expired returns the indexes of the snapshots to delete, oldest first.
func Expired(times []time.Time, p Policy, now time.Time) []int {
	keep := Keep(times, p, now)
	var out []int
	for i, k := range keep {
		if !k {
			out = append(out, i)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return times[out[a]].Before(times[out[b]]) })
	return out
}
