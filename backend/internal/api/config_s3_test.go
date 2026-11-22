package api

import (
	"testing"

	"strataos/internal/store"
)

func TestMergeS3Buckets(t *testing.T) {
	cur := []store.S3Bucket{
		{Name: "vault", Share: "Vault", Versioning: "enabled", Lock: true, LockMode: "COMPLIANCE", LockDays: 365},
		{Name: "local", Share: "Local"},
		{Name: "photos", Share: "Old"},
		{Name: "locked", Share: "Keep", Versioning: "enabled", Lock: true, LockMode: "GOVERNANCE", LockDays: 1},
	}
	file := []store.S3Bucket{
		{Name: "renamed", Share: "Vault"}, // lock off in the file: stays on here
		{Name: "photos", Share: "Photos", Versioning: "enabled"},
		{Name: "locked", Share: "Other"}, // name of a locked bucket here
	}
	var logs int
	out := mergeS3Buckets(cur, file, func(string, ...any) { logs++ })
	got := map[string]store.S3Bucket{}
	for _, b := range out {
		if _, dup := got[b.Name]; dup {
			t.Fatalf("bucket name twice: %s", b.Name)
		}
		got[b.Name] = b
	}
	if v := got["vault"]; v.Share != "Vault" || !v.Lock || v.Versioning != "enabled" || v.LockMode != "COMPLIANCE" || v.LockDays != 365 {
		t.Errorf("locked bucket: %+v", v)
	}
	if _, ok := got["renamed"]; ok {
		t.Errorf("a locked bucket was renamed")
	}
	if v := got["photos"]; v.Share != "Photos" || v.Versioning != "enabled" {
		t.Errorf("file bucket: %+v", v)
	}
	if v, ok := got["local"]; !ok || v.Share != "Local" {
		t.Errorf("bucket of this NAS removed: %+v", out)
	}
	if v := got["locked"]; v.Share != "Keep" || !v.Lock {
		t.Errorf("locked bucket of this NAS: %+v", v)
	}
	if len(out) != 4 || logs != 3 {
		t.Errorf("merge: %d buckets, %d messages: %+v", len(out), logs, out)
	}
}
