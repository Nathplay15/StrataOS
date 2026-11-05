package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// testIdP is a minimal provider: signing keys, a JWKS, discovery and a
// token endpoint answering a prepared ID token.
type testIdP struct {
	rsa  *rsa.PrivateKey
	ec   *ecdsa.PrivateKey
	ed   ed25519.PrivateKey
	srv  *httptest.Server
	next string // ID token the token endpoint returns
	form url.Values
	user string
}

func newIdP(t *testing.T) *testIdP {
	t.Helper()
	p := &testIdP{}
	p.rsa, _ = rsa.GenerateKey(rand.Reader, 2048)
	p.ec, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, p.ed, _ = ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Discovery{Issuer: p.srv.URL, AuthorizationEndpoint: p.srv.URL + "/auth", TokenEndpoint: p.srv.URL + "/token",
			JWKSURI: p.srv.URL + "/keys", CodeChallengeMethods: []string{"S256"}})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		n := p.rsa.PublicKey.N.Bytes()
		e := big.NewInt(int64(p.rsa.PublicKey.E)).Bytes()
		ecb, _ := p.ec.PublicKey.Bytes()
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "r1", "use": "sig", "n": b64.EncodeToString(n), "e": b64.EncodeToString(e)},
			{"kty": "EC", "kid": "e1", "crv": "P-256", "x": b64.EncodeToString(ecb[1:33]), "y": b64.EncodeToString(ecb[33:])},
			{"kty": "OKP", "kid": "d1", "crv": "Ed25519", "x": b64.EncodeToString(p.ed.Public().(ed25519.PublicKey))},
			{"kty": "RSA", "kid": "enc", "use": "enc", "n": b64.EncodeToString(n), "e": b64.EncodeToString(e)},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.form = r.PostForm
		u, _, _ := r.BasicAuth()
		p.user = u
		json.NewEncoder(w).Encode(map[string]string{"id_token": p.next, "access_token": "at", "token_type": "Bearer"})
	})
	t.Cleanup(p.srv.Close)
	return p
}

func (p *testIdP) sign(t *testing.T, alg, kid string, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	pb, _ := json.Marshal(claims)
	signed := b64.EncodeToString(hb) + "." + b64.EncodeToString(pb)
	var sig []byte
	var err error
	switch alg {
	case "RS256":
		h := crypto.SHA256.New()
		h.Write([]byte(signed))
		sig, err = rsa.SignPKCS1v15(rand.Reader, p.rsa, crypto.SHA256, h.Sum(nil))
	case "PS256":
		h := crypto.SHA256.New()
		h.Write([]byte(signed))
		sig, err = rsa.SignPSS(rand.Reader, p.rsa, crypto.SHA256, h.Sum(nil), &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	case "ES256":
		h := crypto.SHA256.New()
		h.Write([]byte(signed))
		r, s, e := ecdsa.Sign(rand.Reader, p.ec, h.Sum(nil))
		err = e
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	case "EdDSA":
		sig = ed25519.Sign(p.ed, []byte(signed))
	case "none":
	case "HS256":
		sig = []byte("whatever")
	}
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + b64.EncodeToString(sig)
}

func (p *testIdP) claims(now time.Time) map[string]any {
	return map[string]any{"iss": p.srv.URL, "sub": "u-123", "aud": "strata", "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"nonce": "n0nce", "preferred_username": "Jean.Dupont", "email": "jean@example.com", "groups": []string{"/nas-admins", "family"}}
}

func TestFlow(t *testing.T) {
	p := newIdP(t)
	c := NewClient()
	ctx := context.Background()
	if _, _, err := c.Provider(ctx, p.srv.URL, false, false); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("http issuer accepted without allowHTTP: %v", err)
	}
	d, ks, err := c.Provider(ctx, p.srv.URL+"/", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ks.Keys) != 3 {
		t.Fatalf("keys: %d (the encryption key must be skipped)", len(ks.Keys))
	}
	u, _ := url.Parse(AuthURL(d, AuthRequest{ClientID: "strata", RedirectURI: "https://nas/cb", State: "st", Nonce: "n0nce", Verifier: "v", Scopes: []string{"profile", "openid"}}))
	q := u.Query()
	if q.Get("code_challenge") != Challenge("v") || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid profile" || q.Get("state") != "st" {
		t.Fatalf("auth URL: %s", u)
	}
	now := time.Now()
	for _, alg := range []string{"RS256", "PS256", "ES256", "EdDSA"} {
		kid := map[string]string{"RS256": "r1", "PS256": "r1", "ES256": "e1", "EdDSA": "d1"}[alg]
		p.next = p.sign(t, alg, kid, p.claims(now))
		tok, err := c.Exchange(ctx, d, "strata", "s3cr:t", "code1", "https://nas/cb", "v")
		if err != nil {
			t.Fatal(err)
		}
		if p.form.Get("code_verifier") != "v" || p.form.Get("client_secret") != "" || p.user != url.QueryEscape("strata") {
			t.Fatalf("token request: %v %q", p.form, p.user)
		}
		cl, err := VerifyIDToken(tok.IDToken, ks, Expect{Issuer: p.srv.URL, ClientID: "strata", Nonce: "n0nce", Now: now})
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		if cl.String("sub") != "u-123" {
			t.Fatal(cl)
		}
	}
	// Public client: client_id in the form, no Basic.
	p.user = ""
	if _, err := c.Exchange(ctx, d, "strata", "", "code", "https://nas/cb", "v"); err != nil || p.form.Get("client_id") != "strata" || p.user != "" {
		t.Fatalf("public client: %v %v", err, p.form)
	}
}

func TestVerifyRefusals(t *testing.T) {
	p := newIdP(t)
	_, ks, err := NewClient().Provider(context.Background(), p.srv.URL, true, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	exp := Expect{Issuer: p.srv.URL, ClientID: "strata", Nonce: "n0nce", Now: now}
	mod := func(f func(m map[string]any)) map[string]any {
		m := p.claims(now)
		f(m)
		return m
	}
	good := p.sign(t, "RS256", "r1", p.claims(now))
	parts := strings.Split(good, ".")
	cases := map[string]struct {
		tok  string
		want string
	}{
		"none":          {p.sign(t, "none", "r1", p.claims(now)), "algorithm"},
		"HS256":         {p.sign(t, "HS256", "r1", p.claims(now)), "algorithm"},
		"unknown kid":   {p.sign(t, "RS256", "zz", p.claims(now)), "unknown key"},
		"wrong key":     {p.sign(t, "ES256", "r1", p.claims(now)), "unknown key"},
		"tampered":      {parts[0] + "." + b64.EncodeToString([]byte(`{"iss":"x"}`)) + "." + parts[2], "signature"},
		"issuer":        {p.sign(t, "RS256", "r1", mod(func(m map[string]any) { m["iss"] = "https://evil" })), "issuer"},
		"audience":      {p.sign(t, "RS256", "r1", mod(func(m map[string]any) { m["aud"] = []string{"other"} })), "another client"},
		"azp":           {p.sign(t, "RS256", "r1", mod(func(m map[string]any) { m["aud"] = []string{"strata", "x"}; m["azp"] = "x" })), "azp"},
		"expired":       {p.sign(t, "RS256", "r1", mod(func(m map[string]any) { m["exp"] = now.Add(-3 * time.Minute).Unix() })), "expired"},
		"future iat":    {p.sign(t, "RS256", "r1", mod(func(m map[string]any) { m["iat"] = now.Add(10 * time.Minute).Unix() })), "future"},
		"nonce":         {p.sign(t, "RS256", "r1", mod(func(m map[string]any) { m["nonce"] = "other" })), "nonce"},
		"no nonce":      {p.sign(t, "RS256", "r1", mod(func(m map[string]any) { delete(m, "nonce") })), "nonce"},
		"no subject":    {p.sign(t, "RS256", "r1", mod(func(m map[string]any) { delete(m, "sub") })), "subject"},
		"two parts":     {parts[0] + "." + parts[1], "malformed"},
		"crit":          {b64.EncodeToString([]byte(`{"alg":"RS256","kid":"r1","crit":["x"]}`)) + "." + parts[1] + "." + parts[2], "algorithm"},
		"garbage sig":   {parts[0] + "." + parts[1] + ".!!", "malformed"},
		"not yet valid": {p.sign(t, "RS256", "r1", mod(func(m map[string]any) { m["nbf"] = now.Add(time.Hour).Unix() })), "not valid yet"},
	}
	for name, c := range cases {
		if _, err := VerifyIDToken(c.tok, ks, exp); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
	if _, err := VerifyIDToken(p.sign(t, "RS256", "zz", p.claims(now)), ks, exp); !errors.Is(err, ErrNoKey) {
		t.Errorf("unknown kid: %v", err)
	}
	if _, err := VerifyIDToken(good, ks, exp); err != nil {
		t.Errorf("good token: %v", err)
	}
	// A small clock skew is tolerated.
	skew := p.sign(t, "RS256", "r1", mod(func(m map[string]any) { m["exp"] = now.Add(-time.Minute).Unix() }))
	if _, err := VerifyIDToken(skew, ks, exp); err != nil {
		t.Errorf("skew: %v", err)
	}
}

func TestDiscoveryIssuerMismatch(t *testing.T) {
	d := &Discovery{Issuer: "https://idp.example/realms/a", AuthorizationEndpoint: "https://idp.example/a", TokenEndpoint: "https://idp.example/t", JWKSURI: "https://idp.example/k"}
	if err := ValidateDiscovery(d, "https://idp.example/realms/a/", false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDiscovery(d, "https://idp.example/realms/b", false); err == nil {
		t.Fatal("issuer mismatch accepted")
	}
	d.TokenEndpoint = "http://idp.example/t"
	if err := ValidateDiscovery(d, "https://idp.example/realms/a", false); err == nil {
		t.Fatal("plain HTTP endpoint accepted")
	}
	d.TokenEndpoint = "https://idp.example/t"
	d.CodeChallengeMethods = []string{"plain"}
	if err := ValidateDiscovery(d, "https://idp.example/realms/a", false); err == nil {
		t.Fatal("provider without S256 accepted")
	}
	for _, bad := range []string{"https://user:pw@idp/x", "ftp://idp/x", "https://idp/x#f", "/relative", "https://"} {
		if _, err := CheckURL(bad, true); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestParseJWKSSkipsWeakKeys(t *testing.T) {
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	data, _ := json.Marshal(map[string]any{"keys": []map[string]string{
		{"kty": "RSA", "kid": "small", "n": b64.EncodeToString(small.N.Bytes()), "e": "AQAB"},
		{"kty": "EC", "kid": "badpoint", "crv": "P-256", "x": b64.EncodeToString(make([]byte, 32)), "y": b64.EncodeToString(make([]byte, 32))},
		{"kty": "oct", "kid": "hmac", "k": "c2VjcmV0"},
	}})
	if _, err := ParseJWKS(data); err == nil {
		t.Fatal("weak or invalid keys accepted")
	}
}

func TestMapClaims(t *testing.T) {
	c := Claims{"sub": "s1", "preferred_username": "Jean.Dupont", "email": "jean@example.com", "groups": []any{"/NAS-Admins", "family"},
		"realm_access": map[string]any{"roles": []any{"nas-user"}}}
	id, err := MapClaims(c, Mapping{AdminGroups: []string{"nas-admins"}, GroupMap: map[string]string{"family": "famille", "absent": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if id.Username != "jean-dupont" || !id.Admin || len(id.Mapped) != 1 || id.Mapped[0] != "famille" || id.Email != "jean@example.com" {
		t.Fatalf("%+v", id)
	}
	// Nested claim (Keycloak roles) as groups, allowed groups.
	id, err = MapClaims(c, Mapping{GroupsClaim: "realm_access.roles", AllowedGroups: []string{"nas-user"}})
	if err != nil || id.Admin || len(id.Groups) != 1 {
		t.Fatalf("%+v %v", id, err)
	}
	if _, err := MapClaims(c, Mapping{AllowedGroups: []string{"other"}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("allowed groups: %v", err)
	}
	// Fallback on the email only with the default claim.
	delete(c, "preferred_username")
	if id, err := MapClaims(c, Mapping{}); err != nil || id.Username != "jean" {
		t.Fatalf("email fallback: %+v %v", id, err)
	}
	if _, err := MapClaims(c, Mapping{UsernameClaim: "upn"}); err == nil {
		t.Fatal("missing custom claim accepted")
	}
	for in, want := range map[string]string{"Marie": "marie", "9lives": "u9lives", "a b@c": "a-b-c", "--x--": "x", strings.Repeat("a", 40): strings.Repeat("a", 32), "€€": ""} {
		if got := NormalizeUsername(in); got != want {
			t.Errorf("NormalizeUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMFA(t *testing.T) {
	if m, h := MFA(Claims{"amr": []any{"pwd"}}, nil); m || h {
		t.Fatal("password alone is not MFA")
	}
	if m, h := MFA(Claims{"amr": []any{"pwd", "otp"}}, nil); !m || h {
		t.Fatal("otp")
	}
	if m, h := MFA(Claims{"amr": []any{"pwd", "hwk"}}, nil); !m || !h {
		t.Fatal("hwk")
	}
	if m, _ := MFA(Claims{"acr": "gold"}, []string{"gold"}); !m {
		t.Fatal("acr")
	}
	if m, _ := MFA(Claims{"acr": "gold"}, nil); m {
		t.Fatal("unknown acr")
	}
}
