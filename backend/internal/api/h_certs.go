package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/store"
)

func (s *Server) certRoutes() {
	if s.certs == nil {
		return
	}
	// Re-enable Let's Encrypt after a restart.
	var domain, email string
	s.store.Read(func(st *store.State) { domain, email = st.Settings.ACMEDomain, st.Settings.ACMEEmail })
	if domain != "" {
		if err := s.certs.SetACME(domain, email); err != nil {
			s.store.Log("error", "system", "", "Let's Encrypt: "+err.Error())
		}
	}

	s.admin("GET /api/system/certificates", func(r *http.Request) (any, error) {
		list, d, e := s.certs.Status()
		return map[string]any{"certificates": list, "acmeDomain": d, "acmeEmail": e}, nil
	})

	s.admin("PUT /api/system/certificates/custom", func(r *http.Request) (any, error) {
		var req struct {
			Cert string `json:"cert"`
			Key  string `json:"key"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := s.certs.SetCustom(strings.TrimSpace(req.Cert)+"\n", strings.TrimSpace(req.Key)+"\n"); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "system", "HTTPS certificate imported")
		return nil, nil
	})

	s.admin("DELETE /api/system/certificates/custom", func(r *http.Request) (any, error) {
		s.certs.RemoveCustom()
		s.audit(r, "system", "Imported HTTPS certificate removed")
		return nil, nil
	})

	s.admin("PUT /api/system/certificates/acme", func(r *http.Request) (any, error) {
		var req struct {
			Domain string `json:"domain"`
			Email  string `json:"email"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
		if strings.ContainsAny(req.Email, " \n\r") {
			return nil, badRequest("invalid email")
		}
		if err := s.certs.SetACME(req.Domain, req.Email); err != nil {
			return nil, badRequest(err.Error())
		}
		_ = s.store.Update(func(st *store.State) error {
			st.Settings.ACMEDomain, st.Settings.ACMEEmail = req.Domain, req.Email
			return nil
		})
		if req.Domain == "" {
			s.audit(r, "system", "Let's Encrypt disabled")
			return nil, nil
		}
		s.audit(r, "system", "Let's Encrypt enabled for "+req.Domain)
		// Ask for the certificate right away so that errors show up in the task list.
		return s.jobs.Start("acme", "Let's Encrypt certificate for "+req.Domain, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			j.Logf("Requesting a certificate for %s (HTTP-01 challenge on port 80)…", req.Domain)
			done := make(chan error, 1)
			go func() { done <- s.certs.Obtain() }()
			select {
			case err := <-done:
				if err == nil {
					j.Logf("Certificate obtained; it renews automatically.")
				}
				return err
			case <-time.After(3 * time.Minute):
				return context.DeadlineExceeded
			case <-ctx.Done():
				return ctx.Err()
			}
		}), nil
	})
}
