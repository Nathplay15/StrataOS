package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/oidc"
	"strataos/internal/store"
)

var (
	providerIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	claimRe      = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,64}$`)
	scopeRe      = regexp.MustCompile(`^[\x21\x23-\x5b\x5d-\x7e]{1,64}$`)
)

func printable(s string, max int) bool {
	return len(s) <= max && !strings.ContainsFunc(s, func(r rune) bool { return r < 32 || r == 127 })
}

// validProvider checks and normalises a provider sent by the settings page.
func validProvider(p *store.OIDCProvider) error {
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	p.Name = strings.TrimSpace(p.Name)
	p.Issuer = oidc.NormalizeIssuer(p.Issuer)
	p.ClientID = strings.TrimSpace(p.ClientID)
	switch {
	case !providerIDRe.MatchString(p.ID):
		return badRequest("invalid identifier (lowercase letters, digits and -)")
	case p.Name == "" || !printable(p.Name, 64):
		return badRequest("enter the name shown on the button (64 characters at most)")
	case p.ClientID == "" || !printable(p.ClientID, 256) || strings.Contains(p.ClientID, " "):
		return badRequest("invalid client ID")
	case !printable(p.ClientSecret, 512):
		return badRequest("invalid client secret")
	case !printable(p.ACRValues, 256):
		return badRequest("invalid acr_values")
	}
	if _, err := oidc.CheckURL(p.Issuer, p.AllowHTTP); err != nil {
		return badRequest(err.Error())
	}
	for _, c := range []string{p.UsernameClaim, p.EmailClaim, p.GroupsClaim} {
		if c != "" && !claimRe.MatchString(c) {
			return badRequest("invalid claim name " + c)
		}
	}
	for _, sc := range p.Scopes {
		if !scopeRe.MatchString(sc) {
			return badRequest("invalid scope " + sc)
		}
	}
	if len(p.Scopes) > 20 || len(p.AdminGroups) > 50 || len(p.AllowedGroups) > 50 || len(p.GroupMap) > 50 || len(p.MFAValues) > 20 {
		return badRequest("too many entries")
	}
	for _, l := range [][]string{p.AdminGroups, p.AllowedGroups, p.MFAValues} {
		for _, g := range l {
			if strings.TrimSpace(g) == "" || !printable(g, 256) {
				return badRequest("invalid group or value " + g)
			}
		}
	}
	for from, to := range p.GroupMap {
		if strings.TrimSpace(from) == "" || !printable(from, 256) {
			return badRequest("invalid provider group " + from)
		}
		if err := accounts.ValidMemberGroup(to); err != nil {
			return badRequest(err.Error())
		}
	}
	return nil
}

type providerView struct {
	store.OIDCProvider
	HasSecret bool `json:"hasSecret"`
	Accounts  int  `json:"accounts"` // linked accounts
}

func (s *Server) oidcAdminRoutes() {
	s.admin("GET /api/security/oidc", func(r *http.Request) (any, error) {
		out := struct {
			Providers   []providerView `json:"providers"`
			RedirectURI string         `json:"redirectUri"`
		}{Providers: []providerView{}}
		if o, err := requestOrigin(r); err == nil {
			out.RedirectURI = o + oidcCallback
		}
		s.store.Read(func(st *store.State) {
			for _, p := range st.OIDC {
				v := providerView{OIDCProvider: *p, HasSecret: p.ClientSecret != ""}
				v.ClientSecret = "" // never sent back
				for _, u := range st.Users {
					if slices.ContainsFunc(u.OIDC, func(l store.OIDCLink) bool { return l.Provider == p.ID }) {
						v.Accounts++
					}
				}
				out.Providers = append(out.Providers, v)
			}
		})
		return out, nil
	})

	// Create, or replace (a blank secret keeps the saved one).
	save := func(r *http.Request, create bool) (any, error) {
		var p store.OIDCProvider
		if err := decode(r, &p); err != nil {
			return nil, err
		}
		if !create {
			p.ID = r.PathValue("id")
		}
		if err := validProvider(&p); err != nil {
			return nil, err
		}
		err := s.store.Update(func(st *store.State) error {
			i := slices.IndexFunc(st.OIDC, func(x *store.OIDCProvider) bool { return x.ID == p.ID })
			switch {
			case create && i >= 0:
				return badRequest("a provider with this identifier exists")
			case !create && i < 0:
				return notFound("provider not found")
			case create:
				if len(st.OIDC) >= 20 {
					return badRequest("20 providers at most")
				}
				p.CreatedAt = time.Now()
				st.OIDC = append(st.OIDC, &p)
			default:
				old := st.OIDC[i]
				if p.ClientSecret == "" {
					p.ClientSecret = old.ClientSecret
				}
				if p.ClientSecret == "-" {
					p.ClientSecret = "" // explicitly cleared: public client
				}
				if old.Issuer != p.Issuer {
					// Links were made with the old issuer's subjects.
					for _, u := range st.Users {
						u.OIDC = slices.DeleteFunc(u.OIDC, func(l store.OIDCLink) bool { return l.Provider == p.ID })
					}
				}
				p.CreatedAt = old.CreatedAt
				st.OIDC[i] = &p
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		oidcClient.Forget(p.Issuer)
		verb := "updated"
		if create {
			verb = "added"
		}
		s.audit(r, "auth", fmt.Sprintf("Sign-in provider %s %s", p.Name, verb))
		return map[string]string{"id": p.ID}, nil
	}
	s.admin("POST /api/security/oidc", func(r *http.Request) (any, error) { return save(r, true) })
	s.admin("PUT /api/security/oidc/{id}", func(r *http.Request) (any, error) { return save(r, false) })

	s.admin("DELETE /api/security/oidc/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var name string
		err := s.store.Update(func(st *store.State) error {
			i := slices.IndexFunc(st.OIDC, func(x *store.OIDCProvider) bool { return x.ID == id })
			if i < 0 {
				return notFound("provider not found")
			}
			name = st.OIDC[i].Name
			st.OIDC = slices.Delete(st.OIDC, i, i+1)
			for _, u := range st.Users {
				u.OIDC = slices.DeleteFunc(u.OIDC, func(l store.OIDCLink) bool { return l.Provider == id })
			}
			return nil
		})
		if err == nil {
			s.audit(r, "auth", "Sign-in provider "+name+" removed")
		}
		return nil, err
	})

	// Fetches the provider's metadata and keys, to check the settings.
	s.admin("POST /api/security/oidc/test", func(r *http.Request) (any, error) {
		var req struct {
			Issuer    string `json:"issuer"`
			AllowHTTP bool   `json:"allowHttp"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Issuer = oidc.NormalizeIssuer(req.Issuer)
		if _, err := oidc.CheckURL(req.Issuer, req.AllowHTTP); err != nil {
			return nil, badRequest(err.Error())
		}
		oidcClient.Forget(req.Issuer)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		d, ks, err := oidcClient.Provider(ctx, req.Issuer, req.AllowHTTP, false)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"ok": true, "authorization": d.AuthorizationEndpoint, "token": d.TokenEndpoint, "userinfo": d.UserinfoEndpoint,
			"keys": len(ks.Keys), "pkce": len(d.CodeChallengeMethods) == 0 || slices.Contains(d.CodeChallengeMethods, "S256")}, nil
	})
}
