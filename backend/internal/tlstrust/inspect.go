package tlstrust

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strataos/internal/store"
)

// Target is a server whose certificate is read: the TLS handshake starts
// right away, or after an SMTP or LDAP STARTTLS command.
type Target struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	StartTLS string `json:"starttls,omitempty"` // "" | smtp | ldap
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-]{0,252})$`)

// ParseTarget reads https://host[:port]/…, ldaps://, ldap:// (STARTTLS),
// smtps:// (implicit TLS), smtp:// (STARTTLS) or a bare host[:port] (HTTPS).
func ParseTarget(raw string) (Target, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return Target{}, errors.New("invalid address")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Target{}, errors.New("invalid address")
	}
	t := Target{Host: u.Hostname(), Port: u.Port()}
	def := ""
	switch strings.ToLower(u.Scheme) {
	case "https", "webdavs", "davs":
		def = "443"
	case "ldaps":
		def = "636"
	case "ldap":
		def, t.StartTLS = "389", "ldap"
	case "smtps":
		def = "465"
	case "smtp":
		def, t.StartTLS = "587", "smtp"
	case "http":
		return Target{}, errors.New("this address uses plain HTTP: there is no certificate to check")
	default:
		return Target{}, fmt.Errorf("unsupported address type %q", u.Scheme)
	}
	if t.Port == "" {
		t.Port = def
	}
	if n, err := strconv.Atoi(t.Port); err != nil || n < 1 || n > 65535 {
		return Target{}, errors.New("invalid port")
	}
	if net.ParseIP(t.Host) == nil && !hostRe.MatchString(t.Host) {
		return Target{}, errors.New("invalid host name")
	}
	return t, nil
}

// CertInfo describes one certificate for the administrator.
type CertInfo struct {
	Subject    string    `json:"subject"`
	Issuer     string    `json:"issuer"`
	NotBefore  time.Time `json:"notBefore"`
	NotAfter   time.Time `json:"notAfter"`
	SHA256     string    `json:"sha256"`
	Names      []string  `json:"names,omitempty"`
	SelfSigned bool      `json:"selfSigned,omitempty"`
	CA         bool      `json:"ca,omitempty"`
	Expired    bool      `json:"expired,omitempty"`
}

// Inspection is what a server presented and what StrataOS makes of it.
type Inspection struct {
	Target Target     `json:"target"`
	Chain  []CertInfo `json:"chain"`
	PEM    string     `json:"pem"`
	// Fingerprint of the server certificate (Chain[0]).
	Fingerprint string `json:"fingerprint"`
	// Trusted: the system CAs vouch for it, nothing to do.
	Trusted bool `json:"trusted"`
	// Problem: why the system CAs refuse it.
	Problem string `json:"problem,omitempty"`
	// NameMismatch: the certificate is not for the address typed (rclone and
	// restic check the name even for a trusted certificate).
	NameMismatch string `json:"nameMismatch,omitempty"`
}

func describe(c *x509.Certificate, now time.Time) CertInfo {
	ci := CertInfo{Subject: c.Subject.String(), Issuer: c.Issuer.String(), NotBefore: c.NotBefore, NotAfter: c.NotAfter, SHA256: Fingerprint(c), CA: c.IsCA}
	ci.Names = append(ci.Names, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		ci.Names = append(ci.Names, ip.String())
	}
	ci.SelfSigned = c.Subject.String() == c.Issuer.String() && c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
	ci.Expired = now.After(c.NotAfter) || now.Before(c.NotBefore)
	return ci
}

// Analyse describes certificates presented for host.
func Analyse(tgt Target, certs []*x509.Certificate, now time.Time) *Inspection {
	in := &Inspection{Target: tgt, PEM: EncodePEM(certs), Fingerprint: Fingerprint(certs[0])}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	for _, c := range certs {
		in.Chain = append(in.Chain, describe(c, now))
	}
	_, err := certs[0].Verify(x509.VerifyOptions{DNSName: tgt.Host, Roots: systemRoots(), Intermediates: inter, CurrentTime: now})
	in.Trusted = err == nil
	if err != nil {
		in.Problem = problem(err, certs[0], now)
	}
	if err := certs[0].VerifyHostname(tgt.Host); err != nil {
		names := in.Chain[0].Names
		if len(names) == 0 {
			in.NameMismatch = fmt.Sprintf("this certificate names no server (only an old-style common name, %q): backups and cloud sync will refuse it even if trusted; create a certificate with a subject alternative name, or ignore certificate errors", certs[0].Subject.CommonName)
		} else {
			in.NameMismatch = fmt.Sprintf("this certificate is for %s, not %s: backups and cloud sync check the name even for a trusted certificate, so use that name in the address (or ignore certificate errors)", strings.Join(names, ", "), tgt.Host)
		}
	}
	return in
}

func problem(err error, leaf *x509.Certificate, now time.Time) string {
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	var hn x509.HostnameError
	switch {
	case errors.As(err, &ci) && ci.Reason == x509.Expired:
		if now.After(leaf.NotAfter) {
			return "the certificate expired on " + leaf.NotAfter.Format("2006-01-02")
		}
		return "the certificate is not valid yet (or the clock of the NAS is wrong)"
	case errors.As(err, &hn):
		return "the certificate is for another name"
	case errors.As(err, &ua):
		if leaf.Subject.String() == leaf.Issuer.String() {
			return "self-signed certificate: no recognised authority signed it"
		}
		return "the certificate is signed by an authority this NAS does not know (a private CA?)"
	}
	return err.Error()
}

// dialTimeout bounds every inspection.
const dialTimeout = 10 * time.Second

// Inspect connects to tgt and returns the certificates it presents (no
// HTTP request, no redirect: only the handshake with the host typed).
func Inspect(ctx context.Context, tgt Target) (*Inspection, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var got []*x509.Certificate
	cfg := &tls.Config{ServerName: tgt.Host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS10,
		VerifyConnection: func(cs tls.ConnectionState) error { got = cs.PeerCertificates; return nil }}
	if net.ParseIP(tgt.Host) != nil {
		cfg.ServerName = ""
	}
	d := &net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", hostPort(tgt.Host, tgt.Port))
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %v", hostPort(tgt.Host, tgt.Port), unwrapNet(err))
	}
	defer conn.Close()
	dl, _ := ctx.Deadline()
	_ = conn.SetDeadline(dl)
	switch tgt.StartTLS {
	case "smtp":
		c, err := smtp.NewClient(conn, tgt.Host)
		if err != nil {
			return nil, fmt.Errorf("SMTP: %v", err)
		}
		if err := c.Hello("strataos"); err != nil {
			return nil, fmt.Errorf("SMTP: %v", err)
		}
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return nil, errors.New("the mail server does not offer STARTTLS on this port")
		}
		if err := c.StartTLS(cfg); err != nil {
			return nil, fmt.Errorf("TLS: %v", err)
		}
		_ = c.Quit()
	case "ldap":
		if err := ldapStartTLS(conn); err != nil {
			return nil, err
		}
		fallthrough
	default:
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("TLS: %v", err)
		}
	}
	if len(got) == 0 {
		return nil, errors.New("the server sent no certificate")
	}
	return Analyse(tgt, got, time.Now()), nil
}

func unwrapNet(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err
	}
	return err
}

// ldapStartTLS sends the LDAP StartTLS extended request (RFC 4511) and
// checks the answer.
func ldapStartTLS(conn net.Conn) error {
	oid := "1.3.6.1.4.1.1466.20037"
	req := []byte{0x30, byte(3 + 2 + 2 + len(oid)), 0x02, 0x01, 0x01, 0x77, byte(2 + len(oid)), 0x80, byte(len(oid))}
	req = append(req, oid...)
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("LDAP: %v", err)
	}
	r := bufio.NewReader(conn)
	tag, body, err := berRead(r)
	if err != nil || tag != 0x30 {
		return errors.New("LDAP: unexpected answer to STARTTLS")
	}
	// messageID, then the ExtendedResponse [APPLICATION 24] starting with resultCode.
	br := bufio.NewReader(strings.NewReader(string(body)))
	if t, _, err := berRead(br); err != nil || t != 0x02 {
		return errors.New("LDAP: unexpected answer to STARTTLS")
	}
	t, resp, err := berRead(br)
	if err != nil || t != 0x78 || len(resp) < 3 || resp[0] != 0x0a {
		return errors.New("LDAP: unexpected answer to STARTTLS")
	}
	if code := resp[2]; code != 0 {
		return fmt.Errorf("the LDAP server refused STARTTLS (result %d)", code)
	}
	return nil
}

// berRead reads one BER element (definite length, at most 64 KiB).
func berRead(r *bufio.Reader) (byte, []byte, error) {
	tag, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	l, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n := int(l)
	if l&0x80 != 0 {
		k := int(l & 0x7f)
		if k == 0 || k > 2 {
			return 0, nil, errors.New("BER length")
		}
		n = 0
		for range k {
			b, err := r.ReadByte()
			if err != nil {
				return 0, nil, err
			}
			n = n<<8 | int(b)
		}
	}
	buf := make([]byte, n)
	_, err = io.ReadFull(r, buf)
	return tag, buf, err
}

// Explain turns a certificate failure against address (a URL or
// host:port) into a message saying what to do. When a certificate was
// trusted and the server now presents another one, it is a ChangedError.
// It returns nil when msg is not about a certificate.
func Explain(ctx context.Context, address string, t store.TLSTrust, msg string) error {
	if !IsCertError(msg) || t.TLSInsecure {
		return nil
	}
	tgt, err := ParseTarget(address)
	if err != nil {
		return nil
	}
	in, ierr := Inspect(ctx, tgt)
	if t.TLSCert != "" {
		if ierr == nil {
			certs, _ := ParsePEM(in.PEM)
			if verr := Verify(t, tgt.Host, certs); verr != nil {
				var ce *ChangedError
				if errors.As(verr, &ce) {
					return ce
				}
				return verr
			}
			if in.NameMismatch != "" {
				return fmt.Errorf("the certificate of %s is trusted, but %s", tgt.Host, in.NameMismatch)
			}
		}
		return fmt.Errorf("the certificate of %s was refused: %s", tgt.Host, lastLine(msg))
	}
	why := "it is not signed by a recognised authority"
	if ierr == nil && in.Problem != "" {
		why = in.Problem
	}
	return fmt.Errorf("the certificate of %s is not trusted (%s): if this server is yours, use “Trust this certificate” where it is configured", tgt.Host, why)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 300 {
		l = l[:300]
	}
	return l
}
