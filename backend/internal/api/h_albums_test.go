package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"strataos/internal/store"
)

func albumClient(t *testing.T, s *Server, who, role string) *jarClient {
	t.Helper()
	return &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create(who, role, "192.0.2.9", time.Hour)}}
}

func TestAlbumShare(t *testing.T) {
	s := newTestServer(t)
	s.albumRoutes()
	s.photoRoutes()
	_ = s.store.Update(func(st *store.State) error {
		for _, u := range []string{"alice", "marie", "bob"} {
			st.Users = append(st.Users, &store.User{Username: u, Role: "user"})
		}
		return nil
	})
	alice, marie, bob := albumClient(t, s, "alice", "user"), albumClient(t, s, "marie", "user"), albumClient(t, s, "bob", "user")

	code, out := alice.do("POST", "/api/photos/albums", map[string]any{"name": "Vacs"})
	if code != http.StatusOK {
		t.Fatalf("create: %d %v", code, out)
	}
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatal("create: no id")
	}

	// A typo shares with nobody: refused, not silently.
	if code, _ := alice.do("PUT", "/api/photos/albums/"+id+"/share", map[string]any{"with": []string{"bob", "typo"}}); code != http.StatusBadRequest {
		t.Fatalf("unknown account: %d, want 400", code)
	}

	if code, out := alice.do("PUT", "/api/photos/albums/"+id+"/share", map[string]any{"with": []string{"marie"}}); code != http.StatusOK {
		t.Fatalf("share: %d %v", code, out)
	}
	// Duplication and the owner herself are not in the list.
	if code, out := alice.do("PUT", "/api/photos/albums/"+id+"/share", map[string]any{"with": []string{"marie", "marie", "alice"}}); code != http.StatusOK {
		t.Fatalf("share twice: %d %v", code, out)
	} else if with, _ := out["with"].([]any); len(with) != 1 {
		t.Fatalf("share twice: with=%v, want only marie", with)
	}

	// Marie sees the album, with its owner; bob does not.
	w := marie.req("GET", "/api/photos/albums", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("marie list: %d", w.Code)
	}
	var list []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["from"] != "alice" || list[0]["name"] != "Vacs" {
		t.Fatalf("marie list: %v", list)
	}
	if w = bob.req("GET", "/api/photos/albums", nil); w.Code != http.StatusOK {
		t.Fatal("bob list: not ok")
	}
	var bobList []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &bobList)
	if len(bobList) != 0 {
		t.Fatalf("bob list: %v, want none", bobList)
	}

	// Marie can read the shared album (empty photo list), bob cannot.
	if code, _ := marie.do("GET", "/api/photos?album="+id, nil); code != http.StatusOK {
		t.Fatalf("marie photos of the album: %d", code)
	}
	if code, _ := bob.do("GET", "/api/photos?album="+id, nil); code == http.StatusOK {
		t.Fatal("bob photos of the album: allowed?")
	}

	// A recipient shares nothing and changes nothing.
	if code, _ := marie.do("POST", "/api/photos/albums/"+id+"/share", map[string]any{"with": []string{"bob"}}); code == http.StatusOK {
		t.Fatal("marie could share the album of alice")
	}

	// Unshare: empty list.
	if code, _ := alice.do("PUT", "/api/photos/albums/"+id+"/share", map[string]any{"with": []string{}}); code != http.StatusOK {
		t.Fatal("unshare failed")
	}
	w = marie.req("GET", "/api/photos/albums", nil)
	list = nil
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Fatalf("marie list after unshare: %v", list)
	}
}

// A shared album photo is reached as its owner reaches it, now.
func TestAlbumPhotoFollowsOwnerAccess(t *testing.T) {
	s := newTestServer(t)
	photo := "/srv/strata/main/Photos/beach.jpg"
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "olga", Role: "admin"}, &store.User{Username: "alice", Role: "user"}, &store.User{Username: "eve", Role: "user"})
		st.Photos.Albums = append(st.Photos.Albums, &store.Album{ID: "a1", Name: "Summer", Owner: "olga", With: []string{"alice"}, Items: []string{photo}})
		return nil
	})
	req := func(u string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		return r.WithContext(context.WithValue(r.Context(), ctxKey{}, &session{User: u, Role: "user"}))
	}
	tg, ok := s.sharedTarget(req("alice"), photo)
	if !ok || tg.acc.write || tg.abs != photo {
		t.Fatalf("alice: %+v %v", tg, ok)
	}
	if _, ok := s.sharedTarget(req("eve"), photo); ok {
		t.Fatal("eve is not a recipient")
	}
	if _, ok := s.sharedTarget(req("alice"), "/srv/strata/main/Photos/other.jpg"); ok {
		t.Fatal("a photo outside the album")
	}
	// The owner loses her access to the share: the album photo closes too.
	_ = s.store.Update(func(st *store.State) error { st.User("olga").Role = "user"; return nil })
	if _, ok := s.sharedTarget(req("alice"), photo); ok {
		t.Fatal("served although the owner can no longer read it")
	}
}
