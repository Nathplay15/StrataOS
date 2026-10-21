package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"

	"strataos/internal/store"
)

func TestDeriveRelyingParty(t *testing.T) {
	cases := []struct {
		host        string
		https       bool
		id, origin  string
		err         error
		description string
	}{
		{"strataos.local:7443", true, "strataos.local", "https://strataos.local:7443", nil, "mDNS name"},
		{"NAS.Example.COM", true, "nas.example.com", "https://nas.example.com", nil, "domain, case folded"},
		{"nas.example.com:443", true, "nas.example.com", "https://nas.example.com", nil, "default port dropped"},
		{"nas.example.com.", true, "nas.example.com", "https://nas.example.com", nil, "trailing dot"},
		{"nas.tail1234.ts.net:7443", true, "nas.tail1234.ts.net", "https://nas.tail1234.ts.net:7443", nil, "MagicDNS"},
		{"localhost:7070", false, "localhost", "http://localhost:7070", nil, "localhost over HTTP"},
		{"app.localhost:80", false, "app.localhost", "http://app.localhost", nil, "localhost subdomain"},
		{"strataos.local:7070", false, "", "", errPasskeyHTTP, "name over plain HTTP"},
		{"192.168.1.10:7443", true, "", "", errPasskeyIP, "IPv4"},
		{"[fe80::1]:7443", true, "", "", errPasskeyIP, "IPv6"},
		{"[::1]", false, "", "", errPasskeyIP, "IPv6 without port"},
		{"10.1", true, "", "", errPasskeyIP, "short IPv4 form"},
		{"0x7f.1", true, "", "", errPasskeyIP, "hex IPv4 form"},
		{"", true, "", "", errPasskeyHost, "empty"},
		{"bad_name.local", true, "", "", errPasskeyHost, "underscore"},
		{"nas.local:99999", true, "", "", errPasskeyHost, "port out of range"},
		{"nas.local:07443", true, "", "", errPasskeyHost, "odd port"},
		{"-nas.local", true, "", "", errPasskeyHost, "leading hyphen"},
	}
	for _, c := range cases {
		rp, err := deriveRelyingParty(c.host, c.https)
		if !errors.Is(err, c.err) {
			t.Errorf("%s (%q): err %v, want %v", c.description, c.host, err, c.err)
			continue
		}
		if rp.ID != c.id || rp.Origin != c.origin {
			t.Errorf("%s (%q): got %+v, want %s %s", c.description, c.host, rp, c.id, c.origin)
		}
	}
}

func TestRequestRelyingPartyBehindLocalTunnel(t *testing.T) {
	r := httptest.NewRequest("GET", "http://nas.example.com/api/login/passkey", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if _, err := requestRelyingParty(r); !errors.Is(err, errPasskeyHTTP) {
		t.Fatalf("forwarded proto trusted from a remote peer: %v", err)
	}
	r.RemoteAddr = "127.0.0.1:5000"
	rp, err := requestRelyingParty(r)
	if err != nil || rp.Origin != "https://nas.example.com" {
		t.Fatalf("local tunnel: %+v %v", rp, err)
	}
}

func TestPasskeyFlowsSingleUseAndExpiry(t *testing.T) {
	f := &passkeyFlows{m: map[string]*passkeyFlow{}}
	now := time.Now()
	id, err := f.put(&passkeyFlow{kind: "login"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if f.take(id, "register", now) != nil {
		t.Fatal("flow of another kind accepted")
	}
	if f.take(id, "login", now) != nil {
		t.Fatal("a failed take must still consume the flow")
	}
	id, _ = f.put(&passkeyFlow{kind: "login"}, now)
	if f.take(id, "login", now) == nil {
		t.Fatal("fresh flow refused")
	}
	if f.take(id, "login", now) != nil {
		t.Fatal("flow used twice")
	}
	id, _ = f.put(&passkeyFlow{kind: "login"}, now)
	if f.take(id, "login", now.Add(passkeyTimeout+time.Minute)) != nil {
		t.Fatal("expired flow accepted")
	}
	for i := 0; i < maxPasskeyFlows; i++ {
		if _, err := f.put(&passkeyFlow{kind: "login"}, now); err != nil {
			t.Fatalf("flow %d refused: %v", i, err)
		}
	}
	if _, err := f.put(&passkeyFlow{kind: "login"}, now); err == nil {
		t.Fatal("pending flows not bounded")
	}
	// Expired ones are pruned and make room.
	if _, err := f.put(&passkeyFlow{kind: "login"}, now.Add(passkeyTimeout+time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestValidPasskeyName(t *testing.T) {
	if n, err := validPasskeyName("  "); err != nil || n != "Passkey" {
		t.Fatal(n, err)
	}
	if _, err := validPasskeyName("a\nb"); err == nil {
		t.Fatal("control character accepted")
	}
	if _, err := validPasskeyName(strings.Repeat("é", 65)); err == nil {
		t.Fatal("long name accepted")
	}
}

// softKey is a software authenticator: a P-256 key with "none" attestation.
type softKey struct {
	priv  *ecdsa.PrivateKey
	id    []byte
	user  []byte // user handle given at registration
	count uint32
}

var b64 = base64.RawURLEncoding

func newSoftKey(t *testing.T) *softKey {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{priv: priv, id: id}
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

func authData(rpID string, flags byte, count uint32, attested []byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	out := append(h[:], flags)
	out = binary.BigEndian.AppendUint32(out, count)
	return append(out, attested...)
}

const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40
)

// create answers navigator.credentials.create() options.
func (k *softKey) create(t *testing.T, opts map[string]any, origin string) map[string]any {
	pk := opts["publicKey"].(map[string]any)
	rpID := pk["rp"].(map[string]any)["id"].(string)
	user, err := b64.DecodeString(pk["user"].(map[string]any)["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	k.user = user
	x, y := make([]byte, 32), make([]byte, 32)
	k.priv.X.FillBytes(x)
	k.priv.Y.FillBytes(y)
	cose, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		t.Fatal(err)
	}
	att := make([]byte, 16) // zero AAGUID
	att = binary.BigEndian.AppendUint16(att, uint16(len(k.id)))
	att = append(att, k.id...)
	att = append(att, cose...)
	ao, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData(rpID, flagUP|flagUV|flagAT, 0, att)})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(clientData("webauthn.create", pk["challenge"].(string), origin)),
			"attestationObject": b64.EncodeToString(ao), "transports": []string{"internal"}},
		"clientExtensionResults": map[string]any{}}
}

// get answers navigator.credentials.get() options; tamper breaks the signature.
func (k *softKey) get(t *testing.T, opts map[string]any, origin string, tamper bool) map[string]any {
	pk := opts["publicKey"].(map[string]any)
	k.count++
	ad := authData(pk["rpId"].(string), flagUP|flagUV, k.count, nil)
	cd := clientData("webauthn.get", pk["challenge"].(string), origin)
	h := sha256.Sum256(cd)
	d := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, d[:])
	if err != nil {
		t.Fatal(err)
	}
	if tamper {
		d[0] ^= 1
		sig, _ = ecdsa.SignASN1(rand.Reader, k.priv, d[:])
	}
	return map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(k.user)},
		"clientExtensionResults": map[string]any{}}
}

const testHost, testOrigin = "nas.example.com:7443", "https://nas.example.com:7443"

type pkClient struct {
	t      *testing.T
	s      *Server
	cookie string
	host   string
}

func (c *pkClient) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, "https://"+c.host+path, strings.NewReader(string(b)))
	r.Header.Set("Origin", "https://"+c.host)
	r.Header.Set("Content-Type", "application/json")
	if c.cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	}
	w := httptest.NewRecorder()
	c.s.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName && ck.Value != "" && c.cookie == "" {
			out["_cookie"] = ck.Value
		}
	}
	return w.Code, out
}

func TestPasskeyRoundTrip(t *testing.T) {
	s := newTestServer(t)
	s.passkeyRoutes()
	s.userRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users,
			&store.User{Username: "admin", Role: "admin", PasswordHash: hash, TOTPSecret: "JBSWY3DPEHPK3PXP"},
			&store.User{Username: "bob", Role: "user", PasswordHash: hash})
		return nil
	})
	admin := &pkClient{t: t, s: s, cookie: s.sessions.create("admin", "admin", "192.0.2.1", time.Hour), host: testHost}
	anon := &pkClient{t: t, s: s, host: testHost}

	// Registration: password first, then the authenticator's answer.
	if code, _ := admin.do("POST", "/api/me/passkeys/begin", map[string]any{"password": "wrong", "name": "Laptop"}); code != 400 {
		t.Fatalf("wrong password: %d", code)
	}
	ipClient := &pkClient{t: t, s: s, cookie: admin.cookie, host: "192.168.1.10:7443"}
	if code, out := ipClient.do("POST", "/api/me/passkeys/begin", map[string]any{"password": "Secret-Pass-1"}); code != 400 || !strings.Contains(out["error"].(string), "strataos.local") {
		t.Fatalf("IP address: %d %v", code, out)
	}
	code, opts := admin.do("POST", "/api/me/passkeys/begin", map[string]any{"password": "Secret-Pass-1", "name": "Laptop"})
	if code != 200 {
		t.Fatalf("begin: %d %v", code, opts)
	}
	pk := opts["publicKey"].(map[string]any)
	if pk["authenticatorSelection"].(map[string]any)["userVerification"] != "required" || pk["rp"].(map[string]any)["id"] != "nas.example.com" {
		t.Fatalf("creation options: %v", pk)
	}
	key := newSoftKey(t)
	// A credential made for another origin is refused.
	code, opts2 := admin.do("POST", "/api/me/passkeys/begin", map[string]any{"password": "Secret-Pass-1"})
	if code != 200 {
		t.Fatal(code)
	}
	if code, _ := admin.do("POST", "/api/me/passkeys/finish", map[string]any{"flow": opts2["flow"], "credential": key.create(t, opts2, "https://evil.example.com")}); code != 400 {
		t.Fatalf("foreign origin: %d", code)
	}
	if code, out := admin.do("POST", "/api/me/passkeys/finish", map[string]any{"flow": opts["flow"], "credential": key.create(t, opts, testOrigin)}); code != 200 || out["rpId"] != "nas.example.com" || out["name"] != "Laptop" {
		t.Fatalf("finish: %d %v", code, out)
	}
	if code, _ := admin.do("POST", "/api/me/passkeys/finish", map[string]any{"flow": opts["flow"], "credential": key.create(t, opts, testOrigin)}); code != 400 {
		t.Fatalf("registration flow reused: %d", code)
	}
	_, list := admin.do("GET", "/api/me/passkeys", nil)
	ps := list["passkeys"].([]any)
	if len(ps) != 1 || ps[0].(map[string]any)["here"] != true {
		t.Fatalf("list: %v", list)
	}
	pid := ps[0].(map[string]any)["id"].(string)

	login := func(c *pkClient, user string, tamper bool) (int, map[string]any) {
		_, o := c.do("POST", "/api/login/passkey/begin", map[string]any{"username": user})
		return c.do("POST", "/api/login/passkey/finish", map[string]any{"flow": o["flow"], "credential": key.get(t, o, "https://"+c.host, tamper)})
	}

	// Discoverable sign-in; TOTP is not asked for: the passkey is two factors.
	code, me := login(anon, "", false)
	if code != 200 || me["username"] != "admin" || me["_cookie"] == nil {
		t.Fatalf("discoverable login: %d %v", code, me)
	}
	// Username first.
	_, o := anon.do("POST", "/api/login/passkey/begin", map[string]any{"username": "Admin"})
	if allow := o["publicKey"].(map[string]any)["allowCredentials"].([]any); len(allow) != 1 {
		t.Fatalf("allowCredentials: %v", o)
	}
	if code, _ := anon.do("POST", "/api/login/passkey/finish", map[string]any{"flow": o["flow"], "credential": key.get(t, o, testOrigin, false)}); code != 200 {
		t.Fatalf("username-first login: %d", code)
	}
	if code, _ := login(anon, "bob", false); code != 401 {
		t.Fatalf("passkey of another account accepted: %d", code)
	}
	if code, _ := login(anon, "", true); code != 401 {
		t.Fatalf("bad signature accepted: %d", code)
	}
	// The flow is single use.
	_, o = anon.do("POST", "/api/login/passkey/begin", map[string]any{})
	cred := key.get(t, o, testOrigin, false)
	if code, _ := anon.do("POST", "/api/login/passkey/finish", map[string]any{"flow": o["flow"], "credential": cred}); code != 200 {
		t.Fatal(code)
	}
	if code, _ := anon.do("POST", "/api/login/passkey/finish", map[string]any{"flow": o["flow"], "credential": cred}); code != 400 {
		t.Fatalf("replay: %d", code)
	}
	// Bound to the name: the same passkey on another name of the NAS is refused.
	other := &pkClient{t: t, s: s, host: "strataos.local:7443"}
	if code, _ := login(other, "", false); code != 401 {
		t.Fatalf("passkey used on another name: %d", code)
	}
	if code, out := anon.do("POST", "/api/login/passkey/begin", nil); code != 200 || out["flow"] == nil {
		t.Fatalf("begin without body: %d", code)
	}
	ipAnon := &pkClient{t: t, s: s, host: "10.0.0.5:7443"}
	if code, _ := ipAnon.do("POST", "/api/login/passkey/begin", map[string]any{}); code != 400 {
		t.Fatalf("begin on an IP: %d", code)
	}
	if _, out := ipAnon.do("GET", "/api/login/passkey", nil); out["available"] != false {
		t.Fatalf("availability on an IP: %v", out)
	}

	// Sign count going backwards: cloned authenticator.
	key.count = 0
	if code, _ := login(anon, "", false); code != 401 {
		t.Fatalf("sign count regression accepted: %d", code)
	}
	key.count = 100

	// A disabled account stays disabled.
	_ = s.store.Update(func(st *store.State) error { st.User("admin").Disabled = true; return nil })
	if code, _ := login(anon, "", false); code != 401 {
		t.Fatalf("disabled user signed in: %d", code)
	}
	_ = s.store.Update(func(st *store.State) error { st.User("admin").Disabled = false; return nil })
	s.limiter.reset("192.0.2.1")
	if code, _ := login(anon, "", false); code != 200 {
		t.Fatalf("login after re-enabling: %d", code)
	}

	// Rename, admin count, delete.
	if code, _ := admin.do("PUT", "/api/me/passkeys/"+pid, map[string]any{"name": "Phone"}); code != 200 {
		t.Fatal(code)
	}
	var stored store.User
	s.store.Read(func(st *store.State) { stored = copyUser(st.User("admin")) })
	if k := stored.PasskeyByID(pid); k == nil || k.Name != "Phone" || k.LastUsed.IsZero() || k.SignCount != 102 {
		t.Fatalf("stored passkey: %+v", k)
	}
	r := httptest.NewRequest("GET", "https://"+testHost+"/api/users", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: admin.cookie})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"passkeys":1`) {
		t.Fatalf("users: %s", w.Body.String())
	}
	if code, _ := admin.do("DELETE", "/api/me/passkeys/"+pid, nil); code != 200 {
		t.Fatal(code)
	}
	if code, _ := login(anon, "", false); code != 401 {
		t.Fatalf("deleted passkey accepted: %d", code)
	}
	if code, _ := admin.do("DELETE", "/api/users/bob/passkeys", nil); code != 200 {
		t.Fatal(code)
	}
	bobc := &pkClient{t: t, s: s, cookie: s.sessions.create("bob", "user", "192.0.2.1", time.Hour), host: testHost}
	if code, _ := bobc.do("DELETE", "/api/users/admin/passkeys", nil); code != 403 {
		t.Fatalf("user removed passkeys of another: %d", code)
	}
}
