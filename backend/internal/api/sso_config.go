package api

import (
	"slices"
	"strings"
	"time"

	"strataos/internal/confbackup"
	"strataos/internal/docker"
	"strataos/internal/sso"
	"strataos/internal/store"
)

// sso applies the single sign-on section of an imported configuration
// (confbackup.SSO): settings, then each application with its access rules.
// Never removes an application that exists only here.
func (im *importer) sso() {
	f := im.p.full.SSO
	if f == nil {
		return
	}
	im.each("sso", func(it confbackup.Item) error {
		if it.Key == "settings" {
			iss := ""
			if f.Issuer != "" {
				var err error
				if iss, err = validIssuer(f.Issuer); err != nil {
					return err
				}
			}
			err := im.s.store.Update(func(st *store.State) error {
				st.SSO.Enabled, st.SSO.Issuer = f.Enabled && iss != "", iss
				// Apps know the accounts by these subjects: keep them.
				for user, sub := range f.Subjects {
					if u := st.User(user); u != nil && u.SSOSubject == "" && len(sub) <= 64 && !slices.ContainsFunc(st.Users, func(x *store.User) bool { return x.SSOSubject == sub }) {
						u.SSOSubject = sub
					}
				}
				return nil
			})
			ssoForget()
			return err
		}
		id := strings.TrimPrefix(it.Key, "client:")
		i := slices.IndexFunc(f.Clients, func(c store.SSOClient) bool { return c.ID == id })
		if i < 0 || !ssoClientIDRe.MatchString(id) || strings.HasPrefix(id, "fwd-") {
			return badRequest("invalid application")
		}
		c := f.Clients[i]
		req := ssoClientReq{Name: c.Name, RedirectURIs: c.RedirectURIs, PostLogoutURIs: c.PostLogoutURIs, Public: c.Public, RequirePKCE: c.RequirePKCE,
			Trusted: c.Trusted, Require2FA: c.Require2FA, AllUsers: c.AllUsers, Users: c.Users, Groups: c.Groups, Disabled: c.Disabled}
		if err := req.check(false); err != nil {
			return err
		}
		lost := !c.Public && (c.Secret == masked || c.Secret == "")
		var app string
		err := im.s.store.Update(func(st *store.State) error {
			x := st.SSOClient(id)
			if x == nil {
				x = &store.SSOClient{ID: id, Kind: "oidc", Created: time.Now()}
				if lost {
					// Without its secret nobody signs in: the administrator
					// gives it a new one and enters it in the app.
					x.Secret, req.Disabled = sso.Random(32), true
				}
				st.SSO.Clients = append(st.SSO.Clients, x)
			}
			req.apply(x)
			if !c.Public && !lost {
				x.Secret = c.Secret
			}
			if c.App != "" && docker.ValidStack(c.App) == nil {
				x.App, x.Catalog = c.App, c.Catalog
				app = c.App
			}
			return nil
		})
		ssoForget()
		if err != nil || app == "" || docker.StackApp(app) == "" {
			return err
		}
		// The stack came back without the override file: write it again.
		if sup := ssoSupportOf(docker.StackApp(app)); sup != nil && sup.Auto && !lost {
			if _, err := im.s.ssoConfigureApp(nil, app, false); err != nil {
				return err
			}
			return docker.StackAction(im.ctx, app, "up", im.j)
		}
		return nil
	})
}
