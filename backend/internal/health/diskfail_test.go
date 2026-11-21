package health

import (
	"testing"
	"time"

	"strataos/internal/storage"
)

func TestFailIn(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	// A reallocated-sector count that gains one every 4 hours: the slow
	// leak of a dying disk, traceable over a two-week window.
	var rising []Point
	for k := 0; k <= 83; k++ {
		rising = append(rising, Point{
			T:           now.Add(-time.Duration(83-k) * 4 * time.Hour).Unix(),
			SmartSample: storage.SmartSample{Reallocated: i64(int64(10 + k))},
		})
	}
	days, field, ok := FailIn(rising, now)
	if !ok || field != "re" {
		t.Fatalf("rising: ok=%v field=%q, want true/re", ok, field)
	}
	// The count is 93 and climbs 6 a day: about 15.5 days before doubling.
	if days < 14.5 || days > 16.5 {
		t.Errorf("rising: days=%v, want about 15.5", days)
	}
	// A flat count is a healthy disk, not a prediction.
	flat := append(rising[:0:0], rising...)
	for i := range flat {
		flat[i].SmartSample = storage.SmartSample{Reallocated: i64(93)}
	}
	if _, _, ok := FailIn(flat, now); ok {
		t.Error("flat: predicted a failure without growth")
	}
	// The first sectors alone are not a trend: a SMART reset makes the
	// counters jump back to 0, do not cry wolf over a handful of them.
	few := append(rising[:0:0], rising...)
	for i := range few {
		few[i].SmartSample = storage.SmartSample{Reallocated: i64(int64(i % 8))}
	}
	if _, _, ok := FailIn(few, now); ok {
		t.Error("count below 10: predicted a failure")
	}
	// Two points one day apart say nothing about a 30-day trend.
	short := rising[len(rising)-2:]
	if _, _, ok := FailIn(short, now); ok {
		t.Error("short span: predicted a failure")
	}
	// A gap (the counter was not read) is not a zero: the trace survives it.
	gapped := append(rising[:0:0], rising...)
	for i := 10; i < 20; i++ {
		gapped[i].SmartSample = storage.SmartSample{}
	}
	if _, field, ok := FailIn(gapped, now); !ok || field != "re" {
		t.Errorf("gapped: ok=%v field=%q, want true/re", ok, field)
	}
	// The soonest doubling wins: a pending-sector count climbing faster
	// than the reallocated one is the one reported.
	var pending []Point
	for k := 0; k <= 83; k++ {
		pending = append(pending, Point{
			T: rising[k].T,
			SmartSample: storage.SmartSample{
				Reallocated: i64(93),
				Pending:     i64(int64(100 + 4*k)),
			},
		})
	}
	if _, field, ok := FailIn(pending, now); !ok || field != "pe" {
		t.Errorf("pending: ok=%v field=%q, want true/pe", ok, field)
	}
	if _, _, ok := FailIn(nil, now); ok {
		t.Error("no points: predicted a failure")
	}
}
