package vmcluster

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTLS(t *testing.T) {
	Root = t.TempDir()
	defer func() { Root = "/" }()
	ca, err := OwnCA("strata-k1")
	if err != nil || ValidCA(ca) != nil {
		t.Fatalf("CA: %v", err)
	}
	if again, _ := OwnCA("strata-k1"); again != ca {
		t.Error("the authority is created once")
	}
	changed, err := EnsureCert("strata-k1", []string{"10.0.0.1"}, -1)
	if err != nil || !changed {
		t.Fatalf("cert: %v %v", changed, err)
	}
	if changed, _ := EnsureCert("strata-k1", []string{"10.0.0.1"}, -1); changed {
		t.Error("certificate issued again for the same address")
	}
	// Signed by the member's authority, for its address, server and client.
	b, _ := os.ReadFile(filepath.Join(Root, lvCert))
	blk, _ := pem.Decode(b)
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(ca))
	for _, u := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "10.0.0.1", KeyUsages: []x509.ExtKeyUsage{u}}); err != nil {
			t.Errorf("verify %v: %v", u, err)
		}
	}
	if leaf.IsCA {
		t.Error("the member certificate must not be a CA (libvirt refuses it)")
	}
	if fi, _ := os.Stat(filepath.Join(Root, lvKey)); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", fi.Mode().Perm())
	}
	if changed, _ := EnsureCert("strata-k1", []string{"10.0.0.9"}, -1); !changed {
		t.Error("new address: new certificate")
	}
	other := t.TempDir()
	Root = other
	ca2, _ := OwnCA("strata-k1")
	Root = t.TempDir()
	if ch, err := SetTrust([]string{ca, ca2, "garbage", ca}); err != nil || !ch {
		t.Fatalf("trust: %v %v", ch, err)
	}
	data, _ := os.ReadFile(filepath.Join(Root, trustFile))
	if strings.Count(string(data), "BEGIN CERTIFICATE") != 2 {
		t.Errorf("trust file:\n%s", data)
	}
	if ch, _ := SetTrust([]string{ca2, ca}); ch {
		t.Error("same authorities: unchanged")
	}
	if u := SocketUnit([]string{"10.0.0.1", "fd00::1", "bad"}); !strings.Contains(u, "ListenStream=10.0.0.1:16514") || !strings.Contains(u, "ListenStream=[fd00::1]:16514") || strings.Contains(u, "bad") {
		t.Errorf("unit:\n%s", u)
	}
}
