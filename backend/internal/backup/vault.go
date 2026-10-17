package backup

// Backups to a vault on another StrataOS: restic speaks plain HTTP to a
// short-lived proxy on the loopback, which forwards to the vault over TLS
// with the certificate pinned at pairing (the other NAS usually has a
// self-signed certificate) and adds the access token.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"strataos/internal/store"
)

// PinnedTransport accepts only the certificate whose SHA-256 is fp.
func PinnedTransport(fp string) *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // replaced by the pin check below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return fmt.Errorf("no certificate")
				}
				sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
				if !strings.EqualFold(hex.EncodeToString(sum[:]), fp) {
					return fmt.Errorf("the certificate of the vault changed: pair again")
				}
				return nil
			},
		},
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
		MaxIdleConnsPerHost: 8,
	}
}

type vaultProxy struct {
	srv *http.Server
	url string
}

func startVaultProxy(t *store.BackupTask) (*vaultProxy, error) {
	target, err := url.Parse(strings.TrimRight(t.VaultURL, "/"))
	if err != nil || target.Scheme != "https" {
		return nil, fmt.Errorf("invalid vault address")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			r.Out.Header.Set("Authorization", "Bearer "+t.VaultToken)
		},
		Transport:     PinnedTransport(t.VaultCert),
		FlushInterval: -1,
	}
	p := &vaultProxy{srv: &http.Server{Handler: rp, ReadHeaderTimeout: 30 * time.Second}, url: "http://" + ln.Addr().String() + "/"}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

func (p *vaultProxy) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)
}
