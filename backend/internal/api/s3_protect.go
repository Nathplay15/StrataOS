package api

// S3 bucket versioning and object lock (WORM). versitygw's POSIX backend
// keeps older versions in --versioning-dir (one link per bucket to
// <volume>/.strata-s3-versions/<bucket>, root only, out of the shared
// folders) and the lock settings, retentions and legal holds in its
// sidecar metadata. Only what goes through S3 is versioned and locked: the
// current files stay ordinary files of the shared folder.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/s3gw"
	"strataos/internal/storage"
	"strataos/internal/store"
)

// s3BucketRules checks the versioning and lock settings of a bucket against
// its previous ones (prev nil: new bucket) and normalises them.
func s3BucketRules(prev *store.S3Bucket, b *store.S3Bucket) error {
	switch b.Versioning {
	case "", "enabled", "suspended":
	default:
		return badRequest("versioning is enabled or suspended")
	}
	if prev != nil && prev.Versioning != "" && b.Versioning == "" {
		b.Versioning = "suspended" // S3 cannot go back to unversioned
	}
	if prev != nil && prev.Lock && !b.Lock {
		return badRequest("object lock cannot be turned off once on (bucket " + b.Name + ")")
	}
	if b.Lock && b.Versioning != "enabled" {
		return badRequest("object lock needs versioning enabled (bucket " + b.Name + ")")
	}
	if !b.Lock {
		b.LockMode, b.LockDays = "", 0
	}
	if err := s3gw.ValidLock(s3gw.Lock{Enabled: b.Lock, DefaultMode: b.LockMode, DefaultDays: b.LockDays}); err != nil {
		return badRequest(b.Name + ": " + err.Error())
	}
	if prev != nil && prev.Lock && b.Name != prev.Name {
		return badRequest("a bucket with object lock keeps its name")
	}
	return nil
}

// s3VolumeOf is the volume holding a shared folder.
func s3VolumeOf(path string) string {
	rest, ok := strings.CutPrefix(filepath.Clean(path), storage.MountRoot+"/")
	if !ok {
		return ""
	}
	first, _, _ := strings.Cut(rest, "/")
	return filepath.Join(storage.MountRoot, first)
}

// s3VersionLinks keeps one link per versioned bucket to its versions folder
// on the bucket's volume. Links of other buckets are removed, never data.
func s3VersionLinks(st *store.State) error {
	want := map[string]string{}
	for _, b := range st.S3.Buckets {
		sh := st.Share(b.Share)
		if b.Versioning == "" || sh == nil || sh.Encrypted {
			continue
		}
		if vol := s3VolumeOf(sh.Path); vol != "" {
			want[b.Name] = filepath.Join(vol, s3gw.VersionsFolder, b.Name)
		}
	}
	if err := os.MkdirAll(s3gw.VersionsDir, 0o700); err != nil {
		return err
	}
	entries, _ := os.ReadDir(s3gw.VersionsDir)
	for _, e := range entries {
		p := filepath.Join(s3gw.VersionsDir, e.Name())
		if t, err := os.Readlink(p); err != nil || want[e.Name()] != t {
			if err == nil || e.Type()&os.ModeSymlink != 0 {
				_ = os.Remove(p)
			}
		}
	}
	for name, target := range want {
		if err := os.MkdirAll(target, 0o700); err != nil {
			return err
		}
		_ = os.Chmod(filepath.Dir(target), 0o700)
		p := filepath.Join(s3gw.VersionsDir, name)
		if t, err := os.Readlink(p); err == nil && t == target {
			continue
		}
		if err := os.Symlink(target, p); err != nil {
			return err
		}
	}
	return nil
}

// s3Client calls the local gateway as its root account.
func s3Client(c store.S3Service, https bool) *s3gw.Client {
	scheme := "http"
	tr := &http.Transport{}
	if https {
		scheme = "https"
		// The loopback address is not in the NAS certificate.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &s3gw.Client{Endpoint: fmt.Sprintf("%s://127.0.0.1:%d", scheme, s3Port(c)), Access: c.RootAccess, Secret: c.RootSecret,
		HTTP: &http.Client{Timeout: 20 * time.Second, Transport: tr}}
}

// s3SyncProtection sets versioning and object lock of every bucket through
// the gateway (it may have just restarted: retried for 20 s).
func s3SyncProtection(c store.S3Service, https bool) error {
	cl := s3Client(c, https)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var errs []string
	for _, b := range c.Buckets {
		if b.Versioning == "" && !b.Lock {
			continue
		}
		var cur string
		var err error
		for i := 0; i < 40; i++ {
			if cur, err = cl.Versioning(ctx, b.Name); err == nil {
				break
			}
			if _, ok := err.(*s3gw.S3Error); ok {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", b.Name, err))
			continue
		}
		want := map[string]string{"enabled": "Enabled", "suspended": "Suspended"}[b.Versioning]
		if want != "" && cur != want {
			if err := cl.SetVersioning(ctx, b.Name, want); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", b.Name, err))
				continue
			}
		}
		if b.Lock {
			l := s3gw.Lock{Enabled: true, DefaultMode: b.LockMode, DefaultDays: b.LockDays}
			if cur, err := cl.ObjectLock(ctx, b.Name); err != nil || cur != l {
				if err := cl.SetObjectLock(ctx, b.Name, l); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", b.Name, err))
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("versioning or object lock not applied: %s", strings.Join(errs, "; "))
	}
	return nil
}
