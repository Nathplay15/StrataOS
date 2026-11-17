package api

import (
	"slices"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestUpdatedApps(t *testing.T) {
	now := time.Now()
	since := now.Add(-7 * 24 * time.Hour)
	ev := func(ago time.Duration, cat, msg string) store.Event {
		return store.Event{Time: now.Add(-ago), Category: cat, Message: msg}
	}
	events := []store.Event{
		ev(time.Hour, "docker", "Updating all containers"), // a request, not an app
		ev(time.Hour, "docker", "Updating nextcloud"),      // a request (may fail)
		ev(time.Hour, "docker", "Container jellyfin updated (jellyfin/jellyfin:latest)"),
		ev(2*time.Hour, "docker", "Stack nextcloud updated"),
		ev(3*time.Hour, "docker", "Automatic update of 2 container(s)"),
		ev(4*time.Hour, "docker", "Container jellyfin updated (jellyfin/jellyfin:latest)"),
		ev(30*24*time.Hour, "docker", "Stack old updated"),  // before the week
		ev(time.Hour, "apps", "Container fake updated (x)"), // another category
	}
	got := updatedApps(events, since)
	if !slices.Equal(got, []string{"jellyfin", "nextcloud", "jellyfin"}) {
		t.Errorf("updatedApps = %v", got)
	}
}

func TestStaleApps(t *testing.T) {
	now := time.Now()
	month := 30 * 24 * time.Hour
	created := map[string]time.Time{
		"fresh":   now.Add(-month),
		"old":     now.Add(-8 * month),
		"updated": now.Add(-8 * month),
	}
	events := []store.Event{{Time: now.Add(-month), Category: "docker", Message: "Stack updated updated"}}
	// "never" has no container: left out (nothing runs), whatever its folder date.
	got := staleApps([]string{"fresh", "old", "updated", "never"}, created, events, now)
	if !slices.Equal(got, []string{"old"}) {
		t.Errorf("staleApps = %v", got)
	}
}
