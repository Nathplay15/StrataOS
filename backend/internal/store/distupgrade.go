package store

import "time"

// DistUpgrade is the system upgrade to the next major Debian release
// (api/h_distupgrade.go).
type DistUpgrade struct {
	// AllowTesting offers the upgrade to the next release while it is
	// still Debian testing (not recommended; for tests).
	AllowTesting bool            `json:"allowTesting,omitempty"`
	Run          *DistUpgradeRun `json:"run,omitempty"`
	// Check: the last compatibility check (daily, or on demand).
	Check *DistCheck `json:"check,omitempty"`
	// The proposal: "No" hides it until the next check (DismissedAt);
	// the administrators are told once per release (NotifiedFor).
	DismissedFor string    `json:"dismissedFor,omitempty"`
	DismissedAt  time.Time `json:"dismissedAt,omitzero"`
	NotifiedFor  string    `json:"notifiedFor,omitempty"`
	// Scheduled: the upgrade waits for the next maintenance window.
	Scheduled *DistSchedule `json:"scheduled,omitempty"`
}

// DistCheck is a compatibility check of the next release.
type DistCheck struct {
	At      time.Time       `json:"at"`
	Target  string          `json:"target"` // codename
	Number  string          `json:"number"`
	Checks  []UpgradeResult `json:"checks"`
	Blocked bool            `json:"blocked"`
}

// DistSchedule is an upgrade asked for the next maintenance window.
type DistSchedule struct {
	Target string    `json:"target"`
	Force  bool      `json:"force,omitempty"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
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
	// The major numbers (read from the archive when it started), and
	// whether an administrator forced it past failed checks.
	FromNumber string `json:"fromNumber,omitempty"`
	ToNumber   string `json:"toNumber,omitempty"`
	Forced     bool   `json:"forced,omitempty"`
}

// UpgradeResult is one check (compatibility or after the restart).
type UpgradeResult struct {
	ID    string `json:"id"`
	Level string `json:"level"` // ok | warn | block
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"`
}
