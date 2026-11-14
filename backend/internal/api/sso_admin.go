package api

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"strataos/internal/accounts"
	"strataos/internal/sso"
	"strataos/internal/store"
)

// ssoClientView is a client as the administration page sees it: never its
// secret.
type ssoClientView struct {
	store.SSOClient
	HasSecret bool   `json:"hasSecret"`
	Sessions  int    `json:"sessions"`
	Grants    int    `json:"grants"`
	Host      string `json:"host,omitempty"` // forward: the protected host
}

// validIssuer checks the address of the NAS given to apps: scheme and host
// only (the endpoints are at fixed paths below it).
func validIssuer(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return "", badRequest("the address of the NAS must look like https://nas.example.com (no path)")
	}
	if !hostPortRe.MatchString(u.Host) {
		return "", badRequest("invalid host name in the address of the NAS")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

var appSchemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.-]*\.[a-z0-9+.-]+$`)

// validRedirect checks a redirect URI registered for a client: http(s) with
// a host, or the private scheme of a native app (com.example.app:/cb), no
// fragment, no wildcard. They are compared exactly at sign-in.
func validRedirect(s string) error {
	u, err := url.Parse(s)
	if err != nil || len(s) > 512 || strings.ContainsAny(s, "* \t\r\n") || u.Fragment != "" || u.User != nil {
		return badRequest("invalid return address: " + s)
	}
	switch {
	case u.Scheme == "https" || u.Scheme == "http":
		if u.Host == "" || !hostPortRe.MatchString(u.Host) {
			return badRequest("invalid return address: " + s)
		}
	case appSchemeRe.MatchString(u.Scheme):
	default:
		return badRequest("a return address must start with https:// (or http://): " + s)
	}
	return nil
}

type ssoClientReq struct {
	Name           string   `json:"name"`
	RedirectURIs   []string `json:"redirectUris"`
	PostLogoutURIs []string `json:"postLogoutUris"`
	Public         bool     `json:"public"`
	RequirePKCE    bool     `json:"requirePkce"`
	Trusted        bool     `json:"trusted"`
	Require2FA     bool     `json:"require2fa"`
	AllUsers       bool     `json:"allUsers"`
	Users          []string `json:"users"`
	Groups         []string `json:"groups"`
	Disabled       bool     `json:"disabled"`
}

func cleanList(in []string) []string {
	var out []string
	for _, x := range in {
		if x = strings.TrimSpace(x); x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func (q *ssoClientReq) check(forward bool) error {
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" || len(q.Name) > 64 || strings.IndexFunc(q.Name, unicode.IsControl) >= 0 {
		return badRequest("enter a name (64 characters at most)")
	}
	q.Users, q.Groups = cleanList(q.Users), cleanList(q.Groups)
	for _, n := range append(slices.Clone(q.Users), q.Groups...) {
		if len(n) > 64 || strings.ContainsAny(n, ",:\r\n\t") {
			return badRequest("invalid user or group name: " + n)
		}
	}
	if len(q.Users) > 500 || len(q.Groups) > 100 {
		return badRequest("too many users or groups")
	}
	if forward {
		return nil
	}
	q.RedirectURIs, q.PostLogoutURIs = cleanList(q.RedirectURIs), cleanList(q.PostLogoutURIs)
	if len(q.RedirectURIs) == 0 || len(q.RedirectURIs) > 10 || len(q.PostLogoutURIs) > 10 {
		return badRequest("enter one to ten return addresses (redirect URIs)")
	}
	for _, u := range append(slices.Clone(q.RedirectURIs), q.PostLogoutURIs...) {
		if err := validRedirect(u); err != nil {
			return err
		}
	}
	return nil
}

// apply copies the request onto a client (access rules only for forward).
func (q *ssoClientReq) apply(c *store.SSOClient) {
	c.Name, c.Trusted, c.Require2FA, c.AllUsers, c.Users, c.Groups, c.Disabled = q.Name, q.Trusted, q.Require2FA, q.AllUsers, q.Users, q.Groups, q.Disabled
	if c.Kind == "forward" {
		return
	}
	c.RedirectURIs, c.PostLogoutURIs, c.RequirePKCE = q.RedirectURIs, q.PostLogoutURIs, q.RequirePKCE || q.Public
	if q.Public != c.Public {
		c.Public = q.Public
		c.Secret = ""
		if !c.Public {
			c.Secret = sso.Random(32)
		}
	}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// newClientID makes a client_id from a name.
func newClientID(name string) string {
	base := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" || strings.HasPrefix(base, "fwd") {
		base = "app"
	}
	return base + "-" + sso.Random(3)
}

func (s *Server) ssoClientViews() []ssoClientView {
	active := map[[2]string]int{}
	grants := map[string]int{}
	if s.sso != nil {
		active = s.sso.Active()
		for _, g := range s.sso.Grants("") {
			grants[g.Client]++
		}
	}
	out := []ssoClientView{}
	s.store.Read(func(st *store.State) {
		for _, c := range st.SSO.Clients {
			v := ssoClientView{SSOClient: *c, HasSecret: c.Secret != "", Grants: grants[c.ID]}
			v.Secret = ""
			for k, n := range active {
				if k[1] == c.ID {
					v.Sessions += n
				}
			}
			for _, d := range st.Remote.Proxy.Domains {
				if d.ID == c.Domain {
					v.Host = d.Host
				}
			}
			out = append(out, v)
		}
	})
	return out
}

// ssoIssuerSuggestions: the addresses an administrator may want as issuer.
func (s *Server) ssoIssuerSuggestions(r *http.Request) []string {
	var out []string
	add := func(u string) {
		if u != "" && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	s.store.Read(func(st *store.State) {
		for _, d := range st.Remote.Proxy.Domains {
			if d.Kind == "ui" {
				add("https://" + d.Host)
			}
		}
		if st.Settings.ACMEDomain != "" && s.certs != nil {
			p := s.certs.HTTPSPort
			if p == "" || p == "443" {
				add("https://" + st.Settings.ACMEDomain)
			} else {
				add("https://" + st.Settings.ACMEDomain + ":" + p)
			}
		}
	})
	if o, err := requestOrigin(r); err == nil {
		add(o)
	}
	return out
}

func (s *Server) ssoAdminRoutes() {
	s.ssoAppRoutes()
	s.admin("GET /api/sso", func(r *http.Request) (any, error) {
		var cfg store.SSO
		var domains []map[string]any
		s.store.Read(func(st *store.State) {
			cfg = st.SSO
			for _, d := range st.Remote.Proxy.Domains {
				if d.Kind == "ui" {
					continue
				}
				domains = append(domains, map[string]any{"id": d.ID, "host": d.Host, "kind": d.Kind, "app": d.App, "sso": d.SSO})
			}
		})
		groups, _ := accounts.ListGroups()
		var gnames []string
		for _, g := range groups {
			gnames = append(gnames, g.Name)
		}
		v := map[string]any{"enabled": cfg.Enabled, "issuer": cfg.Issuer, "suggestions": s.ssoIssuerSuggestions(r),
			"clients": s.ssoClientViews(), "domains": domains, "groups": gnames, "proxy": appProxy != nil && appProxy.Running(),
			"apps": s.ssoAppsView()}
		if s.sso != nil {
			v["keys"] = s.sso.KeyInfo()
		}
		if cfg.Issuer != "" {
			v["discovery"] = cfg.Issuer + "/.well-known/openid-configuration"
		}
		return v, nil
	})

	s.admin("PUT /api/sso", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool   `json:"enabled"`
			Issuer  string `json:"issuer"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		iss := ""
		if req.Issuer != "" || req.Enabled {
			var err error
			if iss, err = validIssuer(req.Issuer); err != nil {
				return nil, err
			}
		}
		if s.sso == nil {
			return nil, &Error{Status: http.StatusServiceUnavailable, Msg: "single sign-on could not start (see the logs)"}
		}
		var prev store.SSO
		_ = s.store.Update(func(st *store.State) error {
			prev = st.SSO
			st.SSO.Enabled, st.SSO.Issuer = req.Enabled, iss
			return nil
		})
		ssoForget()
		switch {
		case req.Enabled && !prev.Enabled:
			s.audit(r, "auth", "Single sign-on enabled ("+iss+")")
		case !req.Enabled && prev.Enabled:
			s.audit(r, "auth", "Single sign-on disabled")
		case prev.Issuer != iss:
			s.audit(r, "auth", "Single sign-on address changed to "+iss)
		}
		return nil, nil
	})

	s.admin("POST /api/sso/clients", func(r *http.Request) (any, error) {
		var req ssoClientReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := req.check(false); err != nil {
			return nil, err
		}
		c := &store.SSOClient{ID: newClientID(req.Name), Kind: "oidc", Created: time.Now()}
		req.apply(c)
		if !c.Public {
			c.Secret = sso.Random(32)
		}
		_ = s.store.Update(func(st *store.State) error {
			st.SSO.Clients = append(st.SSO.Clients, c)
			return nil
		})
		s.audit(r, "auth", "Single sign-on: application "+c.Name+" added")
		v := ssoClientView{SSOClient: *c, HasSecret: c.Secret != ""}
		v.Secret = ""
		// The secret is shown this once (rotate it to see a new one).
		return map[string]any{"client": v, "secret": c.Secret}, nil
	})

	s.admin("PUT /api/sso/clients/{id}", func(r *http.Request) (any, error) {
		var req ssoClientReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		id := r.PathValue("id")
		var secret, name string
		err := s.store.Update(func(st *store.State) error {
			c := st.SSOClient(id)
			if c == nil {
				return notFound("application not found")
			}
			if err := req.check(c.Kind == "forward"); err != nil {
				return err
			}
			wasPublic := c.Public
			req.apply(c)
			if wasPublic && !c.Public {
				secret = c.Secret
			}
			name = c.Name
			return nil
		})
		if err != nil {
			return nil, err
		}
		ssoForget()
		s.audit(r, "auth", "Single sign-on: application "+name+" changed")
		if secret != "" {
			return map[string]string{"secret": secret}, nil
		}
		return nil, nil
	})

	s.admin("DELETE /api/sso/clients/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var c store.SSOClient
		err := s.store.Update(func(st *store.State) error {
			x := st.SSOClient(id)
			if x == nil {
				return notFound("application not found")
			}
			c = *x
			st.SSO.Clients = slices.DeleteFunc(st.SSO.Clients, func(y *store.SSOClient) bool { return y.ID == id })
			if c.Kind == "forward" {
				for _, d := range st.Remote.Proxy.Domains {
					if d.ID == c.Domain {
						d.SSO = false
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.sso.RevokeClient(id, true)
		ssoForget()
		if c.Kind == "forward" && appProxy != nil {
			appProxy.SetRoutes(s.proxyTable())
		}
		s.audit(r, "auth", "Single sign-on: application "+c.Name+" removed")
		return nil, nil
	})

	s.admin("POST /api/sso/clients/{id}/secret", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		secret := sso.Random(32)
		var c store.SSOClient
		err := s.store.Update(func(st *store.State) error {
			x := st.SSOClient(id)
			if x == nil {
				return notFound("application not found")
			}
			if x.Public || x.Kind == "forward" {
				return badRequest("this application has no secret")
			}
			x.Secret = secret
			c = *x
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "auth", "Single sign-on: secret of "+c.Name+" changed")
		out := map[string]any{"secret": secret}
		if c.App != "" {
			// StrataOS wrote the app's configuration: write it again.
			job, err := s.ssoConfigureApp(r, c.App, true)
			if err != nil {
				out["appError"] = err.Error()
			} else {
				out["job"] = job
			}
		}
		return out, nil
	})

	s.admin("POST /api/sso/clients/{id}/revoke", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		s.sso.RevokeClient(id, false)
		s.audit(r, "auth", "Single sign-on: sessions of "+s.ssoClientName(id)+" ended")
		return nil, nil
	})

	s.admin("GET /api/sso/clients/{id}/settings", func(r *http.Request) (any, error) {
		var c store.SSOClient
		var iss string
		s.store.Read(func(st *store.State) {
			if x := st.SSOClient(r.PathValue("id")); x != nil {
				c = *x
			}
			iss = st.SSO.Issuer
		})
		if c.ID == "" {
			return nil, notFound("application not found")
		}
		return ssoManualSettings(&c, iss), nil
	})

	s.admin("POST /api/sso/keys/rotate", func(r *http.Request) (any, error) {
		if err := s.sso.RotateKeys(); err != nil {
			return nil, err
		}
		s.audit(r, "auth", "Single sign-on: signing key rotated")
		return s.sso.KeyInfo(), nil
	})

	s.admin("PUT /api/sso/domains/{id}", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var host string
		err := s.store.Update(func(st *store.State) error {
			for _, d := range st.Remote.Proxy.Domains {
				if d.ID == r.PathValue("id") {
					if d.Kind == "ui" {
						return badRequest("the StrataOS interface has its own sign-in")
					}
					d.SSO, host = req.Enabled, d.Host
					return nil
				}
			}
			return notFound("domain not found")
		})
		if err != nil {
			return nil, err
		}
		if appProxy != nil {
			appProxy.SetRoutes(s.proxyTable())
		} else {
			s.fwdSync()
		}
		if req.Enabled {
			s.audit(r, "auth", "Sign-in with the NAS required on "+host)
		} else {
			s.audit(r, "auth", "Sign-in with the NAS no longer required on "+host)
		}
		return nil, nil
	})
}

// ssoManualSettings: what to type in an app that StrataOS does not
// configure itself.
func ssoManualSettings(c *store.SSOClient, iss string) map[string]any {
	d := sso.Discovery(iss)
	return map[string]any{
		"clientId": c.ID, "issuer": iss, "discovery": iss + "/.well-known/openid-configuration",
		"authorization": d["authorization_endpoint"], "token": d["token_endpoint"], "userinfo": d["userinfo_endpoint"],
		"jwks": d["jwks_uri"], "logout": d["end_session_endpoint"], "redirectUris": c.RedirectURIs,
		"scopes": "openid profile email groups", "pkce": c.RequirePKCE || c.Public,
		"usernameClaim": "preferred_username", "groupsClaim": "groups",
	}
}
