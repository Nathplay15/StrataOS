package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"strataos/internal/store"
)

func eventsServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		for i := 0; i < 30; i++ {
			st.Events = append(st.Events, store.Event{Time: time.Now().Add(-time.Duration(30-i) * time.Minute), Level: "info", Category: "system", User: "admin", Message: fmt.Sprintf("event %02d", i)})
		}
		return nil
	})
	s.systemRoutes()
	return s
}

func TestSystemEventsLimit(t *testing.T) {
	s := eventsServer(t)
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}

	if w := admin.req("GET", "/api/system/events", nil); w.Code != http.StatusOK {
		t.Fatalf("default: %d %s", w.Code, w.Body.String())
	} else {
		var ev []store.Event
		if err := json.Unmarshal(w.Body.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		if len(ev) != 30 {
			t.Fatalf("default limit returned %d events, want 30", len(ev))
		}
	}
	if w := admin.req("GET", "/api/system/events?limit=5", nil); w.Code != http.StatusOK {
		t.Fatalf("limit: %d %s", w.Code, w.Body.String())
	} else {
		var ev []store.Event
		if err := json.Unmarshal(w.Body.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		if len(ev) != 5 {
			t.Fatalf("limit=5 returned %d events, want 5", len(ev))
		}
		if ev[0].Message != "event 29" {
			t.Fatalf("events are not newest first: %q", ev[0].Message)
		}
	}
	// Out-of-range values are clamped instead of rejected.
	if w := admin.req("GET", "/api/system/events?limit=99999", nil); w.Code != http.StatusOK {
		t.Fatalf("huge limit: %d", w.Code)
	}
	if w := admin.req("GET", "/api/system/events?limit=-3", nil); w.Code != http.StatusOK {
		t.Fatalf("negative limit: %d", w.Code)
	}
}
