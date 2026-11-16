package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/proxy"
	"strataos/internal/store"
)

func (s *Server) certRoutes() {
	if s.certs == nil {
		return
	}
	// Re-enable Let's Encrypt after a restart.
	var domain, email, mode string
	s.store.Read(func(st *store.State) {
		domain, email, mode = st.Settings.ACMEDomain, st.Settings.ACMEEmail, st.Settings.ACMEMode
	})
	if domain != "" {
		if mode == "dns" {
			if err := s.certs.LoadDNS(); err != nil {
				s.store.Log("error", "system", "", "Let's Encrypt (DNS-01): "+err.Error())
			}
		} else if err := s.certs.SetACME(domain, email); err != nil {
			s.store.Log("error", "system", "", "Let's Encrypt: "+err.Error())
		}
	}

	s.admin("GET /api/system/certificates", func(r *http.Request) (any, error) {
		list, d, e := s.certs.Status()
		var tok, zone string
		var dd store.DDNS
		s.store.Read(func(st *store.State) { tok, zone, dd = st.Settings.ACMEToken, st.Settings.ACMEZoneID, st.DDNS })
		return map[string]any{"certificates": list, "acmeDomain": d, "acmeEmail": e, "acmeMode": mode, "dnsReady": (tok != "" && zone != "") || (dd.Provider == "cloudflare" && dd.Token != "" && dd.ZoneID != "")}, nil
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
			Mode   string `json:"mode"`   // http | dns ("" = http)
			Token  string `json:"token"`  // dedicated Cloudflare token (Zone DNS: edit)
			ZoneID string `json:"zoneId"` // Cloudflare zone ID
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
		if strings.ContainsAny(req.Email, " \n\r") || strings.ContainsAny(req.Token, " \n\r") {
			return nil, badRequest("invalid email or token")
		}
		if req.Mode == "" {
			req.Mode = "http"
		}
		if req.Mode != "http" && req.Mode != "dns" {
			return nil, badRequest("invalid mode")
		}
		if req.Domain == "" {
			_ = s.certs.SetACME("", "")
			s.certs.ClearACMEDNS()
			_ = s.store.Update(func(st *store.State) error {
				st.Settings.ACMEDomain, st.Settings.ACMEEmail, st.Settings.ACMEMode, st.Settings.ACMEToken, st.Settings.ACMEZoneID = "", "", "", "", ""
				return nil
			})
			s.audit(r, "system", "Let's Encrypt disabled")
			return nil, nil
		}
		if req.Mode == "dns" {
			token, zoneID := req.Token, req.ZoneID
			if token == "" || zoneID == "" {
				var dd store.DDNS
				s.store.Read(func(st *store.State) { dd = st.DDNS })
				if dd.Provider == "cloudflare" && dd.Token != "" && dd.ZoneID != "" {
					token, zoneID = dd.Token, dd.ZoneID
				}
			}
			if token == "" || zoneID == "" {
				return nil, badRequest("DNS-01 needs a Cloudflare API token (Zone DNS: edit) and its zone ID: set them here or configure the Cloudflare dynamic DNS")
			}
			_ = s.certs.SetACME("", "")
			_ = s.store.Update(func(st *store.State) error {
				st.Settings.ACMEDomain, st.Settings.ACMEEmail, st.Settings.ACMEMode = req.Domain, req.Email, "dns"
				st.Settings.ACMEToken, st.Settings.ACMEZoneID = req.Token, req.ZoneID
				return nil
			})
			s.audit(r, "system", "Let's Encrypt enabled for "+req.Domain+" (DNS-01)")
			// Ask for the certificate right away so that errors show up in the task list.
			return s.jobs.Start("acme", "Let's Encrypt certificate for "+req.Domain, userOf(r), func(ctx context.Context, j *jobs.Job) error {
				j.Logf("Requesting a certificate for %s (DNS-01 challenge, Cloudflare)\u2026", req.Domain)
				done := make(chan error, 1)
				go func() { done <- s.obtainDNSCert(ctx, req.Domain, req.Email, token, zoneID, j.Logf) }()
				select {
				case err := <-done:
					if err == nil {
						j.Logf("Certificate obtained; it renews automatically.")
					}
					return err
				case <-time.After(5 * time.Minute):
					return context.DeadlineExceeded
				case <-ctx.Done():
					return ctx.Err()
				}
			}), nil
		}
		if err := s.certs.SetACME(req.Domain, req.Email); err != nil {
			return nil, badRequest(err.Error())
		}
		s.certs.ClearACMEDNS()
		_ = s.store.Update(func(st *store.State) error {
			st.Settings.ACMEDomain, st.Settings.ACMEEmail, st.Settings.ACMEMode = req.Domain, req.Email, "http"
			st.Settings.ACMEToken, st.Settings.ACMEZoneID = "", ""
			return nil
		})
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

// obtainDNSCert requests a certificate with the DNS-01 challenge through
// Cloudflare, then serves it for the main domain.
func (s *Server) obtainDNSCert(ctx context.Context, domain, email, token, zoneID string, logf func(string, ...any)) error {
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	cf := &proxy.Cloudflare{Token: token, ZoneID: zoneID}
	if !cf.Covers(ctx, domain) {
		return fmt.Errorf("the Cloudflare zone does not cover %s", domain)
	}
	dir := filepath.Join(s.dataDir, "tls", "dns01")
	cs, err := proxy.NewCertStore(dir)
	if err != nil {
		return err
	}
	if err := cs.Obtain(ctx, domain, email, cf, logf); err != nil {
		return err
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, domain+".crt"))
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, domain+".key"))
	if err != nil {
		return err
	}
	return s.certs.SetACMEDNS(domain, string(certPEM), string(keyPEM))
}

// certLoop renews the DNS-01 certificate of the main domain (checked hourly,
// renewed 30 days before expiry; after a failure it waits 6 hours, Let's
// Encrypt limits failed attempts).
func (s *Server) certLoop() {
	time.Sleep(5 * time.Minute)
	var lastFail time.Time
	for {
		var mode, domain, email, tok, zone string
		s.store.Read(func(st *store.State) {
			mode, domain, email = st.Settings.ACMEMode, st.Settings.ACMEDomain, st.Settings.ACMEEmail
			tok, zone = st.Settings.ACMEToken, st.Settings.ACMEZoneID
		})
		if s.certs != nil && mode == "dns" && domain != "" && time.Since(lastFail) > 6*time.Hour {
			if _, exp, ok := s.certs.DNSCert(); !ok || time.Until(exp) < 30*24*time.Hour {
				token, zoneID := tok, zone
				if token == "" || zoneID == "" {
					var dd store.DDNS
					s.store.Read(func(st *store.State) { dd = st.DDNS })
					if dd.Provider == "cloudflare" && dd.Token != "" && dd.ZoneID != "" {
						token, zoneID = dd.Token, dd.ZoneID
					}
				}
				if token != "" && zoneID != "" {
					if err := s.obtainDNSCert(context.Background(), domain, email, token, zoneID, func(string, ...any) {}); err != nil {
						lastFail = time.Now()
						s.store.Log("warning", "system", "", "DNS-01 certificate renewal failed: "+err.Error())
					}
				}
			}
		}
		time.Sleep(time.Hour)
	}
}
