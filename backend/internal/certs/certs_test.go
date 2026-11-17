package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testPair(t *testing.T, host string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

func TestACMEDNSLifecycle(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := testPair(t, "nas.example.com")

	if err := m.SetACMEDNS("NAS.Example.com", certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	domain, notAfter, ok := m.DNSCert()
	if !ok || domain != "nas.example.com" || time.Until(notAfter) < 300*24*time.Hour {
		t.Fatalf("DNSCert: %q %v %v", domain, notAfter, ok)
	}
	c, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "NAS.Example.COM"})
	if err != nil || c == nil || c.Leaf == nil || len(c.Leaf.DNSNames) != 1 || c.Leaf.DNSNames[0] != "nas.example.com" {
		t.Fatalf("GetCertificate: %v %+v", err, c)
	}
	// Another name falls back to the self-signed certificate.
	if c, _ := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.example.com"}); c == nil || len(c.Leaf.DNSNames) != 3 {
		t.Fatalf("fallback: %+v", c)
	}
	list, _, _ := m.Status()
	if len(list) != 2 || list[1].Source != "letsencrypt-dns" || !list[1].Active {
		t.Fatalf("status: %+v", list)
	}

	// A new manager reloads the certificate from disk (restart).
	m2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m2.LoadDNS("nas.example.com"); err != nil {
		t.Fatal(err)
	}
	if d, _, ok := m2.DNSCert(); !ok || d != "nas.example.com" {
		t.Fatalf("LoadDNS: %q %v", d, ok)
	}

	m2.ClearACMEDNS()
	if _, _, ok := m2.DNSCert(); ok {
		t.Fatal("ClearACMEDNS left a certificate")
	}
	if _, err := m2.GetCertificate(&tls.ClientHelloInfo{ServerName: "nas.example.com"}); err != nil {
		t.Fatalf("fallback after clear: %v", err)
	}
}

func TestACMEDNSValidation(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := testPair(t, "nas.example.com")
	if err := m.SetACMEDNS("not_a_domain", certPEM, keyPEM); err == nil {
		t.Fatal("invalid domain accepted")
	}
	if err := m.SetACMEDNS("nas.example.com", certPEM, "not a key"); err == nil {
		t.Fatal("mismatched key accepted")
	}
	if _, _, ok := m.DNSCert(); ok {
		t.Fatal("a rejected request left a certificate")
	}
}

// Switching the DNS-01 domain drops the old certificate: it is not loaded
// back after a restart, and it does not count as the current one, so the
// renewal loop asks for the new domain (review 0.12.0).
func TestACMEDNSDomainSwitch(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Old domain "a." sorts before the new one: Glob[0] used to pick it.
	oldCert, oldKey := testPair(t, "a.example.com")
	if err := m.SetACMEDNS("a.example.com", oldCert, oldKey); err != nil {
		t.Fatal(err)
	}
	if !m.DNSCertFits("a.example.com", 30*24*time.Hour) {
		t.Fatal("current certificate must fit")
	}
	// The administrator switches to b.example.com: until it is obtained, the
	// loaded certificate does not fit.
	if m.DNSCertFits("b.example.com", 30*24*time.Hour) {
		t.Fatal("the old domain's certificate fits the new domain")
	}
	newCert, newKey := testPair(t, "b.example.com")
	if err := m.SetACMEDNS("b.example.com", newCert, newKey); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "acme-dns", "a.example.com.crt")); !os.IsNotExist(err) {
		t.Fatalf("old files kept: %v", err)
	}
	m2, _ := New(dir)
	if err := m2.LoadDNS("b.example.com"); err != nil {
		t.Fatal(err)
	}
	if d, _, ok := m2.DNSCert(); !ok || d != "b.example.com" {
		t.Fatalf("loaded %q", d)
	}
	// A configured domain without a certificate loads nothing (no error).
	m3, _ := New(dir)
	if err := m3.LoadDNS("c.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := m3.DNSCert(); ok {
		t.Fatal("another domain's certificate loaded")
	}
	// A certificate expiring soon does not fit either.
	if m2.DNSCertFits("b.example.com", 400*24*time.Hour) {
		t.Fatal("expiry ignored")
	}
}
