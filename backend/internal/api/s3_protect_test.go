package api

import (
	"testing"

	"strataos/internal/store"
)

func TestS3BucketRules(t *testing.T) {
	ok := func(prev *store.S3Bucket, b store.S3Bucket) store.S3Bucket {
		t.Helper()
		if err := s3BucketRules(prev, &b); err != nil {
			t.Fatal(b, err)
		}
		return b
	}
	bad := func(prev *store.S3Bucket, b store.S3Bucket) {
		t.Helper()
		if s3BucketRules(prev, &b) == nil {
			t.Fatal("accepted", b)
		}
	}
	ok(nil, store.S3Bucket{Name: "photos"})
	ok(nil, store.S3Bucket{Name: "photos", Versioning: "enabled", Lock: true, LockMode: "COMPLIANCE", LockDays: 30})
	if b := ok(nil, store.S3Bucket{Name: "p", LockMode: "GOVERNANCE", LockDays: 3}); b.LockMode != "" || b.LockDays != 0 {
		t.Fatal("retention kept without lock", b)
	}
	bad(nil, store.S3Bucket{Name: "p", Versioning: "on"})
	bad(nil, store.S3Bucket{Name: "p", Lock: true})                                                // needs versioning
	bad(nil, store.S3Bucket{Name: "p", Versioning: "suspended", Lock: true})                       // enabled only
	bad(nil, store.S3Bucket{Name: "p", Versioning: "enabled", Lock: true, LockMode: "GOVERNANCE"}) // days
	prev := &store.S3Bucket{Name: "p", Versioning: "enabled"}
	if b := ok(prev, store.S3Bucket{Name: "p"}); b.Versioning != "suspended" {
		t.Fatal("unversioned again", b)
	}
	locked := &store.S3Bucket{Name: "p", Versioning: "enabled", Lock: true}
	bad(locked, store.S3Bucket{Name: "p", Versioning: "enabled"})
	bad(locked, store.S3Bucket{Name: "p", Versioning: "suspended", Lock: true})
	bad(locked, store.S3Bucket{Name: "q", Versioning: "enabled", Lock: true})
	ok(locked, store.S3Bucket{Name: "p", Versioning: "enabled", Lock: true, LockMode: "GOVERNANCE", LockDays: 7})
}

func TestS3VolumeOf(t *testing.T) {
	for in, want := range map[string]string{"/srv/strata/main/Photos": "/srv/strata/main", "/srv/strata/main": "/srv/strata/main", "/etc/x": "", "/srv/strata/../etc": ""} {
		if got := s3VolumeOf(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}
