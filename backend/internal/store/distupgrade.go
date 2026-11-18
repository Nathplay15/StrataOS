package store

import "time"

// DistUpgrade is the Debian release upgrade from the interface.
type DistUpgrade struct {
	// AllowTesting offers the upgrade to the next release while it is
	// still Debian testing (not recommended; for tests).
	AllowTesting bool            `json:"allowTesting,omitempty"`
	Run          *DistUpgradeRun `json:"run,omitempty"`
}

// DistUpgradeRun is the last (or current) upgrade. Phase: sources,
// download, upgrade, finish, reboot (waiting for the restart), post-checks,
// done, failed, rolling-back (going back to the restore point, restart
// pending), rolled-back.
type DistUpgradeRun struct {
	From         string          `json:"from"`
	To           string          `json:"to"`
	Started      time.Time       `json:"started"`
	Finished     time.Time       `json:"finished,omitzero"`
	Phase        string          `json:"phase"`
	Error        string          `json:"error,omitempty"`
	RestorePoint string          `json:"restorePoint,omitempty"`
	SourcesDir   string          `json:"sourcesDir,omitempty"` // copies of the sources before the rewrite
	Kernel       string          `json:"kernel,omitempty"`     // kernel installed for the new release
	BootID       string          `json:"bootId,omitempty"`     // boot of the upgrade (post-checks after the next one)
	PostChecks   []UpgradeResult `json:"postChecks,omitempty"`
	// Auto: the one-button upgrade restarts by itself once the packages
	// are installed, and goes back to the restore point when the upgrade
	// or the checks after the restart fail (Rollback: possible on this
	// system, restore points by snapshot).
	Auto     bool `json:"auto,omitempty"`
	Rollback bool `json:"rollback,omitempty"`
	// RolledBack: why the system went back to the restore point.
	RolledBack string `json:"rolledBack,omitempty"`
}

// UpgradeResult is one post-check.
type UpgradeResult struct {
	ID    string `json:"id"`
	Level string `json:"level"` // ok | warn | block
	Text  string `json:"text"`
}
