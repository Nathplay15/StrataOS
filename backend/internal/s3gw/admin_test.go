package s3gw

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The examples of the AWS Signature V4 documentation for S3.
func TestSignV4(t *testing.T) {
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct{ url, sig string }{
		{"https://examplebucket.s3.amazonaws.com/?lifecycle", "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	} {
		req, _ := http.NewRequest(http.MethodGet, c.url, nil)
		sign(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1", nil, at)
		if a := req.Header.Get("Authorization"); !strings.HasSuffix(a, "Signature="+c.sig) || !strings.Contains(a, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
			t.Errorf("%s: %s", c.url, a)
		}
	}
}

func TestValidLock(t *testing.T) {
	for _, l := range []Lock{{Enabled: true}, {Enabled: true, DefaultMode: Governance, DefaultDays: 30}, {Enabled: true, DefaultMode: Compliance, DefaultDays: 1}} {
		if err := ValidLock(l); err != nil {
			t.Error(l, err)
		}
	}
	for _, l := range []Lock{{DefaultDays: 3}, {DefaultMode: "governance", DefaultDays: 3}, {DefaultMode: Governance}, {DefaultMode: Compliance, DefaultDays: 40000}} {
		if ValidLock(l) == nil {
			t.Error("accepted", l)
		}
	}
}

// TestGatewayLock runs against a real versitygw when STRATA_VERSITYGW names
// its binary: versioning and object lock set through the client.
func TestGatewayLock(t *testing.T) {
	bin := os.Getenv("STRATA_VERSITYGW")
	if bin == "" {
		t.Skip("STRATA_VERSITYGW not set")
	}
	dir := t.TempDir()
	for _, d := range []string{"buckets", "meta", "iam", "versions", "share"} {
		os.MkdirAll(filepath.Join(dir, d), 0o700)
	}
	os.Symlink(filepath.Join(dir, "share"), filepath.Join(dir, "buckets", "photos"))
	cmd := exec.Command(bin, "--port", "127.0.0.1:17399", "--iam-dir", filepath.Join(dir, "iam"), "posix", "--bucketlinks",
		"--sidecar", filepath.Join(dir, "meta"), "--versioning-dir", filepath.Join(dir, "versions"), filepath.Join(dir, "buckets"))
	cmd.Env = append(os.Environ(), "ROOT_ACCESS_KEY=rootkey", "ROOT_SECRET_KEY=rootsecret-0123456789")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	c := &Client{Endpoint: "http://127.0.0.1:17399", Access: "rootkey", Secret: "rootsecret-0123456789"}
	ctx := context.Background()
	var st string
	var err error
	for range 50 {
		if st, err = c.Versioning(ctx, "photos"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || st != "" {
		t.Fatal("versioning before", st, err)
	}
	if l, err := c.ObjectLock(ctx, "photos"); err != nil || l.Enabled {
		t.Fatal("lock before", l, err)
	}
	if err := c.SetVersioning(ctx, "photos", "Enabled"); err != nil {
		t.Fatal(err)
	}
	if st, err := c.Versioning(ctx, "photos"); err != nil || st != "Enabled" {
		t.Fatal("versioning after", st, err)
	}
	want := Lock{Enabled: true, DefaultMode: Compliance, DefaultDays: 7}
	if err := c.SetObjectLock(ctx, "photos", want); err != nil {
		t.Fatal(err)
	}
	if l, err := c.ObjectLock(ctx, "photos"); err != nil || l != want {
		t.Fatal("lock after", l, err)
	}
	if err := c.SetObjectLock(ctx, "photos", Lock{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if l, err := c.ObjectLock(ctx, "photos"); err != nil || l != (Lock{Enabled: true}) {
		t.Fatal("default retention removed", l, err)
	}
	bad := &Client{Endpoint: c.Endpoint, Access: "rootkey", Secret: "wrong"}
	if _, err := bad.Versioning(ctx, "photos"); err == nil {
		t.Fatal("wrong secret accepted")
	}
}
