package api

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"

	"strataos/internal/oidc"
	"strataos/internal/store"
)

// u2fCreate answers creation options like a security key without PIN:
// user present only, not discoverable.
func (k *softKey) u2fCreate(t *testing.T, opts map[string]any, origin string) map[string]any {
	pk := opts["publicKey"].(map[string]any)
	rpID := pk["rp"].(map[string]any)["id"].(string)
	x, y := make([]byte, 32), make([]byte, 32)
	k.priv.X.FillBytes(x)
	k.priv.Y.FillBytes(y)
	cose, _ := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	att := binary.BigEndian.AppendUint16(make([]byte, 16), uint16(len(k.id)))
	att = append(append(att, k.id...), cose...)
	ao, _ := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData(rpID, flagUP|flagAT, 0, att)})
	return map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(clientData("webauthn.create", pk["challenge"].(string), origin)),
			"attestationObject": b64.EncodeToString(ao), "transports": []string{"usb"}},
		"clientExtensionResults": map[string]any{}}
}

// u2fGet answers request options without user verification nor user handle.
func (k *softKey) u2fGet(t *testing.T, opts map[string]any, origin string, tamper bool) map[string]any {
	pk := opts["publicKey"].(map[string]any)
	k.count++
	ad := authData(pk["rpId"].(string), flagUP, k.count, nil)
	cd := clientData("webauthn.get", pk["challenge"].(string), origin)
	h := sha256.Sum256(cd)
	d := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	if tamper {
		d[0] ^= 1
	}
	sig, _ := ecdsa.SignASN1(rand.Reader, k.priv, d[:])
	return map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad), "signature": b64.EncodeToString(sig)},
		"clientExtensionResults": map[string]any{}}
}

// jarClient keeps every cookie (session, pending sign-in, OIDC binding).
type jarClient struct {
	t    *testing.T
	s    *Server
	host string
	jar  map[string]string
}

func (c *jarClient) req(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, "https://"+c.host+path, strings.NewReader(string(b)))
	r.Header.Set("Origin", "https://"+c.host)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range c.jar {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	c.s.ServeHTTP(w, r)
	for _, ck := range w.Result().Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.jar, ck.Name)
		} else {
			c.jar[ck.Name] = ck.Value
		}
	}
	return w
}

func (c *jarClient) do(method, path string, body any) (int, map[string]any) {
	w := c.req(method, path, body)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func authServer(t *testing.T) *Server {
	s := newTestServer(t)
	s.authRoutes()
	s.twoFactorRoutes()
	s.accessRoutes()
	s.passkeyRoutes()
	s.secondFactorRoutes()
	s.oidcRoutes()
	return s
}

func TestSecurityKeySecondFactor(t *testing.T) {
	s := authServer(t)
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin", PasswordHash: hash})
		return nil
	})
	me := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.1", time.Hour)}}
	key := newSoftKey(t)

	code, opts := me.do("POST", "/api/me/security-keys/begin", map[string]any{"password": "Secret-Pass-1", "name": "YubiKey"})
	if code != 200 {
		t.Fatalf("begin: %d %v", code, opts)
	}
	sel := opts["publicKey"].(map[string]any)["authenticatorSelection"].(map[string]any)
	if sel["userVerification"] != "preferred" || sel["residentKey"] != "discouraged" || sel["authenticatorAttachment"] != "cross-platform" {
		t.Fatalf("selection: %v", sel)
	}
	code, out := me.do("POST", "/api/me/security-keys/finish", map[string]any{"flow": opts["flow"], "credential": key.u2fCreate(t, opts, testOrigin)})
	if code != 200 {
		t.Fatalf("finish: %d %v", code, out)
	}
	codes, _ := out["recoveryCodes"].([]any)
	if len(codes) != 8 || out["key"].(map[string]any)["name"] != "YubiKey" {
		t.Fatalf("recovery codes at the first key: %v", out)
	}

	// Password alone: the second step is asked for.
	anon := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{}}
	code, out = anon.do("POST", "/api/login", map[string]any{"username": "admin", "password": "Secret-Pass-1"})
	m, _ := out["methods"].(map[string]any)
	if code != 401 || out["twoFactor"] != true || m["key"] != true || m["keyHere"] != true || m["totp"] != false || anon.jar[pendingCookie] == "" || anon.jar[cookieName] != "" {
		t.Fatalf("password step: %d %v %v", code, out, anon.jar)
	}
	// A wrong signature is refused; the right key signs in.
	_, o := anon.do("POST", "/api/login/key/begin", nil)
	if code, _ := anon.do("POST", "/api/login/key/finish", map[string]any{"flow": o["flow"], "credential": key.u2fGet(t, o, testOrigin, true)}); code != 401 {
		t.Fatalf("bad signature: %d", code)
	}
	_, o = anon.do("POST", "/api/login/key/begin", nil)
	if allow := o["publicKey"].(map[string]any)["allowCredentials"].([]any); len(allow) != 1 {
		t.Fatalf("allowCredentials: %v", o)
	}
	code, out = anon.do("POST", "/api/login/key/finish", map[string]any{"flow": o["flow"], "credential": key.u2fGet(t, o, testOrigin, false)})
	if code != 200 || out["username"] != "admin" || anon.jar[cookieName] == "" || anon.jar[pendingCookie] != "" {
		t.Fatalf("key sign-in: %d %v", code, out)
	}
	// The pending sign-in is single use.
	if code, _ := anon.do("POST", "/api/login/key/begin", nil); code != 401 {
		t.Fatalf("pending reused: %d", code)
	}

	// A recovery code completes the step once.
	rc := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{}}
	rc.do("POST", "/api/login", map[string]any{"username": "admin", "password": "Secret-Pass-1"})
	if code, _ := rc.do("POST", "/api/login/2fa", map[string]any{"code": codes[0]}); code != 200 {
		t.Fatalf("recovery code: %d", code)
	}
	rc = &jarClient{t: t, s: s, host: testHost, jar: map[string]string{}}
	rc.do("POST", "/api/login", map[string]any{"username": "admin", "password": "Secret-Pass-1"})
	if code, _ := rc.do("POST", "/api/login/2fa", map[string]any{"code": codes[0]}); code != 401 {
		t.Fatalf("recovery code reused: %d", code)
	}
	// Five wrong codes end the pending sign-in.
	for range pendingTries {
		rc.do("POST", "/api/login/2fa", map[string]any{"code": "000000"})
	}
	s.limiter.reset("192.0.2.1")
	if code, _ := rc.do("POST", "/api/login/2fa", map[string]any{"code": codes[1]}); code != 401 {
		t.Fatalf("pending sign-in survived %d wrong codes: %d", pendingTries, code)
	}

	// On another name of the NAS the key does not work: said, not attempted.
	other := &jarClient{t: t, s: s, host: "strataos.local:7443", jar: map[string]string{}}
	_, out = other.do("POST", "/api/login", map[string]any{"username": "admin", "password": "Secret-Pass-1"})
	if m := out["methods"].(map[string]any); m["keyHere"] != false || m["keyReason"] == "" {
		t.Fatalf("other name: %v", out)
	}

	// Policy: a key required; the last key cannot go.
	if code, out := me.do("PUT", "/api/security/2fa-policy", map[string]any{"require": true, "requireKey": true, "graceDays": -1}); code != 200 {
		t.Fatalf("policy: %d %v", code, out)
	}
	_, list := me.do("GET", "/api/me/security-keys", nil)
	kid := list["keys"].([]any)[0].(map[string]any)["id"].(string)
	if code, _ := me.do("DELETE", "/api/me/security-keys/"+kid, nil); code != 400 {
		t.Fatalf("last key removed under the policy: %d", code)
	}
	if code, _ := me.do("PUT", "/api/me/security-keys/"+kid, map[string]any{"name": "Blue key"}); code != 200 {
		t.Fatalf("rename: %d", code)
	}
}

func TestTOTPRefusedWhenKeyRequired(t *testing.T) {
	now := time.Now()
	st := &store.State{TwoFactor: store.TwoFactorPolicy{Require: true, RequireKey: true, GraceDays: -1, Since: now.Add(-time.Hour)}}
	u := &store.User{Username: "a", Role: "admin", TOTPSecret: "X", SecurityKeys: []store.Passkey{{ID: "k"}}}
	if setupOnly(st, u, now, "") {
		t.Fatal("admin with a key limited to the setup")
	}
	u.SecurityKeys = nil
	if !setupOnly(st, u, now, "") {
		t.Fatal("admin with TOTP only not limited once a key is required")
	}
	if setupOnly(st, u, now, mfaIdPHardware) || !setupOnly(st, u, now, mfaIdP) {
		t.Fatal("provider second factor: a hardware key satisfies a key policy, another factor does not")
	}
	st.TwoFactor.RequireKey = false
	if setupOnly(st, &store.User{Role: "admin"}, now, mfaIdP) {
		t.Fatal("provider second factor not accepted by the policy")
	}
	u.Passkeys = []store.Passkey{{ID: "p"}}
	st.TwoFactor.RequireKey = true
	if !passkeyOnlyBlocked(st, u, now) {
		t.Fatal("TOTP + passkey admin may use the password and TOTP although a key is required")
	}
}

// fakeIdP serves discovery, keys and a token endpoint for the OIDC tests.
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	claims map[string]any // of the next ID token (nonce filled from the request)
	nonce  string
	code   string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	p := &fakeIdP{}
	p.key, _ = rsa.GenerateKey(rand.Reader, 2048)
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/auth", "token_endpoint": p.srv.URL + "/token", "jwks_uri": p.srv.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "n": b64.EncodeToString(p.key.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(p.key.E)).Bytes())}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		defer p.mu.Unlock()
		if r.PostForm.Get("code") != p.code || r.PostForm.Get("code_verifier") == "" {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		c := map[string]any{"iss": p.srv.URL, "aud": "strata", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": p.nonce}
		for k, v := range p.claims {
			c[k] = v
		}
		hb, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
		pb, _ := json.Marshal(c)
		signed := b64.EncodeToString(hb) + "." + b64.EncodeToString(pb)
		h := sha256.Sum256([]byte(signed))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, h[:])
		json.NewEncoder(w).Encode(map[string]string{"id_token": signed + "." + b64.EncodeToString(sig), "access_token": "at"})
	})
	return p
}

// start follows GET /api/login/oidc/{id}: the provider's authorization URL.
func (p *fakeIdP) start(t *testing.T, c *jarClient, path string) url.Values {
	t.Helper()
	w := c.req("GET", path, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %s", w.Code, w.Header().Get("Location"))
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	q := u.Query()
	if !strings.HasPrefix(u.String(), p.srv.URL+"/auth?") || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != testOrigin+oidcCallback {
		t.Fatalf("authorization URL: %s", u)
	}
	p.mu.Lock()
	p.nonce, p.code = q.Get("nonce"), oidc.Random(8)
	p.mu.Unlock()
	return q
}

func (p *fakeIdP) callback(c *jarClient, q url.Values) (int, string) {
	w := c.req("GET", oidcCallback+"?state="+url.QueryEscape(q.Get("state"))+"&code="+url.QueryEscape(p.code), nil)
	return w.Code, w.Header().Get("Location")
}

func TestOIDCSignIn(t *testing.T) {
	s := authServer(t)
	idp := newFakeIdP(t)
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "marie", Role: "user", PasswordHash: hash},
			&store.User{Username: "admin", Role: "admin", PasswordHash: hash, TOTPSecret: "JBSWY3DPEHPK3PXP"})
		st.OIDC = []*store.OIDCProvider{{ID: "test", Name: "Test IdP", Issuer: idp.srv.URL, ClientID: "strata", ClientSecret: "s", Enabled: true, AllowHTTP: true, TrustMFA: true}}
		return nil
	})
	idp.claims = map[string]any{"sub": "sub-marie", "preferred_username": "marie"}
	anon := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{}}
	errOf := func(loc string) string {
		u, _ := url.Parse(loc)
		_, out := anon.do("GET", "/api/login/oidc/error?id="+u.Query().Get("oidc_error"), nil)
		return out["error"].(string)
	}

	// Not linked, no automatic accounts: refused, the account of the same name is not taken.
	q := idp.start(t, anon, "/api/login/oidc/test")
	if code, loc := idp.callback(anon, q); code != 303 || !strings.Contains(errOf(loc), "no StrataOS account is linked") || anon.jar[cookieName] != "" {
		t.Fatalf("unlinked: %d %s", code, loc)
	}

	// Linking from the profile.
	marie := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("marie", "user", "192.0.2.1", time.Hour)}}
	q = idp.start(t, marie, "/api/login/oidc/test?link=1&next=/system/settings")
	if code, loc := idp.callback(marie, q); code != 303 || loc != "/system/settings" {
		t.Fatalf("link: %d %s", code, loc)
	}
	if _, out := marie.do("GET", "/api/me/oidc", nil); len(out["links"].([]any)) != 1 {
		t.Fatalf("links: %v", out)
	}

	// Now the identity signs in, to a new session, and goes to a local page only.
	q = idp.start(t, anon, "/api/login/oidc/test?next=//evil.example/x")
	code, loc := idp.callback(anon, q)
	if code != 303 || loc != "/" || anon.jar[cookieName] == "" {
		t.Fatalf("sign-in: %d %s", code, loc)
	}
	if _, out := anon.do("GET", "/api/me", nil); out["username"] != "marie" {
		t.Fatalf("me: %v", out)
	}
	// The state is single use and bound to the browser that started it.
	if code, loc := idp.callback(anon, q); code != 303 || !strings.Contains(errOf(loc), "expired") {
		t.Fatalf("state replay: %d %s", code, loc)
	}
	q = idp.start(t, anon, "/api/login/oidc/test")
	stranger := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{}}
	if code, loc := idp.callback(stranger, q); code != 303 || strings.Contains(loc, "oidc_error") == false || stranger.jar[cookieName] != "" {
		t.Fatalf("login CSRF: %d %s", code, loc)
	}
	// A nonce from another sign-in is refused.
	q = idp.start(t, anon, "/api/login/oidc/test")
	idp.nonce = "other"
	if code, loc := idp.callback(anon, q); code != 303 || !strings.Contains(errOf(loc), "nonce") {
		t.Fatalf("nonce: %d %s", code, loc)
	}

	// An account with its own second factor: asked after the provider,
	// unless the provider says it checked one.
	_ = s.store.Update(func(st *store.State) error {
		st.User("admin").OIDC = []store.OIDCLink{{Provider: "test", Issuer: idp.srv.URL, Subject: "sub-admin"}}
		return nil
	})
	idp.claims = map[string]any{"sub": "sub-admin", "preferred_username": "admin", "amr": []string{"pwd"}}
	a := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{}}
	q = idp.start(t, a, "/api/login/oidc/test")
	if code, loc := idp.callback(a, q); code != 303 || loc != "/?signin=2fa" || a.jar[cookieName] != "" || a.jar[pendingCookie] == "" {
		t.Fatalf("second factor after the provider: %d %s", code, loc)
	}
	if _, out := a.do("GET", "/api/login/2fa", nil); out["pending"] != true || out["via"] != "Test IdP" {
		t.Fatalf("pending: %v", out)
	}
	idp.claims["amr"] = []string{"pwd", "otp"}
	a = &jarClient{t: t, s: s, host: testHost, jar: map[string]string{}}
	q = idp.start(t, a, "/api/login/oidc/test")
	if code, loc := idp.callback(a, q); code != 303 || loc != "/" || a.jar[cookieName] == "" {
		t.Fatalf("provider MFA trusted: %d %s", code, loc)
	}

	// Admin view: the secret never comes back; a blank secret keeps it.
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: a.jar[cookieName]}}
	code, out := admin.do("GET", "/api/security/oidc", nil)
	if code != 200 || strings.Contains(string(mustJSON(out)), `"clientSecret"`) || out["redirectUri"] != testOrigin+oidcCallback {
		t.Fatalf("admin view: %d %v", code, out)
	}
	p := out["providers"].([]any)[0].(map[string]any)
	p["clientSecret"] = ""
	if code, out := admin.do("PUT", "/api/security/oidc/test", p); code != 200 {
		t.Fatalf("update: %d %v", code, out)
	}
	if pr, _ := s.oidcProvider("test"); pr.ClientSecret != "s" {
		t.Fatal("blank secret did not keep the saved one")
	}
	p["issuer"] = "http://idp.example"
	p["allowHttp"] = false
	if code, _ := admin.do("PUT", "/api/security/oidc/test", p); code != 400 {
		t.Fatal("plain HTTP issuer accepted")
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/files?path=/srv": "/files?path=/srv", "": "/", "//evil.example": "/", "/\\evil.example": "/",
		"https://evil.example": "/", "/api/logout": "/", "relative": "/", "/x\r\nSet-Cookie: a": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
