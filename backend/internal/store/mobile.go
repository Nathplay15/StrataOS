package store

import "time"

// MobileDevice is a phone paired with the StrataOS app. It signs in with a
// long-lived bearer token of which only the SHA-256 is kept; the token acts
// as its user, with the user's current rights (role read at each request).
type MobileDevice struct {
	ID    string `json:"id"`
	Name  string `json:"name"`  // given by the user ("Pixel of Alice")
	Model string `json:"model"` // reported by the app (manufacturer and model)
	App   string `json:"app,omitempty"`
	// Kind: "desktop" for the sync app of a computer, "" (or "android") for a
	// phone; OS is what the client reports ("Windows 11", "macOS 15.1").
	Kind      string    `json:"kind,omitempty"`
	OS        string    `json:"os,omitempty"`
	TokenHash string    `json:"tokenHash"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen,omitzero"`
	LastIP    string    `json:"lastIp,omitempty"`
	// Push: UnifiedPush endpoint of the app, and its Web Push keys (RFC 8291,
	// base64url) when the distributor expects encrypted messages.
	PushURL    string `json:"pushUrl,omitempty"`
	PushP256DH string `json:"pushP256dh,omitempty"`
	PushAuth   string `json:"pushAuth,omitempty"`
	PushError  string `json:"pushError,omitempty"` // last delivery failure
}

// Mobile holds the server side of the mobile app.
type Mobile struct {
	// VAPIDKey: P-256 private key (PKCS #8, base64) identifying this NAS to
	// push services (RFC 8292). Created at the first registration; never sent.
	VAPIDKey string `json:"vapidKey,omitempty"`
}

// MobileDevice returns the device of the user with that ID.
func (u *User) MobileDevice(id string) *MobileDevice {
	for i := range u.MobileDevices {
		if u.MobileDevices[i].ID == id {
			return &u.MobileDevices[i]
		}
	}
	return nil
}
