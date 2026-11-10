package api

// Sign-in with an OpenID Connect provider: authorization code flow with
// PKCE (S256), a state bound to the browser by a cookie (no login CSRF), a
// nonce in the ID token, keys from the provider's JWKS. An identity signs
// in to the account linked to its subject (issuer + sub), or to a new
// account when the provider may create them; an existing account of the
// same name is never taken over (its owner links it from their profile).
// The local password sign-in always stays (break-glass).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdmaps "maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"strataos/internal/accounts"
	"strataos/internal/oidc"
	"strataos/internal/store"
)

const (
	oidcCookie   = "strata_oidc"
	oidcFlowTTL  = 10 * time.Minute
	oidcCallback = "/api/login/oidc/callback"
)

var oidcClient = oidc.NewClient()

// oidcFlow is a sign-in started on this NAS, keyed by its state.
type oidcFlow struct {
	provider string
	binding  string // hash of the strata_oidc cookie of the browser that started it
	nonce    string
	verifier string
	redirect string // redirect_uri sent, sent again with the code
	next     string // local path to open afterwards
	linkUser string // linking an identity to the signed-in account
	linkSess string
	expires  time.Time
}

type oidcFlows struct {
	mu sync.Mutex
	m  map[string]*oidcFlow
	// errs: messages of failed sign-ins, shown by the sign-in page
	// (GET /api/login/oidc/error?id=), never put in the URL.
	errs map[string]oidcErr
}

type oidcErr struct {
	msg     string
	expires time.Time
}

var pendingOIDC = &oidcFlows{m: map[string]*oidcFlow{}, errs: map[string]oidcErr{}}

func (f *oidcFlows) put(state string, fl *oidcFlow, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.m {
		if now.After(v.expires) {
			delete(f.m, k)
		}
	}
	if len(f.m) >= maxPasskeyFlows {
		return &Error{Status: http.StatusTooManyRequests, Msg: "too many sign-ins in progress, try again in a few minutes"}
	}
	fl.expires = now.Add(oidcFlowTTL)
	f.m[state] = fl
	return nil
}

func (f *oidcFlows) take(state string, now time.Time) *oidcFlow {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[state]
	delete(f.m, state)
	if !ok || now.After(fl.expires) {
		return nil
	}
	return fl
}

func (f *oidcFlows) putErr(msg string, now time.Time) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.errs {
		if now.After(v.expires) {
			delete(f.errs, k)
		}
	}
	if len(f.errs) > 1000 {
		return ""
	}
	id := token()[:24]
	f.errs[id] = oidcErr{msg: msg, expires: now.Add(10 * time.Minute)}
	return id
}

func (f *oidcFlows) takeErr(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.errs[id]
	delete(f.errs, id)
	if !ok || time.Now().After(e.expires) {
		return ""
	}
	return e.msg
}

// safeNext keeps a local path for the redirection after sign-in: never
// another site (//host, /\host, schemes), never the API.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.ContainsAny(next, "\\\r\n\t") || strings.HasPrefix(next, "/api/") || len(next) > 512 {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return next
}

var hostPortRe = regexp.MustCompile(`^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9.-]{1,253})(:[0-9]{1,5})?$`)

// requestOrigin is the scheme and host the browser used (the redirect URI
// is built on it; the provider only accepts the ones registered there).
func requestOrigin(r *http.Request) (string, error) {
	if !hostPortRe.MatchString(r.Host) {
		return "", badRequest("invalid host name in the address")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if peer, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if a, err := netip.ParseAddr(peer); err == nil && a.Unmap().IsLoopback() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
	}
	return scheme + "://" + strings.ToLower(r.Host), nil
}

func (s *Server) oidcProvider(id string) (store.OIDCProvider, bool) {
	var p store.OIDCProvider
	var ok bool
	s.store.Read(func(st *store.State) {
		for _, x := range st.OIDC {
			if x.ID == id {
				p, ok = *x, true
				p.Scopes, p.AdminGroups, p.AllowedGroups, p.MFAValues = slices.Clone(x.Scopes), slices.Clone(x.AdminGroups), slices.Clone(x.AllowedGroups), slices.Clone(x.MFAValues)
				p.GroupMap = stdmaps.Clone(x.GroupMap)
			}
		}
	})
	return p, ok
}

func scopesOf(p store.OIDCProvider) []string {
	if len(p.Scopes) > 0 {
		return p.Scopes
	}
	return []string{"profile", "email"}
}

func (s *Server) oidcRoutes() {
	// Buttons of the sign-in page.
	s.public("GET /api/login/providers", func(r *http.Request) (any, error) {
		type btn struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		out := []btn{}
		s.store.Read(func(st *store.State) {
			for _, p := range st.OIDC {
				if p.Enabled {
					out = append(out, btn{p.ID, p.Name})
				}
			}
		})
		return out, nil
	})

	s.public("GET /api/login/oidc/error", func(r *http.Request) (any, error) {
		return map[string]string{"error": pendingOIDC.takeErr(r.URL.Query().Get("id"))}, nil
	})

	s.mux.HandleFunc("GET /api/login/oidc/{id}", func(w http.ResponseWriter, r *http.Request) {
		fail := func(msg string) { s.oidcFail(w, r, msg) }
		p, ok := s.oidcProvider(r.PathValue("id"))
		if !ok || !p.Enabled {
			fail("this sign-in method is not available")
			return
		}
		origin, err := requestOrigin(r)
		if err != nil {
			fail(err.Error())
			return
		}
		fl := &oidcFlow{provider: p.ID, nonce: oidc.Random(24), verifier: oidc.Random(48), redirect: origin + oidcCallback, next: safeNext(r.URL.Query().Get("next"))}
		if r.URL.Query().Get("link") == "1" {
			// Linking from the profile: only for the signed-in account.
			c, err := r.Cookie(cookieName)
			var ss *session
			if err == nil {
				ss = s.sessions.get(c.Value)
			}
			if ss == nil || ss.Device != "" {
				fail("sign in first to link an account")
				return
			}
			fl.linkUser, fl.linkSess = ss.User, key(c.Value)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		d, _, err := oidcClient.Provider(ctx, p.Issuer, p.AllowHTTP, false)
		if err != nil {
			slog.Warn("oidc discovery", "provider", p.ID, "err", err)
			fail(p.Name + " cannot be reached: " + err.Error())
			return
		}
		binding := token()
		fl.binding = key(binding)
		state := oidc.Random(24)
		if err := pendingOIDC.put(state, fl, time.Now()); err != nil {
			fail(err.Error())
			return
		}
		// Lax: the provider sends the browser back with a cross-site
		// top-level GET, which a Strict cookie would not accompany.
		http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: binding, Path: "/api/login/oidc", HttpOnly: true, SameSite: http.SameSiteLaxMode,
			Secure: strings.HasPrefix(origin, "https:"), MaxAge: int(oidcFlowTTL.Seconds())})
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, oidc.AuthURL(d, oidc.AuthRequest{ClientID: p.ClientID, RedirectURI: fl.redirect, State: state, Nonce: fl.nonce,
			Verifier: fl.verifier, Scopes: scopesOf(p), ACRValues: p.ACRValues}), http.StatusFound)
	})

	s.mux.HandleFunc("GET "+oidcCallback, s.oidcCallback)

	s.user("GET /api/me/oidc", func(r *http.Request) (any, error) {
		type link struct {
			Provider string    `json:"provider"`
			Name     string    `json:"name"`
			Created  bool      `json:"created"`
			Linked   time.Time `json:"linked"`
			LastUsed time.Time `json:"lastUsed,omitzero"`
		}
		type prov struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		out := struct {
			Providers []prov `json:"providers"`
			Links     []link `json:"links"`
		}{Providers: []prov{}, Links: []link{}}
		s.store.Read(func(st *store.State) {
			names := map[string]string{}
			for _, p := range st.OIDC {
				names[p.ID] = p.Name
				if p.Enabled {
					out.Providers = append(out.Providers, prov{p.ID, p.Name})
				}
			}
			if u := st.User(userOf(r)); u != nil {
				for _, l := range u.OIDC {
					out.Links = append(out.Links, link{l.Provider, names[l.Provider], l.Created, l.Linked, l.LastUsed})
				}
			}
		})
		return out, nil
	})

	s.user("DELETE /api/me/oidc/{provider}", func(r *http.Request) (any, error) {
		err := s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil || !slices.ContainsFunc(u.OIDC, func(l store.OIDCLink) bool { return l.Provider == r.PathValue("provider") }) {
				return notFound("link not found")
			}
			if u.PasswordHash == "" && u.Source == "" && len(u.Passkeys) == 0 && len(u.OIDC) == 1 {
				return badRequest("set a password first: this is your only way to sign in")
			}
			u.OIDC = slices.DeleteFunc(u.OIDC, func(l store.OIDCLink) bool { return l.Provider == r.PathValue("provider") })
			return nil
		})
		if err == nil {
			s.audit(r, "auth", "Unlinked the "+r.PathValue("provider")+" sign-in")
		}
		return nil, err
	})

	s.admin("DELETE /api/users/{name}/oidc/{provider}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil {
				return notFound("user not found")
			}
			u.OIDC = slices.DeleteFunc(u.OIDC, func(l store.OIDCLink) bool { return l.Provider == r.PathValue("provider") })
			return nil
		})
		if err == nil {
			s.audit(r, "users", fmt.Sprintf("Unlinked the %s sign-in of %s", r.PathValue("provider"), name))
		}
		return nil, err
	})

	s.oidcAdminRoutes()
}

// oidcFail ends a sign-in on the sign-in page, with its message kept on
// the server.
func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, msg string) {
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/login/oidc", HttpOnly: true, MaxAge: -1})
	w.Header().Set("Cache-Control", "no-store")
	target := "/"
	if id := pendingOIDC.putErr(msg, time.Now()); id != "" {
		target = "/?oidc_error=" + id
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	fail := func(who, msg string, logged bool) {
		if logged {
			s.store.Log("warning", "auth", who, "Failed single sign-on from "+ip+": "+msg)
		}
		s.oidcFail(w, r, msg)
	}
	if !s.limiter.attempt(ip) {
		fail("", "too many failed attempts, try again in a few minutes", false)
		return
	}
	q := r.URL.Query()
	fl := pendingOIDC.take(q.Get("state"), time.Now())
	c, err := r.Cookie(oidcCookie)
	if fl == nil || err != nil || key(c.Value) != fl.binding {
		// Unknown state, or a sign-in started in another browser (login CSRF).
		fail("", "this sign-in has expired or was started elsewhere, try again", false)
		return
	}
	p, ok := s.oidcProvider(fl.provider)
	if !ok || !p.Enabled {
		fail("", "this sign-in method is not available", false)
		return
	}
	if e := q.Get("error"); e != "" {
		fail("", p.Name+" refused the sign-in ("+oidcText(e, 64)+")", true)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	claims, err := s.oidcVerify(ctx, p, fl, q.Get("code"))
	if err != nil {
		slog.Info("oidc sign-in refused", "provider", p.ID, "ip", ip, "err", err)
		fail("", p.Name+": "+err.Error(), true)
		return
	}
	id, err := oidc.MapClaims(claims, oidc.Mapping{UsernameClaim: p.UsernameClaim, EmailClaim: p.EmailClaim, GroupsClaim: p.GroupsClaim,
		AdminGroups: p.AdminGroups, AllowedGroups: p.AllowedGroups, GroupMap: p.GroupMap})
	if err != nil {
		fail("", err.Error(), true)
		return
	}
	var mfa string
	if p.TrustMFA {
		if m, hw := oidc.MFA(claims, p.MFAValues); hw {
			mfa = mfaIdPHardware
		} else if m {
			mfa = mfaIdP
		}
	}
	if fl.linkUser != "" {
		if err := s.oidcLink(fl, p, id); err != nil {
			fail(fl.linkUser, err.Error(), true)
			return
		}
		s.store.Log("info", "auth", fl.linkUser, "Linked the "+p.Name+" sign-in")
		http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/login/oidc", HttpOnly: true, MaxAge: -1})
		http.Redirect(w, r, fl.next, http.StatusSeeOther)
		return
	}
	u, err := s.oidcAccount(p, id)
	if err != nil {
		fail(id.Username, err.Error(), true)
		s.guardFail(ip, "web", id.Username)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/login/oidc", HttpOnly: true, MaxAge: -1})
	var blocked, idpOK bool
	s.store.Read(func(st *store.State) {
		if x := st.User(u.Username); x != nil {
			blocked = passkeyOnlyBlocked(st, x, time.Now())
		}
		idpOK = idpSatisfies(st, mfa)
	})
	switch {
	case blocked && !idpOK:
		fail(u.Username, "two-factor authentication is required for administrators: sign in with your passkey", true)
	case hasSecondFactor(&u) && !idpOK:
		// The account's own second factor, on the sign-in page.
		t, err := pending2FA.put(&pendingLogin{user: u.Username, ip: ip, via: p.Name}, time.Now())
		if err != nil {
			fail(u.Username, err.Error(), false)
			return
		}
		setPendingCookie(w, r, t, pendingTTL)
		http.Redirect(w, r, "/?signin=2fa", http.StatusSeeOther)
	default:
		ttl := s.sessionTTL()
		if c, err := r.Cookie(cookieName); err == nil {
			s.sessions.delete(c.Value)
		}
		s.limiter.reset(ip)
		s.setCookie(w, r, s.sessions.add(&session{User: u.Username, Role: u.Role, Expires: time.Now().Add(ttl), Created: time.Now(), IP: ip, MFA: mfa}), ttl)
		how := "Signed in with " + p.Name
		if mfa != "" {
			how += " (second factor checked by the provider)"
		}
		s.store.Log("info", "auth", u.Username, how+" from "+ip)
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, fl.next, http.StatusSeeOther)
	}
}

// oidcVerify trades the code for tokens and returns the verified claims of
// the ID token, completed by userinfo for the claims it lacks.
func (s *Server) oidcVerify(ctx context.Context, p store.OIDCProvider, fl *oidcFlow, code string) (oidc.Claims, error) {
	if code == "" || len(code) > 4096 {
		return nil, errors.New("no authorization code")
	}
	d, ks, err := oidcClient.Provider(ctx, p.Issuer, p.AllowHTTP, false)
	if err != nil {
		return nil, err
	}
	tok, err := oidcClient.Exchange(ctx, d, p.ClientID, p.ClientSecret, code, fl.redirect, fl.verifier)
	if err != nil {
		return nil, err
	}
	exp := oidc.Expect{Issuer: d.Issuer, ClientID: p.ClientID, Nonce: fl.nonce}
	claims, err := oidc.VerifyIDToken(tok.IDToken, ks, exp)
	if errors.Is(err, oidc.ErrNoKey) {
		// Keys rotated since they were fetched.
		if _, ks, err = oidcClient.Provider(ctx, p.Issuer, p.AllowHTTP, true); err == nil {
			claims, err = oidc.VerifyIDToken(tok.IDToken, ks, exp)
		}
	}
	if err != nil {
		return nil, err
	}
	if info, err := oidcClient.Userinfo(ctx, d, tok.AccessToken, claims.String("sub")); err == nil {
		for k, v := range info {
			if _, ok := claims[k]; !ok {
				claims[k] = v
			}
		}
	}
	return claims, nil
}

// oidcLink ties an identity to the signed-in account that asked for it.
func (s *Server) oidcLink(fl *oidcFlow, p store.OIDCProvider, id oidc.Identity) error {
	if !s.sessionAlive(fl.linkSess, fl.linkUser) {
		return errors.New("your session ended, sign in again to link the account")
	}
	return s.store.Update(func(st *store.State) error {
		for _, o := range st.Users {
			for _, l := range o.OIDC {
				if l.Issuer == p.Issuer && l.Subject == id.Subject {
					if o.Username == fl.linkUser {
						return nil
					}
					return errors.New("this " + p.Name + " account is already linked to another StrataOS account")
				}
			}
		}
		u := st.User(fl.linkUser)
		if u == nil {
			return errors.New("user not found")
		}
		u.OIDC = slices.DeleteFunc(u.OIDC, func(l store.OIDCLink) bool { return l.Provider == p.ID })
		u.OIDC = append(u.OIDC, store.OIDCLink{Provider: p.ID, Issuer: p.Issuer, Subject: id.Subject, Linked: time.Now()})
		return nil
	})
}

func (s *Server) sessionAlive(k, user string) bool {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	ss, ok := s.sessions.m[k]
	return ok && ss.User == user && time.Now().Before(ss.Expires)
}

// oidcAccount finds (or creates) the account of an identity and keeps the
// role and groups of the accounts the provider created in sync.
func (s *Server) oidcAccount(p store.OIDCProvider, id oidc.Identity) (store.User, error) {
	var u store.User
	var found bool
	s.store.Read(func(st *store.State) {
		for _, x := range st.Users {
			for _, l := range x.OIDC {
				if l.Provider == p.ID && l.Issuer == p.Issuer && l.Subject == id.Subject {
					u, found = copyUser(x), true
				}
			}
		}
	})
	if !found {
		if !p.AutoCreate {
			return store.User{}, errors.New("no StrataOS account is linked to this " + p.Name + " account: sign in with your password and link it from your profile, or ask an administrator")
		}
		var err error
		if u, err = s.oidcCreate(p, id); err != nil {
			return store.User{}, err
		}
	}
	if u.Disabled {
		return store.User{}, errors.New("this account is disabled")
	}
	created := slices.ContainsFunc(u.OIDC, func(l store.OIDCLink) bool { return l.Provider == p.ID && l.Created })
	if created && len(p.GroupMap) > 0 {
		s.syncMappedGroups(u.Username, p, id.Mapped)
	}
	var demoteRefused bool
	err := s.store.Update(func(st *store.State) error {
		x := st.User(u.Username)
		if x == nil {
			return errors.New("user not found")
		}
		if created && len(p.AdminGroups) > 0 {
			role := "user"
			if id.Admin {
				role = "admin"
			}
			if role != x.Role {
				if role == "user" && x.Role == "admin" && enabledAdmins(st) < 2 {
					demoteRefused = true // never leave the NAS without an administrator
				} else {
					s.store.LogLocked(st, "info", "users", x.Username, fmt.Sprintf("Role %s given by %s", role, p.Name))
					x.Role = role
					s.sessions.dropUser(x.Username)
				}
			}
		}
		if id.Email != "" && x.Email == "" {
			x.Email = id.Email
		}
		for i := range x.OIDC {
			if x.OIDC[i].Provider == p.ID {
				x.OIDC[i].LastUsed = time.Now()
			}
		}
		u = copyUser(x)
		return nil
	})
	if demoteRefused {
		slog.Warn("oidc: last administrator kept", "user", u.Username)
	}
	return u, err
}

// oidcCreate makes the account of a new identity: a system account (files
// belong to it) without password; the user may set one from their profile.
func (s *Server) oidcCreate(p store.OIDCProvider, id oidc.Identity) (store.User, error) {
	name := id.Username
	if err := accounts.ValidName(name); err != nil {
		return store.User{}, fmt.Errorf("the user name %q cannot be used on the NAS", name)
	}
	var exists bool
	s.store.Read(func(st *store.State) { exists = st.User(name) != nil })
	if exists || accounts.LinuxUserExists(name) {
		return store.User{}, fmt.Errorf("an account named %s already exists: sign in with its password and link it from your profile", name)
	}
	var groups []string
	for _, g := range id.Mapped {
		if accounts.ValidMemberGroup(g) == nil {
			groups = append(groups, g)
		}
	}
	// A long random system password nobody knows (Samba refuses empty ones).
	if err := accounts.Create(name, oidcText(id.Name, 64), token()+"Aa1", groups, false); err != nil {
		return store.User{}, err
	}
	role := "user"
	if id.Admin {
		role = "admin"
	}
	u := &store.User{Username: name, FullName: oidcText(id.Name, 64), Email: oidcText(id.Email, 128), Role: role, CreatedAt: time.Now(),
		OIDC: []store.OIDCLink{{Provider: p.ID, Issuer: p.Issuer, Subject: id.Subject, Created: true, Linked: time.Now()}}}
	err := s.store.Update(func(st *store.State) error {
		if st.User(name) != nil {
			return fmt.Errorf("an account named %s already exists", name)
		}
		st.Users = append(st.Users, u)
		s.store.LogLocked(st, "info", "users", name, "Account created at the first sign-in with "+p.Name)
		return nil
	})
	if err != nil {
		return store.User{}, err
	}
	return copyUser(u), nil
}

// syncMappedGroups puts the account in the StrataOS groups its provider
// groups map to, and out of the mapped ones it lost.
func (s *Server) syncMappedGroups(name string, p store.OIDCProvider, want []string) {
	managed := map[string]bool{}
	for _, g := range p.GroupMap {
		managed[g] = true
	}
	var next []string
	changed := false
	for _, g := range accounts.UserGroups(name) {
		if g == accounts.BaseGroup {
			continue
		}
		if managed[g] && !slices.Contains(want, g) {
			changed = true
			continue
		}
		next = append(next, g)
	}
	for _, g := range want {
		if !slices.Contains(next, g) && accounts.ValidMemberGroup(g) == nil {
			next = append(next, g)
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := accounts.SetUserGroups(name, next); err != nil {
		slog.Warn("oidc groups", "user", name, "err", err)
		return
	}
	go s.applyShares()
}

// oidcText keeps printable characters of an untrusted text.
func oidcText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}
