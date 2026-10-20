package api

import (
	"fmt"
	"strings"
	"time"

	"strataos/internal/storage"
)

// Long storage operations (RAID rebuild, ZFS scrub…) are notified once when
// they start and once when they end, never on each progress step: their
// identity (key) holds no number.

type progressOp struct {
	key   string // "raid:<array>:<action>" or "zfs:<pool>:<scrub|resilver>"
	start string // message when it starts
}

type progressEvent struct {
	level, msg string
}

// progressTracker remembers the operations running at the previous check.
type progressTracker struct {
	running map[string]time.Time // key -> first seen
}

// update compares the running operations with the previous check. end builds
// the message of an operation that is gone, from the current state.
func (t *progressTracker) update(cur []progressOp, now time.Time, end func(key string, d time.Duration) (level, msg string)) []progressEvent {
	if t.running == nil {
		t.running = map[string]time.Time{}
	}
	var out []progressEvent
	seen := map[string]bool{}
	for _, op := range cur {
		seen[op.key] = true
		if _, ok := t.running[op.key]; !ok {
			t.running[op.key] = now
			out = append(out, progressEvent{"info", op.start})
		}
	}
	for key, since := range t.running {
		if seen[key] {
			continue
		}
		delete(t.running, key)
		if level, msg := end(key, now.Sub(since)); msg != "" {
			out = append(out, progressEvent{level, msg})
		}
	}
	return out
}

// RAID sync actions as mdstat names them, and the words used for them.
var raidActionNames = map[string]string{"recovery": "rebuild", "resync": "resync", "check": "check", "repair": "repair", "reshape": "reshape"}

// raidAction returns the action of an array ("recovery (delayed)" counts as
// recovery, so a pending rebuild that starts is the same operation).
func raidAction(a storage.RaidArray) string {
	f := strings.Fields(a.SyncAction)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func raidOpKey(a storage.RaidArray) string {
	return "raid:" + firstNonEmpty(a.Label, a.Name) + ":" + raidAction(a)
}

func raidOps(raids []storage.RaidArray) []progressOp {
	var out []progressOp
	for _, a := range raids {
		act := raidAction(a)
		if act == "" {
			continue
		}
		out = append(out, progressOp{raidOpKey(a), fmt.Sprintf("RAID array %s: %s started", firstNonEmpty(a.Label, a.Name), actionWord(act))})
	}
	return out
}

func actionWord(act string) string {
	if w := raidActionNames[act]; w != "" {
		return w
	}
	return act
}

// raidEnd tells how a RAID operation ended: fine unless the array is now
// failed (a rebuild that stops on a degraded array also failed); an array
// that is gone was stopped or deleted.
func raidEnd(raids []storage.RaidArray, name, act string, d time.Duration) (string, string) {
	w := actionWord(act)
	for _, a := range raids {
		if firstNonEmpty(a.Label, a.Name) != name {
			continue
		}
		if a.Health == "failed" || a.Health == "inactive" || (a.Health == "degraded" && act == "recovery") {
			return "error", fmt.Sprintf("RAID array %s: %s failed after %s", name, w, opDuration(d))
		}
		return "info", fmt.Sprintf("RAID array %s: %s finished in %s", name, w, opDuration(d))
	}
	return "warning", fmt.Sprintf("RAID array %s: %s stopped before the end", name, w)
}

func zfsOps(scans map[string]storage.ZScan) []progressOp {
	var out []progressOp
	for pool, z := range scans {
		if z.Running {
			out = append(out, progressOp{"zfs:" + pool + ":" + z.Kind, fmt.Sprintf("ZFS pool %s: %s started", pool, z.Kind)})
		}
	}
	return out
}

// zfsEnd reads the result of the last scan of the pool.
func zfsEnd(scans map[string]storage.ZScan, pool, kind string, d time.Duration) (string, string) {
	z, ok := scans[pool]
	switch {
	case !ok || z.Kind != kind || z.Canceled:
		return "warning", fmt.Sprintf("ZFS pool %s: %s stopped before the end", pool, kind)
	case z.Errors > 0:
		return "error", fmt.Sprintf("ZFS pool %s: %s finished in %s with %d errors", pool, kind, opDuration(d), z.Errors)
	}
	return "info", fmt.Sprintf("ZFS pool %s: %s finished in %s", pool, kind, opDuration(d))
}

// storageEnd dispatches the end of an operation by its key.
func storageEnd(raids []storage.RaidArray, scans map[string]storage.ZScan) func(string, time.Duration) (string, string) {
	return func(key string, d time.Duration) (string, string) {
		p := strings.SplitN(key, ":", 3)
		if len(p) != 3 {
			return "", ""
		}
		switch p[0] {
		case "raid":
			return raidEnd(raids, p[1], p[2], d)
		case "zfs":
			return zfsEnd(scans, p[1], p[2], d)
		}
		return "", ""
	}
}

// opDuration writes 45 s, 12 min or 3 h 05 (checks run every few minutes:
// the duration is as precise as that).
func opDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %02d", int(d.Hours()), int(d.Minutes())%60)
}
