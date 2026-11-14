package sso

import (
	"crypto/sha256"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// TokenError is an error of the token, userinfo or revocation endpoints.
type TokenError struct {
	Status int
	Code   string
	Desc   string
}

func (e *TokenError) Error() string { return e.Code + ": " + e.Desc }

func invalidGrant(desc string) *TokenError {
	return &TokenError{Status: http.StatusBadRequest, Code: "invalid_grant", Desc: desc}
}

var errInvalidClient = &TokenError{Status: http.StatusUnauthorized, Code: "invalid_client", Desc: "client authentication failed"}

// AuthenticateClient finds and authenticates the client of a token or
// revocation request: client_secret_basic, client_secret_post, or none for
// a public client (which then relies on PKCE). Only one method at a time.
func AuthenticateClient(r *http.Request, form url.Values, find func(string) *Client) (*Client, *TokenError) {
	id, secret, basic := r.BasicAuth()
	if basic {
		// RFC 6749 2.3.1: both are form-encoded inside Basic.
		var err1, err2 error
		id, err1 = url.QueryUnescape(id)
		secret, err2 = url.QueryUnescape(secret)
		if err1 != nil || err2 != nil {
			return nil, errInvalidClient
		}
		if form.Has("client_secret") || (form.Get("client_id") != "" && form.Get("client_id") != id) {
			return nil, &TokenError{Status: http.StatusBadRequest, Code: "invalid_request", Desc: "use one client authentication method"}
		}
	} else {
		id, secret = form.Get("client_id"), form.Get("client_secret")
	}
	if id == "" {
		return nil, errInvalidClient
	}
	c := find(id)
	if c == nil {
		return nil, errInvalidClient
	}
	if c.Public() {
		if secret != "" {
			return nil, errInvalidClient
		}
		return c, nil
	}
	if !c.CheckSecret(secret) {
		return nil, errInvalidClient
	}
	return c, nil
}

// TokenResponse is the answer of the token endpoint.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope"`
}

// redeemLocked takes a code: single use, short life, same client, same
// redirect URI, PKCE verifier. A code presented twice revokes what it gave.
func (p *Provider) redeemLocked(code string, c *Client, redirectURI, verifier string) (*codeEntry, *TokenError) {
	now := p.Now()
	e, ok := p.codes[Hash(code)]
	if code == "" || !ok {
		return nil, invalidGrant("unknown or expired code")
	}
	if e.used {
		fam := e.family
		p.dropLocked(func(_, _, _, family string, _ []string) bool { return family != fam })
		p.saveLocked()
		return nil, invalidGrant("code already used")
	}
	e.used = true
	switch {
	case now.After(e.expires):
		return nil, invalidGrant("code expired")
	case e.req.ClientID != c.ID:
		return nil, invalidGrant("code issued to another client")
	case e.req.RedirectURI != redirectURI:
		return nil, invalidGrant("redirect_uri does not match")
	case !checkPKCE(e.req.Challenge, verifier):
		return nil, invalidGrant("PKCE verification failed")
	}
	return e, nil
}

// Redeem takes a code for the forward authentication of the proxy (no
// tokens: the caller opens a session).
func (p *Provider) Redeem(code string, c *Client, redirectURI, verifier string) (user, sid string, err error) {
	p.mu.Lock()
	e, te := p.redeemLocked(code, c, redirectURI, verifier)
	p.mu.Unlock()
	if te != nil {
		return "", "", te
	}
	if _, err := p.Lookup(e.user, c.ID); err != nil {
		return "", "", err
	}
	return e.user, e.sid, nil
}

// Token answers the token endpoint for an authenticated client.
func (p *Provider) Token(form url.Values, c *Client, issuer string) (*TokenResponse, *TokenError) {
	switch form.Get("grant_type") {
	case "authorization_code":
		p.mu.Lock()
		e, te := p.redeemLocked(form.Get("code"), c, form.Get("redirect_uri"), form.Get("code_verifier"))
		p.mu.Unlock()
		if te != nil {
			return nil, te
		}
		id, err := p.Lookup(e.user, c.ID)
		if err != nil {
			return nil, invalidGrant(err.Error())
		}
		return p.issue(c, id, e.user, e.req.Scopes, e.sid, e.family, e.authTime, e.req.Nonce, issuer)
	case "refresh_token":
		rt := form.Get("refresh_token")
		p.mu.Lock()
		t, ok := p.d.Refresh[Hash(rt)]
		if rt == "" || !ok || p.Now().After(t.Expires) {
			p.mu.Unlock()
			return nil, invalidGrant("unknown or expired refresh token")
		}
		if t.Client != c.ID {
			p.mu.Unlock()
			return nil, invalidGrant("refresh token issued to another client")
		}
		if t.Used {
			// Replayed: someone else holds a copy. End the whole family.
			fam := t.Family
			p.dropLocked(func(_, _, _, family string, _ []string) bool { return family != fam })
			p.saveLocked()
			p.mu.Unlock()
			return nil, invalidGrant("refresh token already used")
		}
		scopes := t.Scopes
		if s := form.Get("scope"); s != "" {
			scopes = nil
			for _, x := range strings.Fields(s) {
				if !slices.Contains(t.Scopes, x) {
					p.mu.Unlock()
					return nil, &TokenError{Status: http.StatusBadRequest, Code: "invalid_scope", Desc: "scope not granted"}
				}
				scopes = append(scopes, x)
			}
		}
		t.Used = true
		user, sid, fam, auth, nonce := t.User, t.Sid, t.Family, t.AuthTime, t.Nonce
		p.saveLocked()
		p.mu.Unlock()
		id, err := p.Lookup(user, c.ID)
		if err != nil {
			p.mu.Lock()
			p.dropLocked(func(_, _, _, family string, _ []string) bool { return family != fam })
			p.saveLocked()
			p.mu.Unlock()
			return nil, invalidGrant(err.Error())
		}
		return p.issue(c, id, user, scopes, sid, fam, auth, nonce, issuer)
	case "":
		return nil, &TokenError{Status: http.StatusBadRequest, Code: "invalid_request", Desc: "grant_type is missing"}
	default:
		return nil, &TokenError{Status: http.StatusBadRequest, Code: "unsupported_grant_type", Desc: "only authorization_code and refresh_token"}
	}
}

func (p *Provider) issue(c *Client, id *Identity, user string, scopes []string, sid, family string, authTime time.Time, nonce, issuer string) (*TokenResponse, *TokenError) {
	now := p.Now()
	at, rt := Random(32), Random(32)
	idt := map[string]any{
		"iss": issuer, "sub": id.Subject, "aud": c.ID, "azp": c.ID,
		"exp": now.Add(IDTokenTTL).Unix(), "iat": now.Unix(), "auth_time": authTime.Unix(),
		"at_hash": halfHash(at),
	}
	if nonce != "" {
		idt["nonce"] = nonce
	}
	if sid != "" {
		idt["sid"] = sid
	}
	for k, v := range Claims(id, scopes) {
		idt[k] = v
	}
	signed, err := p.Sign(idt)
	if err != nil {
		return nil, &TokenError{Status: http.StatusInternalServerError, Code: "server_error", Desc: "cannot sign the ID token"}
	}
	p.mu.Lock()
	p.d.Access[Hash(at)] = &Token{Client: c.ID, User: user, Scopes: scopes, Sid: sid, AuthTime: authTime, Created: now, Expires: now.Add(AccessTTL), Family: family}
	p.d.Refresh[Hash(rt)] = &Token{Client: c.ID, User: user, Scopes: scopes, Sid: sid, AuthTime: authTime, Created: now, Expires: now.Add(RefreshTTL), Family: family, Nonce: nonce}
	p.saveLocked()
	p.mu.Unlock()
	return &TokenResponse{AccessToken: at, TokenType: "Bearer", ExpiresIn: int(AccessTTL.Seconds()), RefreshToken: rt, IDToken: signed, Scope: strings.Join(scopes, " ")}, nil
}

// halfHash is the at_hash of an access token (left half of SHA-256).
func halfHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return b64.EncodeToString(h[:16])
}

// Claims are the user claims released for the scopes.
func Claims(id *Identity, scopes []string) map[string]any {
	c := map[string]any{"sub": id.Subject}
	if slices.Contains(scopes, "profile") {
		c["preferred_username"] = id.Username
		c["name"] = id.Name
		if id.Name == "" {
			c["name"] = id.Username
		}
	}
	if slices.Contains(scopes, "email") && id.Email != "" {
		c["email"] = id.Email
		// Addresses are typed by administrators and users, never checked:
		// apps must not merge accounts on them.
		c["email_verified"] = false
	}
	if slices.Contains(scopes, "groups") {
		g := id.Groups
		if g == nil {
			g = []string{}
		}
		c["groups"] = g
	}
	return c
}

// UserInfo answers the userinfo endpoint for a bearer token.
func (p *Provider) UserInfo(accessToken string) (map[string]any, *TokenError) {
	p.mu.Lock()
	t, ok := p.d.Access[Hash(accessToken)]
	var tc Token
	if ok {
		tc = *t
	}
	p.mu.Unlock()
	bad := &TokenError{Status: http.StatusUnauthorized, Code: "invalid_token", Desc: "unknown or expired access token"}
	if accessToken == "" || !ok || p.Now().After(tc.Expires) {
		return nil, bad
	}
	id, err := p.Lookup(tc.User, tc.Client)
	if err != nil {
		return nil, &TokenError{Status: http.StatusUnauthorized, Code: "invalid_token", Desc: err.Error()}
	}
	return Claims(id, tc.Scopes), nil
}

// Revoke answers the revocation endpoint (RFC 7009): a refresh token takes
// its family along; tokens of other clients are left alone. Unknown tokens
// are not an error.
func (p *Provider) Revoke(tok string, c *Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := Hash(tok)
	if t, ok := p.d.Refresh[h]; ok && t.Client == c.ID {
		fam := t.Family
		p.dropLocked(func(_, _, _, family string, _ []string) bool { return family != fam })
	} else if t, ok := p.d.Access[h]; ok && t.Client == c.ID {
		delete(p.d.Access, h)
	}
	p.saveLocked()
}

// Discovery is the provider metadata (OpenID Connect Discovery 1.0).
func Discovery(issuer string) map[string]any { return DiscoveryFor(issuer, issuer) }

// DiscoveryFor is the metadata for apps that call the provider through
// another address (back) than browsers: the endpoints the browser opens
// stay on the issuer, the others use back.
func DiscoveryFor(issuer, back string) map[string]any {
	return map[string]any{
		"issuer":                                         issuer,
		"authorization_endpoint":                         issuer + "/api/sso/authorize",
		"token_endpoint":                                 back + "/api/sso/token",
		"userinfo_endpoint":                              back + "/api/sso/userinfo",
		"jwks_uri":                                       back + "/api/sso/jwks",
		"end_session_endpoint":                           issuer + "/api/sso/logout",
		"revocation_endpoint":                            back + "/api/sso/revoke",
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"subject_types_supported":                        []string{"public"},
		"id_token_signing_alg_values_supported":          []string{"RS256"},
		"scopes_supported":                               Scopes,
		"claims_supported":                               []string{"sub", "iss", "aud", "exp", "iat", "auth_time", "nonce", "name", "preferred_username", "email", "email_verified", "groups", "sid"},
		"token_endpoint_auth_methods_supported":          []string{"client_secret_basic", "client_secret_post", "none"},
		"code_challenge_methods_supported":               []string{"S256"},
		"prompt_values_supported":                        []string{"none", "login", "consent"},
		"request_parameter_supported":                    false,
		"request_uri_parameter_supported":                false,
		"authorization_response_iss_parameter_supported": true,
		"claims_parameter_supported":                     false,
		"frontchannel_logout_supported":                  false,
		"backchannel_logout_supported":                   false,
	}
}
