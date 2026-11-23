package photos

import (
	"testing"
	"time"
)

func TestRecaps(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.Local)
	at := func(y int, m time.Month, d int) Item { return Item{Path: "/p", Date: time.Date(y, m, d, 10, 0, 0, 0, time.Local).Unix()} }
	items := []Item{ // newest first, as the index keeps them
		at(2026, 11, 20), at(2026, 11, 2), // current month: excluded
		at(2026, 10, 31), at(2026, 10, 15), at(2026, 10, 1),
		at(2025, 12, 24), at(2025, 12, 1),
	}
	recaps := Recaps(items, now, 24)
	if len(recaps) != 2 {
		t.Fatalf("recaps = %+v, want 2 (current month excluded)", recaps)
	}
	if recaps[0].Year != 2026 || recaps[0].Month != 10 || recaps[0].Count != 3 || len(recaps[0].Items) != 3 {
		t.Errorf("first recap = %+v", recaps[0])
	}
	if recaps[1].Year != 2025 || recaps[1].Month != 12 || recaps[1].Count != 2 {
		t.Errorf("second recap = %+v", recaps[1])
	}

	// A month with more items than perMonth keeps a spread, newest first.
	many := []Item{}
	for d := 28; d >= 1; d-- {
		many = append(many, at(2026, 9, d))
	}
	r := Recaps(many, now, 5)
	if len(r) != 1 || len(r[0].Items) != 5 || r[0].Count != 28 {
		t.Fatalf("spread: %+v", r)
	}
	if got := r[0].Items[0].Date; got != at(2026, 9, 28).Date {
		t.Errorf("first picked = day %d, want 28 (newest)", time.Unix(got, 0).Day())
	}
	if got := r[0].Items[4].Date; got != at(2026, 9, 6).Date {
		t.Errorf("last picked = day %d, want 6", time.Unix(got, 0).Day())
	}
}
