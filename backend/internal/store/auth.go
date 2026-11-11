package store

import "time"

// OIDCProvider is an OpenID Connect identity provider offered on the
// sign-in page ("Sign in with Authentik"…). The client secret never
// leaves the server.
type OIDCProvider struct {
	ID           string   `json:"id"`   // short slug used in URLs
	Name         string   `json:"name"` // shown on the button
	Issuer       string   `json:"issuer"`
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret,omitempty"` // "" = public client (PKCE only)
	Scopes       []string `json:"scopes,omitempty"`       // besides openid; default profile email
	Enabled      bool     `json:"enabled"`
	// AllowHTTP accepts an issuer over plain HTTP (a test IdP on the LAN).
	AllowHTTP bool `json:"allowHttp,omitempty"`
	// TLS: certificate of a self-hosted provider signed by a private CA
	// (TLSInsecure is never accepted for sign-in).
	TLSTrust
	// Claim names: username (default preferred_username), email, groups.
	UsernameClaim string `json:"usernameClaim,omitempty"`
	EmailClaim    string `json:"emailClaim,omitempty"`
	GroupsClaim   string `json:"groupsClaim,omitempty"`
	// AutoCreate creates a StrataOS account at the first sign-in of an
	// unknown user; otherwise only linked accounts may sign in.
	AutoCreate bool `json:"autoCreate,omitempty"`
	// AdminGroups: provider groups whose members are administrators (only
	// for accounts the provider created; linked accounts keep their role).
	AdminGroups []string `json:"adminGroups,omitempty"`
	// AllowedGroups: when set, only members of one of them may sign in.
	AllowedGroups []string `json:"allowedGroups,omitempty"`
	// GroupMap: provider group → StrataOS group, kept in sync at sign-in
	// for accounts the provider created.
	GroupMap map[string]string `json:"groupMap,omitempty"`
	// TrustMFA: an ID token whose amr/acr says the provider checked a
	// second factor counts as two-factor authentication.
	TrustMFA bool `json:"trustMfa,omitempty"`
	// MFAValues: amr or acr values meaning "second factor checked"
	// (default: mfa, otp, hwk, swk, fpt, face, pin, sc, mca).
	MFAValues []string `json:"mfaValues,omitempty"`
	// ACRValues are asked of the provider (acr_values), e.g. to make it
	// require a second factor.
	ACRValues string    `json:"acrValues,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// OIDCLink ties an account to a provider's subject (issuer + sub).
type OIDCLink struct {
	Provider string    `json:"provider"` // OIDCProvider.ID
	Issuer   string    `json:"issuer"`
	Subject  string    `json:"subject"`
	Created  bool      `json:"created,omitempty"` // the account was created by this provider
	Linked   time.Time `json:"linked"`
	LastUsed time.Time `json:"lastUsed,omitzero"`
}

// S3Service is the built-in S3-compatible service (versitygw): shared
// folders exposed as buckets.
type S3Service struct {
	Enabled bool `json:"enabled,omitempty"`
	Port    int  `json:"port,omitempty"` // 0 = 7380
	// Domain enables virtual-host-style addressing (bucket.<domain>);
	// path style always works.
	Domain  string     `json:"domain,omitempty"`
	Buckets []S3Bucket `json:"buckets,omitempty"`
	// Root credentials of the gateway: never given out, they let StrataOS
	// own the buckets.
	RootAccess string `json:"rootAccess,omitempty"`
	RootSecret string `json:"rootSecret,omitempty"`
}

// S3Bucket exposes a shared folder under a bucket name.
type S3Bucket struct {
	Name  string `json:"name"`
	Share string `json:"share"`
}

// S3Key is an access key of a user for the S3 service. The secret is
// shown once at creation; the gateway needs it in clear to check request
// signatures, so it is kept (state.json is readable by root only).
type S3Key struct {
	Access   string    `json:"access"`
	Secret   string    `json:"secret"`
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed,omitzero"`
}
