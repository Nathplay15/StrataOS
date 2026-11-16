package api

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"strataos/internal/certs"
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
