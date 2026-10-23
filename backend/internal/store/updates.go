package store

import "time"

// Update modes.
const (
	UpdateOff     = ""        // nothing automatic
	UpdateNotify  = "notify"  // check in the window and notify
	UpdateInstall = "install" // install in the maintenance window
)

// AutoUpdates: update channel and scheduled automatic updates. The three
// kinds of updates are chosen independently; they all run in one
// maintenance window (days and hour).
type AutoUpdates struct {
	Channel  string `json:"channel,omitempty"`  // "" (stable) | beta
	Strata   string `json:"strata,omitempty"`   // StrataOS: off | notify | install
	Security string `json:"security,omitempty"` // Debian security updates: off | install
	System   string `json:"system,omitempty"`   // other system updates: off | notify | install
	Days     []int  `json:"days,omitempty"`     // 0 = Sunday … 6 = Saturday; none = every day
	Hour     int    `json:"hour,omitempty"`     // start of the window, local time
	Reboot   bool   `json:"reboot,omitempty"`   // restart at the end when an update needs it
	// Notified is what "notify only" last told, not to repeat it every night.
	Notified string         `json:"notified,omitempty"`
	Last     *AutoUpdateRun `json:"last,omitempty"`
}

// Enabled tells whether anything runs in the maintenance window.
func (a AutoUpdates) Enabled() bool {
	return a.Strata != UpdateOff || a.Security != UpdateOff || a.System != UpdateOff
}

// AutoUpdateRun is the result of the last automatic run.
type AutoUpdateRun struct {
	Window   time.Time  `json:"window"` // start of the window it ran in
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	// running | success | failed | skipped
	Status  string `json:"status"`
	DryRun  bool   `json:"dryRun,omitempty"`
	Job     string `json:"job,omitempty"`
	Message string `json:"message,omitempty"`
	// What was installed (or would be, in a dry run) and what is only offered.
	Security  int    `json:"security,omitempty"`
	Others    int    `json:"others,omitempty"`
	StrataTo  string `json:"strataTo,omitempty"`
	Available int    `json:"available,omitempty"` // system updates left for the administrator
	Offered   string `json:"offered,omitempty"`   // StrataOS version left for the administrator
	Restart   string `json:"restart,omitempty"`   // "" | required | done
	// Phase "strata" while the StrataOS package installs: the service
	// restarts in the middle, and the new one finishes the run.
	Phase string `json:"phase,omitempty"`
	From  string `json:"from,omitempty"`
}
