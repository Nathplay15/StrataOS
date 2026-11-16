package store

import "time"

// APIToken is a scoped token letting an API client (script, automation,
// MCP assistant) call the API as its user, without sharing the password.
// Only the SHA-256 of the secret is kept; the prefix identifies the token
// in the interface. Scopes bound what the token may reach:
//   - "read": GET requests on the user-level API;
//   - "write": state-changing requests on the user-level API;
//   - "admin": everything, including administrator-only routes;
//   - "mcp": the MCP endpoint (/api/mcp).
type APIToken struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	// TokenHash: SHA-256 hex of the full secret ("stt_…").
	TokenHash string    `json:"tokenHash"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"createdAt"`
	// ExpiresAt: optional expiry; zero means the token does not expire.
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	LastUsed  time.Time `json:"lastUsed,omitzero"`
	LastIP    string    `json:"lastIp,omitempty"`
}

// APIToken returns the API token of the user with that ID.
func (u *User) APIToken(id string) *APIToken {
	for i := range u.APITokens {
		if u.APITokens[i].ID == id {
			return &u.APITokens[i]
		}
	}
	return nil
}

// APITokenByHash finds an API token by the SHA-256 of its secret.
func (u *User) APITokenByHash(hash string) *APIToken {
	for i := range u.APITokens {
		if u.APITokens[i].TokenHash == hash {
			return &u.APITokens[i]
		}
	}
	return nil
}

// HasScope reports whether the token grants that scope ("admin" grants all).
func (t *APIToken) HasScope(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope || s == "admin" {
			return true
		}
	}
	return false
}
