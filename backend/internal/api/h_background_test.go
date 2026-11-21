package api

import (
	"testing"
	"time"

	"strataos/internal/store"
)

func TestBackgroundWait(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 11, h, m, 0, 0, time.Local) }
	night := store.BackgroundLimits{Hours: "23:00-06:30"}
	for _, c := range []struct {
		l    store.BackgroundLimits
		now  time.Time
		load float64
		own  int
		shut time.Time
		want string
	}{
		{store.BackgroundLimits{}, at(12, 0), 0.5, 0, time.Time{}, ""},
		{night, at(12, 0), 0, 0, time.Time{}, "hours"},
		{night, at(23, 30), 0, 0, time.Time{}, ""},
		{night, at(6, 29), 0, 0, time.Time{}, ""},
		{night, at(6, 30), 0, 0, time.Time{}, "hours"},
		{store.BackgroundLimits{Hours: "09:00-17:00"}, at(17, 0), 0, 0, time.Time{}, "hours"},
		{store.BackgroundLimits{}, at(12, 0), 3.5, 0, time.Time{}, "busy"},
		{store.BackgroundLimits{}, at(12, 0), 3.5, 2, time.Time{}, ""},            // our own threads
		{store.BackgroundLimits{AnyLoad: true}, at(12, 0), 9, 0, time.Time{}, ""}, // busy allowed
		{store.BackgroundLimits{}, at(12, 0), 0, 0, at(12, 10), "shutdown"},
		{store.BackgroundLimits{}, at(12, 0), 0, 0, at(13, 0), ""},
	} {
		if got := backgroundWait(c.l, c.now, c.load, 4, c.own, c.shut); got != c.want {
			t.Errorf("%+v at %s load %v: %q, want %q", c.l, c.now.Format("15:04"), c.load, got, c.want)
		}
	}
	if validLimits(store.BackgroundLimits{Hours: "25:00-01:00"}) == nil || validLimits(store.BackgroundLimits{Threads: -1}) == nil || validLimits(store.BackgroundLimits{Hours: "01:00-07:00", Threads: 1}) != nil {
		t.Error("validLimits")
	}
}
