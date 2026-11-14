package sso

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"strataos/internal/oidc"
)

const iss = "https://nas.example.com"

type fixture struct {
	p       *Provider
	now     time.Time
	clients map[string]*Client
	denied  map[string]bool // users the Lookup refuses
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), denied: map[string]bool{}}
	p, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.Now = func() time.Time { return f.now }
	p.Lookup = func(user, client string) (*Identity, error) {
		if f.denied[user] {
			return nil, errors.New("not allowed")
		}
		return &Identity{Subject: "sub-" + user, Username: user, Name: "Alice A.", Email: user + "@example.com", Groups: []string{"family"}}, nil
	}
	f.p = p
	f.clients = map[string]*Client{
		"grafana": {ID: "grafana", Secret: "s3cret", RedirectURIs: []string{"https://grafana.example.com/login/generic_oauth"}, PostLogoutURIs: []string{"https://grafana.example.com/"}},
		"spa":     {ID: "spa", RedirectURIs: []string{"https://spa.example.com/cb"}},
		"strict":  {ID: "strict", Secret: "x", RedirectURIs: []string{"https://s.example.com/cb"}, RequirePKCE: true},
	}
	return f
}

func (f *fixture) find(id string) *Client { return f.clients[id] }

const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk0123"

func authQuery(client, redirect string, extra ...string) url.Values {
	q := url.Values{"client_id": {client}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {"openid profile email groups"}, "state": {"st"}, "nonce": {"n0nce"},
		"code_challenge": {S256(verifier)}, "code_challenge_method": {"S256"}}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] == "-" {
			q.Del(extra[i])
		} else {
			q.Set(extra[i], extra[i+1])
		}
	}
	return q
}

func TestAuthorizeRefusals(t *testing.T) {
	f := newFixture(t)
	gr := "https://grafana.example.com/login/generic_oauth"
	direct := map[string]url.Values{
		"unknown client":       authQuery("nope", gr),
		"no client":            authQuery("", gr),
		"unregistered":         authQuery("grafana", "https://evil.example.com/cb"),
		"prefix":               authQuery("grafana", gr+"/x"),
		"query added":          authQuery("grafana", gr+"?a=1"),
		"trailing slash":       authQuery("grafana", gr+"/"),
		"case":                 authQuery("grafana", strings.ToUpper(gr)),
		"no redirect":          authQuery("grafana", ""),
		"redirect of another":  authQuery("grafana", "https://spa.example.com/cb"),
		"repeated client":      {"client_id": {"grafana", "spa"}, "redirect_uri": {gr}},
		"repeated redirect_ui": {"client_id": {"grafana"}, "redirect_uri": {gr, "https://evil.example.com"}},
	}
	for name, q := range direct {
		_, err := ParseAuthorize(q, f.find, f.now)
		var de *DirectError
		if !errors.As(err, &de) {
			t.Errorf("%s: want a direct error (no redirect), got %v", name, err)
		}
	}
	redirected := map[string]struct {
		q    url.Values
		code string
	}{
		"token flow":         {authQuery("grafana", gr, "response_type", "token"), "unsupported_response_type"},
		"hybrid":             {authQuery("grafana", gr, "response_type", "code id_token"), "unsupported_response_type"},
		"no openid":          {authQuery("grafana", gr, "scope", "profile email"), "invalid_scope"},
		"plain pkce":         {authQuery("grafana", gr, "code_challenge_method", "plain"), "invalid_request"},
		"pkce default plain": {authQuery("grafana", gr, "code_challenge_method", "-"), "invalid_request"},
		"bad challenge":      {authQuery("grafana", gr, "code_challenge", "short"), "invalid_request"},
		"public no pkce":     {authQuery("spa", "https://spa.example.com/cb", "code_challenge", "-", "code_challenge_method", "-"), "invalid_request"},
		"required pkce":      {authQuery("strict", "https://s.example.com/cb", "code_challenge", "-", "code_challenge_method", "-"), "invalid_request"},
		"request object":     {authQuery("grafana", gr, "request", "eyJ..."), "request_not_supported"},
		"form_post":          {authQuery("grafana", gr, "response_mode", "form_post"), "invalid_request"},
		"prompt none+login":  {authQuery("grafana", gr, "prompt", "none login"), "invalid_request"},
		"bad max_age":        {authQuery("grafana", gr, "max_age", "-3"), "invalid_request"},
	}
	for name, c := range redirected {
		_, err := ParseAuthorize(c.q, f.find, f.now)
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Code != c.code {
			t.Errorf("%s: want %s, got %v", name, c.code, err)
			continue
		}
		u, _ := url.Parse(ae.URL())
		if !strings.HasPrefix(ae.URL(), gr) && !strings.HasPrefix(ae.URL(), "https://spa.example.com/cb") && !strings.HasPrefix(ae.URL(), "https://s.example.com/cb") {
			t.Errorf("%s: error sent to %s", name, ae.URL())
		}
		if u.Query().Get("state") != "st" {
			t.Errorf("%s: state lost", name)
		}
	}
	// A confidential client may skip PKCE unless required.
	if _, err := ParseAuthorize(authQuery("grafana", gr, "code_challenge", "-", "code_challenge_method", "-"), f.find, f.now); err != nil {
		t.Fatal(err)
	}
	r, err := ParseAuthorize(authQuery("grafana", gr, "scope", "openid email unknown openid"), f.find, f.now)
	if err != nil || len(r.Scopes) != 2 {
		t.Fatalf("scopes: %v %v", r, err)
	}
}

func jwks(t *testing.T, p *Provider) oidc.KeySet {
	ks, err := oidc.ParseJWKS(p.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

// login runs authorize → code → token for user and returns the answer.
func (f *fixture) login(t *testing.T, client, redirect, user string, extra ...string) *TokenResponse {
	t.Helper()
	req, err := ParseAuthorize(authQuery(client, redirect, extra...), f.find, f.now)
	if err != nil {
		t.Fatal(err)
	}
	code := f.p.IssueCode(req, user, "sid1", f.now.Add(-time.Minute))
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
	tr, te := f.p.Token(form, f.clients[client], iss)
	if te != nil {
		t.Fatal(te)
	}
	return tr
}

func TestCodeFlow(t *testing.T) {
	f := newFixture(t)
	gr := "https://grafana.example.com/login/generic_oauth"
	tr := f.login(t, "grafana", gr, "alice")
	c, err := oidc.VerifyIDToken(tr.IDToken, jwks(t, f.p), oidc.Expect{Issuer: iss, ClientID: "grafana", Nonce: "n0nce", Now: f.now})
	if err != nil {
		t.Fatal(err)
	}
	if c.String("sub") != "sub-alice" || c.String("preferred_username") != "alice" || c.String("email") != "alice@example.com" ||
		len(c.Strings("groups")) != 1 || c.String("sid") != "sid1" || c.String("at_hash") != halfHash(tr.AccessToken) {
		t.Fatalf("claims: %v", c)
	}
	if c.Bool("email_verified") {
		t.Fatal("emails are not verified")
	}
	// Our own verifier agrees; a wrong nonce or audience is refused by the RP side.
	if _, err := oidc.VerifyIDToken(tr.IDToken, jwks(t, f.p), oidc.Expect{Issuer: iss, ClientID: "other", Nonce: "n0nce", Now: f.now}); err == nil {
		t.Fatal("audience not checked")
	}
	info, te := f.p.UserInfo(tr.AccessToken)
	if te != nil || info["sub"] != "sub-alice" || info["name"] != "Alice A." {
		t.Fatalf("userinfo: %v %v", info, te)
	}
	// Scopes limit the claims.
	tr2 := f.login(t, "grafana", gr, "bob", "scope", "openid")
	info, _ = f.p.UserInfo(tr2.AccessToken)
	if len(info) != 1 {
		t.Fatalf("openid only: %v", info)
	}
	// Expired access token.
	f.now = f.now.Add(AccessTTL + time.Second)
	if _, te := f.p.UserInfo(tr.AccessToken); te == nil || te.Status != 401 {
		t.Fatal("expired access token accepted")
	}
	if _, te := f.p.UserInfo("nonsense"); te == nil {
		t.Fatal("unknown access token accepted")
	}
}

func TestCodeRefusals(t *testing.T) {
	f := newFixture(t)
	gr := "https://grafana.example.com/login/generic_oauth"
	issue := func(extra ...string) string {
		req, err := ParseAuthorize(authQuery("grafana", gr, extra...), f.find, f.now)
		if err != nil {
			t.Fatal(err)
		}
		return f.p.IssueCode(req, "alice", "sid1", f.now)
	}
	tok := func(code, redirect, ver, client string) *TokenError {
		_, te := f.p.Token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {ver}}, f.clients[client], iss)
		return te
	}
	cases := map[string]func() *TokenError{
		"wrong verifier":    func() *TokenError { return tok(issue(), gr, strings.Repeat("a", 43), "grafana") },
		"no verifier":       func() *TokenError { return tok(issue(), gr, "", "grafana") },
		"short verifier":    func() *TokenError { return tok(issue(), gr, "abc", "grafana") },
		"verifier no chall": func() *TokenError { return tok(issue("code_challenge", "-", "code_challenge_method", "-"), gr, verifier, "grafana") },
		"wrong redirect":    func() *TokenError { return tok(issue(), gr+"/", verifier, "grafana") },
		"other client":      func() *TokenError { return tok(issue(), gr, verifier, "spa") },
		"unknown code":      func() *TokenError { return tok("abc", gr, verifier, "grafana") },
		"empty code":        func() *TokenError { return tok("", gr, verifier, "grafana") },
		"expired": func() *TokenError {
			c := issue()
			f.now = f.now.Add(CodeTTL + time.Second)
			return tok(c, gr, verifier, "grafana")
		},
	}
	for name, fn := range cases {
		if te := fn(); te == nil || te.Code != "invalid_grant" {
			t.Errorf("%s: want invalid_grant, got %v", name, te)
		}
	}
	// A failed attempt still consumes the code.
	c := issue()
	_ = tok(c, gr, "", "grafana")
	if te := tok(c, gr, verifier, "grafana"); te == nil {
		t.Fatal("code usable after a failed attempt")
	}
	// Reuse: the second exchange fails and revokes the first one's tokens.
	c = issue()
	_, te := f.p.Token(url.Values{"grant_type": {"authorization_code"}, "code": {c}, "redirect_uri": {gr}, "code_verifier": {verifier}}, f.clients["grafana"], iss)
	if te != nil {
		t.Fatal(te)
	}
	tr, _ := f.p.Token(url.Values{"grant_type": {"authorization_code"}, "code": {c}, "redirect_uri": {gr}, "code_verifier": {verifier}}, f.clients["grafana"], iss)
	if tr != nil {
		t.Fatal("code used twice")
	}
	f.p.mu.Lock()
	n := len(f.p.d.Access) + len(f.p.d.Refresh)
	f.p.mu.Unlock()
	if n != 0 {
		t.Fatalf("tokens of a reused code kept: %d", n)
	}
	// User disabled or removed from the group between approval and exchange.
	c = issue()
	f.denied["alice"] = true
	if te := tok(c, gr, verifier, "grafana"); te == nil {
		t.Fatal("denied user got tokens")
	}
	// Grants other than the two.
	if _, te := f.p.Token(url.Values{"grant_type": {"password"}}, f.clients["grafana"], iss); te == nil || te.Code != "unsupported_grant_type" {
		t.Fatal(te)
	}
}

func TestRefresh(t *testing.T) {
	f := newFixture(t)
	gr := "https://grafana.example.com/login/generic_oauth"
	tr := f.login(t, "grafana", gr, "alice", "scope", "openid profile offline_access")
	ref := func(rt, client string, extra ...string) (*TokenResponse, *TokenError) {
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}}
		for i := 0; i+1 < len(extra); i += 2 {
			form.Set(extra[i], extra[i+1])
		}
		return f.p.Token(form, f.clients[client], iss)
	}
	if _, te := ref(tr.RefreshToken, "spa"); te == nil {
		t.Fatal("refresh token of another client accepted")
	}
	if _, te := ref(tr.RefreshToken, "grafana", "scope", "openid email"); te == nil || te.Code != "invalid_scope" {
		t.Fatal("scope widened")
	}
	tr2, te := ref(tr.RefreshToken, "grafana")
	if te != nil || tr2.RefreshToken == tr.RefreshToken {
		t.Fatal("refresh failed or not rotated", te)
	}
	if _, err := oidc.VerifyIDToken(tr2.IDToken, jwks(t, f.p), oidc.Expect{Issuer: iss, ClientID: "grafana", Nonce: "n0nce", Now: f.now}); err != nil {
		t.Fatal(err)
	}
	// Replaying the old one revokes the whole family, the new one included.
	if _, te := ref(tr.RefreshToken, "grafana"); te == nil {
		t.Fatal("refresh token reused")
	}
	if _, te := ref(tr2.RefreshToken, "grafana"); te == nil {
		t.Fatal("family survived a replay")
	}
	if _, te := f.p.UserInfo(tr2.AccessToken); te == nil {
		t.Fatal("access token survived a replay")
	}
	// Expired refresh token, disabled user.
	tr = f.login(t, "grafana", gr, "alice")
	f.denied["alice"] = true
	if _, te := ref(tr.RefreshToken, "grafana"); te == nil {
		t.Fatal("disabled user refreshed")
	}
	delete(f.denied, "alice")
	tr = f.login(t, "grafana", gr, "alice")
	f.now = f.now.Add(RefreshTTL + time.Hour)
	if _, te := ref(tr.RefreshToken, "grafana"); te == nil {
		t.Fatal("expired refresh token")
	}
}

func TestClientAuth(t *testing.T) {
	f := newFixture(t)
	req := func(basicID, basicSecret string, form url.Values) (*Client, *TokenError) {
		r := httptest.NewRequest("POST", "/api/sso/token", nil)
		if basicID != "" {
			r.SetBasicAuth(url.QueryEscape(basicID), url.QueryEscape(basicSecret))
		}
		return AuthenticateClient(r, form, f.find)
	}
	if c, te := req("grafana", "s3cret", url.Values{}); te != nil || c.ID != "grafana" {
		t.Fatal("basic", te)
	}
	if c, te := req("", "", url.Values{"client_id": {"grafana"}, "client_secret": {"s3cret"}}); te != nil || c.ID != "grafana" {
		t.Fatal("post", te)
	}
	if c, te := req("", "", url.Values{"client_id": {"spa"}}); te != nil || c.ID != "spa" {
		t.Fatal("public", te)
	}
	bad := map[string][3]any{
		"wrong secret":     {"grafana", "nope", url.Values{}},
		"no secret":        {"", "", url.Values{"client_id": {"grafana"}}},
		"unknown":          {"who", "x", url.Values{}},
		"two methods":      {"grafana", "s3cret", url.Values{"client_secret": {"s3cret"}}},
		"public w/ secret": {"", "", url.Values{"client_id": {"spa"}, "client_secret": {"x"}}},
		"nothing":          {"", "", url.Values{}},
	}
	for name, b := range bad {
		if _, te := req(b[0].(string), b[1].(string), b[2].(url.Values)); te == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestKeyRotation(t *testing.T) {
	f := newFixture(t)
	gr := "https://grafana.example.com/login/generic_oauth"
	info := f.p.KeyInfo()
	if len(info) != 2 || info[0].State != "next" || info[1].State != "active" {
		t.Fatalf("keys: %+v", info)
	}
	next := info[0].ID
	old := f.login(t, "grafana", gr, "alice")
	// The JWKS cached by a client before the rotation already has the new key.
	cached := jwks(t, f.p)
	if err := f.p.RotateKeys(); err != nil {
		t.Fatal(err)
	}
	tr := f.login(t, "grafana", gr, "alice")
	if _, err := oidc.VerifyIDToken(tr.IDToken, cached, oidc.Expect{Issuer: iss, ClientID: "grafana", Nonce: "n0nce", Now: f.now}); err != nil {
		t.Fatal("new key not pre-published:", err)
	}
	if !strings.Contains(string(f.p.JWKS()), next) {
		t.Fatal("next key missing")
	}
	if _, err := oidc.VerifyIDToken(old.IDToken, jwks(t, f.p), oidc.Expect{Issuer: iss, ClientID: "grafana", Nonce: "n0nce", Now: f.now}); err != nil {
		t.Fatal("token of the retired key refused:", err)
	}
	// Retired keys go after KeepRetired; rotation happens on schedule.
	f.now = f.now.Add(KeepRetired + time.Hour)
	if err := f.p.MaintainKeys(); err != nil {
		t.Fatal(err)
	}
	if len(f.p.KeyInfo()) != 2 {
		t.Fatalf("retired key kept: %+v", f.p.KeyInfo())
	}
	f.now = f.now.Add(RotateEvery)
	_ = f.p.MaintainKeys()
	ki := f.p.KeyInfo()
	if len(ki) != 3 || ki[0].State != "next" {
		t.Fatalf("scheduled rotation: %+v", ki)
	}
	// Keys survive a restart.
	p2, err := Open(f.p.dir)
	if err != nil || string(p2.JWKS()) != string(f.p.JWKS()) {
		t.Fatal("keys not persisted", err)
	}
	// A token signed elsewhere, tampered or with alg none is refused.
	tr = f.login(t, "grafana", gr, "alice")
	parts := strings.Split(tr.IDToken, ".")
	for _, bad := range []string{parts[0] + "." + parts[1] + ".", "eyJhbGciOiJub25lIn0." + parts[1] + ".", parts[0] + ".e30." + parts[2]} {
		if f.p.VerifyToken(bad, nil, false) == nil {
			t.Fatal("bad token verified")
		}
	}
	if f.p.VerifyToken(tr.IDToken, nil, true) != nil {
		t.Fatal("good token refused")
	}
	f.now = f.now.Add(2 * time.Hour)
	if f.p.VerifyToken(tr.IDToken, nil, true) == nil || f.p.VerifyToken(tr.IDToken, nil, false) != nil {
		t.Fatal("expiry check")
	}
}

func TestRevocationAndSessions(t *testing.T) {
	f := newFixture(t)
	gr := "https://grafana.example.com/login/generic_oauth"
	a := f.login(t, "grafana", gr, "alice")
	b := f.login(t, "grafana", gr, "bob")
	f.p.Grant("alice", "grafana", []string{"openid", "profile"})
	if !f.p.Granted("alice", "grafana", []string{"openid"}) || f.p.Granted("alice", "grafana", []string{"openid", "email"}) || f.p.Granted("bob", "grafana", nil) {
		t.Fatal("grants")
	}
	fw := f.p.NewFwdSession("fwd-x", "alice", "sid1", time.Hour)
	if s := f.p.Fwd(fw); s == nil || s.User != "alice" {
		t.Fatal("fwd session")
	}
	// RFC 7009: another client cannot revoke; the owner can.
	f.p.Revoke(a.RefreshToken, f.clients["spa"])
	if _, te := f.p.UserInfo(a.AccessToken); te != nil {
		t.Fatal("revoked by another client")
	}
	f.p.Revoke(a.RefreshToken, f.clients["grafana"])
	if _, te := f.p.UserInfo(a.AccessToken); te == nil {
		t.Fatal("refresh revocation kept the access token")
	}
	// Logout of the NAS session ends forward-auth sessions and tokens.
	f.p.EndSession("sid1")
	if f.p.Fwd(fw) != nil {
		t.Fatal("fwd session survived the logout")
	}
	if _, te := f.p.UserInfo(b.AccessToken); te == nil {
		t.Fatal("token survived the logout")
	}
	// offline_access refresh tokens survive it.
	c := f.login(t, "grafana", gr, "carol", "scope", "openid offline_access")
	f.p.EndSession("sid1")
	if _, te := f.p.Token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.RefreshToken}}, f.clients["grafana"], iss); te != nil {
		t.Fatal("offline refresh token ended with the session", te)
	}
	// The user's revoke button.
	f.p.RevokeUserClient("alice", "grafana")
	if len(f.p.Grants("alice")) != 0 {
		t.Fatal("grant kept")
	}
	fw = f.p.NewFwdSession("fwd-x", "dave", "", time.Hour)
	f.p.RevokeClient("fwd-x", false)
	if f.p.Fwd(fw) != nil {
		t.Fatal("client revocation")
	}
	fw = f.p.NewFwdSession("fwd-x", "dave", "", time.Hour)
	f.now = f.now.Add(2 * time.Hour)
	if f.p.Fwd(fw) != nil {
		t.Fatal("expired fwd session")
	}
	// Sessions survive a restart (hashed on disk).
	fw = f.p.NewFwdSession("fwd-x", "erin", "", time.Hour)
	p2, _ := Open(f.p.dir)
	p2.Now = f.p.Now
	if p2.Fwd(fw) == nil {
		t.Fatal("fwd session not persisted")
	}
}

func TestPending(t *testing.T) {
	f := newFixture(t)
	r := &AuthRequest{ID: "r1", Created: f.now}
	_ = f.p.PutRequest(r)
	if f.p.Request("r1") == nil || f.p.TakeRequest("r1") == nil || f.p.TakeRequest("r1") != nil {
		t.Fatal("request take once")
	}
	_ = f.p.PutRequest(&AuthRequest{ID: "r2", Created: f.now})
	f.now = f.now.Add(RequestTTL + time.Second)
	if f.p.Request("r2") != nil {
		t.Fatal("expired request")
	}
	_ = f.p.PutFlow("s", &FwdFlow{Host: "h"})
	if f.p.TakeFlow("s") == nil || f.p.TakeFlow("s") != nil {
		t.Fatal("flow take once")
	}
	req := &AuthRequest{Created: f.now, Prompt: []string{"login"}}
	if !req.NeedsLogin(f.now.Add(-time.Second), f.now) || req.NeedsLogin(f.now.Add(time.Second), f.now) {
		t.Fatal("prompt=login")
	}
	req = &AuthRequest{Created: f.now, MaxAge: 60}
	if !req.NeedsLogin(f.now.Add(-2*time.Minute), f.now) || req.NeedsLogin(f.now.Add(-30*time.Second), f.now) {
		t.Fatal("max_age")
	}
}

func TestDiscovery(t *testing.T) {
	d := Discovery(iss)
	if d["issuer"] != iss || d["token_endpoint"] != iss+"/api/sso/token" {
		t.Fatal(d)
	}
	if m := d["code_challenge_methods_supported"].([]string); len(m) != 1 || m[0] != "S256" {
		t.Fatal("S256 only")
	}
	u := CodeURL("https://a.example.com/cb?x=1", "c", "s", iss)
	if !strings.HasPrefix(u, "https://a.example.com/cb?x=1&") || !strings.Contains(u, "iss=") {
		t.Fatal(u)
	}
}
