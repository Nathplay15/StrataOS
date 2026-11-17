package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/certs"
	"strataos/internal/proxy"
	"strataos/internal/store"
)

func certsServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin", PasswordHash: hash})
		return nil
	})
	cm, err := certs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.certs = cm
	s.certRoutes()
	return s
}

func TestCertificateACMEModeValidation(t *testing.T) {
	s := certsServer(t)
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}

	// DNS-01 without any Cloudflare token is refused.
	if w := admin.req("PUT", "/api/system/certificates/acme", map[string]string{"domain": "nas.example.com", "email": "a@b.fr", "mode": "dns"}); w.Code != http.StatusBadRequest {
		t.Fatalf("dns without token: %d %s", w.Code, w.Body.String())
	}
	// Unknown mode is refused.
	if w := admin.req("PUT", "/api/system/certificates/acme", map[string]string{"domain": "nas.example.com", "email": "a@b.fr", "mode": "carrier-pigeon"}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad mode: %d %s", w.Code, w.Body.String())
	}
	// Nothing was enabled.
	if w := admin.req("GET", "/api/system/certificates", nil); w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	} else {
		var out struct {
			ACMEDomain string `json:"acmeDomain"`
			ACMEMode   string `json:"acmeMode"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.ACMEDomain != "" || out.ACMEMode != "" {
			t.Fatalf("settings changed: %+v", out)
		}
	}
}

func TestCertificateDisableClearsDNS(t *testing.T) {
	s := certsServer(t)
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}

	_ = s.store.Update(func(st *store.State) error {
		st.Settings.ACMEDomain, st.Settings.ACMEMode = "nas.example.com", "dns"
		return nil
	})
	certPath, keyPath := s.certs.Files() // self-signed pair of the manager
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.certs.SetACMEDNS("nas.example.com", string(certPEM), string(keyPEM)); err != nil {
		t.Fatal(err)
	}

	if w := admin.req("PUT", "/api/system/certificates/acme", map[string]string{"domain": "", "email": ""}); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if _, _, ok := s.certs.DNSCert(); ok {
		t.Fatal("disable left the DNS-01 certificate")
	}
	s.store.Read(func(st *store.State) {
		if st.Settings.ACMEDomain != "" || st.Settings.ACMEMode != "" || st.Settings.ACMEToken != "" {
			t.Error("disable left the Let's Encrypt settings")
		}
	})
}

// The mode shown is the saved one, not the one at startup (review 0.12.0:
// it stayed "http" until a restart, and saving the page went back to HTTP-01).
func TestCertificateModeLive(t *testing.T) {
	s := certsServer(t)
	s.systemRoutes()
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}
	_ = s.store.Update(func(st *store.State) error {
		st.Settings.ACMEDomain, st.Settings.ACMEMode, st.Settings.ACMEToken, st.Settings.ACMEZoneID = "nas.example.com", "dns", "cf-secret-token", "zone1"
		return nil
	})
	w := admin.req("GET", "/api/system/certificates", nil)
	var out struct {
		ACMEDomain string `json:"acmeDomain"`
		ACMEMode   string `json:"acmeMode"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.ACMEMode != "dns" || out.ACMEDomain != "nas.example.com" {
		t.Fatalf("certificates: %s", w.Body.String())
	}
	// The Cloudflare token is masked like the other secrets, and a masked
	// value sent back keeps the saved one.
	w = admin.req("GET", "/api/settings", nil)
	if strings.Contains(w.Body.String(), "cf-secret-token") {
		t.Fatalf("token in clear: %s", w.Body.String())
	}
	var set map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &set)
	if w = admin.req("PUT", "/api/settings", set); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	s.store.Read(func(st *store.State) {
		if st.Settings.ACMEToken != "cf-secret-token" {
			t.Fatalf("token = %q", st.Settings.ACMEToken)
		}
	})
}

// App domains covered by the wildcard skip their own certificate only while
// a valid wildcard exists (review 0.12.0: they expired once the wildcard
// was switched on, as it was never issued).
func TestWildcardServes(t *testing.T) {
	dir := t.TempDir()
	cs, err := proxy.NewCertStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := appProxy
	appProxy = &proxy.Server{Certs: cs}
	defer func() { appProxy = old }()
	if wildcardServes("example.com", "app.example.com") {
		t.Fatal("no wildcard yet")
	}
	write := func(days int, names ...string) {
		c, k := testCertPair(t, time.Now().AddDate(0, 0, days), names...)
		_ = os.WriteFile(filepath.Join(dir, "example.com.crt"), []byte(c), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "example.com.key"), []byte(k), 0o600)
	}
	write(60, "example.com")
	if wildcardServes("example.com", "app.example.com") {
		t.Fatal("a certificate without *.example.com is no wildcard")
	}
	cs2, _ := proxy.NewCertStore(dir)
	appProxy = &proxy.Server{Certs: cs2}
	write(60, "example.com", "*.example.com")
	if !wildcardServes("example.com", "app.example.com") || wildcardServes("example.com", "a.b.example.com") || wildcardServes("", "app.example.com") {
		t.Fatal("valid wildcard")
	}
	cs3, _ := proxy.NewCertStore(dir)
	appProxy = &proxy.Server{Certs: cs3}
	write(5, "example.com", "*.example.com")
	if wildcardServes("example.com", "app.example.com") {
		t.Fatal("a wildcard about to expire must not stop the app's own renewal")
	}
}

func testCertPair(t *testing.T, notAfter time.Time, names ...string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, DNSNames: names}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}
