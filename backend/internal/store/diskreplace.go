package store

import "time"

// DiskReplacement is a guided disk replacement (Storage → Disks → Replace).
// Kernel names (/dev/sdX) change between boots: serial numbers identify the
// disks when a replacement is resumed after a restart.
type DiskReplacement struct {
	ID      string `json:"id"`
	JobID   string `json:"jobId,omitempty"`
	Kind    string `json:"kind"`  // raid | zfs | lvm
	Group   string `json:"group"` // group ID (raid:md127, zfs:tank:mirror-0, lvm:vg0)
	Name    string `json:"name"`  // array label, pool or volume group
	Level   string `json:"level,omitempty"`
	Mode    string `json:"mode"`          // add-first | degraded
	Old     string `json:"old,omitempty"` // member name as the tool knows it
	OldDisk string `json:"oldDisk,omitempty"`
	// OldSerial and OldModel name the disk to pull at the end.
	OldSerial string `json:"oldSerial,omitempty"`
	OldModel  string `json:"oldModel,omitempty"`
	OldByID   string `json:"oldById,omitempty"`
	New       string `json:"new"`
	NewSerial string `json:"newSerial,omitempty"`
	NewModel  string `json:"newModel,omitempty"`
	SmartTest bool   `json:"smartTest,omitempty"`
	// Phase: check | add | rebuild | remove | done | failed.
	Phase    string    `json:"phase"`
	Error    string    `json:"error,omitempty"`
	Warning  string    `json:"warning,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	User     string    `json:"user,omitempty"`
}

// MaxDiskReplacements bounds the replacements kept in the state file.
const MaxDiskReplacements = 20
