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
		st.Users = append(st.Users, &store.User{Username: "alice", Role: "user"}, &store.User{Username: "bob", Role: "user"}, &store.User{Username: "root", Role: "admin"})
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

func TestCinemaAgeLimitEnforced(t *testing.T) {
	s := cinemaFixture(t)
	statePath, _, _ := s.cinemaPaths()
	_ = cinema.SaveState(statePath, cinema.State{Items: []cinema.Item{
		{Path: "/srv/strata/main/Family/Up.mkv", Title: "Up", Age: 0, Rated: true},
		{Path: "/srv/strata/main/Family/Heat.mkv", Title: "Heat", Age: 16, Rated: true},
		{Path: "/srv/strata/main/Family/Unknown.mkv", Title: "Unknown"},
	}})
	admin := albumClient(t, s, "root", "admin")
	if rec := admin.req("POST", "/api/cinema/profiles", map[string]any{"name": "Kids", "maxAge": 12, "accounts": []string{"alice"}}); rec.Code != http.StatusOK {
		t.Fatalf("create profile: %d %s", rec.Code, rec.Body.String())
	}
	alice := albumClient(t, s, "alice", "user")
	body := alice.req("GET", "/api/cinema", nil).Body.String()
	if !strings.Contains(body, `"Up"`) || strings.Contains(body, "Heat") || strings.Contains(body, "Unknown") || !strings.Contains(body, `"maxAge":12`) {
		t.Fatalf("alice (limit 12) sees: %s", body)
	}
	// She cannot lift her own limit, nor see who is limited.
	var id string
	s.store.Read(func(st *store.State) { id = st.Media.Profiles[0].ID })
	if rec := alice.req("PUT", "/api/cinema/profiles/"+id, map[string]any{"name": "Kids", "maxAge": 18}); rec.Code != http.StatusForbidden {
		t.Fatalf("alice changed her limit: %d", rec.Code)
	}
	if rec := alice.req("DELETE", "/api/cinema/profiles/"+id, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("alice deleted her profile: %d", rec.Code)
	}
	if body := alice.req("GET", "/api/cinema/profiles", nil).Body.String(); strings.Contains(body, "alice") {
		t.Fatalf("profile accounts shown to a user: %s", body)
	}
	// Bob has no limit.
	if body := albumClient(t, s, "bob", "user").req("GET", "/api/cinema", nil).Body.String(); !strings.Contains(body, "Heat") || !strings.Contains(body, "Unknown") {
		t.Fatalf("bob: %s", body)
	}
}
