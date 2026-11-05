package api

import (
	"testing"

	"strataos/internal/s3gw"
	"strataos/internal/store"
)

func TestS3GrantsFollowSharePermissions(t *testing.T) {
	st := &store.State{
		Shares: []*store.Share{
			{Name: "Photos", Path: "/srv/strata/main/Photos", Permissions: map[string]string{"@famille": "rw", "bob": "ro", "eve": "none"}},
			{Name: "Full", Path: "/srv/strata/main/Full", Permissions: map[string]string{"alice": "rw"}, QuotaFull: true},
			{Name: "Public", Path: "/srv/strata/main/Public", Guest: true, Permissions: map[string]string{}},
		},
		Users: []*store.User{
			{Username: "alice", S3Keys: []store.S3Key{{Access: "KA1"}, {Access: "KA2"}}},
			{Username: "bob", S3Keys: []store.S3Key{{Access: "KB"}}},
			{Username: "eve", S3Keys: []store.S3Key{{Access: "KE"}}},
			{Username: "carl", Disabled: true, S3Keys: []store.S3Key{{Access: "KC"}}},
			{Username: "dan"},
		},
		S3: store.S3Service{Buckets: []store.S3Bucket{{Name: "photos", Share: "Photos"}, {Name: "full", Share: "Full"}, {Name: "public", Share: "Public"}, {Name: "gone", Share: "Deleted"}}},
	}
	groups := func(u string) []string {
		if u == "alice" || u == "eve" || u == "carl" {
			return []string{"famille"}
		}
		return nil
	}
	g := s3Grants(st, groups)
	want := map[string][]s3gw.Grant{
		// eve's own "none" wins over her group; carl is disabled.
		"photos": {{Access: "KA1", Write: true}, {Access: "KA2", Write: true}, {Access: "KB", Write: false}},
		// Over its quota: read-only.
		"full": {{Access: "KA1"}, {Access: "KA2"}},
		// Guest share: every account may write, like over SMB.
		"public": {{Access: "KA1", Write: true}, {Access: "KA2", Write: true}, {Access: "KB", Write: true}, {Access: "KE", Write: true}},
		"gone":   {},
	}
	for b, w := range want {
		if len(g[b]) != len(w) {
			t.Errorf("%s: %v, want %v", b, g[b], w)
			continue
		}
		for i := range w {
			if g[b][i] != w[i] {
				t.Errorf("%s: %v, want %v", b, g[b], w)
			}
		}
	}
}
