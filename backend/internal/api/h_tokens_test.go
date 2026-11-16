package api

import (
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
