// Package oidc signs users in with an OpenID Connect provider (Authentik,
// Keycloak, Authelia, Google, Microsoft Entra…): discovery, authorization
// code flow with PKCE, ID token validation against the provider's keys
// (JWKS), and the mapping of claims to a StrataOS account. The parsing and
// validation are pure and tested; the api package keeps the flows.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

var b64 = base64.RawURLEncoding

// Discovery is the part of the provider metadata StrataOS uses.
type Discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint,omitempty"`
	JWKSURI               string   `json:"jwks_uri"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported,omitempty"`
	IDTokenAlgs           []string `json:"id_token_signing_alg_values_supported,omitempty"`
}

// CheckURL accepts an absolute https URL (http only with allowHTTP), without
// credentials or fragment.
func CheckURL(raw string, allowHTTP bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, " \n\r\t") {
		return nil, fmt.Errorf("invalid URL %q", raw)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowHTTP:
	case u.Scheme == "http":
		return nil, fmt.Errorf("%s: the provider must use HTTPS", u.Host)
	default:
		return nil, fmt.Errorf("invalid URL %q", raw)
	}
	return u, nil
}

// NormalizeIssuer trims a trailing slash, as providers publish it either way
// (the comparison with the discovered issuer stays exact otherwise).
func NormalizeIssuer(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

// ValidateDiscovery checks the metadata of issuer.
func ValidateDiscovery(d *Discovery, issuer string, allowHTTP bool) error {
	if NormalizeIssuer(d.Issuer) != NormalizeIssuer(issuer) {
		return fmt.Errorf("the provider calls itself %q, not %q: check the issuer URL", d.Issuer, issuer)
	}
	for _, u := range []string{d.AuthorizationEndpoint, d.TokenEndpoint, d.JWKSURI} {
		if _, err := CheckURL(u, allowHTTP); err != nil {
			return fmt.Errorf("provider metadata: %w", err)
		}
	}
	if d.UserinfoEndpoint != "" {
		if _, err := CheckURL(d.UserinfoEndpoint, allowHTTP); err != nil {
			d.UserinfoEndpoint = ""
		}
	}
	if len(d.CodeChallengeMethods) > 0 && !slices.Contains(d.CodeChallengeMethods, "S256") {
		return errors.New("the provider does not support PKCE (S256)")
	}
	return nil
}

const maxBody = 1 << 20

func getJSON(ctx context.Context, hc *http.Client, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v)
}

// Client fetches and caches the metadata and keys of providers.
type Client struct {
	HTTP *http.Client
	mu   sync.Mutex
	m    map[string]*cached
}

type cached struct {
	disc      *Discovery
	keys      KeySet
	fetched   time.Time
	refreshed time.Time // last forced JWKS refresh (unknown key ID)
}

const cacheFor = time.Hour

func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		m:    map[string]*cached{},
	}
}

// Provider returns the metadata and keys of an issuer, fetched at most every
// hour; refresh forces a new key set (an unknown key ID after a rotation),
// at most once a minute.
func (c *Client) Provider(ctx context.Context, issuer string, allowHTTP, refresh bool) (*Discovery, KeySet, error) {
	issuer = NormalizeIssuer(issuer)
	c.mu.Lock()
	e := c.m[issuer]
	if e != nil && time.Since(e.fetched) < cacheFor && (!refresh || time.Since(e.refreshed) < time.Minute) {
		c.mu.Unlock()
		return e.disc, e.keys, nil
	}
	c.mu.Unlock()
	if _, err := CheckURL(issuer, allowHTTP); err != nil {
		return nil, KeySet{}, err
	}
	var d Discovery
	if err := getJSON(ctx, c.HTTP, issuer+"/.well-known/openid-configuration", &d); err != nil {
		return nil, KeySet{}, err
	}
	if err := ValidateDiscovery(&d, issuer, allowHTTP); err != nil {
		return nil, KeySet{}, err
	}
	var raw json.RawMessage
	if err := getJSON(ctx, c.HTTP, d.JWKSURI, &raw); err != nil {
		return nil, KeySet{}, err
	}
	ks, err := ParseJWKS(raw)
	if err != nil {
		return nil, KeySet{}, err
	}
	now := time.Now()
	c.mu.Lock()
	c.m[issuer] = &cached{disc: &d, keys: ks, fetched: now, refreshed: now}
	c.mu.Unlock()
	return &d, ks, nil
}

// Forget drops the cache of an issuer (provider settings changed).
func (c *Client) Forget(issuer string) {
	c.mu.Lock()
	delete(c.m, NormalizeIssuer(issuer))
	c.mu.Unlock()
}

// Random returns a URL-safe random string of n bytes of entropy.
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b64.EncodeToString(b)
}

// Challenge is the PKCE S256 challenge of a verifier.
func Challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return b64.EncodeToString(h[:])
}

// AuthRequest is what goes into the authorization URL.
type AuthRequest struct {
	ClientID, RedirectURI, State, Nonce, Verifier, ACRValues string
	Scopes                                                   []string
}

// AuthURL builds the URL the browser is sent to.
func AuthURL(d *Discovery, a AuthRequest) string {
	scopes := []string{"openid"}
	for _, s := range a.Scopes {
		if s != "" && s != "openid" && !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", a.ClientID)
	v.Set("redirect_uri", a.RedirectURI)
	v.Set("scope", strings.Join(scopes, " "))
	v.Set("state", a.State)
	v.Set("nonce", a.Nonce)
	v.Set("code_challenge", Challenge(a.Verifier))
	v.Set("code_challenge_method", "S256")
	if a.ACRValues != "" {
		v.Set("acr_values", a.ACRValues)
	}
	sep := "?"
	if strings.Contains(d.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return d.AuthorizationEndpoint + sep + v.Encode()
}

// Tokens is the answer of the token endpoint.
type Tokens struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// Exchange trades the authorization code for tokens. A confidential client
// authenticates with HTTP Basic (client_secret_basic), or in the form when
// the provider only supports client_secret_post.
func (c *Client) Exchange(ctx context.Context, d *Discovery, clientID, secret, code, redirectURI, verifier string) (Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	post := secret != "" && len(d.TokenAuthMethods) > 0 && !slices.Contains(d.TokenAuthMethods, "client_secret_basic") && slices.Contains(d.TokenAuthMethods, "client_secret_post")
	if secret == "" || post {
		form.Set("client_id", clientID)
	}
	if post {
		form.Set("client_secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if secret != "" && !post {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &e)
		return Tokens{}, fmt.Errorf("token request refused (HTTP %d %s %s)", resp.StatusCode, clean(e.Error), clean(e.Desc))
	}
	var t Tokens
	if err := json.Unmarshal(body, &t); err != nil || t.IDToken == "" {
		return Tokens{}, errors.New("the provider returned no ID token")
	}
	return t, nil
}

// Userinfo reads the userinfo endpoint (for claims missing from the ID
// token); its subject must be the ID token's.
func (c *Client) Userinfo(ctx context.Context, d *Discovery, accessToken, subject string) (Claims, error) {
	if d.UserinfoEndpoint == "" || accessToken == "" {
		return nil, errors.New("no userinfo endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return nil, fmt.Errorf("userinfo: HTTP %d", resp.StatusCode)
	}
	var cl Claims
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&cl); err != nil {
		return nil, err
	}
	if cl.String("sub") != subject {
		return nil, errors.New("userinfo of another subject")
	}
	return cl, nil
}

// clean keeps a provider's error text short and printable for the log.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
