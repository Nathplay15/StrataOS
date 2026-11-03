package storage

import (
	"strings"

	"strataos/internal/sys"
)

// OnUSB reports whether the file system holding p sits on a USB disk (any
// disk under it: RAID, LVM and LUKS layers are followed down).
func OnUSB(p string) bool {
	src, err := sys.Run("findmnt", "-n", "-o", "SOURCE", "-T", p)
	if err != nil {
		return false
	}
	src = strings.TrimSpace(strings.Split(src, "\n")[0])
	if !strings.HasPrefix(src, "/dev/") {
		return false // ZFS dataset, network mount…
	}
	if i := strings.IndexByte(src, '['); i > 0 {
		src = src[:i] // Btrfs sub-volume: /dev/sdb[/sub]
	}
	out, err := sys.Run("lsblk", "-n", "-s", "-o", "TRAN", src)
	if err != nil {
		return false
	}
	for _, l := range strings.Fields(out) {
		if l == "usb" {
			return true
		}
	}
	return false
}
