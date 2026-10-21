package storage

import (
	"os"
	"path/filepath"
	"strings"
)

// BlockInUse tells why a block device must not be handed to something else
// (an iSCSI initiator…): mounted, active swap, held by device-mapper, md or
// LVM, member of a pool. It returns "" when the device is free.
func BlockInUse(dev string) string {
	real := resolve(dev)
	name := filepath.Base(real)
	devs := []string{real}
	// Partitions of the device (zd0p1…) count too.
	if parts, _ := filepath.Glob("/sys/class/block/" + name + "/" + name + "*"); len(parts) > 0 {
		for _, p := range parts {
			devs = append(devs, "/dev/"+filepath.Base(p))
		}
	}
	swaps, _ := os.ReadFile("/proc/swaps")
	for _, d := range devs {
		if mounted(d) {
			return d + " is mounted"
		}
		for _, l := range strings.Split(string(swaps), "\n") {
			if f := strings.Fields(l); len(f) > 0 && resolve(f[0]) == d {
				return d + " is used as swap"
			}
		}
		if h, _ := os.ReadDir("/sys/class/block/" + filepath.Base(d) + "/holders"); len(h) > 0 {
			return d + " is used by " + h[0].Name()
		}
	}
	switch fs, _, _ := Probe(real); fs {
	case "LVM2_member", "linux_raid_member", "zfs_member", "swap", "crypto_LUKS", "bcache":
		return dev + " holds " + fs
	}
	return ""
}
