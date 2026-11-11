package backup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
	"strataos/internal/tlstrust"
)

func TestShellQuote(t *testing.T) {
	for _, s := range []string{"tank/backup", "a b", "it's", "$(touch /tmp/pwned)", "`id`", "x;rm -rf /", `"q"`} {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("shellQuote(%q) round-trip gave %q (%v)", s, out, err)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := []store.BackupTask{
		{Name: "a", Kind: "rsync", Sources: []string{"/srv/strata/main/Photos"}, DestType: "local", DestPath: "/srv/strata/usb/backup"},
		{Name: "b", Kind: "rsync", Sources: []string{"/srv/strata/main/Photos"}, DestType: "ssh", Host: "nas2.local", User: "backup", DestPath: "/volume1/backup"},
		{Name: "c", Kind: "restic", Sources: []string{"/srv/strata/main"}, DestType: "s3", S3Endpoint: "https://s3.example.com", DestPath: "bucket/nas", AccessKey: "k", SecretKey: "s", Password: "longenough"},
		{Name: "d", Kind: "zfs", Sources: []string{"tank/media"}, DestType: "local", DestPath: "backup/media"},
	}
	for _, b := range ok {
		if err := Validate(&b); err != nil {
			t.Errorf("%s rejected: %v", b.Name, err)
		}
	}
	bad := map[string]store.BackupTask{
		"source outside volumes":     {Name: "x", Kind: "rsync", Sources: []string{"/etc"}, DestType: "local", DestPath: "/srv/strata/b"},
		"dest inside source":         {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/main"}, DestType: "local", DestPath: "/srv/strata/main/copy"},
		"option injection in host":   {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/a"}, DestType: "ssh", Host: "-oProxyCommand=id", User: "u", DestPath: "/b"},
		"option injection in user":   {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/a"}, DestType: "ssh", Host: "h", User: "-x", DestPath: "/b"},
		"relative remote path":       {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/a"}, DestType: "ssh", Host: "h", User: "u", DestPath: "b"},
		"short restic password":      {Name: "x", Kind: "restic", Sources: []string{"/srv/strata/a"}, DestType: "local", DestPath: "/srv/strata/r", Password: "123"},
		"zfs dest in source":         {Name: "x", Kind: "zfs", Sources: []string{"tank"}, DestType: "local", DestPath: "tank/copy"},
		"zfs dest with shell chars":  {Name: "x", Kind: "zfs", Sources: []string{"tank/a"}, DestType: "ssh", Host: "h", User: "u", DestPath: "pool/x;id"},
		"exclude starting with dash": {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/a"}, DestType: "local", DestPath: "/srv/strata/b", Excludes: []string{"--delete"}},
		"s3 endpoint not http":       {Name: "x", Kind: "restic", Sources: []string{"/srv/strata/a"}, DestType: "s3", S3Endpoint: "file:///etc", DestPath: "b", AccessKey: "k", SecretKey: "s", Password: "longenough"},
	}
	for name, b := range bad {
		if err := Validate(&b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r := store.BackupTask{Name: "r", Kind: "restic", Sources: []string{"/srv/strata/a"}, DestType: "local", DestPath: "/srv/strata/r", Password: "longenough"}
	_ = Validate(&r)
	if r.KeepDaily != 7 || r.KeepMonthly != 12 {
		t.Errorf("default restic retention not applied: %+v", r)
	}
}

func TestLineWriter(t *testing.T) {
	var lines []string
	w := &lineWriter{fn: func(l string) { lines = append(lines, l) }}
	_, _ = w.Write([]byte("a\r  10%  \rb\nc"))
	_, _ = w.Write([]byte("d\n"))
	if strings.Join(lines, "|") != "a|  10%  |b|cd" {
		t.Errorf("got %q", lines)
	}
}

func TestResticTLSArgs(t *testing.T) {
	tlstrust.Dir = t.TempDir()
	pemCert := testCertPEM(t)
	task := store.BackupTask{Name: "x", Kind: "restic", Sources: []string{"/srv/strata/main/a"}, DestType: "rest", DestPath: "https://nas2.lan:8000/repo", Password: "12345678",
		TLSTrust: store.TLSTrust{TLSCert: pemCert, TLSFingerprint: "forged"}}
	if err := Validate(&task); err != nil {
		t.Fatal(err)
	}
	if task.TLSFingerprint == "forged" || len(task.TLSFingerprint) != 64 {
		t.Fatalf("fingerprint not recomputed: %q", task.TLSFingerprint)
	}
	args := resticArgs(&task, "snapshots")
	if len(args) != 3 || args[0] != "--cacert" || !strings.HasPrefix(args[1], tlstrust.Dir) || args[2] != "snapshots" {
		t.Fatalf("args: %v", args)
	}
	task.TLSTrust = store.TLSTrust{TLSInsecure: true}
	if args := resticArgs(&task, "snapshots"); !slices.Equal(args, []string{"--insecure-tls", "snapshots"}) {
		t.Fatalf("insecure args: %v", args)
	}
	// A local repository never keeps certificate options.
	local := store.BackupTask{Name: "x", Kind: "restic", Sources: []string{"/srv/strata/main/a"}, DestType: "local", DestPath: "/srv/strata/main/repo", Password: "12345678", TLSTrust: store.TLSTrust{TLSInsecure: true}}
	if err := Validate(&local); err != nil || local.TLSTrust.Set() || len(resticArgs(&local, "x")) != 1 {
		t.Fatalf("local repository: %v %+v", err, local.TLSTrust)
	}
	// A datastore hands its trust to the tasks using it.
	ds := store.Datastore{ID: "d1", Name: "repo", DestType: "s3", S3Endpoint: "https://minio.lan:9000", DestPath: "bucket", AccessKey: "a", SecretKey: "b", Password: "12345678", TLSTrust: store.TLSTrust{TLSCert: pemCert}}
	if err := ValidateDatastore(&ds); err != nil || ds.TLSFingerprint == "" {
		t.Fatalf("datastore: %v", err)
	}
	if rt := RepoTask(&ds); rt.TLSFingerprint != ds.TLSFingerprint || resticArgs(&rt)[0] != "--cacert" {
		t.Fatal("datastore trust not applied")
	}
}

func testCertPEM(t *testing.T) string {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nas2.lan"}, DNSNames: []string{"nas2.lan"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// restic retries a refused certificate for minutes: the first x509 line
// stops it and the error says what to do.
func TestResticStopsOnCertError(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'Stat(<config/>) returned error, retrying after 1m: tls: failed to verify certificate: x509: certificate signed by unknown authority' >&2\nsleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "restic"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	task := store.BackupTask{ID: "x", DestType: "rest", DestPath: "https://127.0.0.1:1/", Password: "12345678"}
	start := time.Now()
	_, err := restic(context.Background(), &task, nil, "cat", "config")
	if err == nil || !strings.Contains(err.Error(), "x509") && !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("error: %v", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatalf("restic was not stopped at the first certificate error (%s)", time.Since(start))
	}
}
