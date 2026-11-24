package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"strataos/internal/cinema"
	"strataos/internal/store"
)

// cinemaFixture: a library with a video in a share alice reads and one in a
// share she does not.
func cinemaFixture(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "alice", Role: "user"}, &store.User{Username: "bob", Role: "user"})
		st.Shares = append(st.Shares,
			&store.Share{Name: "Family", Path: "/srv/strata/main/Family", Permissions: map[string]string{"alice": "ro", "bob": "rw"}},
			&store.Share{Name: "Private", Path: "/srv/strata/main/Private", Permissions: map[string]string{"bob": "rw"}})
		return nil
	})
	statePath, _, _ := s.cinemaPaths()
	if err := cinema.SaveState(statePath, cinema.State{Items: []cinema.Item{
		{Path: "/srv/strata/main/Family/Up.mkv", Title: "Up", TMDB: 1},
		{Path: "/srv/strata/main/Private/Secret.mkv", Title: "Secret", TMDB: 2},
	}}); err != nil {
		t.Fatal(err)
	}
	s.cinemaRoutes()
	return s
}

func as(r *http.Request, user, role string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, &session{User: user, Role: role}))
}

func TestCinemaFilteredByReadableShares(t *testing.T) {
	s := cinemaFixture(t)
	items := s.cinemaItems(as(httptest.NewRequest("GET", "/api/cinema", nil), "alice", "user"))
	if len(items) != 1 || items[0].Title != "Up" {
		t.Fatalf("alice sees %+v", items)
	}
	if n := len(s.cinemaItems(as(httptest.NewRequest("GET", "/api/cinema", nil), "bob", "user"))); n != 2 {
		t.Fatalf("bob sees %d", n)
	}
	// A list shared by bob with alice: alice sees only Up, and saving the
	// list from her side keeps bob's Secret.
	_ = s.store.Update(func(st *store.State) error {
		st.Media.WatchLists = append(st.Media.WatchLists, store.WatchList{ID: "l1", Name: "Weekend", Owner: "bob", With: []string{"alice"},
			Items: []string{"/srv/strata/main/Family/Up.mkv", "/srv/strata/main/Private/Secret.mkv"}})
		return nil
	})
	alice := albumClient(t, s, "alice", "user")
	rec := alice.req("GET", "/api/cinema/lists", nil)
	if strings.Contains(rec.Body.String(), "Secret") || !strings.Contains(rec.Body.String(), "Up") {
		t.Fatalf("alice's lists: %s", rec.Body.String())
	}
	rec = alice.req("PUT", "/api/cinema/lists/l1", map[string]any{"items": []string{"/srv/strata/main/Family/Up.mkv", "/srv/strata/main/Private/Other.mkv"}})
	var l []string
	s.store.Read(func(st *store.State) { l = st.Media.WatchLists[0].Items })
	b, _ := json.Marshal(l)
	if !strings.Contains(string(b), "Secret") || strings.Contains(string(b), "Other") || !strings.Contains(string(b), "Up") {
		t.Fatalf("list after alice's save (%d %s): %s", rec.Code, rec.Body.String(), b)
	}
}

func TestCinemaStaleScanReset(t *testing.T) {
	s := newTestServer(t)
	statePath, _, _ := s.cinemaPaths()
	_ = cinema.SaveState(statePath, cinema.State{Running: true, Scanned: 3})
	s.cinemaRoutes()
	if st := cinema.LoadState(statePath); st.Running || st.Scanned != 3 {
		t.Fatalf("state after start: %+v", st)
	}
}
