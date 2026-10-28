package vmcluster

// The transport of migrations: libvirt over TLS (qemu+tls, port 16514) and
// QEMU's own TLS for the memory and disk streams. Each member has its own
// small certificate authority (its key never leaves it) that signs its
// certificate (server and client); every member trusts the authorities of
// the members, exchanged over the cluster's authenticated peer API. No
// password, no SSH, and a NAS outside the cluster cannot connect.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Files of the transport. Root is changed by tests.
var Root = "/"

func p(rel string) string { return filepath.Join(Root, rel) }

const (
	ownCA     = "var/lib/strata/vmcluster/ca.pem"
	ownCAKey  = "var/lib/strata/vmcluster/ca-key.pem"
	trustFile = "etc/pki/CA/cacert.pem"
	lvCert    = "etc/pki/libvirt/servercert.pem"
	lvKey     = "etc/pki/libvirt/private/serverkey.pem"
	lvCCert   = "etc/pki/libvirt/clientcert.pem"
	lvCKey    = "etc/pki/libvirt/private/clientkey.pem"
	qemuDir   = "etc/pki/qemu"
	// SocketDropIn makes libvirtd listen for TLS on the cluster address only.
	SocketDropIn = "etc/systemd/system/libvirtd-tls.socket.d/strata.conf"
)

// OwnCA returns this member's authority (PEM), creating it once.
func OwnCA(org string) (string, error) {
	if b, err := os.ReadFile(p(ownCA)); err == nil {
		if _, err := os.Stat(p(ownCAKey)); err == nil {
			return string(b), nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{Organization: []string{org}, CommonName: "StrataOS VM cluster CA " + host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(30, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := writeFile(ownCAKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600, -1); err != nil {
		return "", err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeFile(ownCA, cert, 0o644, -1); err != nil {
		return "", err
	}
	return string(cert), nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

func writeFile(rel string, data []byte, mode os.FileMode, gid int) error {
	path := p(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if gid >= 0 {
		if err := os.Chown(tmp, 0, gid); err != nil && Root == "/" {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, err := os.ReadFile(p(ownCA))
	if err != nil {
		return nil, nil, err
	}
	kb, err := os.ReadFile(p(ownCAKey))
	if err != nil {
		return nil, nil, err
	}
	c, _ := pem.Decode(cb)
	k, _ := pem.Decode(kb)
	if c == nil || k == nil {
		return nil, nil, fmt.Errorf("damaged VM cluster authority")
	}
	cert, err := x509.ParseCertificate(c.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(k.Bytes)
	return cert, key, err
}

// leafValid reports whether the certificate in path is for these addresses.
func leafValid(path string, ips []string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return false
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil || time.Until(c.NotAfter) < 30*24*time.Hour {
		return false
	}
	var have []string
	for _, ip := range c.IPAddresses {
		have = append(have, ip.String())
	}
	for _, ip := range ips {
		if !slices.Contains(have, ip) {
			return false
		}
	}
	return true
}

// EnsureCert issues this member's certificate (for its cluster addresses)
// from its authority and installs it for libvirt and QEMU. changed reports
// new files (libvirtd must then be restarted).
func EnsureCert(org string, ips []string, qemuGID int) (changed bool, err error) {
	if _, err := OwnCA(org); err != nil {
		return false, err
	}
	if leafValid(p(lvCert), ips) && leafValid(p(filepath.Join(qemuDir, "server-cert.pem")), ips) {
		return false, nil
	}
	ca, caKey, err := loadCA()
	if err != nil {
		return false, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{Organization: []string{org}, CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:              []string{host},
		BasicConstraintsValid: true,
	}
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return false, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return false, err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	// libvirtd (root) and QEMU (its own account, through its group).
	for _, f := range []struct {
		rel  string
		data []byte
		mode os.FileMode
		gid  int
	}{
		{lvCert, cert, 0o644, -1}, {lvKey, keyPEM, 0o600, -1}, {lvCCert, cert, 0o644, -1}, {lvCKey, keyPEM, 0o600, -1},
		{filepath.Join(qemuDir, "server-cert.pem"), cert, 0o644, -1}, {filepath.Join(qemuDir, "server-key.pem"), keyPEM, 0o640, qemuGID},
		{filepath.Join(qemuDir, "client-cert.pem"), cert, 0o644, -1}, {filepath.Join(qemuDir, "client-key.pem"), keyPEM, 0o640, qemuGID},
	} {
		if err := writeFile(f.rel, f.data, f.mode, f.gid); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Fingerprint identifies an authority (to compare without the PEM).
func Fingerprint(pemCA string) string {
	blk, _ := pem.Decode([]byte(pemCA))
	if blk == nil {
		return ""
	}
	sum := sha256.Sum256(blk.Bytes)
	return hex.EncodeToString(sum[:])
}

// ValidCA checks an authority received from a member: one CA certificate.
func ValidCA(pemCA string) error {
	blk, rest := pem.Decode([]byte(pemCA))
	if blk == nil || blk.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) > 0 {
		return fmt.Errorf("invalid certificate authority")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil || !c.IsCA {
		return fmt.Errorf("invalid certificate authority")
	}
	return nil
}

// SetTrust installs the authorities of the members (this one included) as
// the only ones libvirt and QEMU accept. changed reports a new list.
func SetTrust(cas []string) (bool, error) {
	var list []string
	for _, c := range cas {
		if ValidCA(c) == nil {
			list = append(list, strings.TrimSpace(c)+"\n")
		}
	}
	slices.Sort(list)
	list = slices.Compact(list)
	data := []byte(strings.Join(list, ""))
	old, _ := os.ReadFile(p(trustFile))
	oldQ, _ := os.ReadFile(p(filepath.Join(qemuDir, "ca-cert.pem")))
	if bytes.Equal(old, data) && bytes.Equal(oldQ, data) {
		return false, nil
	}
	if err := writeFile(trustFile, data, 0o644, -1); err != nil {
		return false, err
	}
	return true, writeFile(filepath.Join(qemuDir, "ca-cert.pem"), data, 0o644, -1)
}

// SocketUnit is the drop-in binding libvirtd's TLS socket to the cluster
// addresses (FreeBind: the address may come up after the socket).
func SocketUnit(ips []string) string {
	var b strings.Builder
	b.WriteString("# Written by StrataOS: libvirt TLS for the migrations of the VM cluster.\n[Socket]\nListenStream=\n")
	var done []string
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil || slices.Contains(done, ip.String()) {
			continue
		}
		done = append(done, ip.String())
		if ip.To4() != nil {
			fmt.Fprintf(&b, "ListenStream=%s:16514\n", ip)
		} else {
			fmt.Fprintf(&b, "ListenStream=[%s]:16514\n", ip)
		}
	}
	b.WriteString("FreeBind=yes\n")
	return b.String()
}

// WriteSocketUnit installs SocketUnit; changed reports a new content.
func WriteSocketUnit(ips []string) (bool, error) {
	data := []byte(SocketUnit(ips))
	if old, _ := os.ReadFile(p(SocketDropIn)); bytes.Equal(old, data) {
		return false, nil
	}
	return true, writeFile(SocketDropIn, data, 0o644, -1)
}

// RemoveTLS deletes the transport files (leaving the cluster).
func RemoveTLS() {
	for _, f := range []string{ownCA, ownCAKey, trustFile, lvCert, lvKey, lvCCert, lvCKey, SocketDropIn} {
		_ = os.Remove(p(f))
	}
	for _, f := range []string{"ca-cert.pem", "server-cert.pem", "server-key.pem", "client-cert.pem", "client-key.pem"} {
		_ = os.Remove(p(filepath.Join(qemuDir, f)))
	}
}
