package tlstrust

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

// mkCert makes a certificate for names, signed by parent (self-signed when nil).
func mkCert(t *testing.T, cn string, names []string, ca bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{SerialNumber: n, Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: ca}
	for _, h := range names {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	signer, signKey := tpl, key
	if parent != nil {
		signer, signKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, signer, &key.PublicKey, signKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

func trustOf(t *testing.T, certs ...*x509.Certificate) store.TLSTrust {
	tr := store.TLSTrust{TLSCert: EncodePEM(certs)}
	if err := Normalize(&tr, true); err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestNormalize(t *testing.T) {
	c, _ := mkCert(t, "nas", []string{"nas.lan"}, false, nil, nil)
	tr := store.TLSTrust{TLSCert: "\n" + EncodePEM([]*x509.Certificate{c}), TLSFingerprint: "forged"}
	if err := Normalize(&tr, true); err != nil {
		t.Fatal(err)
	}
	if tr.TLSFingerprint != Fingerprint(c) || len(tr.TLSFingerprint) != 64 {
		t.Fatalf("fingerprint not recomputed: %q", tr.TLSFingerprint)
	}
	if err := Normalize(&store.TLSTrust{TLSCert: "garbage"}, true); err == nil {
		t.Fatal("garbage accepted")
	}
	if err := Normalize(&store.TLSTrust{TLSInsecure: true}, false); err == nil {
		t.Fatal("insecure accepted where it is not allowed")
	}
	empty := store.TLSTrust{TLSFingerprint: "x"}
	if err := Normalize(&empty, false); err != nil || empty.TLSFingerprint != "" {
		t.Fatalf("empty trust: %v %+v", err, empty)
	}
	if got := Colons("ab01ff"); got != "AB:01:FF" {
		t.Fatal(got)
	}
}

func TestVerifyPinAndCA(t *testing.T) {
	self, _ := mkCert(t, "nas", []string{"nas.lan"}, false, nil, nil)
	pin := trustOf(t, self)
	// A pinned certificate is accepted whatever the name used.
	if err := Verify(pin, "192.168.1.5", []*x509.Certificate{self}); err != nil {
		t.Fatalf("pinned certificate refused: %v", err)
	}
	other, _ := mkCert(t, "nas", []string{"nas.lan"}, false, nil, nil)
	var ce *ChangedError
	if err := Verify(pin, "nas.lan", []*x509.Certificate{other}); !errors.As(err, &ce) || ce.Received != Fingerprint(other) || ce.Trusted != Fingerprint(self) {
		t.Fatalf("changed certificate: %v", err)
	}
	if !strings.Contains(ce.Error(), "the certificate of nas.lan changed") {
		t.Fatal(ce.Error())
	}

	// A private CA: its leaves are accepted for their names, renewals included.
	ca, caKey := mkCert(t, "Home CA", nil, true, nil, nil)
	leaf1, _ := mkCert(t, "cloud", []string{"cloud.home"}, false, ca, caKey)
	leaf2, _ := mkCert(t, "cloud", []string{"cloud.home"}, false, ca, caKey)
	trust := trustOf(t, leaf1, ca)
	if err := Verify(trust, "cloud.home", []*x509.Certificate{leaf2}); err != nil {
		t.Fatalf("renewed leaf of the trusted CA refused: %v", err)
	}
	if err := Verify(trust, "evil.home", []*x509.Certificate{leaf2}); err == nil {
		t.Fatal("a leaf of the trusted CA was accepted for another name")
	}
}

func tlsServer(t *testing.T, cert *x509.Certificate, key *ecdsa.PrivateKey) *httptest.Server {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{cert.Raw}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func get(cl *http.Client, u string) error {
	resp, err := cl.Get(u)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func TestHTTPClientAndInspect(t *testing.T) {
	cert, key := mkCert(t, "nas", []string{"127.0.0.1"}, false, nil, nil)
	srv := tlsServer(t, cert, key)
	if err := get(HTTPClient(store.TLSTrust{}, 5*time.Second), srv.URL); err == nil || !IsCertError(err.Error()) {
		t.Fatalf("a self-signed server was accepted without trust: %v", err)
	}
	tgt, err := ParseTarget(srv.URL + "/remote.php/dav")
	if err != nil {
		t.Fatal(err)
	}
	in, err := Inspect(context.Background(), tgt)
	if err != nil {
		t.Fatal(err)
	}
	if in.Trusted || in.Fingerprint != Fingerprint(cert) || len(in.Chain) != 1 || !in.Chain[0].SelfSigned || in.NameMismatch != "" || !strings.Contains(in.Problem, "self-signed") {
		t.Fatalf("inspection: %+v", in)
	}
	trust := store.TLSTrust{TLSCert: in.PEM}
	if err := Normalize(&trust, true); err != nil {
		t.Fatal(err)
	}
	if err := get(HTTPClient(trust, 5*time.Second), srv.URL); err != nil {
		t.Fatalf("trusted certificate refused: %v", err)
	}
	if err := get(HTTPClient(store.TLSTrust{TLSInsecure: true}, 5*time.Second), srv.URL); err != nil {
		t.Fatalf("insecure refused: %v", err)
	}
	// The server gets another certificate: a clear error, and Explain says so.
	cert2, key2 := mkCert(t, "nas", []string{"127.0.0.1"}, false, nil, nil)
	srv2 := tlsServer(t, cert2, key2)
	err = get(HTTPClient(trust, 5*time.Second), srv2.URL)
	var ce *ChangedError
	if !errors.As(err, &ce) {
		t.Fatalf("changed certificate: %v", err)
	}
	ex := Explain(context.Background(), srv2.URL, trust, "Fatal: x509: certificate signed by unknown authority")
	if !errors.As(ex, &ce) || ce.Received != Fingerprint(cert2) {
		t.Fatalf("explain: %v", ex)
	}
	if ex := Explain(context.Background(), srv.URL, store.TLSTrust{}, "x509: certificate signed by unknown authority"); ex == nil || !strings.Contains(ex.Error(), "Trust this certificate") {
		t.Fatalf("explain without trust: %v", ex)
	}
	if Explain(context.Background(), srv.URL, trust, "connection refused") != nil {
		t.Fatal("explain of an unrelated error")
	}
}

func TestNameMismatch(t *testing.T) {
	cert, key := mkCert(t, "nas", []string{"nas.lan"}, false, nil, nil)
	srv := tlsServer(t, cert, key)
	tgt, _ := ParseTarget(srv.URL)
	in, err := Inspect(context.Background(), tgt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in.NameMismatch, "nas.lan") {
		t.Fatalf("name mismatch not reported: %+v", in)
	}
	// Go clients pin the certificate itself, whatever the name.
	if err := get(HTTPClient(trustOf(t, cert), 5*time.Second), srv.URL); err != nil {
		t.Fatalf("pinned certificate refused: %v", err)
	}
	legacy, lkey := mkCert(t, "old-nas", nil, false, nil, nil)
	in = Analyse(Target{Host: "old-nas", Port: "443"}, []*x509.Certificate{legacy}, time.Now())
	if !strings.Contains(in.NameMismatch, "common name") {
		t.Fatalf("legacy common name: %+v", in)
	}
	_ = lkey
}

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]Target{
		"https://cloud.example.com/remote.php/dav": {Host: "cloud.example.com", Port: "443"},
		"https://192.168.1.5:8443":                 {Host: "192.168.1.5", Port: "8443"},
		"nas.lan:9000":                             {Host: "nas.lan", Port: "9000"},
		"https://[fd00::1]:5000/x":                 {Host: "fd00::1", Port: "5000"},
		"ldap://dc.corp.lan":                       {Host: "dc.corp.lan", Port: "389", StartTLS: "ldap"},
		"ldaps://dc.corp.lan":                      {Host: "dc.corp.lan", Port: "636"},
		"smtp://mail.lan:25":                       {Host: "mail.lan", Port: "25", StartTLS: "smtp"},
		"smtps://mail.lan":                         {Host: "mail.lan", Port: "465"},
	} {
		got, err := ParseTarget(in)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "http://nas.lan", "ftp://x", "https://-x", "https://a b", "https://nas:0", "https://nas:99999", "file:///etc/passwd"} {
		if _, err := ParseTarget(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestToolFlags(t *testing.T) {
	c, _ := mkCert(t, "nas", []string{"nas.lan"}, false, nil, nil)
	trust := trustOf(t, c)
	if got := RcloneFlags(trust, "/var/lib/strata/tls/x.pem"); !slices.Equal(got, []string{"--ca-cert", "/var/lib/strata/tls/x.pem"}) {
		t.Fatal(got)
	}
	if got := ResticFlags(trust, "/f.pem"); !slices.Equal(got, []string{"--cacert", "/f.pem"}) {
		t.Fatal(got)
	}
	ins := store.TLSTrust{TLSInsecure: true}
	if got := RcloneFlags(ins, ""); !slices.Equal(got, []string{"--no-check-certificate"}) {
		t.Fatal(got)
	}
	if got := ResticFlags(ins, ""); !slices.Equal(got, []string{"--insecure-tls"}) {
		t.Fatal(got)
	}
	if RcloneFlags(store.TLSTrust{}, "") != nil || ResticFlags(store.TLSTrust{}, "") != nil {
		t.Fatal("flags without trust")
	}
	Dir = t.TempDir()
	f, err := CAFile(trust)
	if err != nil || !strings.HasPrefix(f, Dir) {
		t.Fatal(f, err)
	}
	if f2, _ := CAFile(trust); f2 != f {
		t.Fatal("CA file not stable")
	}
}

// A fake LDAP server answering StartTLS, then a TLS handshake.
func TestLDAPStartTLS(t *testing.T) {
	cert, key := mkCert(t, "dc", []string{"127.0.0.1"}, false, nil, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 31)
		if _, err := io.ReadFull(c, buf); err != nil || !strings.Contains(string(buf), "1.3.6.1.4.1.1466.20037") {
			return
		}
		c.Write([]byte{0x30, 0x0c, 0x02, 0x01, 0x01, 0x78, 0x07, 0x0a, 0x01, 0x00, 0x04, 0x00, 0x04, 0x00})
		tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{cert.Raw}, PrivateKey: key}}})
		_ = tc.Handshake()
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	in, err := Inspect(context.Background(), Target{Host: "127.0.0.1", Port: port, StartTLS: "ldap"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Fingerprint != Fingerprint(cert) {
		t.Fatal("wrong certificate")
	}
}
