package proxy

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// LetsEncrypt is the production ACME directory (STRATA_ACME_DIRECTORY
// replaces it, e.g. for the staging one).
const LetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"

// CertStore keeps one certificate per host name in dir: <host>.crt/.key from
// Let's Encrypt, otherwise a self-signed <host>.self.crt/.key made on demand.
type CertStore struct {
	dir    string
	mu     sync.Mutex
	cache  map[string]*tls.Certificate
	tokens map[string]string // HTTP-01 token → key authorization
	// Directory is the ACME directory URL.
	Directory string
}

// CertInfo describes the certificate served for a host name.
type CertInfo struct {
	Source   string    `json:"source"` // letsencrypt | self-signed | none
	Issuer   string    `json:"issuer,omitempty"`
	NotAfter time.Time `json:"notAfter,omitzero"`
}

// NewCertStore opens (and creates) the certificate folder.
func NewCertStore(dir string) (*CertStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	d := LetsEncrypt
	if v := os.Getenv("STRATA_ACME_DIRECTORY"); v != "" {
		d = v
	}
	return &CertStore{dir: dir, cache: map[string]*tls.Certificate{}, tokens: map[string]string{}, Directory: d}, nil
}

func (c *CertStore) path(host, ext string) string { return filepath.Join(c.dir, host+ext) }

// Get returns the certificate of host (validated by the caller).
func (c *CertStore) Get(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cert, ok := c.cache[host]; ok && cert.Leaf != nil && time.Now().Before(cert.Leaf.NotAfter) {
		return cert, nil
	}
	if cert, err := tls.LoadX509KeyPair(c.path(host, ".crt"), c.path(host, ".key")); err == nil && time.Now().Before(cert.Leaf.NotAfter) {
		c.cache[host] = &cert
		return &cert, nil
	}
	cert, err := tls.LoadX509KeyPair(c.path(host, ".self.crt"), c.path(host, ".self.key"))
	if err != nil || time.Now().After(cert.Leaf.NotAfter.Add(-24*time.Hour)) {
		if cert, err = selfSigned(host, c.path(host, ".self.crt"), c.path(host, ".self.key")); err != nil {
			return nil, err
		}
	}
	c.cache[host] = &cert
	return &cert, nil
}

// Info tells which certificate host gets.
func (c *CertStore) Info(host string) CertInfo {
	cert, err := c.Get(host)
	if err != nil || cert.Leaf == nil {
		return CertInfo{Source: "none"}
	}
	src := "letsencrypt"
	if len(cert.Leaf.Subject.Organization) > 0 && cert.Leaf.Subject.Organization[0] == "StrataOS" && cert.Leaf.Issuer.CommonName == cert.Leaf.Subject.CommonName {
		src = "self-signed"
	}
	return CertInfo{Source: src, Issuer: cert.Leaf.Issuer.CommonName, NotAfter: cert.Leaf.NotAfter}
}

// NeedsCertificate reports whether host has no Let's Encrypt certificate or
// one expiring within 30 days.
func (c *CertStore) NeedsCertificate(host string) bool {
	cert, err := tls.LoadX509KeyPair(c.path(host, ".crt"), c.path(host, ".key"))
	return err != nil || time.Until(cert.Leaf.NotAfter) < 30*24*time.Hour
}

// Remove deletes the certificates of a host name.
func (c *CertStore) Remove(host string) {
	c.mu.Lock()
	delete(c.cache, host)
	c.mu.Unlock()
	for _, ext := range []string{".crt", ".key", ".self.crt", ".self.key"} {
		_ = os.Remove(c.path(host, ext))
	}
}

// ServeChallenge answers the HTTP-01 challenges in progress.
func (c *CertStore) ServeChallenge(w http.ResponseWriter, r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.URL.Path, "/.well-known/acme-challenge/")
	if !ok {
		return false
	}
	c.mu.Lock()
	v, ok := c.tokens[tok]
	c.mu.Unlock()
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(v))
	return true
}

// DNSSolver publishes the TXT record of a DNS-01 challenge.
type DNSSolver interface {
	Present(ctx context.Context, name, value string) (cleanup func(), err error)
}

// Obtain asks Let's Encrypt for a certificate: DNS-01 when dns is set,
// HTTP-01 on port 80 otherwise.
func (c *CertStore) Obtain(ctx context.Context, host, email string, dns DNSSolver, logf func(string, ...any)) error {
	key, err := c.accountKey()
	if err != nil {
		return err
	}
	cl := &acme.Client{Key: key, DirectoryURL: c.Directory, UserAgent: "StrataOS"}
	acct := &acme.Account{}
	if email != "" {
		acct.Contact = []string{"mailto:" + email}
	}
	if _, err := cl.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return fmt.Errorf("Let's Encrypt account: %w", err)
	}
	order, err := cl.AuthorizeOrder(ctx, acme.DomainIDs(host))
	if err != nil {
		return err
	}
	want := "http-01"
	if dns != nil {
		want = "dns-01"
	}
	for _, u := range order.AuthzURLs {
		z, err := cl.GetAuthorization(ctx, u)
		if err != nil {
			return err
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var ch *acme.Challenge
		for _, x := range z.Challenges {
			if x.Type == want {
				ch = x
			}
		}
		if ch == nil {
			return fmt.Errorf("Let's Encrypt offers no %s challenge", want)
		}
		if dns != nil {
			val, err := cl.DNS01ChallengeRecord(ch.Token)
			if err != nil {
				return err
			}
			logf("Publishing the DNS record _acme-challenge.%s…", host)
			cleanup, err := dns.Present(ctx, "_acme-challenge."+host, val)
			if err != nil {
				return err
			}
			defer cleanup()
			select { // propagation to the authoritative servers
			case <-time.After(20 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			resp, err := cl.HTTP01ChallengeResponse(ch.Token)
			if err != nil {
				return err
			}
			c.mu.Lock()
			c.tokens[ch.Token] = resp
			c.mu.Unlock()
			defer func() { c.mu.Lock(); delete(c.tokens, ch.Token); c.mu.Unlock() }()
			logf("Answering the HTTP challenge on port 80…")
		}
		if _, err := cl.Accept(ctx, ch); err != nil {
			return err
		}
		if _, err := cl.WaitAuthorization(ctx, z.URI); err != nil {
			return err
		}
	}
	if order, err = cl.WaitOrder(ctx, order.URI); err != nil {
		return err
	}
	ck, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}}, ck)
	if err != nil {
		return err
	}
	ders, _, err := cl.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return err
	}
	var chain []byte
	for _, d := range ders {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	if err := writeKeyPair(c.path(host, ".crt"), c.path(host, ".key"), chain, ck); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.cache, host)
	c.mu.Unlock()
	return nil
}

func (c *CertStore) accountKey() (crypto.Signer, error) {
	p := filepath.Join(c.dir, "account.key")
	if b, err := os.ReadFile(p); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				return k, nil
			}
		}
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, _ := x509.MarshalECPrivateKey(k)
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

func writeKeyPair(certPath, keyPath string, chain []byte, key *ecdsa.PrivateKey) error {
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, chain, 0o644)
}

// selfSigned makes the stand-in certificate of a host name (one year).
func selfSigned(host, certPath, keyPath string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{"StrataOS"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writeKeyPair(certPath, keyPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}
