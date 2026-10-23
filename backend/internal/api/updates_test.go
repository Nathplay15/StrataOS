package api

import (
	"testing"
	"time"

	"strataos/internal/store"
)

func TestAutoWindow(t *testing.T) {
	loc := time.FixedZone("test", 3600)
	at := func(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, loc) } // 2026-10-19 is a Monday
	weekend := store.AutoUpdates{Strata: "install", Days: []int{0, 6}, Hour: 3}
	every := store.AutoUpdates{Security: "install", Hour: 23}
	for _, c := range []struct {
		name string
		cfg  store.AutoUpdates
		now  time.Time
		in   bool
		ws   time.Time
		next time.Time
	}{
		{"monday, weekend only", weekend, at(19, 12, 0), false, time.Time{}, at(24, 3, 0)},
		{"saturday before the window", weekend, at(24, 2, 59), false, time.Time{}, at(24, 3, 0)},
		{"saturday in the window", weekend, at(24, 3, 0), true, at(24, 3, 0), at(24, 3, 0)},
		{"end of the window", weekend, at(24, 5, 59), true, at(24, 3, 0), at(24, 3, 0)},
		{"after the window", weekend, at(24, 6, 0), false, time.Time{}, at(25, 3, 0)},
		{"sunday, then next saturday", weekend, at(25, 7, 0), false, time.Time{}, at(31, 3, 0)},
		{"every day, past midnight", every, at(20, 1, 30), true, at(19, 23, 0), at(19, 23, 0)},
		{"every day, after", every, at(20, 2, 0), false, time.Time{}, at(20, 23, 0)},
		{"nothing enabled", store.AutoUpdates{Hour: 3}, at(24, 3, 30), true, at(24, 3, 0), time.Time{}},
	} {
		ws, in := autoWindow(c.cfg, c.now)
		if in != c.in || !ws.Equal(c.ws) {
			t.Errorf("%s: window %v %v, want %v %v", c.name, ws, in, c.ws, c.in)
		}
		if n := nextAutoWindow(c.cfg, c.now); !n.Equal(c.next) {
			t.Errorf("%s: next %v, want %v", c.name, n, c.next)
		}
	}
	// A window that already ran is not offered again.
	done := weekend
	done.Last = &store.AutoUpdateRun{Window: at(24, 3, 0), Status: "success"}
	if n := nextAutoWindow(done, at(24, 4, 0)); !n.Equal(at(25, 3, 0)) {
		t.Errorf("after a run: next %v", n)
	}
}

func TestAutoDecide(t *testing.T) {
	loc := time.FixedZone("test", 0)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 24, h, m, 0, 0, loc) }
	cfg := store.AutoUpdates{System: "install", Hour: 3}
	idle := func() string { return "" }
	backup := func() string { return "Backup: photos" }
	asked := false
	noAsk := func() string { asked = true; return "" }

	if a, _, _ := autoDecide(cfg, at(2, 0), noAsk, time.Time{}); a != "" || asked {
		t.Errorf("outside the window: %q (busy asked: %v)", a, asked)
	}
	if a, ws, _ := autoDecide(cfg, at(3, 0), idle, time.Time{}); a != "run" || !ws.Equal(at(3, 0)) {
		t.Errorf("window start: %q %v", a, ws)
	}
	if a, _, why := autoDecide(cfg, at(3, 0), backup, time.Time{}); a != "postpone" || why != "Backup: photos" {
		t.Errorf("busy: %q %q", a, why)
	}
	// Postponed: nothing until the retry time, then tried again.
	if a, _, _ := autoDecide(cfg, at(3, 5), idle, at(3, 10)); a != "" {
		t.Errorf("before the retry: %q", a)
	}
	if a, _, _ := autoDecide(cfg, at(3, 10), idle, at(3, 10)); a != "run" {
		t.Errorf("at the retry: %q", a)
	}
	// Still busy when no retry fits in the window: skipped.
	if a, _, _ := autoDecide(cfg, at(5, 49), backup, time.Time{}); a != "postpone" {
		t.Errorf("11 minutes left: %q", a)
	}
	if a, _, _ := autoDecide(cfg, at(5, 50), backup, time.Time{}); a != "skip" {
		t.Errorf("10 minutes left: %q", a)
	}
	// Already done in this window (ran, or skipped): nothing more.
	done := cfg
	done.Last = &store.AutoUpdateRun{Window: at(3, 0), Status: "skipped"}
	if a, _, _ := autoDecide(done, at(4, 0), idle, time.Time{}); a != "" {
		t.Errorf("window done: %q", a)
	}
	done.Last.Window = at(3, 0).AddDate(0, 0, -1)
	if a, _, _ := autoDecide(done, at(4, 0), idle, time.Time{}); a != "run" {
		t.Errorf("yesterday's run: %q", a)
	}
	if a, _, _ := autoDecide(store.AutoUpdates{Hour: 3}, at(3, 0), idle, time.Time{}); a != "" {
		t.Errorf("all off: %q", a)
	}
}

func TestValidAutoUpdates(t *testing.T) {
	for _, c := range []struct {
		cfg store.AutoUpdates
		ok  bool
	}{
		{store.AutoUpdates{}, true},
		{store.AutoUpdates{Strata: "notify", Security: "install", System: "install", Days: []int{0, 6}, Hour: 23, Reboot: true}, true},
		{store.AutoUpdates{Security: "notify"}, false},
		{store.AutoUpdates{Strata: "always"}, false},
		{store.AutoUpdates{System: "install", Hour: 24}, false},
		{store.AutoUpdates{System: "install", Days: []int{7}}, false},
	} {
		if err := validAutoUpdates(c.cfg); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.cfg, err)
		}
	}
}

func TestSecuritySourceAndScrub(t *testing.T) {
	if !isSecuritySource("trixie-security") || !isSecuritySource("trixie-updates,trixie-security") || isSecuritySource("trixie-updates") || isSecuritySource("stable") {
		t.Error("security sources")
	}
	if !btrfsScrubRunning("UUID: x\nScrub started: Sat\nStatus:           running\n") || btrfsScrubRunning("Status:           finished\n") {
		t.Error("btrfs scrub status")
	}
}
