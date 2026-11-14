package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"strataos/internal/oidc"
	"strataos/internal/proxy"
	"strataos/internal/sso"
	"strataos/internal/store"
)

const ssoIss = "https://nas.example.com:7443"
const ssoRP = "https://grafana.example.com/login/generic_oauth"

var ssoGroups = map[string][]string{"alice": {"family"}, "erin": {"family"}}

func ssoServer(t *testing.T) *Server {
	t.Helper()
	ssoUserGroups = func(u string) []string { return ssoGroups[u] }
	t.Cleanup(func() { ssoForget() })
	ssoForget()
	s := newTestServer(t)
	s.authRoutes()
	s.ssoRoutes()
	if s.sso == nil {
		t.Fatal("sso did not start")
	}
	_ = s.store.Update(func(st *store.State) error {
		for _, u := range []string{"alice", "bob", "carol", "erin"} {
			st.Users = append(st.Users, &store.User{Username: u, Role: "user", FullName: strings.ToUpper(u[:1]) + u[1:], Email: u + "@example.com", Disabled: u == "carol"})
		}
		st.Users = append(st.Users, &store.User{Username: "root1", Role: "admin"})
		st.SSO = store.SSO{Enabled: true, Issuer: ssoIss, Clients: []*store.SSOClient{
			{ID: "grafana", Name: "Grafana", Kind: "oidc", Secret: "s3cret", RedirectURIs: []string{ssoRP},
				PostLogoutURIs: []string{"https://grafana.example.com/login"}, Groups: []string{"family"}},
		}}
		return nil
	})
	return s
}

func (c *jarClient) raw(r *http.Request) *httptest.ResponseRecorder {
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

func browser(t *testing.T, s *Server, user string) *jarClient {
	jar := map[string]string{}
	if user != "" {
		jar[cookieName] = s.sessions.create(user, "user", "192.0.2.1", time.Hour)
	}
	return &jarClient{t: t, s: s, host: testHost, jar: jar}
}

const ssoVerifier = "verifier-verifier-verifier-verifier-verifier-1"

func ssoAuthQuery(extra ...string) string {
	q := url.Values{"client_id": {"grafana"}, "redirect_uri": {ssoRP}, "response_type": {"code"}, "scope": {"openid profile email groups"},
		"state": {"xyz"}, "nonce": {"nn"}, "code_challenge": {sso.S256(ssoVerifier)}, "code_challenge_method": {"S256"}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q.Encode()
}

// start opens the authorization URL like a browser coming from the app: no
// Origin, and the Strict session cookie of the NAS is not sent.
func start(t *testing.T, b *jarClient, query string) string {
	t.Helper()
	sess := b.jar[cookieName]
	delete(b.jar, cookieName)
	w := b.raw(httptest.NewRequest("GET", "https://"+testHost+"/api/sso/authorize?"+query, nil))
	if sess != "" {
		b.jar[cookieName] = sess
	}
	loc := w.Header().Get("Location")
	if w.Code != 302 || !strings.HasPrefix(loc, "/sso/r/") {
		t.Fatalf("authorize: %d %q %s", w.Code, loc, w.Body)
	}
	return strings.TrimPrefix(loc, "/sso/r/")
}

func approve(t *testing.T, b *jarClient, id string, yes bool) (int, string) {
	t.Helper()
	code, out := b.do("POST", "/api/sso/requests/"+id, map[string]bool{"approve": yes})
	r, _ := out["redirect"].(string)
	return code, r
}

func tokenReq(s *Server, form url.Values, id, secret string) (int, map[string]any) {
	r := httptest.NewRequest("POST", "http://host.docker.internal:7070/api/sso/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if id != "" {
		r.SetBasicAuth(id, secret)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func codeOf(t *testing.T, redirect string) string {
	t.Helper()
	u, err := url.Parse(redirect)
	if err != nil || !strings.HasPrefix(redirect, ssoRP+"?") || u.Query().Get("state") != "xyz" || u.Query().Get("iss") != ssoIss {
		t.Fatalf("redirect %q", redirect)
	}
	return u.Query().Get("code")
}

func userinfo(s *Server, at string) int {
	r := httptest.NewRequest("GET", "https://"+testHost+"/api/sso/userinfo", nil)
	r.Header.Set("Authorization", "Bearer "+at)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w.Code
}

func TestSSOCodeFlow(t *testing.T) {
	s := ssoServer(t)
	b := browser(t, s, "alice")
	id := start(t, b, ssoAuthQuery())
	code, v := b.do("GET", "/api/sso/requests/"+id, nil)
	if code != 200 || v["consent"] != true || v["signedIn"] != true || v["client"] != "Grafana" {
		t.Fatalf("view: %d %v", code, v)
	}
	// CSRF: another site cannot post the approval, nor can another browser.
	r := httptest.NewRequest("POST", "https://"+testHost+"/api/sso/requests/"+id, strings.NewReader(`{"approve":true}`))
	r.Header.Set("Origin", "https://evil.example.com")
	if w := b.raw(r); w.Code != 403 {
		t.Fatalf("cross-site approval: %d", w.Code)
	}
	other := browser(t, s, "alice")
	if c, _ := approve(t, other, id, true); c != 403 {
		t.Fatalf("approval from another browser: %d", c)
	}
	c, redirect := approve(t, b, id, true)
	if c != 200 {
		t.Fatal(c)
	}
	// The request is answered once.
	if c, _ := approve(t, b, id, true); c != 404 {
		t.Fatalf("request reused: %d", c)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {codeOf(t, redirect)}, "redirect_uri": {ssoRP}, "code_verifier": {ssoVerifier}}
	if c, _ := tokenReq(s, form, "grafana", "wrong"); c != 401 {
		t.Fatalf("wrong secret: %d", c)
	}
	c, tok := tokenReq(s, form, "grafana", "s3cret")
	if c != 200 {
		t.Fatalf("token: %d %v", c, tok)
	}
	// The ID token, checked by the OIDC client of StrataOS itself.
	jw := httptest.NewRecorder()
	s.ServeHTTP(jw, httptest.NewRequest("GET", "https://"+testHost+"/api/sso/jwks", nil))
	ks, err := oidc.ParseJWKS(jw.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	cl, err := oidc.VerifyIDToken(tok["id_token"].(string), ks, oidc.Expect{Issuer: ssoIss, ClientID: "grafana", Nonce: "nn"})
	if err != nil {
		t.Fatal(err)
	}
	if cl.String("preferred_username") != "alice" || cl.String("sub") == "alice" || len(cl.Strings("groups")) != 1 {
		t.Fatalf("claims %v", cl)
	}
	// The subject is stable and random.
	var sub string
	s.store.Read(func(st *store.State) { sub = st.User("alice").SSOSubject })
	if cl.String("sub") != sub || len(sub) != 32 {
		t.Fatal("subject")
	}
	if c := userinfo(s, tok["access_token"].(string)); c != 200 {
		t.Fatal("userinfo", c)
	}
	// A code presented again revokes what it gave.
	if c, _ := tokenReq(s, form, "grafana", "s3cret"); c != 400 {
		t.Fatalf("code reused: %d", c)
	}
	if userinfo(s, tok["access_token"].(string)) != 401 {
		t.Fatal("tokens of a reused code kept")
	}
	id = start(t, b, ssoAuthQuery())
	_, redirect = approve(t, b, id, true)
	form.Set("code", codeOf(t, redirect))
	_, tok = tokenReq(s, form, "grafana", "s3cret")
	// Second time: no consent screen; the user page lists the app.
	id = start(t, b, ssoAuthQuery())
	if _, v := b.do("GET", "/api/sso/requests/"+id, nil); v["consent"] == true {
		t.Fatal("consent asked again")
	}
	_, apps := b.do("GET", "/api/me/sso", nil)
	if !strings.Contains(b.req("GET", "/api/me/sso", nil).Body.String(), `"client":"grafana"`) {
		t.Fatalf("my apps: %v", apps)
	}
	// Revoked from the user page: tokens end and the consent is asked again.
	if c, _ := b.do("DELETE", "/api/me/sso/grafana", nil); c != 200 {
		t.Fatal(c)
	}
	if userinfo(s, tok["access_token"].(string)) != 401 {
		t.Fatal("token survived the revocation")
	}
	if c, _ := tokenReq(s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}}, "grafana", "s3cret"); c != 400 {
		t.Fatal("refresh token survived the revocation")
	}
	if _, v := b.do("GET", "/api/sso/requests/"+id, nil); v["consent"] != true {
		t.Fatal("consent not asked after revocation")
	}
}

func TestSSORefusals(t *testing.T) {
	s := ssoServer(t)
	// Unregistered redirect URI: a page, never a redirect.
	b := browser(t, s, "alice")
	w := b.raw(httptest.NewRequest("GET", "https://"+testHost+"/api/sso/authorize?"+ssoAuthQuery("redirect_uri", "https://evil.example.com/cb"), nil))
	if w.Code != 400 || w.Header().Get("Location") != "" {
		t.Fatalf("bad redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
	// plain PKCE: back to the app with an error.
	w = b.raw(httptest.NewRequest("GET", "https://"+testHost+"/api/sso/authorize?"+ssoAuthQuery("code_challenge_method", "plain"), nil))
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "error=invalid_request") {
		t.Fatalf("plain PKCE: %d %q", w.Code, w.Header().Get("Location"))
	}
	// Not in an allowed group.
	bob := browser(t, s, "bob")
	id := start(t, bob, ssoAuthQuery())
	if _, v := bob.do("GET", "/api/sso/requests/"+id, nil); v["denied"] == nil {
		t.Fatalf("bob allowed: %v", v)
	}
	if _, r := approve(t, bob, id, true); !strings.Contains(r, "error=access_denied") {
		t.Fatalf("bob approval: %q", r)
	}
	// Disabled user: the session does not count.
	carol := browser(t, s, "carol")
	id = start(t, carol, ssoAuthQuery())
	if _, v := carol.do("GET", "/api/sso/requests/"+id, nil); v["signedIn"] != false {
		t.Fatalf("disabled user signed in: %v", v)
	}
	if c, _ := approve(t, carol, id, true); c != 401 {
		t.Fatalf("disabled user approved: %d", c)
	}
	// prompt=none without session.
	anon := browser(t, s, "")
	id = start(t, anon, ssoAuthQuery("prompt", "none"))
	if _, v := anon.do("GET", "/api/sso/requests/"+id, nil); !strings.Contains(v["redirect"].(string), "error=login_required") {
		t.Fatalf("prompt=none: %v", v)
	}
	// prompt=none needing consent.
	b = browser(t, s, "alice")
	id = start(t, b, ssoAuthQuery("prompt", "none"))
	if _, v := b.do("GET", "/api/sso/requests/"+id, nil); !strings.Contains(v["redirect"].(string), "error=consent_required") {
		t.Fatalf("prompt=none consent: %v", v)
	}
	// Refused by the user.
	id = start(t, b, ssoAuthQuery())
	if _, r := approve(t, b, id, false); !strings.Contains(r, "error=access_denied") || !strings.Contains(r, "state=xyz") {
		t.Fatalf("deny: %q", r)
	}
	// Second factor required by the app.
	_ = s.store.Update(func(st *store.State) error { st.SSOClient("grafana").Require2FA = true; return nil })
	id = start(t, b, ssoAuthQuery())
	if _, v := b.do("GET", "/api/sso/requests/"+id, nil); v["denied"] == nil {
		t.Fatalf("no second factor allowed: %v", v)
	}
	_ = s.store.Update(func(st *store.State) error { st.User("alice").TOTPSecret = "JBSWY3DPEHPK3PXP"; return nil })
	id = start(t, b, ssoAuthQuery())
	if _, v := b.do("GET", "/api/sso/requests/"+id, nil); v["denied"] != nil {
		t.Fatalf("second factor refused: %v", v)
	}
	// prompt=login with an older session.
	id = start(t, b, ssoAuthQuery("prompt", "login"))
	if _, v := b.do("GET", "/api/sso/requests/"+id, nil); v["reauth"] != true {
		t.Fatalf("prompt=login: %v", v)
	}
	if c, _ := approve(t, b, id, true); c != 409 {
		t.Fatalf("prompt=login approved: %d", c)
	}
	// Request of another browser.
	id = start(t, b, ssoAuthQuery())
	if c, _ := browser(t, s, "alice").do("GET", "/api/sso/requests/"+id, nil); c != 403 {
		t.Fatalf("other browser: %d", c)
	}
	// SSO off: nothing answers.
	_ = s.store.Update(func(st *store.State) error { st.SSO.Enabled = false; return nil })
	w = b.raw(httptest.NewRequest("GET", "https://"+testHost+"/.well-known/openid-configuration", nil))
	if w.Code != 404 {
		t.Fatalf("discovery while off: %d", w.Code)
	}
}

func TestSSOLogout(t *testing.T) {
	s := ssoServer(t)
	b := browser(t, s, "alice")
	id := start(t, b, ssoAuthQuery())
	_, redirect := approve(t, b, id, true)
	_, tok := tokenReq(s, url.Values{"grant_type": {"authorization_code"}, "code": {codeOf(t, redirect)}, "redirect_uri": {ssoRP}, "code_verifier": {ssoVerifier}}, "grafana", "s3cret")
	idt := tok["id_token"].(string)
	logout := func(q url.Values) *httptest.ResponseRecorder {
		sess := b.jar[cookieName]
		delete(b.jar, cookieName)
		defer func() { b.jar[cookieName] = sess }()
		return b.raw(httptest.NewRequest("GET", "https://"+testHost+"/api/sso/logout?"+q.Encode(), nil))
	}
	// Open redirect refused.
	if w := logout(url.Values{"id_token_hint": {idt}, "post_logout_redirect_uri": {"https://evil.example.com/"}}); w.Code != 400 {
		t.Fatalf("unregistered post-logout: %d", w.Code)
	}
	if w := logout(url.Values{"post_logout_redirect_uri": {"https://grafana.example.com/login"}}); w.Code != 400 {
		t.Fatalf("post-logout without client: %d", w.Code)
	}
	if w := logout(url.Values{"id_token_hint": {idt + "x"}}); w.Code != 400 {
		t.Fatalf("forged hint: %d", w.Code)
	}
	w := logout(url.Values{"id_token_hint": {idt}, "post_logout_redirect_uri": {"https://grafana.example.com/login"}, "state": {"st"}})
	loc := w.Header().Get("Location")
	if w.Code != 302 || !strings.HasPrefix(loc, "/sso/logout/") {
		t.Fatalf("logout: %d %q", w.Code, loc)
	}
	lid := strings.TrimPrefix(loc, "/sso/logout/")
	if _, v := b.do("GET", "/api/sso/logouts/"+lid, nil); v["sameUser"] != true {
		t.Fatalf("logout view: %v", v)
	}
	r := httptest.NewRequest("POST", "https://"+testHost+"/api/sso/logouts/"+lid, nil)
	r.Header.Set("Origin", "https://evil.example.com")
	if w := b.raw(r); w.Code != 403 {
		t.Fatalf("cross-site logout: %d", w.Code)
	}
	sess := b.jar[cookieName]
	code, out := b.do("POST", "/api/sso/logouts/"+lid, nil)
	if code != 200 || out["redirect"] != "https://grafana.example.com/login?state=st" {
		t.Fatalf("logout: %d %v", code, out)
	}
	if s.sessions.get(sess) != nil {
		t.Fatal("NAS session kept")
	}
	if userinfo(s, tok["access_token"].(string)) != 401 {
		t.Fatal("access token kept after logout")
	}
}

func TestSSOForwardAuth(t *testing.T) {
	s := ssoServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Remote.Proxy.Domains = append(st.Remote.Proxy.Domains, &store.AppDomain{ID: "d1", Host: "kuma.example.com", Kind: "url", Target: "http://127.0.0.1:3001", SSO: true})
		return nil
	})
	s.fwdSync()
	_ = s.store.Update(func(st *store.State) error {
		c := st.SSOClient("fwd-d1")
		if c == nil {
			t.Fatal("no forward client")
		}
		c.AllUsers, c.Groups = false, []string{"family"}
		return nil
	})
	rt := &proxy.Route{Host: "kuma.example.com", Auth: true}
	app := map[string]string{} // cookies of the protected host
	call := func(method, path string, hdr ...string) (*httptest.ResponseRecorder, http.Header, bool) {
		r := httptest.NewRequest(method, "https://kuma.example.com"+path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		for k, v := range app {
			r.AddCookie(&http.Cookie{Name: k, Value: v})
		}
		w := httptest.NewRecorder()
		id, done := s.forwardAuth(w, r, rt)
		for _, c := range w.Result().Cookies() {
			if c.MaxAge < 0 {
				delete(app, c.Name)
			} else {
				app[c.Name] = c.Value
				if !c.Secure || !c.HttpOnly || c.Domain != "" {
					t.Errorf("cookie %s not host-only secure", c.Name)
				}
			}
		}
		return w, id, done
	}
	// Scripts get 401, pages go to the sign-in.
	if w, _, done := call("GET", "/api/x", "Sec-Fetch-Mode", "cors"); !done || w.Code != 401 {
		t.Fatalf("xhr: %d", w.Code)
	}
	w, _, _ := call("GET", "/dashboard?x=1")
	loc := w.Header().Get("Location")
	if w.Code != 302 || !strings.HasPrefix(loc, ssoIss+"/api/sso/authorize?") {
		t.Fatalf("start: %d %q", w.Code, loc)
	}
	u, _ := url.Parse(loc)
	b := browser(t, s, "alice")
	id := start(t, b, u.RawQuery)
	if _, v := b.do("GET", "/api/sso/requests/"+id, nil); v["consent"] == true {
		t.Fatal("forward clients are trusted by default")
	}
	_, redirect := approve(t, b, id, true)
	cb, _ := url.Parse(redirect)
	if cb.Host != "kuma.example.com" || cb.Path != fwdCallback {
		t.Fatalf("callback %q", redirect)
	}
	// The code cannot be taken to the token endpoint.
	if c, _ := tokenReq(s, url.Values{"grant_type": {"authorization_code"}, "client_id": {"fwd-d1"}, "code": {cb.Query().Get("code")}, "redirect_uri": {fwdRedirect("kuma.example.com")}}, "", ""); c != 400 {
		t.Fatalf("internal client at the token endpoint: %d", c)
	}
	// Without the state cookie (another browser): refused.
	saved := map[string]string{}
	for k, v := range app {
		saved[k] = v
	}
	clear(app)
	if w, _, _ := call("GET", cb.RequestURI()); w.Code != 400 || app[proxy.FwdCookie] != "" {
		t.Fatalf("callback without state cookie: %d", w.Code)
	}
	for k, v := range saved {
		app[k] = v
	}
	w, _, _ = call("GET", cb.RequestURI())
	if w.Code != 302 || w.Header().Get("Location") != "/dashboard?x=1" || app[proxy.FwdCookie] == "" {
		t.Fatalf("callback: %d %q", w.Code, w.Header().Get("Location"))
	}
	if _, id, done := call("GET", "/dashboard"); done || id.Get("Remote-User") != "alice" || id.Get("Remote-Groups") != "family" || id.Get("Remote-Email") != "alice@example.com" {
		t.Fatalf("identity: %v %v", id, done)
	}
	// Replayed callback: the flow is gone.
	if w, _, _ := call("GET", cb.RequestURI()); w.Code != 400 {
		t.Fatalf("callback replay: %d", w.Code)
	}
	// Removed from the group: refused within the cache time (cache cleared here).
	ssoGroups["alice"] = nil
	ssoForget()
	if w, _, done := call("GET", "/"); !done || w.Code != 403 {
		t.Fatalf("not in group: %d", w.Code)
	}
	ssoGroups["alice"] = []string{"family"}
	ssoForget()
	// The cookie of one protected host is useless on another.
	_ = s.store.Update(func(st *store.State) error {
		st.Remote.Proxy.Domains = append(st.Remote.Proxy.Domains, &store.AppDomain{ID: "d2", Host: "other.example.com", Kind: "url", Target: "http://127.0.0.1:3002", SSO: true})
		return nil
	})
	s.fwdSync()
	r := httptest.NewRequest("GET", "https://other.example.com/", nil)
	r.AddCookie(&http.Cookie{Name: proxy.FwdCookie, Value: app[proxy.FwdCookie]})
	ow := httptest.NewRecorder()
	if _, done := s.forwardAuth(ow, r, &proxy.Route{Host: "other.example.com", Auth: true}); !done || ow.Code != 302 {
		t.Fatalf("cookie accepted on another host: %d", ow.Code)
	}
	// Signing out of the NAS ends it.
	if c, _ := b.do("POST", "/api/logout", nil); c != 200 {
		t.Fatal(c)
	}
	if w, _, done := call("GET", "/"); !done || w.Code != 302 {
		t.Fatalf("after NAS logout: %d", w.Code)
	}
	// Protection removed: the client goes.
	_ = s.store.Update(func(st *store.State) error { st.Remote.Proxy.Domains[0].SSO = false; return nil })
	s.fwdSync()
	s.store.Read(func(st *store.State) {
		if st.SSOClient("fwd-d1") != nil {
			t.Error("forward client kept")
		}
	})
}

func TestSSOAdmin(t *testing.T) {
	s := ssoServer(t)
	a := browser(t, s, "")
	a.jar[cookieName] = s.sessions.create("root1", "admin", "192.0.2.1", time.Hour)
	code, out := a.do("POST", "/api/sso/clients", map[string]any{"name": "My App", "redirectUris": []string{"https://app.example.com/cb"}, "allUsers": true})
	secret, _ := out["secret"].(string)
	if code != 200 || len(secret) != 64 {
		t.Fatalf("create: %d %v", code, out)
	}
	for _, bad := range []map[string]any{
		{"name": "x", "redirectUris": []string{"https://*.example.com/cb"}},
		{"name": "x", "redirectUris": []string{"javascript:alert(1)"}},
		{"name": "x", "redirectUris": []string{"https://a.example.com/cb#frag"}},
		{"name": "x", "redirectUris": []string{}},
		{"name": "", "redirectUris": []string{"https://a.example.com/cb"}},
	} {
		if c, _ := a.do("POST", "/api/sso/clients", bad); c != 400 {
			t.Errorf("accepted %v", bad)
		}
	}
	body := a.req("GET", "/api/sso", nil).Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, "s3cret") {
		t.Fatal("secret sent back")
	}
	if c, _ := browser(t, s, "alice").do("GET", "/api/sso", nil); c != 403 {
		t.Fatal("non-admin read the settings")
	}
	for in, ok := range map[string]bool{"https://nas.example.com": true, "https://nas.example.com:7443/": true, "http://192.168.1.5:7070": true,
		"https://nas.example.com/sso": false, "ftp://nas": false, "https://u:p@nas.example.com": false, "nas.example.com": false, "https://nas.example.com?a": false} {
		if _, err := validIssuer(in); (err == nil) != ok {
			t.Errorf("validIssuer(%q): %v", in, err)
		}
	}
	// Rotating the secret: the old one stops working at once.
	var cid string
	s.store.Read(func(st *store.State) { cid = st.SSO.Clients[1].ID })
	_, out = a.do("POST", "/api/sso/clients/"+cid+"/secret", nil)
	if out["secret"] == secret || s.ssoFind(cid).CheckSecret(secret) {
		t.Fatal("secret not rotated")
	}
}
