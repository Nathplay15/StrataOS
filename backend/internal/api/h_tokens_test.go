package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func tokensServer(t *testing.T) *Server {
	s := newTestServer(t)
	s.tokenRoutes()
	s.openapiRoutes()
	s.user("GET /api/ping-test", func(r *http.Request) (any, error) {
		return map[string]bool{"ok": true}, nil
	})
	s.user("POST /api/ping-test", func(r *http.Request) (any, error) {
		return map[string]bool{"ok": true}, nil
	})
	s.admin("GET /api/users-test", func(r *http.Request) (any, error) {
		var names []string
		s.store.Read(func(st *store.State) {
			for _, u := range st.Users {
				names = append(names, u.Username)
			}
		})
		return names, nil
	})
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users,
			&store.User{Username: "admin", Role: "admin", PasswordHash: hash},
			&store.User{Username: "bob", PasswordHash: hash})
		return nil
	})
	return s
}

// tokenReq calls the API with a bearer API token (no cookie, no Origin).
func tokenReq(t *testing.T, s *Server, method, path, tok string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, "https://"+testHost+path, strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// createToken creates a token for user through the API.
func createToken(t *testing.T, s *Server, user string, scopes []string, days int) string {
	t.Helper()
	c := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create(user, roleOf(s, user), "192.0.2.9", time.Hour)}}
	w := c.req("POST", "/api/me/tokens", map[string]any{"name": "ci", "scopes": scopes, "days": days})
	if w.Code != 200 {
		t.Fatalf("create token: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !strings.HasPrefix(out.Token, apiTokenPrefix) {
		t.Fatalf("token shape: %q", out.Token)
	}
	return out.Token
}

func roleOf(s *Server, user string) string {
	var role string
	s.store.Read(func(st *store.State) {
		if u := st.User(user); u != nil {
			role = u.Role
		}
	})
	return role
}

func TestTokenCreateListRevoke(t *testing.T) {
	s := tokensServer(t)
	tok := createToken(t, s, "bob", []string{"read"}, 30)

	// Token management is out of reach of API tokens themselves, even read.
	if w := tokenReq(t, s, "GET", "/api/me/tokens", tok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("read token on token management: %d", w.Code)
	}
	bob := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("bob", "", "192.0.2.9", time.Hour)}}
	w := bob.req("GET", "/api/me/tokens", nil)
	if w.Code != 200 {
		t.Fatalf("list: %d", w.Code)
	}
	var list []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["prefix"] == nil || list[0]["name"] != "ci" {
		t.Fatalf("token list: %v", list)
	}
	if _, ok := list[0]["tokenHash"]; ok {
		t.Fatalf("the hash must never leave the server: %v", list[0])
	}
	id := list[0]["id"].(string)

	// A read token cannot revoke (write) — and cannot manage tokens at all.
	if w := tokenReq(t, s, "DELETE", "/api/me/tokens/"+id, tok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("read token must not revoke: %d", w.Code)
	}

	// Revoke with the owner's session, then the token is dead.
	if w := bob.req("DELETE", "/api/me/tokens/"+id, nil); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if w := tokenReq(t, s, "GET", "/api/me/tokens", tok, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token must be rejected: %d", w.Code)
	}
}

func TestTokenScopesGateMethods(t *testing.T) {
	s := tokensServer(t)
	read := createToken(t, s, "bob", []string{"read"}, 0)
	write := createToken(t, s, "bob", []string{"write"}, 0)
	userAdmin := createToken(t, s, "bob", []string{"admin"}, 0)

	// read: GET ok on a normal endpoint, POST forbidden.
	if w := tokenReq(t, s, "GET", "/api/ping-test", read, nil); w.Code != 200 {
		t.Fatalf("read token GET: %d", w.Code)
	}
	if w := tokenReq(t, s, "POST", "/api/ping-test", read, nil); w.Code != http.StatusForbidden {
		t.Fatalf("read token POST: %d", w.Code)
	}
	if w := tokenReq(t, s, "GET", "/api/users-test", read, nil); w.Code != http.StatusForbidden {
		t.Fatalf("read token on admin route: %d", w.Code)
	}
	if w := tokenReq(t, s, "POST", "/api/me/tokens", read, map[string]any{"name": "x", "scopes": []string{"read"}}); w.Code != http.StatusForbidden {
		t.Fatalf("read token POST: %d", w.Code)
	}
	// write: POST forbidden on token management (denied path) even with write.
	if w := tokenReq(t, s, "POST", "/api/me/tokens", write, map[string]any{"name": "x", "scopes": []string{"read"}}); w.Code != http.StatusForbidden {
		t.Fatalf("write token on tokens endpoint: %d", w.Code)
	}
	// admin scope alone does not grant administrator routes to a plain user.
	if w := tokenReq(t, s, "GET", "/api/users-test", userAdmin, nil); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin with admin scope: %d", w.Code)
	}
	// unknown token rejected.
	if w := tokenReq(t, s, "GET", "/api/me/tokens", apiTokenPrefix+"deadbeef", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d", w.Code)
	}
}

func TestTokenAdminScopeOnAdminRoutes(t *testing.T) {
	s := tokensServer(t)
	adminTok := createToken(t, s, "admin", []string{"read", "admin"}, 0)
	readTok := createToken(t, s, "admin", []string{"read"}, 0)

	if w := tokenReq(t, s, "GET", "/api/users-test", adminTok, nil); w.Code != 200 {
		t.Fatalf("admin token on admin GET: %d", w.Code)
	}
	if w := tokenReq(t, s, "GET", "/api/users-test", readTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("read token on admin route: %d", w.Code)
	}
}

func TestTokenExpiry(t *testing.T) {
	s := tokensServer(t)
	tok := createToken(t, s, "bob", []string{"read"}, 0)
	_ = s.store.Update(func(st *store.State) error {
		u := st.User("bob")
		h := sha256.Sum256([]byte(tok))
		sum := hex.EncodeToString(h[:])
		if t2 := u.APITokenByHash(sum); t2 != nil {
			t2.ExpiresAt = time.Now().Add(-time.Minute)
		}
		return nil
	})
	if w := tokenReq(t, s, "GET", "/api/me/tokens", tok, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", w.Code)
	}
}

func TestOpenAPIDocument(t *testing.T) {
	s := tokensServer(t)
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}
	w := admin.req("GET", "/api/openapi.json", nil)
	if w.Code != 200 {
		t.Fatalf("openapi: %d", w.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("version: %v", doc["openapi"])
	}
	paths := doc["paths"].(map[string]any)
	if _, ok := paths["/api/me/tokens"]; !ok {
		t.Fatalf("tokens route missing from the document: %d paths", len(paths))
	}
	if _, ok := paths["/api/openapi.json"]; !ok {
		t.Fatal("openapi route missing from the document")
	}
}

// Whatever its scopes, a token cannot manage the credentials of the account:
// everything under /api/me/ is closed except an explicit list (review 0.12.0:
// a write token could create a permanent S3 key, remove security keys or
// change the password).
func TestTokenAccountRoutesClosed(t *testing.T) {
	s := tokensServer(t)
	for _, p := range []string{"POST /api/me/s3/keys", "POST /api/me/password", "POST /api/me/recovery-codes",
		"DELETE /api/me/security-keys/{id}", "DELETE /api/me/oidc/{provider}", "POST /api/me/passkeys/begin",
		"GET /api/me/quotas", "PUT /api/me/desktop", "GET /api/me/s3", "DELETE /api/devices/{id}"} {
		s.user(p, func(r *http.Request) (any, error) { return map[string]bool{"ok": true}, nil })
	}
	tok := createToken(t, s, "admin", []string{"read", "write", "admin"}, 0)
	for _, c := range []struct {
		method, path string
		code         int
	}{
		{"POST", "/api/me/s3/keys", 403},
		{"POST", "/api/me/password", 403},
		{"POST", "/api/me/recovery-codes", 403},
		{"DELETE", "/api/me/security-keys/k1", 403},
		{"DELETE", "/api/me/oidc/google", 403},
		{"POST", "/api/me/passkeys/begin", 403},
		{"GET", "/api/me/s3", 403},
		{"DELETE", "/api/devices/d1", 403},
		{"GET", "/api/me/quotas", 200},
		{"PUT", "/api/me/desktop", 200},
	} {
		if w := tokenReq(t, s, c.method, c.path, tok, map[string]any{}); w.Code != c.code {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, w.Code, c.code)
		}
	}
	// Approving an app's single sign-on would hand out tokens for that app.
	for _, p := range []string{"/api/sso/requests/r1", "/api/sso/logouts/l1", "/api/me/sso", "/api/me/sso/grafana"} {
		if !tokenDenied(p) {
			t.Errorf("%s reachable with a token", p)
		}
	}
	// The OpenAPI document says so.
	if !strings.Contains(scopeNote, "/api/me/quotas") || !strings.Contains(scopeNote, "S3 keys") {
		t.Fatalf("scope note: %s", scopeNote)
	}
}

// An administrator's token without the admin scope acts as a user, also for
// the file routes that test isAdmin (review 0.12.0).
func TestIsAdminFollowsTokenScopes(t *testing.T) {
	s := tokensServer(t)
	s.user("GET /api/whoami-test", func(r *http.Request) (any, error) {
		return map[string]bool{"admin": isAdmin(r)}, nil
	})
	for scopes, want := range map[string]bool{"read": false, "mcp": false, "read,write": false, "admin": true} {
		tok := createToken(t, s, "admin", strings.Split(scopes, ","), 0)
		w := tokenReq(t, s, "GET", "/api/whoami-test", tok, nil)
		if want != strings.Contains(w.Body.String(), `"admin":true`) {
			t.Errorf("scopes %s: %d %s", scopes, w.Code, w.Body.String())
		}
	}
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}
	if w := admin.req("GET", "/api/whoami-test", nil); !strings.Contains(w.Body.String(), `"admin":true`) {
		t.Fatalf("admin session: %s", w.Body.String())
	}
}

// The in-process calls of the MCP tools and the assistant no longer skip the
// token checks: the closed routes stay closed and writes need the write scope.
func TestInternalCallsKeepTokenRules(t *testing.T) {
	ss := &session{User: "bob", Scopes: []string{"mcp"}}
	internal := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, "http://localhost"+path, nil)
		return r.WithContext(context.WithValue(r.Context(), mcpInternalKey{}, true))
	}
	if !tokenAllows(ss, internal("GET", "/api/system/info"), false) {
		t.Fatal("mcp token: internal read refused")
	}
	if tokenAllows(ss, internal("GET", "/api/me/tokens"), false) {
		t.Fatal("internal call reached token management")
	}
	if tokenAllows(ss, internal("POST", "/api/files/mkdir"), false) {
		t.Fatal("internal write without the write scope")
	}
	if tokenAllows(ss, httptest.NewRequest("GET", "http://localhost/api/system/info", nil), false) {
		t.Fatal("mcp token: direct read allowed")
	}
}
