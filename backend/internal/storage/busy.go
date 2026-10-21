package storage

import (
	"fmt"
	"sync"
)

// busy lists the disks a long operation (burn-in test) is using: they look
// free, but nothing may be created on them or wipe them meanwhile.
var busy sync.Map // resolved device path → reason

// SetBusy marks a disk as used by reason until ClearBusy. It fails when the
// disk is already busy.
func SetBusy(path, reason string) error {
	if r, loaded := busy.LoadOrStore(resolve(path), reason); loaded {
		return fmt.Errorf("%s is busy (%s)", path, r)
	}
	return nil
}

// ClearBusy releases a disk marked by SetBusy.
func ClearBusy(path string) { busy.Delete(resolve(path)) }

// BusyReason returns why a disk is busy ("" when it is not).
func BusyReason(path string) string {
	if r, ok := busy.Load(resolve(path)); ok {
		return r.(string)
	}
	return ""
}
