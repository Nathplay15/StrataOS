package api

import (
	"testing"
	"time"

	"strataos/internal/store"
)

func TestReportDue(t *testing.T) {
	s := newTestServer(t)
	monday := time.Date(2026, 11, 2, 8, 0, 0, 0, time.Local) // also not the 1st
	first := time.Date(2026, 12, 1, 8, 0, 0, 0, time.Local)  // a Tuesday
	cases := []struct {
		report  string
		monthly bool
		tick    time.Time
		want    string
	}{
		{"", false, monday, ""},
		{"", true, first, "monthly"}, // before 0.9.1
		{"", true, monday, ""},
		{"weekly", false, monday, "weekly"},
		{"weekly", true, first, ""},
		{"monthly", false, first, "monthly"},
		{"off", true, first, ""},
		{"weekly", false, monday.Add(time.Minute), ""},
	}
	for _, c := range cases {
		_ = s.store.Update(func(st *store.State) error {
			st.Settings.Report, st.Settings.MonthlyReport = c.report, c.monthly
			return nil
		})
		if got := s.reportDue(c.tick); got != c.want {
			t.Errorf("report=%q monthly=%v at %s: %q, want %q", c.report, c.monthly, c.tick, got, c.want)
		}
	}
}
