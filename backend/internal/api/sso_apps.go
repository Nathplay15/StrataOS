package api

// "Sign in with StrataOS" for catalog apps. Each supported app is checked
// against its documentation: Grafana and Paperless-ngx read their OpenID
// settings from environment variables, which StrataOS writes in an override
// compose file of the stack (docker.OverrideFile) before restarting it; the
// others are registered and the page shows what to type in the app. Apps
// reach the NAS from their container through host.docker.internal on the
// plain HTTP port of the daemon (the browser keeps the issuer address): the
// container never needs to trust the NAS's certificate nor resolve its name.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"strataos/internal/docker"
	"strataos/internal/jobs"
	"strataos/internal/sso"
	"strataos/internal/store"
)

// ssoInternalHost is how containers reach the daemon.
const ssoInternalHost = "host.docker.internal"

// ssoSupport describes how a catalog app signs in with the NAS.
type ssoSupport struct {
	App string `json:"app"` // catalog ID
	// Method: "oidc" (OpenID Connect) or "header" (forward authentication
	// with Remote-User on its app domain).
	Method string `json:"method"`
	// Auto: StrataOS writes the app's configuration.
	Auto bool `json:"auto"`
	// Redirects: paths (or full URIs for native apps) of the return addresses.
	Redirects  []string `json:"redirects,omitempty"`
	PostLogout string   `json:"postLogout,omitempty"`
	PKCE       bool     `json:"pkce,omitempty"`
	service    string   // compose service configured (Auto)
	env        func(c ssoAppCtx) map[string]string
}

// ssoAppCtx is what a configuration is made of.
type ssoAppCtx struct {
	Issuer, Internal, AppURL, ClientID, Secret string
}

var ssoCatalog = []ssoSupport{
	{App: "grafana", Method: "oidc", Auto: true, Redirects: []string{"/login/generic_oauth"}, PostLogout: "/login", PKCE: true, service: "grafana",
		env: func(c ssoAppCtx) map[string]string {
			return map[string]string{
				"GF_SERVER_ROOT_URL":                          c.AppURL + "/",
				"GF_AUTH_GENERIC_OAUTH_ENABLED":               "true",
				"GF_AUTH_GENERIC_OAUTH_NAME":                  "StrataOS",
				"GF_AUTH_GENERIC_OAUTH_CLIENT_ID":             c.ClientID,
				"GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET":         c.Secret,
				"GF_AUTH_GENERIC_OAUTH_SCOPES":                "openid profile email groups",
				"GF_AUTH_GENERIC_OAUTH_AUTH_URL":              c.Issuer + "/api/sso/authorize",
				"GF_AUTH_GENERIC_OAUTH_TOKEN_URL":             c.Internal + "/api/sso/token",
				"GF_AUTH_GENERIC_OAUTH_API_URL":               c.Internal + "/api/sso/userinfo",
				"GF_AUTH_GENERIC_OAUTH_USE_PKCE":              "true",
				"GF_AUTH_GENERIC_OAUTH_ALLOW_SIGN_UP":         "true",
				"GF_AUTH_GENERIC_OAUTH_LOGIN_ATTRIBUTE_PATH":  "preferred_username",
				"GF_AUTH_GENERIC_OAUTH_NAME_ATTRIBUTE_PATH":   "name",
				"GF_AUTH_GENERIC_OAUTH_EMAIL_ATTRIBUTE_PATH":  "email",
				"GF_AUTH_GENERIC_OAUTH_GROUPS_ATTRIBUTE_PATH": "groups",
				// Members of the NAS group grafana-admins administer Grafana.
				"GF_AUTH_GENERIC_OAUTH_ROLE_ATTRIBUTE_PATH": "contains(groups[*], 'grafana-admins') && 'Admin' || 'Viewer'",
				"GF_AUTH_SIGNOUT_REDIRECT_URL":              c.Issuer + "/api/sso/logout?" + url.Values{"client_id": {c.ClientID}, "post_logout_redirect_uri": {c.AppURL + "/login"}}.Encode(),
			}
		}},
	{App: "paperless", Method: "oidc", Auto: true, Redirects: []string{"/accounts/oidc/strataos/login/callback/"}, PKCE: true, service: "webserver",
		env: func(c ssoAppCtx) map[string]string {
			prov, _ := json.Marshal(map[string]any{"openid_connect": map[string]any{
				"SCOPE": []string{"openid", "profile", "email", "groups"}, "OAUTH_PKCE_ENABLED": true,
				"APPS": []map[string]any{{"provider_id": "strataos", "name": "StrataOS", "client_id": c.ClientID, "secret": c.Secret,
					"settings": map[string]string{"server_url": c.Internal + "/.well-known/openid-configuration"}}},
			}})
			return map[string]string{
				"PAPERLESS_URL":                     c.AppURL,
				"PAPERLESS_APPS":                    "allauth.socialaccount.providers.openid_connect",
				"PAPERLESS_SOCIALACCOUNT_PROVIDERS": string(prov),
				"PAPERLESS_SOCIAL_AUTO_SIGNUP":      "true",
			}
		}},
	{App: "nextcloud", Method: "oidc", Redirects: []string{"/apps/user_oidc/code", "/index.php/apps/user_oidc/code"}},
	{App: "immich", Method: "oidc", Redirects: []string{"/auth/login", "/user-settings", "app.immich:///oauth-callback"}},
	{App: "gitea", Method: "oidc", Redirects: []string{"/user/oauth2/StrataOS/callback"}},
	{App: "mealie", Method: "oidc", Redirects: []string{"/login"}, PostLogout: "/login", PKCE: true},
	{App: "navidrome", Method: "header"},
}

func ssoSupportOf(app string) *ssoSupport {
	for i := range ssoCatalog {
		if ssoCatalog[i].App == app {
			return &ssoCatalog[i]
		}
	}
	return nil
}

// ssoAppURL is the address users open an app with: its app domain, or the
// NAS's name and the app's port.
func (s *Server) ssoAppURL(stack string) (string, error) {
	var host, iss string
	s.store.Read(func(st *store.State) {
		iss = st.SSO.Issuer
		for _, d := range st.Remote.Proxy.Domains {
			if d.Kind == "app" && d.App == stack && host == "" {
				host = d.Host
			}
		}
	})
	if host != "" {
		return "https://" + host, nil
	}
	u, err := url.Parse(iss)
	if err != nil || u.Hostname() == "" {
		return "", badRequest("set the address of the NAS first")
	}
	p := docker.AppWebPort(stack)
	if p == "" {
		return "", badRequest("this app has no web interface")
	}
	h := u.Hostname()
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	return "http://" + h + ":" + p, nil
}

// ssoInternal is the address of the daemon seen from a container.
func (s *Server) ssoInternal() string { return "http://" + ssoInternalHost + ":" + s.httpPort }

// ssoAppView is an installed app on the administration page.
type ssoAppView struct {
	Stack   string      `json:"stack"`
	App     string      `json:"app"`
	Support *ssoSupport `json:"support,omitempty"`
	Client  string      `json:"client,omitempty"` // registered client
	Domain  string      `json:"domain,omitempty"` // its app domain (header method)
	URL     string      `json:"url,omitempty"`
}

func (s *Server) ssoAppsView() []ssoAppView {
	out := []ssoAppView{}
	for _, stack := range docker.StackNames() {
		app := docker.StackApp(stack)
		sup := ssoSupportOf(app)
		if sup == nil {
			continue
		}
		v := ssoAppView{Stack: stack, App: app, Support: sup}
		s.store.Read(func(st *store.State) {
			for _, c := range st.SSO.Clients {
				if c.App == stack && c.Kind == "oidc" {
					v.Client = c.ID
				}
			}
			for _, d := range st.Remote.Proxy.Domains {
				if d.Kind == "app" && d.App == stack {
					v.Domain = d.ID
				}
			}
		})
		v.URL, _ = s.ssoAppURL(stack)
		out = append(out, v)
	}
	return out
}

// ssoOverride renders the override compose file of an app.
func ssoOverride(sup *ssoSupport, c ssoAppCtx) (string, error) {
	f := map[string]any{"services": map[string]any{sup.service: map[string]any{
		"environment": sup.env(c),
		"extra_hosts": []string{ssoInternalHost + ":host-gateway"},
	}}}
	b, err := yaml.Marshal(f)
	if err != nil {
		return "", err
	}
	return "# Written by StrataOS: sign-in with the NAS (Single sign-on page). Do not edit.\n" + string(b), nil
}

// ssoConfigureApp writes the configuration of an app whose client is
// registered and restarts it (job).
func (s *Server) ssoConfigureApp(r *http.Request, stack string, restart bool) (any, error) {
	app := docker.StackApp(stack)
	sup := ssoSupportOf(app)
	if sup == nil || !sup.Auto {
		return nil, nil
	}
	var c store.SSOClient
	var iss string
	s.store.Read(func(st *store.State) {
		iss = st.SSO.Issuer
		for _, x := range st.SSO.Clients {
			if x.App == stack && x.Kind == "oidc" {
				c = *x
			}
		}
	})
	if c.ID == "" {
		return nil, notFound("this app does not sign in with the NAS")
	}
	appURL, err := s.ssoAppURL(stack)
	if err != nil {
		return nil, err
	}
	content, err := ssoOverride(sup, ssoAppCtx{Issuer: iss, Internal: s.ssoInternal(), AppURL: appURL, ClientID: c.ID, Secret: c.Secret})
	if err != nil {
		return nil, err
	}
	if err := docker.WriteOverride(stack, content); err != nil {
		return nil, err
	}
	if !restart {
		return nil, nil
	}
	return s.jobs.Start("app", "Sign-in with the NAS for "+stack, userOf(r), func(ctx context.Context, j *jobs.Job) error {
		return docker.StackAction(ctx, stack, "up", j)
	}), nil
}

func (s *Server) ssoAppRoutes() {
	s.admin("GET /api/sso/apps", func(r *http.Request) (any, error) { return s.ssoAppsView(), nil })

	// Turns "Sign in with StrataOS" on or off for an installed app.
	s.admin("PUT /api/sso/apps/{stack}", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		stack := r.PathValue("stack")
		if docker.ValidStack(stack) != nil {
			return nil, notFound("app not found")
		}
		app := docker.StackApp(stack)
		sup := ssoSupportOf(app)
		if sup == nil || sup.Method != "oidc" {
			return nil, badRequest("this app cannot sign in with the NAS through OpenID Connect")
		}
		if on, _ := s.ssoConfig(); req.Enabled && !on {
			return nil, badRequest("turn single sign-on on first (Single sign-on page)")
		}
		var existing store.SSOClient
		s.store.Read(func(st *store.State) {
			for _, c := range st.SSO.Clients {
				if c.App == stack && c.Kind == "oidc" {
					existing = *c
				}
			}
		})
		if !req.Enabled {
			if existing.ID == "" {
				return nil, nil
			}
			_ = s.store.Update(func(st *store.State) error {
				st.SSO.Clients = slices.DeleteFunc(st.SSO.Clients, func(c *store.SSOClient) bool { return c.ID == existing.ID })
				return nil
			})
			s.sso.RevokeClient(existing.ID, true)
			ssoForget()
			s.audit(r, "auth", "Single sign-on: "+stack+" no longer signs in with the NAS")
			if sup.Auto {
				if err := docker.WriteOverride(stack, ""); err != nil {
					return nil, err
				}
				return map[string]any{"job": s.jobs.Start("app", "Sign-in with the NAS removed from "+stack, userOf(r), func(ctx context.Context, j *jobs.Job) error {
					return docker.StackAction(ctx, stack, "up", j)
				})}, nil
			}
			return nil, nil
		}
		appURL, err := s.ssoAppURL(stack)
		if err != nil {
			return nil, err
		}
		var redirects, logout []string
		for _, p := range sup.Redirects {
			if strings.HasPrefix(p, "/") {
				redirects = append(redirects, appURL+p)
			} else {
				redirects = append(redirects, p)
			}
		}
		if sup.PostLogout != "" {
			logout = []string{appURL + sup.PostLogout}
		}
		secret := ""
		c := existing
		err = s.store.Update(func(st *store.State) error {
			if x := st.SSOClient(existing.ID); x != nil {
				// Enabled again (address changed, say): keep the client and
				// its access rules, follow the app's address.
				x.RedirectURIs, x.PostLogoutURIs = redirects, logout
				c = *x
				return nil
			}
			c = store.SSOClient{ID: newClientID(stack), Name: appDisplayName(app, stack), Kind: "oidc", App: stack, Catalog: app,
				Secret: sso.Random(32), RedirectURIs: redirects, PostLogoutURIs: logout, RequirePKCE: sup.PKCE, AllUsers: true, Created: time.Now()}
			st.SSO.Clients = append(st.SSO.Clients, &c)
			secret = c.Secret
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "auth", "Single sign-on: "+stack+" signs in with the NAS")
		out := map[string]any{"client": c.ID}
		if sup.Auto {
			job, err := s.ssoConfigureApp(r, stack, true)
			if err != nil {
				return nil, err
			}
			out["job"] = job
			return out, nil
		}
		// Configured by hand: the secret is shown this once.
		var iss string
		s.store.Read(func(st *store.State) { iss = st.SSO.Issuer })
		out["settings"] = ssoManualSettings(&c, iss)
		out["secret"] = secret
		return out, nil
	})
}

// appDisplayName is the catalog name of an app ("Grafana"), with the stack
// when it differs.
func appDisplayName(app, stack string) string {
	for _, a := range docker.Catalog {
		if a.ID == app {
			if stack != app {
				return fmt.Sprintf("%s (%s)", a.Name, stack)
			}
			return a.Name
		}
	}
	return stack
}
