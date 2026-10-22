package api

import (
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestShareSaveConflict(t *testing.T) {
	st := &store.State{Shares: []*store.Share{{Name: "Media"}, {Name: "Photos"}}}
	for _, c := range []struct {
		from, name string
		create     bool
		err        string
	}{
		{"", "media", true, "already exists"},
		{"", "Films", true, ""},
		{"Media", "Media", false, ""},
		{"Media", "Films", false, ""},
		{"Media", "MEDIA", false, ""}, // case change of the same share
		{"Media", "photos", false, "already exists"},
		{"Nope", "Films", false, "not found"},
	} {
		err := shareSaveConflict(st, c.from, c.name, c.create)
		if (err == nil) != (c.err == "") || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func TestMediaFollowSharesEvents(t *testing.T) {
	s := newTestServer(t)
	// DLNA disabled and no OwnTone: only the events are produced.
	s.mediaFollowShares(mediaChange{dlna: true, music: true, deleted: "Films"}, "admin")
	s.mediaFollowShares(mediaChange{dlna: true}, "admin") // rename: no event
	var msgs []string
	s.store.Read(func(st *store.State) {
		for _, e := range st.Events {
			msgs = append(msgs, e.Level+" "+e.Category+" "+e.Message)
		}
	})
	want := []string{
		"warning media Shared folder Films was deleted: the DLNA server no longer publishes it",
		"warning media Shared folder Films was deleted: it is no longer in the music library",
	}
	if strings.Join(msgs, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s", strings.Join(msgs, "\n"))
	}
}
