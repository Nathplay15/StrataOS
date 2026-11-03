package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestLinkOptions(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	str := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	l := &store.ShareLink{Upload: true}
	o := linkOptions{Title: str("  Holiday photos "), Message: str("Line 1\nLine 2"), AskGuest: str("required"), MaxFileMB: i64(5), Expires: str("2026-12-01T00:00:00Z")}
	if err := o.apply(l, now); err != nil {
		t.Fatal(err)
	}
	if l.Title != "Holiday photos" || l.AskGuest != "required" || l.MaxFileBytes != 5<<20 || l.Expires.Month() != 12 {
		t.Fatalf("%+v", l)
	}
	for _, bad := range []linkOptions{
		{Title: str("a\x00b")},
		{Title: str("line\nbreak")},
		{AskGuest: str("maybe")},
		{MaxFileMB: i64(-1)},
		{Expires: str("2020-01-01T00:00:00Z")},
		{Expires: str("tomorrow")},
		{Password: str("abc")},
	} {
		c := *l
		if err := bad.apply(&c, now); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := (linkOptions{Expires: str("")}).apply(l, now); err != nil || !l.Expires.IsZero() {
		t.Fatal("never expires")
	}
	// Upload options are dropped from download-only links.
	d := &store.ShareLink{Download: true}
	if err := o.apply(d, now); err != nil || d.AskGuest != "" || d.MaxFileBytes != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestFirstRange(t *testing.T) {
	for h, want := range map[string]bool{"": true, "bytes=0-": true, "bytes=0-1023": true, "bytes=1024-": false} {
		r := httptest.NewRequest("GET", "/", nil)
		if h != "" {
			r.Header.Set("Range", h)
		}
		if firstRange(r) != want {
			t.Error(h)
		}
	}
}

func TestCountView(t *testing.T) {
	now := time.Now()
	if !countView("l1", "198.51.100.1", now) || countView("l1", "198.51.100.1", now.Add(time.Minute)) {
		t.Fatal("once per 30 minutes")
	}
	if !countView("l1", "198.51.100.2", now) || !countView("l1", "198.51.100.1", now.Add(31*time.Minute)) {
		t.Fatal("other address, later visit")
	}
}
