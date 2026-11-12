package store

// TLSTrust says how StrataOS checks the certificate of a server the
// administrator configured (WebDAV, S3 endpoint, REST server, webhook,
// mail server, identity provider…) when no public CA signed it: TLSCert
// holds the certificates the server presented when the administrator chose
// "Trust this certificate" (PEM, server certificate first), TLSFingerprint
// the SHA-256 of that server certificate; TLSInsecure skips the check.
// Empty: the system CAs are used, as before 0.11.1.
type TLSTrust struct {
	TLSCert        string `json:"tlsCert,omitempty"`
	TLSFingerprint string `json:"tlsFingerprint,omitempty"`
	TLSInsecure    bool   `json:"tlsInsecure,omitempty"`
}

// Set tells whether anything differs from the system CAs.
func (t TLSTrust) Set() bool { return t.TLSCert != "" || t.TLSInsecure }
