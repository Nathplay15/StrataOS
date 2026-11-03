package storage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

// ZCacheDev is an SSD helping a ZFS pool: read cache (L2ARC) or separate
// intent log (SLOG).
type ZCacheDev struct {
	Pool   string `json:"pool"`
	Role   string `json:"role"` // l2arc | slog
	Device string `json:"device"`
}

// ZFSCacheDevices lists the cache and log devices of the imported pools.
func ZFSCacheDevices() []ZCacheDev {
	pools, _ := ListZPools(false)
	var out []ZCacheDev
	for _, p := range pools {
		st, err := sys.Run("zpool", "status", "-P", p.Name)
		if err != nil {
			continue
		}
		for _, d := range parseZCacheDevs(p.Name, st) {
			if real, err := filepath.EvalSymlinks(d.Device); err == nil {
				d.Device = real
			}
			out = append(out, d)
		}
	}
	return out
}

// parseZCacheDevs reads the "logs" and "cache" sections of "zpool status -P".
func parseZCacheDevs(pool, status string) []ZCacheDev {
	var out []ZCacheDev
	role := ""
	for _, l := range strings.Split(status, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case pool, "spares", "special", "dedup":
			role = ""
		case "logs":
			role = "slog"
		case "cache":
			role = "l2arc"
		default:
			if role != "" && strings.HasPrefix(f[0], "/dev/") {
				out = append(out, ZCacheDev{Pool: pool, Role: role, Device: f[0]})
			}
		}
	}
	return out
}

// L2ARCStats returns the L2ARC hits and misses since the ZFS module loaded
// (all pools together).
func L2ARCStats() (hits, misses int64, ok bool) {
	b, err := os.ReadFile("/proc/spl/kstat/zfs/arcstats")
	if err != nil {
		return 0, 0, false
	}
	return parseARCStats(string(b))
}

func parseARCStats(s string) (hits, misses int64, ok bool) {
	var h, m bool
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) != 3 {
			continue
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "l2_hits":
			hits, h = n, true
		case "l2_misses":
			misses, m = n, true
		}
	}
	return hits, misses, h && m
}

// DiskOf returns the disk holding dev (itself or one of its partitions).
func DiskOf(disks []BlockDevice, dev string) *BlockDevice {
	for i := range disks {
		found := false
		walk(disks[i], func(b BlockDevice) {
			if b.Path == dev {
				found = true
			}
		})
		if found {
			return &disks[i]
		}
	}
	return nil
}
