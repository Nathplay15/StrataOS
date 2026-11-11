package store

import "time"

// SysDiskOp is a change of the system disk (internal/sysdisk, api/h_sysdisk.go):
// mirroring it onto a second disk (kind "mirror") or moving the system to
// another disk ("move"). It goes through a restart, so everything needed to
// carry on is saved here; disks are found again by serial number.
type SysDiskOp struct {
	ID    string `json:"id"`
	JobID string `json:"jobId,omitempty"`
	Kind  string `json:"kind"` // mirror | move
	// Phase: prepare | copy | ready (copied, waiting for the restart) |
	// switching | rebooting | booted (runs from the new disk) | fell-back
	// (started from the old disk again) | completing (mirror: adding the old
	// disk) | done | failed | cancelled.
	Phase    string   `json:"phase"`
	Message  string   `json:"message,omitempty"`
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// Unreadable: files the copy could not read (failing disk), 200 at most.
	Unreadable      []string `json:"unreadable,omitempty"`
	UnreadableCount int      `json:"unreadableCount,omitempty"`

	Boot   string `json:"boot"`   // uefi | bios
	FSType string `json:"fsType"` // of the system
	Subvol string `json:"subvol,omitempty"`
	// Parts: the partitions of the new layout (sysdisk.PartSpec).
	Parts []SysDiskPart `json:"parts"`

	Old       string `json:"old"`
	OldSerial string `json:"oldSerial,omitempty"`
	OldModel  string `json:"oldModel,omitempty"`
	OldSize   int64  `json:"oldSize,omitempty"`
	New       string `json:"new"`
	NewSerial string `json:"newSerial,omitempty"`
	NewModel  string `json:"newModel,omitempty"`

	OldRootUUID string `json:"oldRootUuid,omitempty"`
	// OldESP: PARTUUID of the old disk's EFI system partition.
	OldESP string `json:"oldEsp,omitempty"`
	// The copy: file system UUIDs, and the arrays of a mirror.
	RootUUID  string `json:"rootUuid,omitempty"`
	ESPUUID   string `json:"espUuid,omitempty"`
	SwapUUID  string `json:"swapUuid,omitempty"`
	RootArray string `json:"rootArray,omitempty"` // /dev/md/<name>
	SwapArray string `json:"swapArray,omitempty"`

	// UEFI: the boot entry of the new disk, and the entry and order the
	// system started with. BIOS: the GRUB entry added to the old system.
	BootEntry   string `json:"bootEntry,omitempty"`
	OldBootNum  string `json:"oldBootNum,omitempty"`
	OldBootOrd  string `json:"oldBootOrder,omitempty"`
	GrubEntry   string `json:"grubEntry,omitempty"`
	GrubOneShot bool   `json:"grubOneShot,omitempty"`
	// BootID of the system that restarted into the copy.
	BootID string `json:"bootId,omitempty"`

	Started  time.Time `json:"started"`
	Updated  time.Time `json:"updated,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	User     string    `json:"user,omitempty"`
}

// SysDiskPart is a partition of the new system disk layout.
type SysDiskPart struct {
	Num  int    `json:"num"`
	Role string `json:"role"` // esp | biosgrub | swap | root
	MiB  int64  `json:"mib"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// SysBackup is an export of the system (System → System disk → Backup).
type SysBackup struct {
	ID       string    `json:"id"`
	Path     string    `json:"path"` // folder holding the archive and its manifest
	Size     int64     `json:"size"`
	Created  time.Time `json:"created"`
	Hostname string    `json:"hostname,omitempty"`
	Version  string    `json:"version,omitempty"`
	SHA256   string    `json:"sha256,omitempty"`
}

// MaxSysDiskOps bounds the operations kept in the state file.
const MaxSysDiskOps = 10
