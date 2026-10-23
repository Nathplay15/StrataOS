package api

import (
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestRecoveryCodeFlow(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)}
	rc := newRecoveryCodes(clk.now)
	if rc.check("ABCD-EFGH-JKMN") {
		t.Fatal("no code issued yet")
	}
	code, exp, err := rc.issue()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 14 || code[4] != '-' || code[9] != '-' || strings.ContainsAny(code, "01OIL") {
		t.Fatalf("bad code format %q", code)
	}
	if exp.Sub(clk.t) != 15*time.Minute {
		t.Fatalf("expires %v", exp)
	}
	// Typed in lower case with spaces: still valid, and checking does not use it.
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	if !rc.check(typed) || !rc.check(code) {
		t.Fatal("valid code refused")
	}
	if !rc.consume(code) {
		t.Fatal("first use refused")
	}
	if rc.consume(code) || rc.check(code) {
		t.Fatal("code accepted twice")
	}

	// Expiry.
	code, _, _ = rc.issue()
	clk.t = clk.t.Add(15*time.Minute - time.Second)
	if !rc.check(code) {
		t.Fatal("refused before expiry")
	}
	clk.t = clk.t.Add(time.Second)
	if rc.consume(code) {
		t.Fatal("accepted after expiry")
	}

	// A new code replaces the previous one.
	old, _, _ := rc.issue()
	code, _, _ = rc.issue()
	if old == code || rc.check(old) || !rc.check(code) {
		t.Fatal("the previous code must be cancelled")
	}

	// Wrong codes burn the current one.
	for i := 0; i < recoveryMaxFails; i++ {
		rc.check("AAAA-AAAA-AAAA")
	}
	if rc.check(code) {
		t.Fatal("code still valid after too many wrong attempts")
	}

	// clear (recovery codes switched off).
	code, _, _ = rc.issue()
	rc.clear()
	if rc.check(code) {
		t.Fatal("cleared code accepted")
	}
}

func TestRecoveryCodeRateLimit(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)}
	rc := newRecoveryCodes(clk.now)
	for i := 0; i < recoveryMaxIssued; i++ {
		if _, _, err := rc.issue(); err != nil {
			t.Fatalf("code %d: %v", i+1, err)
		}
		clk.t = clk.t.Add(time.Minute)
	}
	if _, _, err := rc.issue(); err != errRecoveryRate {
		t.Fatalf("got %v, want the rate limit", err)
	}
	clk.t = clk.t.Add(55*time.Minute + 30*time.Second) // only the first one is an hour old
	if _, _, err := rc.issue(); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
	if _, _, err := rc.issue(); err != errRecoveryRate {
		t.Fatalf("got %v, want the rate limit again", err)
	}
}

func TestConsoleWanted(t *testing.T) {
	for _, tc := range []struct {
		off, decided, graphical, want bool
	}{
		{false, false, false, true}, // default: on
		{false, false, true, false}, // a graphical login may use tty1
		{false, true, true, true},   // unless an administrator asked for it
		{true, true, false, false},
	} {
		c := consoleWanted(storeConsole(tc.off, tc.decided), tc.graphical)
		if c != tc.want {
			t.Errorf("%+v: got %v", tc, c)
		}
	}
}

func storeConsole(off, decided bool) store.Console { return store.Console{Off: off, Decided: decided} }
