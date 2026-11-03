package store

import "time"

// FleetUpdateRun is a grouped update started from the Multi-NAS console.
type FleetUpdateRun struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`     // strata | system
	Strategy string     `json:"strategy"` // parallel | rolling
	Reboot   bool       `json:"reboot,omitempty"`
	DryRun   bool       `json:"dryRun,omitempty"`
	Cancel   bool       `json:"cancel,omitempty"` // no new NAS starts; those updating finish
	User     string     `json:"user"`
	Status   string     `json:"status"` // running | success | failed | cancelled
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	// Lanes run side by side; the NAS of a lane one after the other (indexes
	// in Items). Rolling is one lane; an HA pair always shares a lane.
	Lanes [][]int           `json:"lanes"`
	Items []FleetUpdateItem `json:"items"`
}

// FleetUpdateItem is the update of one NAS inside a run.
type FleetUpdateItem struct {
	Member string `json:"member"` // FleetMember.ID
	Name   string `json:"name"`
	Host   string `json:"host"`
	HA     string `json:"ha,omitempty"` // primary | secondary when part of a pair
	// State: pending, running (remote job), restarting (service or apt
	// still busy), rebooting, verifying, success, failed, skipped.
	State    string     `json:"state"`
	Job      string     `json:"job,omitempty"` // job ID on the watched NAS
	Progress float64    `json:"progress"`
	From     string     `json:"from,omitempty"`   // version before
	Target   string     `json:"target,omitempty"` // StrataOS version expected after
	Version  string     `json:"version,omitempty"`
	BootID   string     `json:"bootId,omitempty"`
	Alerts   int        `json:"alerts"` // error alerts before the update
	Rebooted bool       `json:"rebooted,omitempty"`
	Note     string     `json:"note,omitempty"`
	Error    string     `json:"error,omitempty"`
	Since    time.Time  `json:"since,omitzero"` // start of the current state (timeouts)
	Started  *time.Time `json:"started,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
}

// FleetTransfer is a VM or container copied (or moved) from one NAS of the
// console to another. From and To are FleetMember IDs, or "local".
type FleetTransfer struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"` // vm | lxc
	Name     string            `json:"name"`
	NewName  string            `json:"newName"`
	From     string            `json:"from"`
	To       string            `json:"to"`
	FromName string            `json:"fromName"`
	ToName   string            `json:"toName"`
	Mode     string            `json:"mode"` // stop | snapshot
	Move     bool              `json:"move,omitempty"`
	Storage  string            `json:"storage"`           // folder (VM) or volume (container) on the destination
	Bridges  map[string]string `json:"bridges,omitempty"` // source bridge → destination bridge ("nat": containers only)
	Start    bool              `json:"start,omitempty"`
	User     string            `json:"user"`
	// Status: running | success | failed | cancelled. A failed transfer
	// keeps its files on both NAS and can resume.
	Status   string     `json:"status"`
	Stage    string     `json:"stage"` // export | transfer | verify | restore | finish
	Progress float64    `json:"progress"`
	Bytes    int64      `json:"bytes"`
	Total    int64      `json:"total"`
	Job      string     `json:"job,omitempty"` // console job
	Error    string     `json:"error,omitempty"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
}
