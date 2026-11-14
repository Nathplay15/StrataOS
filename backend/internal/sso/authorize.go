package sso

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Lifetimes.
const (
	RequestTTL = 10 * time.Minute // an authorization request waiting for the user
	CodeTTL    = time.Minute
	AccessTTL  = time.Hour
	IDTokenTTL = time.Hour
	RefreshTTL = 30 * 24 * time.Hour
)

// Scopes the provider knows; others are ignored.
var Scopes = []string{"openid", "profile", "email", "groups", "offline_access"}

// Client is a relying party as the protocol sees it.
type Client struct {
	ID             string
	Secret         string // "" = public client (PKCE required)
	RedirectURIs   []string
	PostLogoutURIs []string
	RequirePKCE    bool
}

// Public reports whether the client has no secret.
func (c *Client) Public() bool { return c.Secret == "" }

// CheckSecret compares a presented secret in constant time.
func (c *Client) CheckSecret(s string) bool {
	if c.Secret == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(c.Secret)), sha256.Sum256([]byte(s))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// AuthRequest is a validated authorization request.
type AuthRequest struct {
	ID          string    `json:"id"`
	ClientID    string    `json:"clientId"`
	RedirectURI string    `json:"redirectUri"`
	Scopes      []string  `json:"scopes"`
	State       string    `json:"-"`
	Nonce       string    `json:"-"`
	Challenge   string    `json:"-"`
	Prompt      []string  `json:"prompt,omitempty"`
	MaxAge      int       `json:"maxAge"` // -1: none
	Created     time.Time `json:"created"`
	// Binding: hash of the cookie of the browser that started it.
	Binding string `json:"-"`
}

// Has reports whether the request asked for a scope.
func (a *AuthRequest) Has(scope string) bool { return slices.Contains(a.Scopes, scope) }

// HasPrompt reports whether prompt contains v.
func (a *AuthRequest) HasPrompt(v string) bool { return slices.Contains(a.Prompt, v) }

// DirectError is an authorization error that must not be sent to the
// redirect URI (unknown client, redirect URI not registered): the user sees it.
type DirectError struct{ Msg string }

func (e *DirectError) Error() string { return e.Msg }

// AuthError is an authorization error sent back to the client.
type AuthError struct {
	Code, Desc         string
	RedirectURI, State string
}

func (e *AuthError) Error() string { return e.Code + ": " + e.Desc }

// URL is where the browser goes with the error.
func (e *AuthError) URL() string {
	q := url.Values{"error": {e.Code}}
	if e.Desc != "" {
		q.Set("error_description", e.Desc)
	}
	if e.State != "" {
		q.Set("state", e.State)
	}
	return withQuery(e.RedirectURI, q)
}

func withQuery(u string, q url.Values) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + q.Encode()
}

// CodeURL is where the browser goes with a code.
func CodeURL(redirectURI, code, state, issuer string) string {
	q := url.Values{"code": {code}}
	if state != "" {
		q.Set("state", state)
	}
	// RFC 9207: tells the client which provider answered (mix-up attacks).
	if issuer != "" {
		q.Set("iss", issuer)
	}
	return withQuery(redirectURI, q)
}

var challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var verifierRe = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// ParseAuthorize validates the parameters of an authorization request. The
// client and the redirect URI are checked first: until both are known good,
// errors are a *DirectError, never a redirect (no open redirect).
func ParseAuthorize(q url.Values, find func(id string) *Client, now time.Time) (*AuthRequest, error) {
	for k, v := range q {
		if len(v) > 1 {
			return nil, &DirectError{"the parameter " + k + " is repeated"}
		}
	}
	c := find(q.Get("client_id"))
	if q.Get("client_id") == "" || c == nil {
		return nil, &DirectError{"unknown application (client_id)"}
	}
	ru := q.Get("redirect_uri")
	if ru == "" || !slices.Contains(c.RedirectURIs, ru) {
		// Exact match only: no prefix, no wildcard, no normalisation.
		return nil, &DirectError{"this return address (redirect_uri) is not registered for the application"}
	}
	fail := func(code, desc string) error {
		return &AuthError{Code: code, Desc: desc, RedirectURI: ru, State: q.Get("state")}
	}
	if q.Has("request") || q.Has("request_uri") {
		return nil, fail("request_not_supported", "request objects are not supported")
	}
	if q.Get("response_type") != "code" {
		return nil, fail("unsupported_response_type", "only the authorization code flow is supported")
	}
	if m := q.Get("response_mode"); m != "" && m != "query" {
		return nil, fail("invalid_request", "only response_mode=query is supported")
	}
	var scopes []string
	for _, s := range strings.Fields(q.Get("scope")) {
		if slices.Contains(Scopes, s) && !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	if !slices.Contains(scopes, "openid") {
		return nil, fail("invalid_scope", "the openid scope is required")
	}
	ch, method := q.Get("code_challenge"), q.Get("code_challenge_method")
	switch {
	case ch == "" && (c.Public() || c.RequirePKCE):
		return nil, fail("invalid_request", "PKCE (code_challenge with S256) is required")
	case ch == "" && method != "":
		return nil, fail("invalid_request", "code_challenge_method without code_challenge")
	case ch != "" && method != "S256":
		// "plain" (and the default when absent) is refused: S256 only.
		return nil, fail("invalid_request", "only code_challenge_method=S256 is supported")
	case ch != "" && !challengeRe.MatchString(ch):
		return nil, fail("invalid_request", "invalid code_challenge")
	}
	if len(q.Get("state")) > 2048 || len(q.Get("nonce")) > 2048 {
		return nil, fail("invalid_request", "state or nonce too long")
	}
	prompt := strings.Fields(q.Get("prompt"))
	for _, p := range prompt {
		if !slices.Contains([]string{"none", "login", "consent", "select_account"}, p) {
			return nil, fail("invalid_request", "unknown prompt value")
		}
	}
	if slices.Contains(prompt, "none") && len(prompt) > 1 {
		return nil, fail("invalid_request", "prompt=none cannot be combined")
	}
	maxAge := -1
	if v := q.Get("max_age"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fail("invalid_request", "invalid max_age")
		}
		maxAge = n
	}
	return &AuthRequest{ID: Random(16), ClientID: c.ID, RedirectURI: ru, Scopes: scopes, State: q.Get("state"),
		Nonce: q.Get("nonce"), Challenge: ch, Prompt: prompt, MaxAge: maxAge, Created: now}, nil
}

// NeedsLogin reports whether the session (signed in at authTime) is too old
// for the request (prompt=login, max_age).
func (a *AuthRequest) NeedsLogin(authTime, now time.Time) bool {
	if !authTime.Before(a.Created) {
		return false // signed in for this request
	}
	return a.HasPrompt("login") || (a.MaxAge >= 0 && now.Sub(authTime) > time.Duration(a.MaxAge)*time.Second)
}

// Random returns n random bytes in hexadecimal.
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Hash is how tokens and codes are kept: never in clear.
func Hash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// S256 is the PKCE challenge of a verifier.
func S256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return b64.EncodeToString(h[:])
}

// checkPKCE compares a verifier with the challenge in constant time.
func checkPKCE(challenge, verifier string) bool {
	if challenge == "" {
		return verifier == ""
	}
	if !verifierRe.MatchString(verifier) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(S256(verifier)), []byte(challenge)) == 1
}
