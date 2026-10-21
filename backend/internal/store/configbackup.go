package store

import "time"

// ConfigBackup is the automatic daily export of the configuration
// (Settings → Configuration backup) into a folder of a volume.
type ConfigBackup struct {
	Enabled bool   `json:"enabled"`
	Folder  string `json:"folder"` // under /srv/strata
	Keep    int    `json:"keep"`   // newest copies kept (the older ones are deleted)
	Hour    int    `json:"hour"`   // local hour of the daily export
	// Secrets: include the secrets, encrypted with Passphrase.
	Secrets    bool      `json:"secrets,omitempty"`
	Passphrase string    `json:"passphrase,omitempty"`
	LastRun    time.Time `json:"lastRun,omitzero"`
	LastResult string    `json:"lastResult,omitempty"`
	LastFile   string    `json:"lastFile,omitempty"`
}
