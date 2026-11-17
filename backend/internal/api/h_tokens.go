package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"strataos/internal/store"
)

// apiTokenPrefix identifies a scoped API token ("stt_" then 43 base64url
// characters); it must not collide with the mobile device prefix (stdev_).
const apiTokenPrefix = "stt_"

// apiTokenScopes are the scopes a token may carry, with their description
// (shown by the OpenAPI document and the interface).
var apiTokenScopes = map[string]string{
	"read":  "GET requests on the API",
	"write": "state-changing requests on the API",
	"admin": "everything, including administrator routes",
	"mcp":   "the MCP endpoint (/api/mcp)",
}

const apiTokenMaxPerUser = 20

// tokenView is a token as the interface lists it: no hash, only the prefix
// to recognize it.
type tokenView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	LastUsed  time.Time `json:"lastUsed,omitzero"`
	LastIP    string    `json:"lastIp,omitempty"`
}

func (s *Server) tokenRoutes() {
	s.user("GET /api/me/tokens", func(r *http.Request) (any, error) {
		var out []tokenView
		s.store.Read(func(st *store.State) {
			out = []tokenView{}
			if u := st.User(userOf(r)); u != nil {
				for _, t := range u.APITokens {
					out = append(out, tokenView{ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scopes: t.Scopes, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsed: t.LastUsed, LastIP: t.LastIP})
				}
			}
		})
		return out, nil
	})
	s.user("POST /api/me/tokens", func(r *http.Request) (any, error) {
		var in struct {
			Name   string   `json:"name"`
			Scopes []string `json:"scopes"`
			Days   int      `json:"days"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" || len(in.Name) > 100 {
			return nil, badRequest("a token name is required (100 characters at most)")
		}
		if len(in.Scopes) == 0 {
			return nil, badRequest("at least one scope is required")
		}
		seen := map[string]bool{}
		for _, sc := range in.Scopes {
			if _, ok := apiTokenScopes[sc]; !ok {
				return nil, badRequest("unknown scope: " + sc)
			}
			if seen[sc] {
				return nil, badRequest("duplicate scope: " + sc)
			}
			seen[sc] = true
		}
		if in.Days < 0 || in.Days > 3650 {
			return nil, badRequest("expiry must be between 0 (never) and 3650 days")
		}
		created := time.Now()
		var out struct {
			Token  string            `json:"token"`
			View   *tokenView        `json:"view"`
			Scopes map[string]string `json:"scopes"`
		}
		err := s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil {
				return errUnauthorized
			}
			if len(u.APITokens) >= apiTokenMaxPerUser {
				return badRequest("too many tokens: revoke one first")
			}
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			secret := apiTokenPrefix + hex.EncodeToString(b)
			sum := sha256.Sum256([]byte(secret))
			t := store.APIToken{
				ID:        token()[:16],
				Name:      in.Name,
				Prefix:    secret[:12],
				TokenHash: hex.EncodeToString(sum[:]),
				Scopes:    in.Scopes,
				CreatedAt: created,
			}
			if in.Days > 0 {
				t.ExpiresAt = created.AddDate(0, 0, in.Days)
			}
			u.APITokens = append(u.APITokens, t)
			out.Token = secret
			out.View = &tokenView{ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scopes: t.Scopes, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt}
			out.Scopes = apiTokenScopes
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "security", "created API token "+in.Name+" ("+strings.Join(in.Scopes, ", ")+")")
		return out, nil
	})
	s.user("DELETE /api/me/tokens/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var ok bool
		var revoked string
		err := s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil {
				return errUnauthorized
			}
			if i := indexToken(u.APITokens, id); i >= 0 {
				revoked = u.APITokens[i].Name
				u.APITokens = append(u.APITokens[:i], u.APITokens[i+1:]...)
				ok = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, notFound("token not found")
		}
		s.audit(r, "security", "revoked API token "+revoked)
		return map[string]bool{"ok": true}, nil
	})
}

func indexToken(l []store.APIToken, id string) int {
	for i := range l {
		if l[i].ID == id {
			return i
		}
	}
	return -1
}

// apiTokenSession authenticates a scoped API token (stt_…) and returns the
// session it acts as (nil: unknown, expired, revoked or disabled user).
// The scopes travel in the session; a nil Scopes means unrestricted
// (cookie or mobile-device session).
func (s *Server) apiTokenSession(tok, ip string) *session {
	if !strings.HasPrefix(tok, apiTokenPrefix) || len(tok) > 128 {
		return nil
	}
	sum := sha256.Sum256([]byte(tok))
	hash := hex.EncodeToString(sum[:])
	var ss *session
	var stale bool
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if u.Disabled {
				continue
			}
			if t := u.APITokenByHash(hash); t != nil {
				if !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt) {
					return
				}
				ss = &session{User: u.Username, Role: u.Role, Created: t.CreatedAt, Expires: time.Now().Add(time.Hour), IP: ip, Scopes: t.Scopes}
				stale = time.Since(t.LastUsed) > time.Minute || t.LastIP != ip
			}
		}
	})
	if ss != nil && stale {
		_ = s.store.Update(func(st *store.State) error {
			if u := st.User(ss.User); u != nil {
				if t := u.APITokenByHash(hash); t != nil {
					t.LastUsed, t.LastIP = time.Now(), ip
				}
			}
			return nil
		})
	}
	return ss
}

// tokenMeAllowed are the only routes of the account (/api/me/…) a scoped
// API token may reach: reading the profile, quotas and activity, and the
// desktop preferences and language. Everything else there manages the
// credentials of the account (password, recovery codes, two-factor, security
// keys, OIDC links, S3 keys, devices, tokens) and stays out of reach, even
// with the admin scope: a token must never be able to create another way in
// that survives its revocation.
var tokenMeAllowed = map[string]bool{
	"/api/me/quotas": true, "/api/me/activity": true, "/api/me/desktop": true, "/api/me/lang": true,
}

// tokenDenied reports paths a scoped API token may never reach.
func tokenDenied(path string) bool {
	if strings.HasPrefix(path, "/api/me/") && !tokenMeAllowed[path] {
		return true
	}
	for _, p := range []string{
		"/api/logout", "/api/sessions", "/api/devices", "/api/assistant/approvals",
	} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// hasScope reports whether a scope list grants scope ("admin" grants all).
func hasScope(sc []string, scope string) bool {
	for _, s := range sc {
		if subtle.ConstantTimeCompare([]byte(s), []byte(scope)) == 1 || s == "admin" {
			return true
		}
	}
	return false
}

// mcpInternalKey marks the in-process calls of the MCP tools and of the
// assistant (h_mcp.go): the mcp scope already let the outer request in, so
// it stands for read here; writes still need the write scope and the routes
// a token may never reach stay closed.
type mcpInternalKey struct{}

// tokenAllows checks the scopes of a token session against the request.
func tokenAllows(ss *session, r *http.Request, adminOnly bool) bool {
	if ss.Scopes == nil {
		return true
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
	if r.URL.Path == "/api/mcp" {
		return hasScope(ss.Scopes, "mcp")
	}
	if tokenDenied(r.URL.Path) {
		return false
	}
	if read {
		if r.Context().Value(mcpInternalKey{}) != nil && hasScope(ss.Scopes, "mcp") {
			return true
		}
		return hasScope(ss.Scopes, "read")
	}
	return hasScope(ss.Scopes, "write")
}
