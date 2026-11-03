package store

import (
	"testing"
	"time"
)

func TestMessageLang(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st := &State{}
	if l := st.MessageLang(); l != "en" {
		t.Fatalf("no user: %q", l)
	}
	st.Users = []*User{
		{Username: "bob", Role: "user", Lang: "de", CreatedAt: t0.Add(-time.Hour)},
		{Username: "late", Role: "admin", Lang: "it", CreatedAt: t0.Add(time.Hour)},
		{Username: "gone", Role: "admin", Lang: "nl", CreatedAt: t0.Add(-2 * time.Hour), Disabled: true},
		{Username: "admin", Role: "admin", Lang: "fr", CreatedAt: t0},
		{Username: "odd", Role: "admin", Lang: "xx", CreatedAt: t0.Add(-3 * time.Hour)},
	}
	if l := st.MessageLang(); l != "fr" {
		t.Fatalf("first admin: %q", l)
	}
	st.Settings.NotifyLang = "es"
	if l := st.MessageLang(); l != "es" {
		t.Fatalf("chosen: %q", l)
	}
	st.Settings.NotifyLang = "bogus"
	if l := st.MessageLang(); l != "fr" {
		t.Fatalf("invalid setting: %q", l)
	}
}
