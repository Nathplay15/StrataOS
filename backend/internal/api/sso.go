package api

// Single sign-on for the apps: the NAS is an OpenID Connect provider
// (internal/sso holds the protocol). Browsers reach the authorization and
// logout endpoints from another site, where the Strict session cookie of
// the NAS is not sent: those endpoints only validate the request and send
// the browser to a page of the interface (/sso/...), whose own requests
// carry the session. The page shows the consent and posts the answer; a
// cookie binds each request to the browser that started it.

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/gate"
	"strataos/internal/sso"
	"strataos/internal/store"
)

const (
	ssoBindCookie = "strata_ssob"
	ssoBindPath   = "/api/sso/"
)

var (
	// ssoReads: discovery, keys, userinfo; ssoAuth: authorize, token,
	// logout, revoke (per client address).
	ssoReads = &gate.RateLimiter{Burst: 120, Every: 250 * time.Millisecond}
	ssoAuth  = &gate.RateLimiter{Burst: 60, Every: time.Second}
)

var errSSOOff = &Error{Status: http.StatusNotFound, Msg: "single sign-on is not enabled"}

// ssoConfig returns whether SSO is on and the issuer.
func (s *Server) ssoConfig() (bool, string) {
	var on bool
	var iss string
	s.store.Read(func(st *store.State) { on, iss = st.SSO.Enabled && st.SSO.Issuer != "", st.SSO.Issuer })
	return on, iss
}

// ssoFind is the protocol view of an enabled client (nil when SSO is off,
// the client unknown or disabled).
func (s *Server) ssoFind(id string) *sso.Client {
	var c *sso.Client
	s.store.Read(func(st *store.State) {
		if !st.SSO.Enabled {
			return
		}
		if x := st.SSOClient(id); x != nil && !x.Disabled {
			c = protoClient(x)
		}
	})
	return c
}

func protoClient(x *store.SSOClient) *sso.Client {
	c := &sso.Client{ID: x.ID, RedirectURIs: slices.Clone(x.RedirectURIs), PostLogoutURIs: slices.Clone(x.PostLogoutURIs), RequirePKCE: x.RequirePKCE || x.Kind == "forward"}
	if !x.Public {
		c.Secret = x.Secret
		if c.Secret == "" {
			c.Secret = "\x00" // never matches: a confidential client without secret is unusable
		}
	}
	return c
}

// ---- identities ----

type ssoIdent struct {
	id  *sso.Identity
	err error
	at  time.Time
}

// ssoCache keeps identities for a few seconds: forward authentication asks
// at every request of a protected app, and group lookups read /etc/group
// (or the directory).
var ssoCache = struct {
	sync.Mutex
	m map[[2]string]ssoIdent
}{m: map[[2]string]ssoIdent{}}

const ssoCacheTTL = 20 * time.Second

func ssoForget() {
	ssoCache.Lock()
	clear(ssoCache.m)
	ssoCache.Unlock()
}

// ssoUserGroups: the groups of a user (tests replace it).
var ssoUserGroups = accounts.UserGroups

var errSSOAccount = errors.New("this account is disabled or no longer exists")

// ssoAllowed applies the access rules of a client.
func ssoAllowed(c *store.SSOClient, user string, groups []string) bool {
	if c.AllUsers || slices.Contains(c.Users, user) {
		return true
	}
	for _, g := range groups {
		if slices.Contains(c.Groups, g) {
			return true
		}
	}
	return false
}

// ssoLookup is sso.Provider.Lookup: the identity of a user for a client, or
// why they may not use it.
func (s *Server) ssoLookup(user, client string) (*sso.Identity, error) {
	k := [2]string{user, client}
	ssoCache.Lock()
	if e, ok := ssoCache.m[k]; ok && time.Since(e.at) < ssoCacheTTL {
		ssoCache.Unlock()
		return e.id, e.err
	}
	ssoCache.Unlock()
	id, err := s.ssoLookupNow(user, client)
	ssoCache.Lock()
	if len(ssoCache.m) > 10000 {
		clear(ssoCache.m)
	}
	ssoCache.m[k] = ssoIdent{id: id, err: err, at: time.Now()}
	ssoCache.Unlock()
	return id, err
}

func (s *Server) ssoLookupNow(user, client string) (*sso.Identity, error) {
	var u store.User
	var c store.SSOClient
	var found, on bool
	s.store.Read(func(st *store.State) {
		on = st.SSO.Enabled
		if x := st.User(user); x != nil {
			u, found = *x, true
		}
		if x := st.SSOClient(client); x != nil {
			c = *x
		}
	})
	if !on || c.ID == "" || c.Disabled {
		return nil, errors.New("this application may no longer sign in with the NAS")
	}
	if !found || u.Disabled {
		return nil, errSSOAccount
	}
	groups := ssoUserGroups(user)
	if !ssoAllowed(&c, user, groups) {
		return nil, fmt.Errorf("your account may not use %s: ask an administrator", c.Name)
	}
	if u.SSOSubject == "" {
		sub := sso.Random(16)
		_ = s.store.Update(func(st *store.State) error {
			if x := st.User(user); x != nil {
				if x.SSOSubject == "" {
					x.SSOSubject = sub
				}
				sub = x.SSOSubject
			}
			return nil
		})
		u.SSOSubject = sub
	}
	return &sso.Identity{Subject: u.SSOSubject, Username: u.Username, Name: u.FullName, Email: u.Email, Groups: groups}, nil
}

// ---- sessions of the interface ----

// currentSession returns the valid session of the request's cookie.
func (s *Server) currentSession(r *http.Request) (*session, string) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, ""
	}
	ss := s.sessions.get(c.Value)
	if ss == nil {
		return nil, ""
	}
	ok := false
	s.store.Read(func(st *store.State) {
		u := st.User(ss.User)
		ok = u != nil && !u.Disabled && (ss.Device == "" || u.MobileDevice(ss.Device) != nil)
	})
	if !ok {
		return nil, ""
	}
	return ss, c.Value
}

// ssoSid is the "sid" of a session of the interface: tokens and
// forward-auth sessions end with it (never the session token itself).
func ssoSid(cookieValue string) string {
	if cookieValue == "" {
		return ""
	}
	return sso.Hash("sid:" + key(cookieValue))[:32]
}

// ssoEndSession follows the logout of a session of the interface.
func (s *Server) ssoEndSession(cookieValue string) {
	if s.sso != nil {
		s.sso.EndSession(ssoSid(cookieValue))
	}
}

// ssoBinding returns the browser binding (hash of its cookie), making the
// cookie when needed.
func ssoBinding(w http.ResponseWriter, r *http.Request) string {
	v := ""
	if c, err := r.Cookie(ssoBindCookie); err == nil && len(c.Value) == 64 {
		v = c.Value
	} else {
		v = token()
	}
	http.SetCookie(w, &http.Cookie{Name: ssoBindCookie, Value: v, Path: ssoBindPath, HttpOnly: true, Secure: r.TLS != nil,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sso.RequestTTL.Seconds())})
	return key(v)
}

// ssoBound reports whether the request comes from the browser bound to b.
func ssoBound(r *http.Request, b string) bool {
	c, err := r.Cookie(ssoBindCookie)
	return err == nil && b != "" && subtle.ConstantTimeCompare([]byte(key(c.Value)), []byte(b)) == 1
}

// ---- small answers ----

func ssoPage(w http.ResponseWriter, status int, title, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>%s</title><body style="font-family:system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#333"><h1 style="font-size:1.4rem">%s</h1><p>%s</p><p style="color:#888;font-size:.85rem">StrataOS</p></body>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(text))
}

func ssoCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
}

func ssoTokenErr(w http.ResponseWriter, te *sso.TokenError) {
	if te.Status == http.StatusUnauthorized {
		if te.Code == "invalid_token" {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		} else {
			w.Header().Set("WWW-Authenticate", `Basic realm="StrataOS"`)
		}
	}
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, te.Status, map[string]string{"error": te.Code, "error_description": te.Desc})
}

// ssoClientName is the name shown for a client.
func (s *Server) ssoClientName(id string) string {
	name := id
	s.store.Read(func(st *store.State) {
		if c := st.SSOClient(id); c != nil && c.Name != "" {
			name = c.Name
		}
	})
	return name
}

// ssoRoutes: protocol endpoints (public), the pages' requests (session),
// administration (sso_admin.go) and forward authentication (sso_forward.go).
func (s *Server) ssoRoutes() {
	p, err := sso.Open(s.dataDir + "/sso")
	if err != nil {
		slog.Error("single sign-on", "err", err)
		return
	}
	p.Lookup = s.ssoLookup
	s.sso = p
	s.sessions.onDrop = func(user string) {
		ssoForget()
		s.sso.RevokeUser(user, false)
	}
	go s.ssoLoop()

	s.mux.Handle("GET /.well-known/openid-configuration", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ssoCORS(w)
		on, iss := s.ssoConfig()
		if !on {
			writeErr(w, errSSOOff)
			return
		}
		if !ssoReads.Allow(clientIP(r), 1) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many requests"})
			return
		}
		if hostOnlyName(r.Host) == ssoInternalHost {
			// Apps in containers (sso_apps.go): calls between them and the
			// NAS stay on the machine.
			writeJSON(w, http.StatusOK, sso.DiscoveryFor(iss, s.ssoInternal()))
			return
		}
		writeJSON(w, http.StatusOK, sso.Discovery(iss))
	}))
	s.mux.Handle("GET /api/sso/jwks", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ssoCORS(w)
		if on, _ := s.ssoConfig(); !on {
			writeErr(w, errSSOOff)
			return
		}
		if !ssoReads.Allow(clientIP(r), 1) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many requests"})
			return
		}
		w.Header().Set("Content-Type", "application/jwk-set+json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(s.sso.JWKS())
	}))

	authorize := func(w http.ResponseWriter, r *http.Request) {
		if on, _ := s.ssoConfig(); !on {
			ssoPage(w, http.StatusNotFound, "Sign-in unavailable", "Single sign-on is not enabled on this NAS.")
			return
		}
		if !ssoAuth.Allow(clientIP(r), 1) {
			ssoPage(w, http.StatusTooManyRequests, "Too many requests", "Try again in a few minutes.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		q := r.URL.Query()
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				ssoPage(w, http.StatusBadRequest, "Sign-in refused", "Invalid request.")
				return
			}
			q = r.PostForm
		}
		req, err := sso.ParseAuthorize(q, s.ssoFind, time.Now())
		var de *sso.DirectError
		var ae *sso.AuthError
		switch {
		case errors.As(err, &de):
			ssoPage(w, http.StatusBadRequest, "Sign-in refused", de.Msg)
			return
		case errors.As(err, &ae):
			http.Redirect(w, r, ae.URL(), http.StatusFound)
			return
		case err != nil:
			writeErr(w, err)
			return
		}
		req.Binding = ssoBinding(w, r)
		if err := s.sso.PutRequest(req); err != nil {
			ssoPage(w, http.StatusTooManyRequests, "Sign-in unavailable", err.Error())
			return
		}
		http.Redirect(w, r, "/sso/r/"+req.ID, http.StatusFound)
	}
	s.mux.Handle("GET /api/sso/authorize", http.HandlerFunc(authorize))
	s.mux.Handle("POST /api/sso/authorize", http.HandlerFunc(authorize))

	// What the page shows: works without a session (the page then signs in
	// first), answers prompt=none at once.
	s.mux.Handle("GET /api/sso/requests/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ssoRequestView(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}))
	s.user("POST /api/sso/requests/{id}", func(r *http.Request) (any, error) {
		var body struct {
			Approve bool `json:"approve"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		return s.ssoAnswer(r, body.Approve)
	})

	token := func(w http.ResponseWriter, r *http.Request) {
		ssoCORS(w)
		w.Header().Set("Pragma", "no-cache")
		if on, _ := s.ssoConfig(); !on {
			writeErr(w, errSSOOff)
			return
		}
		ip := clientIP(r)
		if !ssoAuth.Allow(ip, 1) {
			ssoTokenErr(w, &sso.TokenError{Status: http.StatusTooManyRequests, Code: "slow_down", Desc: "too many requests"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		if err := r.ParseForm(); err != nil {
			ssoTokenErr(w, &sso.TokenError{Status: http.StatusBadRequest, Code: "invalid_request", Desc: "invalid form"})
			return
		}
		for k, v := range r.PostForm {
			if len(v) > 1 {
				ssoTokenErr(w, &sso.TokenError{Status: http.StatusBadRequest, Code: "invalid_request", Desc: "repeated parameter " + k})
				return
			}
		}
		c, te := sso.AuthenticateClient(r, r.PostForm, s.ssoFind)
		if te != nil {
			s.store.Log("warning", "auth", "", "Single sign-on: failed client authentication from "+ip)
			ssoAuth.Allow(ip, 5) // failures cost more
			ssoTokenErr(w, te)
			return
		}
		if strings.HasPrefix(c.ID, "fwd-") {
			// Forward-auth clients are internal: their codes are redeemed
			// by the proxy only.
			ssoTokenErr(w, &sso.TokenError{Status: http.StatusBadRequest, Code: "unauthorized_client", Desc: "internal client"})
			return
		}
		_, iss := s.ssoConfig()
		resp, te := s.sso.Token(r.PostForm, c, iss)
		if te != nil {
			ssoTokenErr(w, te)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
	s.mux.Handle("POST /api/sso/token", http.HandlerFunc(token))

	userinfo := func(w http.ResponseWriter, r *http.Request) {
		ssoCORS(w)
		if on, _ := s.ssoConfig(); !on {
			writeErr(w, errSSOOff)
			return
		}
		if !ssoReads.Allow(clientIP(r), 1) {
			ssoTokenErr(w, &sso.TokenError{Status: http.StatusTooManyRequests, Code: "slow_down", Desc: "too many requests"})
			return
		}
		at, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			at, ok = strings.CutPrefix(r.Header.Get("Authorization"), "bearer ")
		}
		if !ok || strings.TrimSpace(at) == "" {
			ssoTokenErr(w, &sso.TokenError{Status: http.StatusUnauthorized, Code: "invalid_token", Desc: "bearer token required"})
			return
		}
		claims, te := s.sso.UserInfo(strings.TrimSpace(at))
		if te != nil {
			ssoTokenErr(w, te)
			return
		}
		writeJSON(w, http.StatusOK, claims)
	}
	s.mux.Handle("GET /api/sso/userinfo", http.HandlerFunc(userinfo))
	s.mux.Handle("POST /api/sso/userinfo", http.HandlerFunc(userinfo))

	s.mux.Handle("POST /api/sso/revoke", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ssoCORS(w)
		if on, _ := s.ssoConfig(); !on {
			writeErr(w, errSSOOff)
			return
		}
		ip := clientIP(r)
		if !ssoAuth.Allow(ip, 1) {
			ssoTokenErr(w, &sso.TokenError{Status: http.StatusTooManyRequests, Code: "slow_down", Desc: "too many requests"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		if err := r.ParseForm(); err != nil {
			ssoTokenErr(w, &sso.TokenError{Status: http.StatusBadRequest, Code: "invalid_request", Desc: "invalid form"})
			return
		}
		c, te := sso.AuthenticateClient(r, r.PostForm, s.ssoFind)
		if te != nil {
			ssoAuth.Allow(ip, 5)
			ssoTokenErr(w, te)
			return
		}
		s.sso.Revoke(r.PostForm.Get("token"), c)
		w.WriteHeader(http.StatusOK)
	}))
	for _, p := range []string{"/api/sso/token", "/api/sso/userinfo", "/api/sso/revoke", "/api/sso/jwks", "/.well-known/openid-configuration"} {
		s.mux.Handle("OPTIONS "+p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ssoCORS(w)
			w.WriteHeader(http.StatusNoContent)
		}))
	}

	// RP-initiated logout (OpenID Connect RP-Initiated Logout 1.0).
	s.mux.Handle("GET /api/sso/logout", http.HandlerFunc(s.ssoLogoutStart))
	s.mux.Handle("POST /api/sso/logout", http.HandlerFunc(s.ssoLogoutStart))
	s.mux.Handle("GET /api/sso/logouts/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lr := s.sso.Logout(r.PathValue("id"), false)
		if lr == nil {
			writeErr(w, notFound("this sign-out request expired"))
			return
		}
		ss, _ := s.currentSession(r)
		v := map[string]any{"signedIn": ss != nil, "redirect": lr.Redirect != ""}
		if lr.Client != "" {
			v["client"] = s.ssoClientName(lr.Client)
		}
		if ss != nil {
			v["user"] = ss.User
			v["sameUser"] = lr.User != "" && lr.User == ss.User
		}
		writeJSON(w, http.StatusOK, v)
	}))
	s.mux.Handle("POST /api/sso/logouts/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "cross-origin request rejected"})
			return
		}
		lr := s.sso.Logout(r.PathValue("id"), false)
		if lr == nil {
			writeErr(w, notFound("this sign-out request expired"))
			return
		}
		// sameOrigin above: another site cannot sign the user out.
		s.sso.Logout(lr.ID, true)
		if ss, tok := s.currentSession(r); ss != nil {
			s.sessions.delete(tok)
			s.ssoEndSession(tok)
			s.setCookie(w, r, "", -time.Second)
			s.store.Log("info", "auth", ss.User, "Signed out from "+s.ssoClientName(lr.Client))
		}
		to := "/"
		if lr.Redirect != "" {
			to = lr.Redirect
			if lr.State != "" {
				to = withState(to, lr.State)
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"redirect": to})
	}))

	// The user's authorized apps.
	s.user("GET /api/me/sso", func(r *http.Request) (any, error) {
		return s.ssoAppsOf(userOf(r)), nil
	})
	s.user("DELETE /api/me/sso/{client}", func(r *http.Request) (any, error) {
		id := r.PathValue("client")
		s.sso.RevokeUserClient(userOf(r), id)
		s.audit(r, "auth", "Access of "+s.ssoClientName(id)+" revoked")
		return nil, nil
	})

	s.ssoAdminRoutes()
}

func hostOnlyName(h string) string {
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.ToLower(h)
}

func withState(u, state string) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + url.Values{"state": {state}}.Encode()
}

// ssoLoop rotates the signing keys when due.
func (s *Server) ssoLoop() {
	for {
		time.Sleep(6 * time.Hour)
		if err := s.sso.MaintainKeys(); err != nil {
			slog.Warn("sso keys", "err", err)
		}
	}
}

// ssoView is what the sign-in page shows.
type ssoView struct {
	SignedIn bool     `json:"signedIn"`
	Client   string   `json:"client"`
	ClientID string   `json:"clientId"`
	Host     string   `json:"host,omitempty"` // where the browser goes back
	User     string   `json:"user,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	Consent  bool     `json:"consent,omitempty"`
	Reauth   bool     `json:"reauth,omitempty"`
	Denied   string   `json:"denied,omitempty"`
	// Redirect: the request is answered (prompt=none): go there.
	Redirect string `json:"redirect,omitempty"`
}

// ssoCheck decides a request for a session: denied (with why), new sign-in
// needed, consent needed.
func (s *Server) ssoCheck(req *sso.AuthRequest, ss *session) (denied string, reauth, consent bool) {
	if _, err := s.ssoLookup(ss.User, req.ClientID); err != nil {
		return err.Error(), false, false
	}
	var c store.SSOClient
	var mfaOK, setup bool
	s.store.Read(func(st *store.State) {
		if x := st.SSOClient(req.ClientID); x != nil {
			c = *x
		}
		if u := st.User(ss.User); u != nil {
			mfaOK = hasSecondFactor(u) || ss.MFA != ""
			setup = setupOnly(st, u, time.Now(), ss.MFA)
		}
	})
	if setup {
		return "set up two-factor authentication on the NAS first", false, false
	}
	if c.Require2FA && !mfaOK {
		return c.Name + " requires two-factor authentication: add an authenticator app or a security key to your account first", false, false
	}
	if req.NeedsLogin(ss.Created, time.Now()) {
		return "", true, false
	}
	consent = !c.Trusted && (req.HasPrompt("consent") || !s.sso.Granted(ss.User, c.ID, req.Scopes))
	return "", false, consent
}

func (s *Server) ssoRequestView(r *http.Request) (*ssoView, error) {
	req := s.sso.Request(r.PathValue("id"))
	if req == nil {
		return nil, notFound("this sign-in request expired: go back to the app and try again")
	}
	if !ssoBound(r, req.Binding) {
		return nil, &Error{Status: http.StatusForbidden, Msg: "this sign-in was started in another browser"}
	}
	v := &ssoView{Client: s.ssoClientName(req.ClientID), ClientID: req.ClientID, Scopes: req.Scopes}
	if u, err := url.Parse(req.RedirectURI); err == nil {
		v.Host = u.Host
	}
	fail := func(code string) (*ssoView, error) {
		s.sso.TakeRequest(req.ID)
		return &ssoView{Client: v.Client, Redirect: (&sso.AuthError{Code: code, RedirectURI: req.RedirectURI, State: req.State}).URL()}, nil
	}
	ss, _ := s.currentSession(r)
	if ss == nil {
		if req.HasPrompt("none") {
			return fail("login_required")
		}
		return v, nil
	}
	v.SignedIn, v.User = true, ss.User
	v.Denied, v.Reauth, v.Consent = s.ssoCheck(req, ss)
	if req.HasPrompt("none") {
		switch {
		case v.Denied != "":
			return fail("access_denied")
		case v.Reauth:
			return fail("login_required")
		case v.Consent:
			return fail("consent_required")
		}
	}
	return v, nil
}

// ssoAnswer issues the code (or the refusal) of an approved request.
func (s *Server) ssoAnswer(r *http.Request, approve bool) (any, error) {
	id := r.PathValue("id")
	req := s.sso.Request(id)
	if req == nil {
		return nil, notFound("this sign-in request expired: go back to the app and try again")
	}
	if !ssoBound(r, req.Binding) {
		return nil, &Error{Status: http.StatusForbidden, Msg: "this sign-in was started in another browser"}
	}
	ss, tok := s.currentSession(r)
	if ss == nil {
		return nil, errUnauthorized
	}
	if req = s.sso.TakeRequest(id); req == nil {
		return nil, notFound("this sign-in request expired: go back to the app and try again")
	}
	name := s.ssoClientName(req.ClientID)
	deny := func() (any, error) {
		return map[string]string{"redirect": (&sso.AuthError{Code: "access_denied", Desc: "the user refused", RedirectURI: req.RedirectURI, State: req.State}).URL()}, nil
	}
	if !approve {
		return deny()
	}
	denied, reauth, consent := s.ssoCheck(req, ss)
	if denied != "" {
		s.store.Log("warning", "auth", ss.User, "Sign-in to "+name+" refused: "+denied)
		return deny()
	}
	if reauth {
		return nil, &Error{Status: http.StatusConflict, Msg: "sign in again first"}
	}
	if consent {
		s.store.Log("info", "auth", ss.User, "Allowed "+name+" to use the account")
	}
	s.sso.Grant(ss.User, req.ClientID, req.Scopes)
	_, iss := s.ssoConfig()
	code := s.sso.IssueCode(req, ss.User, ssoSid(tok), ss.Created)
	if !strings.HasPrefix(req.ClientID, "fwd-") {
		s.store.Log("info", "auth", ss.User, "Signed in to "+name+" from "+clientIP(r))
	}
	return map[string]string{"redirect": sso.CodeURL(req.RedirectURI, code, req.State, iss)}, nil
}

// ssoLogoutStart validates an RP-initiated logout and sends the browser to
// the page that confirms it.
func (s *Server) ssoLogoutStart(w http.ResponseWriter, r *http.Request) {
	if on, _ := s.ssoConfig(); !on {
		ssoPage(w, http.StatusNotFound, "Sign-out unavailable", "Single sign-on is not enabled on this NAS.")
		return
	}
	if !ssoAuth.Allow(clientIP(r), 1) {
		ssoPage(w, http.StatusTooManyRequests, "Too many requests", "Try again in a few minutes.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			ssoPage(w, http.StatusBadRequest, "Sign-out refused", "Invalid request.")
			return
		}
		q = r.PostForm
	}
	lr := &sso.LogoutRequest{ID: sso.Random(16), Created: time.Now(), Client: q.Get("client_id")}
	if hint := q.Get("id_token_hint"); hint != "" {
		var c struct {
			Sub string `json:"sub"`
			Aud any    `json:"aud"`
		}
		// Expired hints are fine (the point is to say who signs out).
		if err := s.sso.VerifyToken(hint, &c, false); err != nil {
			ssoPage(w, http.StatusBadRequest, "Sign-out refused", "The application sent an invalid id_token_hint.")
			return
		}
		aud, _ := c.Aud.(string)
		if lr.Client != "" && aud != lr.Client {
			ssoPage(w, http.StatusBadRequest, "Sign-out refused", "client_id does not match the id_token_hint.")
			return
		}
		lr.Client = aud
		s.store.Read(func(st *store.State) {
			for _, u := range st.Users {
				if u.SSOSubject != "" && u.SSOSubject == c.Sub {
					lr.User = u.Username
				}
			}
		})
	}
	if pl := q.Get("post_logout_redirect_uri"); pl != "" {
		c := s.ssoFind(lr.Client)
		if c == nil || !slices.Contains(c.PostLogoutURIs, pl) {
			// Never an open redirect: only an address registered for the client.
			ssoPage(w, http.StatusBadRequest, "Sign-out refused", "This return address (post_logout_redirect_uri) is not registered for the application.")
			return
		}
		lr.Redirect, lr.State = pl, q.Get("state")
	}
	if err := s.sso.PutLogout(lr); err != nil {
		ssoPage(w, http.StatusTooManyRequests, "Sign-out unavailable", err.Error())
		return
	}
	http.Redirect(w, r, "/sso/logout/"+lr.ID, http.StatusFound)
}

// ssoApp is an application the user authorized.
type ssoApp struct {
	Client   string    `json:"client"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind,omitempty"`
	Scopes   []string  `json:"scopes"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed,omitzero"`
	Sessions int       `json:"sessions"`
}

func (s *Server) ssoAppsOf(user string) []ssoApp {
	out := []ssoApp{}
	if s.sso == nil {
		return out
	}
	active := s.sso.Active()
	for _, g := range s.sso.Grants(user) {
		a := ssoApp{Client: g.Client, Name: g.Client, Scopes: g.Scopes, Created: g.Created, LastUsed: g.LastUsed, Sessions: active[[2]string{g.User, g.Client}]}
		s.store.Read(func(st *store.State) {
			if c := st.SSOClient(g.Client); c != nil {
				a.Name, a.Kind = c.Name, c.Kind
			}
		})
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b ssoApp) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

var ssoClientIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
