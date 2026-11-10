package store

import "time"

// TwoFactorPolicy is the setting "Require two-factor for administrators"
// (gate.Policy). Kept out of Settings, which the settings page replaces
// as a whole.
type TwoFactorPolicy struct {
	Require bool      `json:"require,omitempty"`
	Since   time.Time `json:"since,omitzero"`
	// GraceDays before it is enforced: 0 = 7 days, -1 = at once.
	GraceDays int `json:"graceDays,omitempty"`
	// RequireKey: only a security key or a passkey counts (not TOTP).
	RequireKey bool `json:"requireKey,omitempty"`
	// KeyUV: user verification asked of security keys: "" (preferred) or
	// "discouraged" (old U2F keys without PIN).
	KeyUV string `json:"keyUv,omitempty"`
}

// LinkUpload is one file received by a share link (newest last).
type LinkUpload struct {
	Time  time.Time `json:"time"`
	Name  string    `json:"name"` // as stored, relative to the link's folder
	Size  int64     `json:"size"`
	Guest string    `json:"guest,omitempty"`
	Email string    `json:"email,omitempty"`
}

// MaxLinkUploads is how many uploads a link remembers.
const MaxLinkUploads = 100
