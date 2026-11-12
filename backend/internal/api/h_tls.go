package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"strataos/internal/store"
	"strataos/internal/tlstrust"
)

// tlsRoutes: the certificate a server presents, for "Trust this
// certificate" in the forms of cloud syncs, backups, notifications, sign-in
// providers and LDAP. Only a TLS handshake (after STARTTLS for smtp:// and
// ldap://) with the host the administrator typed: no HTTP request, no
// redirect, 10 seconds at most.
func (s *Server) tlsRoutes() {
	s.admin("POST /api/tls/inspect", func(r *http.Request) (any, error) {
		var req struct {
			URL string `json:"url"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		tgt, err := tlstrust.ParseTarget(req.URL)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		in, err := tlstrust.Inspect(r.Context(), tgt)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return in, nil
	})
}

// tlsHint replaces a certificate error of a Go client talking to address
// by what to do about it (trust the certificate, or it changed).
func tlsHint(address string, t store.TLSTrust, err error) error {
	if err == nil {
		return nil
	}
	var ce *tlstrust.ChangedError
	if errors.As(err, &ce) {
		return ce
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ex := tlstrust.Explain(ctx, address, t, err.Error()); ex != nil {
		return ex
	}
	return err
}

// normalizeTLS checks the trust of a server at address before it is saved
// (cleared when the address is not HTTPS/TLS).
func normalizeTLS(t *store.TLSTrust, address string, allowInsecure bool) error {
	if _, err := tlstrust.ParseTarget(address); address == "" || err != nil {
		*t = store.TLSTrust{}
		return nil
	}
	return tlstrust.Normalize(t, allowInsecure)
}
