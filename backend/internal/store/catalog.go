package store

import "time"

// CatalogSource is a community app catalog added to the App Center
// (internal/market). Its verified index is cached in
// /var/lib/strata/catalogs/<id>.json.
type CatalogSource struct {
	ID   string `json:"id"`
	URL  string `json:"url"`  // as typed, normalized (git+https://…#branch for Git)
	Kind string `json:"kind"` // https | git
	Name string `json:"name"` // from the index
	// Trust: an Ed25519 key ("ed25519:<base64>") whose signature every
	// refresh checks, or else the SHA-256 the administrator accepted.
	PublicKey string    `json:"publicKey,omitempty"`
	SHA256    string    `json:"sha256"`
	Commit    string    `json:"commit,omitempty"` // git: commit of the accepted index
	AddedBy   string    `json:"addedBy,omitempty"`
	AddedAt   time.Time `json:"addedAt"`
	Refreshed time.Time `json:"refreshed,omitzero"`
	LastError string    `json:"lastError,omitempty"`
	// Pending: a pinned source whose index changed; the new one waits in
	// <id>.pending.json until the administrator accepts its checksum.
	Pending string `json:"pending,omitempty"`
}
