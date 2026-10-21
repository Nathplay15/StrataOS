package store

import "time"

// Passkey is a WebAuthn credential of a user. Only public data: the private
// key never leaves the authenticator.
type Passkey struct {
	ID        string `json:"id"`        // credential ID, base64url without padding
	PublicKey []byte `json:"publicKey"` // COSE key
	SignCount uint32 `json:"signCount,omitempty"`
	// RPID is the name the passkey was created on (strataos.local, a domain…):
	// browsers only offer it on that name.
	RPID           string    `json:"rpId"`
	Name           string    `json:"name"`
	Transports     []string  `json:"transports,omitempty"`
	AAGUID         []byte    `json:"aaguid,omitempty"`
	BackupEligible bool      `json:"backupEligible,omitempty"` // synced passkey (iCloud, Google…)
	BackupState    bool      `json:"backupState,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	LastUsed       time.Time `json:"lastUsed,omitzero"`
}

// PasskeyByID returns the passkey of the user with that credential ID.
func (u *User) PasskeyByID(id string) *Passkey {
	for i := range u.Passkeys {
		if u.Passkeys[i].ID == id {
			return &u.Passkeys[i]
		}
	}
	return nil
}
