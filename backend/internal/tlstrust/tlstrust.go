// Package tlstrust lets StrataOS reach a server the administrator configured
// (WebDAV, S3 endpoint, restic REST server, webhook, ntfy, mail server,
// identity provider) whose certificate no public CA signed: another NAS, a
// Nextcloud at home, a company CA. The administrator either trusts the
// certificate the server presents (stored with the task, used for that
// connection only: a CA file for rclone and restic, a verification callback
// for Go clients) or, as a last resort, ignores certificate errors.
package tlstrust

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// Dir holds the CA files handed to rclone and restic (named by content).
var Dir = "/var/lib/strata/tls"

const (
	maxPEM   = 64 << 10
	maxCerts = 10
)

// Fingerprint is the SHA-256 of a DER certificate, lowercase hex.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// Colons formats a hex fingerprint as AB:CD:… for people.
func Colons(fp string) string {
	fp = strings.ToUpper(fp)
	var b strings.Builder
	for i := 0; i+1 < len(fp); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(fp[i : i+2])
	}
	return b.String()
}

// ParsePEM reads the certificates of a PEM bundle.
func ParsePEM(data string) ([]*x509.Certificate, error) {
	if len(data) > maxPEM {
		return nil, errors.New("the certificate is too large")
	}
	var certs []*x509.Certificate
	rest := []byte(data)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("invalid certificate: %v", err)
		}
		certs = append(certs, c)
		if len(certs) > maxCerts {
			return nil, errors.New("too many certificates")
		}
	}
	if len(certs) == 0 {
		return nil, errors.New("no PEM certificate found")
	}
	return certs, nil
}

// EncodePEM writes certificates as a PEM bundle.
func EncodePEM(certs []*x509.Certificate) string {
	var b strings.Builder
	for _, c := range certs {
		_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	return b.String()
}

// Normalize checks a trust setting before it is saved: the certificates
// must parse, the fingerprint is recomputed from them (never taken from the
// browser), and insecure is refused when allowInsecure is false.
func Normalize(t *store.TLSTrust, allowInsecure bool) error {
	if t.TLSInsecure && !allowInsecure {
		return errors.New("certificate errors cannot be ignored here: trust the certificate instead")
	}
	t.TLSCert = strings.TrimSpace(t.TLSCert)
	if t.TLSCert == "" {
		t.TLSFingerprint = ""
		return nil
	}
	certs, err := ParsePEM(t.TLSCert)
	if err != nil {
		return fmt.Errorf("trusted certificate: %v", err)
	}
	t.TLSCert = EncodePEM(certs)
	t.TLSFingerprint = Fingerprint(certs[0])
	return nil
}

// ChangedError says the server presents a certificate other than the one
// the administrator trusted.
type ChangedError struct {
	Host     string
	Trusted  string // fingerprint trusted
	Received string // fingerprint presented now
}

func (e *ChangedError) Error() string {
	return fmt.Sprintf("the certificate of %s changed (trusted SHA-256 %s…, now %s…): if this change is expected (certificate renewed), open the settings and trust the new certificate", e.Host, short(e.Trusted), short(e.Received))
}

func short(fp string) string {
	c := Colons(fp)
	if len(c) > 23 {
		return c[:23]
	}
	return c
}

// systemRoots is replaced in tests (nil = the system pool).
var systemRoots func() *x509.CertPool = func() *x509.CertPool { return nil }

// Verify accepts the certificates a server presented for host when: the
// system CAs vouch for them; or the server certificate is one the
// administrator trusted (a pin: the name is not checked, the certificate
// itself was approved); or they chain up to a trusted certificate (a
// private CA) for that name.
func Verify(t store.TLSTrust, host string, presented []*x509.Certificate) error {
	if len(presented) == 0 {
		return errors.New("the server sent no certificate")
	}
	leaf := presented[0]
	inter := x509.NewCertPool()
	for _, c := range presented[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: systemRoots(), Intermediates: inter}); err == nil {
		return nil
	}
	trusted, err := ParsePEM(t.TLSCert)
	if err != nil {
		return err
	}
	fp := Fingerprint(leaf)
	pool := x509.NewCertPool()
	for _, c := range trusted {
		if Fingerprint(c) == fp {
			return nil
		}
		pool.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: pool, Intermediates: inter}); err == nil {
		return nil
	}
	return &ChangedError{Host: host, Trusted: t.TLSFingerprint, Received: fp}
}

// ClientConfig is the TLS configuration of a Go client for a server whose
// trust is t. roots is the pool used when nothing is trusted (nil: system).
func ClientConfig(t store.TLSTrust, serverName string, roots *x509.CertPool) *tls.Config {
	cfg := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12, RootCAs: roots}
	switch {
	case t.TLSInsecure:
		cfg.InsecureSkipVerify = true
	case t.TLSCert != "":
		// The standard check would refuse the certificate: Verify does it.
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			host := serverName
			if host == "" {
				host = cs.ServerName
			}
			return Verify(t, host, cs.PeerCertificates)
		}
	}
	return cfg
}

// HTTPClient is an HTTP client for one server whose trust is t (a fresh
// transport: its connections are not shared with other servers).
func HTTPClient(t store.TLSTrust, timeout time.Duration) *http.Client {
	if !t.Set() {
		return &http.Client{Timeout: timeout}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = ClientConfig(t, "", nil)
	tr.DisableKeepAlives = true
	return &http.Client{Timeout: timeout, Transport: tr}
}

// CAFile writes the trusted certificates where rclone and restic can read
// them, named by their content so that concurrent tasks never race on it.
func CAFile(t store.TLSTrust) (string, error) {
	if t.TLSCert == "" {
		return "", nil
	}
	sum := sha256.Sum256([]byte(t.TLSCert))
	path := filepath.Join(Dir, hex.EncodeToString(sum[:12])+".pem")
	if b, err := os.ReadFile(path); err == nil && string(b) == t.TLSCert {
		return path, nil
	}
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return "", err
	}
	return path, sys.WriteFileAtomic(path, []byte(t.TLSCert), 0o600)
}

// RcloneFlags are the rclone options for trust t, caFile being CAFile(t).
func RcloneFlags(t store.TLSTrust, caFile string) []string {
	switch {
	case t.TLSInsecure:
		return []string{"--no-check-certificate"}
	case t.TLSCert != "" && caFile != "":
		return []string{"--ca-cert", caFile}
	}
	return nil
}

// ResticFlags are the restic options for trust t, caFile being CAFile(t).
func ResticFlags(t store.TLSTrust, caFile string) []string {
	switch {
	case t.TLSInsecure:
		return []string{"--insecure-tls"}
	case t.TLSCert != "" && caFile != "":
		return []string{"--cacert", caFile}
	}
	return nil
}

// IsCertError tells whether a tool's error output is about the server
// certificate (Go's x509 messages, used by rclone and restic).
func IsCertError(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{"x509:", "certificate signed by unknown authority", "certificate is not valid", "certificate has expired", "tls: failed to verify", "certificate is valid for", "certificate relies on legacy common name"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// HostOf returns the host of a URL or host:port ("" when there is none).
func HostOf(raw string) string {
	tgt, err := ParseTarget(raw)
	if err != nil {
		return ""
	}
	return tgt.Host
}

// hostPort joins a host (IPv6 in brackets) and a port.
func hostPort(host, port string) string { return net.JoinHostPort(host, port) }
