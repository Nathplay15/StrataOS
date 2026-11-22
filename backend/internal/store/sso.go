package store

import "time"

// SSO makes the NAS an OpenID Connect provider for its apps and protects app
// domains with forward authentication (api/sso*.go, internal/sso). Tokens,
// consents and signing keys live in DataDir/sso, not in the state.
type SSO struct {
	Enabled bool `json:"enabled,omitempty"`
	// Issuer: the address of the NAS that browsers and apps use, without a
	// trailing slash (https://nas.example.com); the discovery document is
	// at Issuer/.well-known/openid-configuration.
	Issuer  string       `json:"issuer,omitempty"`
	Clients []*SSOClient `json:"clients,omitempty"`
}

// SSOClient is an application allowed to sign its users in with the NAS.
type SSOClient struct {
	ID   string `json:"id"` // client_id
	Name string `json:"name"`
	// Kind: "oidc" (the app speaks OpenID Connect) or "forward" (an app
	// domain the reverse proxy protects; Domain is the AppDomain ID).
	Kind   string `json:"kind"`
	Domain string `json:"domain,omitempty"`
	// App: the installed app (stack) whose configuration StrataOS wrote;
	// Catalog: its catalog ID.
	App     string `json:"app,omitempty"`
	Catalog string `json:"catalog,omitempty"`
	// Secret: client secret (a secret: never sent back, exported only in
	// the sealed part); Public clients have none and must use PKCE.
	Secret         string   `json:"secret,omitempty"`
	Public         bool     `json:"public,omitempty"`
	RedirectURIs   []string `json:"redirectUris,omitempty"`
	PostLogoutURIs []string `json:"postLogoutUris,omitempty"`
	RequirePKCE    bool     `json:"requirePkce,omitempty"`
	// Trusted: no consent screen.
	Trusted bool `json:"trusted,omitempty"`
	// Require2FA: only accounts with a second factor (or signed in through
	// a provider that checked one) may use the app.
	Require2FA bool `json:"require2fa,omitempty"`
	// Access: everybody, or the users and members of the groups listed.
	AllUsers bool      `json:"allUsers,omitempty"`
	Users    []string  `json:"users,omitempty"`
	Groups   []string  `json:"groups,omitempty"`
	Disabled bool      `json:"disabled,omitempty"`
	Created  time.Time `json:"created"`
}

// SSOClient returns the client of an ID, or nil.
func (st *State) SSOClient(id string) *SSOClient {
	for _, c := range st.SSO.Clients {
		if c.ID == id {
			return c
		}
	}
	return nil
}
