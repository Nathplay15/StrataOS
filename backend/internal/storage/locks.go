package storage

// Snapshot locks: a locked snapshot cannot be deleted before its date, by the
// retention, the web interface or the API — the protection against
// ransomware and against a stolen administrator account.
//
// ZFS: a user hold ("strata-lock", the pool refuses to destroy the snapshot)
// and the property strata:lockuntil (Unix time).
// Btrfs: the .snapshots folder is append-only (chattr +a: snapshots can be
// added, none can be removed) and .snapshots/.locks.json records the dates.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/sys"
)

const zfsHold = "strata-lock"

var btrfsLockMu sync.Mutex

// LockZFSSnapshot locks snap (dataset@name) until the given time.
func LockZFSSnapshot(snap string, until time.Time, recursive bool) error {
	if err := validDS(snap); err != nil || !strings.Contains(snap, "@") {
		return fmt.Errorf("invalid snapshot")
	}
	args := []string{"set"}
	if _, err := sys.Run("zfs", append(args, "strata:lockuntil="+strconv.FormatInt(until.Unix(), 10), snap)...); err != nil {
		return err
	}
	hold := []string{"hold"}
	if recursive {
		hold = append(hold, "-r")
	}
	_, err := sys.Run("zfs", append(hold, zfsHold, snap)...)
	if err != nil && strings.Contains(err.Error(), "tag already exists") {
		return nil
	}
	return err
}

// ZFSLockUntil returns the lock date of a snapshot (zero when not locked).
func ZFSLockUntil(snap string) time.Time {
	out, err := sys.Run("zfs", "get", "-Hp", "-o", "value", "strata:lockuntil", snap)
	if err != nil {
		return time.Time{}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// unlockZFSIfExpired releases the hold of a snapshot whose lock is over.
func unlockZFSIfExpired(snap string, recursive bool) error {
	until := ZFSLockUntil(snap)
	if until.IsZero() {
		return nil
	}
	if time.Now().Before(until) {
		return fmt.Errorf("snapshot %s is locked until %s", snap, until.Format("2006-01-02 15:04"))
	}
	args := []string{"release"}
	if recursive {
		args = append(args, "-r")
	}
	_, err := sys.Run("zfs", append(args, zfsHold, snap)...)
	if err != nil && strings.Contains(err.Error(), "no such tag") {
		return nil
	}
	return err
}

func btrfsLocksPath(mp string) string { return filepath.Join(mp, ".snapshots", ".locks.json") }

func btrfsLocks(mp string) map[string]int64 {
	m := map[string]int64{}
	if b, err := os.ReadFile(btrfsLocksPath(mp)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// setAppendOnly toggles chattr +a on the .snapshots folder.
func setAppendOnly(mp string, on bool) error {
	flag := "-a"
	if on {
		flag = "+a"
	}
	_, err := sys.Run("chattr", flag, filepath.Join(mp, ".snapshots"))
	return err
}

// LockBtrfsSnapshot locks .snapshots/<name> of the volume mounted at mp.
func LockBtrfsSnapshot(mp, name string, until time.Time) error {
	btrfsLockMu.Lock()
	defer btrfsLockMu.Unlock()
	m := btrfsLocks(mp)
	m[name] = until.Unix()
	// Forget locks of snapshots that no longer exist or have expired.
	for n, t := range m {
		if _, err := os.Stat(filepath.Join(mp, ".snapshots", n)); err != nil || time.Unix(t, 0).Before(time.Now()) && n != name {
			delete(m, n)
		}
	}
	b, _ := json.Marshal(m)
	_ = setAppendOnly(mp, false)
	err := sys.WriteFileAtomic(btrfsLocksPath(mp), b, 0o600)
	if e := setAppendOnly(mp, true); err == nil {
		err = e
	}
	return err
}

// BtrfsLockUntil returns the lock date of .snapshots/<name>.
func BtrfsLockUntil(mp, name string) time.Time {
	if t, ok := btrfsLocks(mp)[name]; ok {
		return time.Unix(t, 0)
	}
	return time.Time{}
}

// BtrfsSnapshotsLocked reports whether a volume has snapshots still locked.
func BtrfsSnapshotsLocked(mp string) bool {
	for _, t := range btrfsLocks(mp) {
		if time.Now().Before(time.Unix(t, 0)) {
			return true
		}
	}
	return false
}

// withSnapshotsWritable runs fn with the append-only flag lifted when needed.
func withSnapshotsWritable(mp string, fn func() error) error {
	btrfsLockMu.Lock()
	defer btrfsLockMu.Unlock()
	attrs, _ := sys.Run("lsattr", "-d", filepath.Join(mp, ".snapshots"))
	appendOnly := strings.Contains(strings.Fields(attrs + " -")[0], "a")
	if appendOnly {
		if err := setAppendOnly(mp, false); err != nil {
			return err
		}
		defer setAppendOnly(mp, true)
	}
	return fn()
}

// ZFSPoolHasLocks reports whether a pool holds locked snapshots.
func ZFSPoolHasLocks(pool string) bool {
	out, err := sys.Run("zfs", "get", "-Hp", "-r", "-t", "snapshot", "-o", "value", "strata:lockuntil", pool)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		if n, err := strconv.ParseInt(strings.TrimSpace(l), 10, 64); err == nil && time.Now().Before(time.Unix(n, 0)) {
			return true
		}
	}
	return false
}
