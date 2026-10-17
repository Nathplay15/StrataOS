package storage

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

// QuotaTarget is a place where per-user quotas can apply: an ext4/XFS volume or a ZFS dataset.
type QuotaTarget struct {
	Kind       string `json:"kind"`   // ext4 | xfs | zfs | btrfs
	Target     string `json:"target"` // volume ID or ZFS dataset
	Name       string `json:"name"`
	MountPoint string `json:"mountPoint"`
	Enabled    bool   `json:"enabled"`
	Supported  bool   `json:"supported"`
}

type UserQuota struct {
	QuotaTarget
	Used  int64 `json:"used"`  // bytes
	Limit int64 `json:"limit"` // bytes, 0 = unlimited
}

// QuotaActive reports whether user quota limits are enforced on a mounted ext4/XFS
// filesystem. For ext4 the quota feature alone only tracks usage: limits apply when
// the filesystem is mounted with usrquota (or quotaon was run).
func QuotaActive(mp, fstype string) bool {
	if fstype != "ext4" && fstype != "xfs" {
		return false
	}
	out, _ := sys.Run("findmnt", "-no", "OPTIONS", mp)
	return strings.Contains(out, "usrquota") || strings.Contains(out, "uquota")
}

// EnsureQuotaOn turns enforcement on for an ext4 volume mounted with usrquota.
func EnsureQuotaOn(mp, fstype string) {
	if fstype == "ext4" && QuotaActive(mp, fstype) {
		_, _ = sys.Run("quotaon", "-u", mp)
	}
}

// EnableQuota turns on user quotas for an ext4/XFS volume. The filesystem must be
// unmounted for a moment, so nothing may be using it.
func EnableQuota(ctx context.Context, mp, uuid, fstype string, logf func(string, ...any)) error {
	if !strings.HasPrefix(mp, MountRoot+"/") {
		return fmt.Errorf("not a StrataOS volume")
	}
	if !sys.Has("setquota") {
		return fmt.Errorf("the quota package is not installed")
	}
	dev := DeviceOf(mp)
	if dev == "" {
		dev = FindByUUID(uuid)
	}
	if dev == "" {
		return fmt.Errorf("volume device not found")
	}
	Lock.Lock()
	defer Lock.Unlock()
	logf("Unmounting %s", mp)
	if _, err := sys.RunCtx(ctx, "umount", mp); err != nil {
		users, _ := sys.RunOpts(ctx, sys.Opts{AllowCodes: []int{1}}, "fuser", "-vm", mp)
		return fmt.Errorf("the volume is in use (stop the applications and close the files using it, then retry): %v %s", err, strings.TrimSpace(users))
	}
	var err error
	switch fstype {
	case "ext4":
		logf("Enabling the ext4 quota feature")
		if _, err = sys.RunCtx(ctx, "tune2fs", "-O", "quota", "-Q", "usrquota", dev); err == nil {
			err = fstabAddOption(mp, "usrquota")
		}
	case "xfs":
		err = fstabAddOption(mp, "uquota")
	default:
		err = fmt.Errorf("quotas are not supported on %s", fstype)
	}
	logf("Remounting %s", mp)
	_, _ = sys.Run("systemctl", "daemon-reload")
	if _, merr := sys.RunCtx(ctx, "mount", mp); merr != nil && err == nil {
		err = merr
	}
	if err == nil {
		EnsureQuotaOn(mp, fstype)
	}
	return err
}

// fstabAddOption adds a mount option to the fstab entry of mp.
func fstabAddOption(mp, opt string) error {
	data, err := os.ReadFile(fstabPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) >= 4 && !strings.HasPrefix(f[0], "#") && f[1] == mp {
			found = true
			if !strings.Contains(","+f[3]+",", ","+opt+",") {
				f[3] += "," + opt
				lines[i] = strings.Join(f, " ")
			}
		}
	}
	if !found {
		return fmt.Errorf("no fstab entry for %s", mp)
	}
	return sys.WriteFileAtomic(fstabPath, []byte(strings.Join(lines, "\n")), 0o644)
}

// SetUserQuota sets (bytes > 0) or removes (0) a user's hard limit.
func SetUserQuota(t QuotaTarget, user string, bytes int64) error {
	if bytes < 0 {
		return fmt.Errorf("invalid limit")
	}
	switch t.Kind {
	case "zfs":
		if err := validDS(t.Target); err != nil {
			return err
		}
		v := "none"
		if bytes > 0 {
			v = strconv.FormatInt(bytes, 10)
		}
		_, err := sys.Run("zfs", "set", "userquota@"+user+"="+v, t.Target)
		return err
	case "ext4", "xfs":
		if !t.Enabled {
			return fmt.Errorf("enable quotas on volume %s first", t.Name)
		}
		kb := strconv.FormatInt((bytes+1023)/1024, 10)
		if bytes == 0 {
			kb = "0"
		}
		_, err := sys.Run("setquota", "-u", user, "0", kb, "0", "0", t.MountPoint)
		return err
	}
	return fmt.Errorf("per-user quotas are not available on %s", t.Kind)
}

// GetUserQuota returns usage and limit for a user on one target.
func GetUserQuota(t QuotaTarget, user string) (used, limit int64) {
	switch t.Kind {
	case "zfs":
		out, err := sys.Run("zfs", "get", "-Hp", "-o", "value", "userused@"+user+",userquota@"+user, t.Target)
		if err == nil {
			f := strings.Fields(out)
			if len(f) == 2 {
				used, _ = strconv.ParseInt(f[0], 10, 64)
				limit, _ = strconv.ParseInt(f[1], 10, 64)
			}
		}
	case "ext4", "xfs":
		if !t.Enabled {
			return 0, 0
		}
		out, err := sys.Run("repquota", "-u", "-O", "csv", t.MountPoint)
		if err != nil {
			return 0, 0
		}
		rows, _ := csv.NewReader(strings.NewReader(out)).ReadAll()
		if len(rows) < 2 {
			return 0, 0
		}
		col := map[string]int{}
		for i, h := range rows[0] {
			col[h] = i
		}
		for _, r := range rows[1:] {
			if len(r) > col["BlockHardLimit"] && r[col["User"]] == user {
				u, _ := strconv.ParseInt(r[col["BlockUsed"]], 10, 64)
				l, _ := strconv.ParseInt(r[col["BlockHardLimit"]], 10, 64)
				return u * 1024, l * 1024
			}
		}
	}
	return 0, 0
}
