package directory

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func testCA(t *testing.T) string {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestValidCACert(t *testing.T) {
	ca := testCA(t)
	c := store.Directory{TLSVerify: true, CACert: "  " + ca + ca}
	if err := ValidCACert(&c); err != nil || !strings.HasPrefix(c.CACert, "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("two certificates: %v", err)
	}
	for _, bad := range []string{"hello", ca + "trailing junk", strings.Replace(ca, "CERTIFICATE", "PRIVATE KEY", 2), "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"} {
		c := store.Directory{TLSVerify: true, CACert: bad}
		if err := ValidCACert(&c); err == nil {
			t.Errorf("accepted %q", bad[:min(len(bad), 40)])
		}
	}
	// Without checks the CA is dropped.
	c = store.Directory{CACert: ca}
	if err := ValidCACert(&c); err != nil || c.CACert != "" {
		t.Fatal("CA kept without verification")
	}
}

func TestLDAPCertificatePolicy(t *testing.T) {
	if env := ldapEnv(store.Directory{}); !slices.Equal(env, []string{"LDAPTLS_REQCERT=allow"}) {
		t.Fatal(env)
	}
	if env := ldapEnv(store.Directory{TLSVerify: true}); !slices.Equal(env, []string{"LDAPTLS_REQCERT=demand"}) {
		t.Fatal(env)
	}
	if env := ldapEnv(store.Directory{TLSVerify: true, CACert: "x"}); len(env) != 2 || env[1] != "LDAPTLS_CACERT="+LDAPCAFile {
		t.Fatal(env)
	}
	if reqcert(store.Directory{TLSVerify: true}) != "demand" || reqcert(store.Directory{}) != "allow" {
		t.Fatal("sssd policy")
	}
}

func TestCanonicalAndAdmin(t *testing.T) {
	ad := store.Directory{Type: "ad", Realm: "STRATA.TEST", Workgroup: "STRATA", AdminGroup: "STRATA\\Domain Admins"}
	for in, want := range map[string]string{"Marie": `STRATA\marie`, `strata\marie`: `STRATA\marie`, "marie@strata.test": `STRATA\marie`, `OTHER\marie`: "", "marie@other.test": "", "a(b": ""} {
		got, ok := Canonical(ad, in)
		if got != want || ok != (want != "") {
			t.Errorf("Canonical(%q) = %q %v", in, got, ok)
		}
	}
	if !IsAdmin(ad, []string{`strata\domain admins`}) || IsAdmin(ad, []string{"users"}) || IsAdmin(store.Directory{}, []string{""}) {
		t.Fatal("admin group")
	}
	ldap := store.Directory{Type: "ldap"}
	if n, ok := Canonical(ldap, "paul"); !ok || n != "paul" {
		t.Fatal("ldap name")
	}
	if _, ok := Canonical(ldap, "paul x"); ok {
		t.Fatal("ldap name with a space")
	}
}
