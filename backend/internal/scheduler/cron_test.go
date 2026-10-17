package scheduler

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	cases := []struct{ expr, from, want string }{
		{"0 3 * * *", "2026-10-17 01:00", "2026-10-17 03:00"},
		{"0 3 * * *", "2026-10-17 03:00", "2026-10-18 03:00"},
		{"*/15 * * * *", "2026-10-17 10:07", "2026-10-17 10:15"},
		{"0 2 * * 0", "2026-10-17 12:00", "2026-10-18 02:00"}, // 2026-10-17 is a Saturday
		{"0 2 * * 7", "2026-10-17 12:00", "2026-10-18 02:00"},
		{"30 4 1 * *", "2026-10-17 12:00", "2026-11-01 04:30"},
		{"0 0 29 2 *", "2026-10-17 12:00", "2028-02-29 00:00"},
		{"@hourly", "2026-10-17 12:30", "2026-10-17 13:00"},
		{"0 9 1-7 * 1", "2026-10-17 12:00", "2026-10-19 09:00"}, // dom OR dow
		{"0 0,12 * * 1-5", "2026-10-17 12:00", "2026-10-19 00:00"},
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		got := cr.Next(at(c.from))
		if !got.Equal(at(c.want)) {
			t.Errorf("%s from %s: got %s, want %s", c.expr, c.from, got.Format("2006-01-02 15:04"), c.want)
		}
		if !cr.Match(got) {
			t.Errorf("%s: Next returned a non-matching time", c.expr)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, e := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "*/0 * * * *", "5-1 * * * *", "a * * * *"} {
		if _, err := ParseCron(e); err == nil {
			t.Errorf("%q: expected an error", e)
		}
	}
}

func TestExpired(t *testing.T) {
	now := at("2026-10-17 12:00")
	days := []time.Time{now, now.Add(-24 * time.Hour), now.Add(-48 * time.Hour), now.Add(-72 * time.Hour), now.Add(-30 * 24 * time.Hour)}
	id := func(t time.Time) time.Time { return t }
	if got := expired(days, id, 3, 0, now); len(got) != 2 {
		t.Errorf("keepCount=3: %d removed, want 2", len(got))
	}
	if got := expired(days, id, 0, 2, now); len(got) != 2 { // 3 and 30 days old
		t.Errorf("keepDays=2: %d removed, want 2", len(got))
	}
	if got := expired(days[4:], id, 0, 1, now); len(got) != 0 {
		t.Errorf("the newest snapshot must always be kept")
	}
}
