package api

// Forward authentication of the app domains: the reverse proxy asks a
// protected host's requests for a NAS sign-in. The host is an internal
// OpenID client ("fwd-<domain id>"): an unknown visitor is sent through the
// usual authorization (consent, access rules, second factor, all in the
// interface), comes back to https://<host>/.strata-sso/callback, where the
// proxy redeems the code in process and sets a host-only session cookie.
// The app then gets Remote-User, Remote-Groups, Remote-Email and
// Remote-Name; clients' own copies of these headers are always removed
// (proxy.StripIdentity).

import (
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"strataos/internal/proxy"
	"strataos/internal/sso"
	"strataos/internal/store"
)

const (
	fwdPrefix   = "/.strata-sso/"
	fwdCallback = fwdPrefix + "callback"
	fwdLogout   = fwdPrefix + "logout"
	fwdState    = "strata_fwds_" // + first 16 characters of the state
)

// fwdClientID is the internal client of an app domain.
func fwdClientID(domainID string) string { return "fwd-" + domainID }

// fwdRedirect is the callback of a protected host.
func fwdRedirect(host string) string { return "https://" + host + fwdCallback }

// headerSafe drops what a header value cannot carry.
func headerSafe(v string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
}

// forwardAuth is proxy.Server.ForwardAuth.
func (s *Server) forwardAuth(w http.ResponseWriter, r *http.Request, rt *proxy.Route) (http.Header, bool) {
	var client *store.SSOClient
	on, iss := false, ""
	s.store.Read(func(st *store.State) {
		on, iss = st.SSO.Enabled && st.SSO.Issuer != "", st.SSO.Issuer
		for _, d := range st.Remote.Proxy.Domains {
			if d.Host == rt.Host && d.SSO {
				if c := st.SSOClient(fwdClientID(d.ID)); c != nil && !c.Disabled {
					x := *c
					client = &x
				}
			}
		}
	})
	if !on || client == nil || s.sso == nil {
		ssoPage(w, http.StatusServiceUnavailable, "Sign-in unavailable", "This application needs a sign-in with the NAS, which is not available: single sign-on is turned off.")
		return nil, true
	}
	switch r.URL.Path {
	case fwdCallback:
		s.fwdFinish(w, r, rt.Host, client, iss)
		return nil, true
	case fwdLogout:
		if c, err := r.Cookie(proxy.FwdCookie); err == nil {
			s.sso.DropFwd(c.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: proxy.FwdCookie, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		ssoPage(w, http.StatusOK, "Signed out", "You are signed out of "+rt.Host+". Your session on the NAS stays open.")
		return nil, true
	}
	if c, err := r.Cookie(proxy.FwdCookie); err == nil {
		if f := s.sso.Fwd(c.Value); f != nil && f.Client == client.ID {
			id, err := s.ssoLookup(f.User, client.ID)
			if err != nil {
				ssoPage(w, http.StatusForbidden, "Access denied", err.Error())
				return nil, true
			}
			h := http.Header{}
			h.Set("Remote-User", id.Username)
			h.Set("Remote-Groups", strings.Join(id.Groups, ","))
			if id.Email != "" {
				h.Set("Remote-Email", headerSafe(id.Email))
			}
			if id.Name != "" {
				h.Set("Remote-Name", headerSafe(id.Name))
			}
			return h, false
		}
	}
	// Not signed in: only page loads are sent to the sign-in; scripts get 401.
	nav := r.Method == http.MethodGet || r.Method == http.MethodHead
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" && m != "navigate" {
		nav = false
	}
	if r.Header.Get("X-Requested-With") != "" || strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		nav = false
	}
	if !nav {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "sign-in required", http.StatusUnauthorized)
		return nil, true
	}
	state, verifier := sso.Random(24), sso.Random(32)
	next := r.URL.RequestURI()
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") || strings.ContainsAny(next, "\r\n") || strings.HasPrefix(next, fwdPrefix) || len(next) > 2048 {
		next = "/"
	}
	if err := s.sso.PutFlow(state, &sso.FwdFlow{Client: client.ID, Host: rt.Host, Verifier: verifier, Next: next}); err != nil {
		ssoPage(w, http.StatusTooManyRequests, "Sign-in unavailable", err.Error())
		return nil, true
	}
	http.SetCookie(w, &http.Cookie{Name: fwdState + state[:16], Value: state, Path: fwdPrefix, HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sso.RequestTTL.Seconds())})
	q := url.Values{"client_id": {client.ID}, "redirect_uri": {fwdRedirect(rt.Host)}, "response_type": {"code"},
		"scope": {"openid profile email groups"}, "state": {state}, "code_challenge": {sso.S256(verifier)}, "code_challenge_method": {"S256"}}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, iss+"/api/sso/authorize?"+q.Encode(), http.StatusFound)
	return nil, true
}

// fwdFinish is the callback: state bound to this browser by its cookie, code
// redeemed with the PKCE verifier, session cookie on this host only.
func (s *Server) fwdFinish(w http.ResponseWriter, r *http.Request, host string, client *store.SSOClient, iss string) {
	q := r.URL.Query()
	state := q.Get("state")
	if len(state) != 48 {
		ssoPage(w, http.StatusBadRequest, "Sign-in failed", "Invalid answer of the sign-in: start again from the application's address.")
		return
	}
	c, err := r.Cookie(fwdState + state[:16])
	if err != nil || c.Value != state {
		ssoPage(w, http.StatusBadRequest, "Sign-in failed", "This sign-in was started in another browser (or took too long): open the application's address again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: fwdState + state[:16], Value: "", Path: fwdPrefix, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	f := s.sso.TakeFlow(state)
	if f == nil || f.Host != host || f.Client != client.ID {
		ssoPage(w, http.StatusBadRequest, "Sign-in failed", "This sign-in expired: open the application's address again.")
		return
	}
	if e := q.Get("error"); e != "" {
		msg := "The sign-in was cancelled."
		if e != "access_denied" {
			msg = "The sign-in failed (" + headerSafe(e) + ")."
		}
		ssoPage(w, http.StatusForbidden, "Access denied", msg)
		return
	}
	if v := q.Get("iss"); v != "" && v != iss {
		ssoPage(w, http.StatusBadRequest, "Sign-in failed", "The answer comes from another provider.")
		return
	}
	user, sid, err := s.sso.Redeem(q.Get("code"), protoClient(client), fwdRedirect(host), f.Verifier)
	if err != nil {
		ssoPage(w, http.StatusForbidden, "Access denied", err.Error())
		return
	}
	ttl := s.sessionTTL()
	t := s.sso.NewFwdSession(client.ID, user, sid, ttl)
	http.SetCookie(w, &http.Cookie{Name: proxy.FwdCookie, Value: t, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds())})
	s.store.Log("info", "auth", user, "Signed in to "+host+" from "+clientIP(r))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, f.Next, http.StatusFound)
}

// fwdSync keeps the internal clients of the protected app domains in step
// with them (redirect URI on the host; removed with the protection, their
// sessions with them).
func (s *Server) fwdSync() {
	inSync := true
	s.store.Read(func(st *store.State) {
		n := 0
		for _, d := range st.Remote.Proxy.Domains {
			if d.SSO && d.Kind != "ui" {
				n++
				c := st.SSOClient(fwdClientID(d.ID))
				if c == nil || c.Kind != "forward" || len(c.RedirectURIs) != 1 || c.RedirectURIs[0] != fwdRedirect(d.Host) {
					inSync = false
				}
			}
		}
		for _, c := range st.SSO.Clients {
			if c.Kind == "forward" {
				n--
			}
		}
		inSync = inSync && n == 0
	})
	if inSync {
		return
	}
	var gone []string
	_ = s.store.Update(func(st *store.State) error {
		gone = fwdSyncLocked(st)
		return nil
	})
	for _, id := range gone {
		if s.sso != nil {
			s.sso.RevokeClient(id, true)
		}
	}
	ssoForget()
}

func fwdSyncLocked(st *store.State) (gone []string) {
	want := map[string]*store.AppDomain{}
	for _, d := range st.Remote.Proxy.Domains {
		if d.SSO && d.Kind != "ui" {
			want[fwdClientID(d.ID)] = d
		}
	}
	keep := st.SSO.Clients[:0]
	for _, c := range st.SSO.Clients {
		if c.Kind != "forward" {
			keep = append(keep, c)
			continue
		}
		d, ok := want[c.ID]
		if !ok {
			gone = append(gone, c.ID)
			continue
		}
		c.Domain, c.RedirectURIs, c.Public, c.RequirePKCE = d.ID, []string{fwdRedirect(d.Host)}, true, true
		if c.Name == "" {
			c.Name = d.Host
		}
		delete(want, c.ID)
		keep = append(keep, c)
	}
	st.SSO.Clients = keep
	for id, d := range want {
		st.SSO.Clients = append(st.SSO.Clients, &store.SSOClient{ID: id, Name: d.Host, Kind: "forward", Domain: d.ID,
			Public: true, RequirePKCE: true, RedirectURIs: []string{fwdRedirect(d.Host)}, Trusted: true, AllUsers: true, Created: time.Now()})
	}
	return gone
}
