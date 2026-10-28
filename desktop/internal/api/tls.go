package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// A NAS uses a self-signed certificate by default: the app pins its
// fingerprint once the user accepted it (shown at pairing, or carried by
// the pairing link). A certificate from a public CA is accepted too (Let's
// Encrypt renews it every few months, changing the fingerprint). Same rules
// as the Android app.

// Fingerprint is the SHA-256 of a certificate as the NAS shows it ("AB:CD:…").
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	var b strings.Builder
	for i, x := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", x)
	}
	return b.String()
}

// NormalizeFingerprint accepts a fingerprint typed in any common format ("" if invalid).
func NormalizeFingerprint(v string) string {
	var hex []byte
	for _, c := range strings.ToUpper(v) {
		switch {
		case c >= '0' && c <= '9', c >= 'A' && c <= 'F':
			hex = append(hex, byte(c))
		case c == ':' || c == ' ' || c == '-':
		default:
			return ""
		}
	}
	if len(hex) != 64 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < 64; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.Write(hex[i : i+2])
	}
	return b.String()
}

// CertError: the certificate is neither the pinned one nor vouched for by a CA.
type CertError struct {
	Presented string // fingerprint of the certificate presented
	Pinned    string // "" on first use: the user must look at it
}

func (e *CertError) Error() string {
	if e.Pinned != "" {
		return "the NAS certificate changed (now " + e.Presented + "): if you did not renew it, someone may be intercepting the connection"
	}
	return "certificate not trusted yet (" + e.Presented + ")"
}

// tlsConfig checks the pin, else the system roots and the host name.
func tlsConfig(host, pin string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // verified below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no certificate")
			}
			leaf := cs.PeerCertificates[0]
			fp := Fingerprint(leaf.Raw)
			if pin != "" && fp == pin {
				return nil // the NAS itself, whatever address reaches it
			}
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
			}
			if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter}); err == nil {
				return nil
			}
			return &CertError{Presented: fp, Pinned: pin}
		},
	}
}

// ProbeCertificate returns the fingerprint of the certificate served at
// base (https://host:port), and whether a public CA vouches for it.
func ProbeCertificate(ctx context.Context, base string) (fp string, trusted bool, err error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false, errors.New("the address must look like https://nas.local:7443")
	}
	host := u.Hostname()
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(host, "443")
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true, ServerName: host}}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", false, err
	}
	defer c.Close()
	cs := c.(*tls.Conn).ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return "", false, errors.New("no certificate")
	}
	leaf := cs.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, x := range cs.PeerCertificates[1:] {
		inter.AddCert(x)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter})
	return Fingerprint(leaf.Raw), verr == nil, nil
}
