// Package certs serves the HTTPS certificate: self-signed by default, an imported
// certificate, or Let's Encrypt (HTTP-01 challenge on port 80, or DNS-01 with a
// DNS provider: the ACME exchange of the DNS-01 variant is done by the caller).
package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

type Manager struct {
	mu         sync.RWMutex
	dir        string
	selfSigned *tls.Certificate
	custom     *tls.Certificate
	acme       *autocert.Manager
	domain     string
	email      string
	dnsCert    *tls.Certificate // Let's Encrypt certificate obtained with the DNS-01 challenge
	dnsDomain  string
	httpSrv    *http.Server
	httpH      http.Handler
	// external: another server owns port 80 (app domains) and passes the
	// requests through HTTPHandler.
	external bool
	// HTTPSPort is used to redirect plain HTTP requests received on port 80.
	HTTPSPort string
}

type Info struct {
	Source    string    `json:"source"` // self-signed | custom | letsencrypt
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	DNSNames  []string  `json:"dnsNames"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	Active    bool      `json:"active"`
}

// New loads (or creates) the self-signed certificate and any imported certificate in dir.
func New(dir string) (*Manager, error) {
	m := &Manager{dir: dir, HTTPSPort: "7443"}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c, err := loadOrCreateSelfSigned(dir)
	if err != nil {
		return nil, err
	}
	m.selfSigned = &c
	if cc, err := tls.LoadX509KeyPair(filepath.Join(dir, "custom.pem"), filepath.Join(dir, "custom.key")); err == nil {
		m.custom = &cc
	}
	return m, nil
}

// Files returns the PEM files of the imported certificate, or of the self-signed one,
// for other services (FTPS).
func (m *Manager) Files() (cert, key string) {
	m.mu.RLock()
	custom := m.custom != nil
	m.mu.RUnlock()
	if custom {
		return filepath.Join(m.dir, "custom.pem"), filepath.Join(m.dir, "custom.key")
	}
	return filepath.Join(m.dir, "cert.pem"), filepath.Join(m.dir, "key.pem")
}

// GetCertificate picks Let's Encrypt for its domain, then the imported certificate, then self-signed.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	acme, domain, custom, self := m.acme, m.domain, m.custom, m.selfSigned
	dnsCert, dnsDomain := m.dnsCert, m.dnsDomain
	m.mu.RUnlock()
	if acme != nil && strings.EqualFold(hello.ServerName, domain) {
		if c, err := acme.GetCertificate(hello); err == nil {
			return c, nil
		} else {
			slog.Warn("let's encrypt certificate unavailable, falling back", "err", err)
		}
	}
	if dnsCert != nil && dnsCert.Leaf != nil && time.Now().Before(dnsCert.Leaf.NotAfter) && strings.EqualFold(hello.ServerName, dnsDomain) {
		return dnsCert, nil
	}
	if custom != nil {
		return custom, nil
	}
	return self, nil
}

// SetCustom validates and installs an imported certificate (PEM chain + key).
func (m *Manager) SetCustom(certPEM, keyPEM string) error {
	c, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return fmt.Errorf("invalid certificate or key: %w", err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return err
	}
	if time.Now().After(leaf.NotAfter) {
		return fmt.Errorf("this certificate expired on %s", leaf.NotAfter.Format("2006-01-02"))
	}
	if err := os.WriteFile(filepath.Join(m.dir, "custom.pem"), []byte(certPEM), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.dir, "custom.key"), []byte(keyPEM), 0o600); err != nil {
		return err
	}
	m.mu.Lock()
	m.custom = &c
	m.mu.Unlock()
	return nil
}

func (m *Manager) RemoveCustom() {
	_ = os.Remove(filepath.Join(m.dir, "custom.pem"))
	_ = os.Remove(filepath.Join(m.dir, "custom.key"))
	m.mu.Lock()
	m.custom = nil
	m.mu.Unlock()
}

var domainRe = regexp.MustCompile(`^(?i)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// SetACME enables (domain != "") or disables Let's Encrypt. Enabling listens on
// port 80 for the HTTP-01 challenge; other requests there are redirected to HTTPS.
func (m *Manager) SetACME(domain, email string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = m.httpSrv.Shutdown(ctx)
		cancel()
		m.httpSrv = nil
	}
	m.acme, m.domain, m.email, m.httpH = nil, "", "", nil
	if domain == "" {
		return nil
	}
	if !domainRe.MatchString(domain) {
		return fmt.Errorf("invalid domain name")
	}
	var ln net.Listener
	if !m.external {
		var err error
		if ln, err = net.Listen("tcp", ":80"); err != nil {
			return fmt.Errorf("port 80 is not available (an application may be using it): %w", err)
		}
	}
	am := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(filepath.Join(m.dir, "acme")),
		HostPolicy: autocert.HostWhitelist(domain),
		Email:      email,
	}
	port := m.HTTPSPort
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		http.Redirect(w, r, "https://"+net.JoinHostPort(host, port)+r.URL.RequestURI(), http.StatusFound)
	})
	h := am.HTTPHandler(redirect)
	m.acme, m.domain, m.email, m.httpH = am, domain, email, h
	if ln == nil {
		return nil
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("acme http listener", "err", err)
		}
	}()
	m.httpSrv = srv
	return nil
}

// SetACMEDNS installs the Let's Encrypt certificate obtained with the DNS-01
// challenge (the ACME exchange itself is done by the caller, which hands the
// PEM pair over). An empty domain removes it.
func (m *Manager) SetACMEDNS(domain, certPEM, keyPEM string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if domain == "" {
		m.clearDNSLocked()
		return nil
	}
	if !domainRe.MatchString(domain) {
		return fmt.Errorf("invalid domain name")
	}
	c, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return fmt.Errorf("invalid certificate or key: %w", err)
	}
	d := filepath.Join(m.dir, "acme-dns")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	domain = strings.ToLower(domain)
	if err := os.WriteFile(filepath.Join(d, domain+".crt"), []byte(certPEM), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d, domain+".key"), []byte(keyPEM), 0o600); err != nil {
		return err
	}
	// The certificate of a previous domain must not come back after a restart.
	if entries, err := os.ReadDir(d); err == nil {
		for _, e := range entries {
			if n := e.Name(); n != domain+".crt" && n != domain+".key" {
				_ = os.Remove(filepath.Join(d, n))
			}
		}
	}
	m.dnsCert, m.dnsDomain = &c, domain
	return nil
}

// ClearACMEDNS removes the DNS-01 certificate.
func (m *Manager) ClearACMEDNS() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearDNSLocked()
}

func (m *Manager) clearDNSLocked() {
	m.dnsCert, m.dnsDomain = nil, ""
	_ = os.RemoveAll(filepath.Join(m.dir, "acme-dns"))
}

// LoadDNS re-enables the DNS-01 certificate of the configured domain after
// a restart, from the files written by SetACMEDNS. Nothing is loaded when the
// domain has none yet (the renewal loop asks for it).
func (m *Manager) LoadDNS(domain string) error {
	domain = strings.ToLower(domain)
	if !domainRe.MatchString(domain) {
		return fmt.Errorf("invalid domain name")
	}
	p := filepath.Join(m.dir, "acme-dns", domain)
	c, err := tls.LoadX509KeyPair(p+".crt", p+".key")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.dnsCert, m.dnsDomain = &c, domain
	m.mu.Unlock()
	return nil
}

// DNSCertFits tells whether the DNS-01 certificate is the one of domain,
// covers it and has more than renewBefore left.
func (m *Manager) DNSCertFits(domain string, renewBefore time.Duration) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.dnsCert == nil || m.dnsCert.Leaf == nil || !strings.EqualFold(m.dnsDomain, domain) {
		return false
	}
	return m.dnsCert.Leaf.VerifyHostname(domain) == nil && time.Until(m.dnsCert.Leaf.NotAfter) > renewBefore
}

// DNSCert returns the domain and the expiry of the DNS-01 certificate;
// ok is false when there is none.
func (m *Manager) DNSCert() (domain string, notAfter time.Time, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.dnsCert == nil || m.dnsCert.Leaf == nil {
		return "", time.Time{}, false
	}
	return m.dnsDomain, m.dnsCert.Leaf.NotAfter, true
}

// SetExternalHTTP hands port 80 over to another server (on = true), which then
// passes the requests it does not handle to HTTPHandler, or takes it back.
func (m *Manager) SetExternalHTTP(on bool) error {
	m.mu.Lock()
	if m.external == on {
		m.mu.Unlock()
		return nil
	}
	m.external = on
	domain, email := m.domain, m.email
	m.mu.Unlock()
	if domain == "" {
		return nil
	}
	return m.SetACME(domain, email)
}

// HTTPHandler answers the challenges of the interface certificate and
// redirects to HTTPS; nil when Let's Encrypt is off.
func (m *Manager) HTTPHandler() http.Handler {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.httpH
}

// Obtain requests the Let's Encrypt certificate now, to report errors immediately.
func (m *Manager) Obtain() error {
	m.mu.RLock()
	acme, domain := m.acme, m.domain
	m.mu.RUnlock()
	if acme == nil {
		return fmt.Errorf("Let's Encrypt is not enabled")
	}
	_, err := acme.GetCertificate(&tls.ClientHelloInfo{ServerName: domain, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}, SupportedProtos: []string{"h2", "http/1.1"}})
	return err
}

func describe(c *tls.Certificate, source string) Info {
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return Info{Source: source}
	}
	return Info{Source: source, Subject: leaf.Subject.CommonName, Issuer: leaf.Issuer.CommonName, DNSNames: leaf.DNSNames, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}
}

// Status describes the available certificates; Active marks the default one.
func (m *Manager) Status() (list []Info, domain, email string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	self := describe(m.selfSigned, "self-signed")
	self.Active = m.custom == nil
	list = append(list, self)
	if m.custom != nil {
		c := describe(m.custom, "custom")
		c.Active = true
		list = append(list, c)
	}
	if m.dnsCert != nil {
		c := describe(m.dnsCert, "letsencrypt-dns")
		c.Active = true
		list = append(list, c)
	}
	if m.acme != nil {
		if data, err := os.ReadFile(filepath.Join(m.dir, "acme", m.domain)); err == nil {
			if block, rest := pem.Decode(data); block != nil {
				for block != nil && block.Type != "CERTIFICATE" {
					block, rest = pem.Decode(rest)
				}
				if block != nil {
					if leaf, err := x509.ParseCertificate(block.Bytes); err == nil {
						list = append(list, Info{Source: "letsencrypt", Subject: leaf.Subject.CommonName, Issuer: leaf.Issuer.CommonName, DNSNames: leaf.DNSNames, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, Active: true})
					}
				}
			}
		}
	}
	return list, m.domain, m.email
}

func loadOrCreateSelfSigned(dir string) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	host, _ := os.Hostname()
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{"StrataOS"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host, host + ".local", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}
