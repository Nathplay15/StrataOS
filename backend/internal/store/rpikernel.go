package store

import "time"

// RPiKernel follows the switch of a Raspberry Pi from Debian's kernel to the
// Raspberry Pi kernels (see api/h_rpikernel.go).
type RPiKernel struct {
	// Phase: "" (never started, or undone), "switched" (files in place,
	// waiting for the restart on the new kernel), "fell-back" (the system
	// started on Debian's kernel again), "failed", "done".
	Phase   string    `json:"phase,omitempty"`
	Message string    `json:"message,omitempty"`
	Model   string    `json:"model,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	// BootID of the system that made the switch: the next one is the trial.
	BootID string `json:"bootId,omitempty"`
	// Backup: tarball of the firmware partition as it was before.
	Backup       string `json:"backup,omitempty"`
	RestorePoint string `json:"restorePoint,omitempty"`
	// Previous holds the versions of the packages replaced by the Raspberry
	// Pi archive's (raspi-firmware, initramfs-tools…), for an undo.
	Previous map[string]string `json:"previous,omitempty"`
}
