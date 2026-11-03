package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// ReplaceOp carries out a disk replacement planned by PlanReplace, in three
// steps: Start (erase the new disk, hand it to mdadm/ZFS/LVM), Poll until
// the copy is done, Finish (take the old disk out). Device names are those of
// the current boot: the caller finds them again by serial number after a restart.
type ReplaceOp struct {
	Kind    string // raid | zfs | lvm
	Group   string // array label or name, pool, volume group
	Mode    string // add-first | degraded
	Old     string // member name as the tool knows it ("" = an empty RAID slot)
	OldDisk string // whole disk of Old ("" when it is gone)
	New     string // the new whole disk
}

// ReplaceStatus is a poll of a running replacement.
type ReplaceStatus struct {
	Done     bool
	Waiting  bool    // queued (another array syncing) or not started yet
	Progress float64 // percent
	ETA      int64   // seconds left announced by the tool (0: unknown)
	Warning  string  // finished, but with something to look at
}

func sameDev(a, b string) bool { return a != "" && b != "" && (a == b || resolve(a) == resolve(b)) }

// Start erases the new disk and starts the copy or rebuild onto it.
func (o ReplaceOp) Start(ctx context.Context, logf func(string, ...any)) error {
	if err := ValidateDevice(o.New); err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	switch o.Kind {
	case "raid":
		a, err := findArray(o.Group)
		if err != nil {
			return err
		}
		if o.Old != "" {
			if err := a.hasMember(o.Old); err != nil {
				return err
			}
		}
		logf("Erasing %s", o.New)
		if err := Wipe(o.New); err != nil {
			return err
		}
		if o.Mode == "degraded" && o.Old != "" {
			// The failed disk is still listed: take it out first.
			logf("Removing the failed disk %s from %s", o.Old, a.Device)
			_, _ = sys.Run("mdadm", "--manage", a.Device, "--fail", o.Old)
			if _, err := sys.Run("mdadm", "--manage", a.Device, "--remove", o.Old); err != nil {
				return err
			}
			_, _ = sys.Run("mdadm", "--zero-superblock", o.Old)
		}
		logf("mdadm --manage %s --add %s", a.Device, o.New)
		if _, err := sys.Run("mdadm", "--manage", a.Device, "--add", o.New); err != nil {
			return err
		}
		if o.Mode == "add-first" {
			logf("mdadm --manage %s --replace %s --with %s", a.Device, o.Old, o.New)
			if _, err := sys.Run("mdadm", "--manage", a.Device, "--replace", o.Old, "--with", o.New); err != nil {
				// Leave nothing half done: the new disk was only a spare.
				_, _ = sys.Run("mdadm", "--manage", a.Device, "--remove", o.New)
				_, _ = sys.Run("mdadm", "--zero-superblock", o.New)
				return err
			}
		}
		return SaveMdadmConf()
	case "zfs":
		if err := ValidName(o.Group); err != nil {
			return err
		}
		if err := validVdev(o.Old); err != nil {
			return err
		}
		logf("Erasing %s", o.New)
		if err := Wipe(o.New); err != nil {
			return err
		}
		nd := byID(o.New)
		logf("zpool replace %s %s %s", o.Group, o.Old, nd)
		_, err := sys.RunOpts(ctx, sys.Opts{Timeout: 10 * time.Minute}, "zpool", "replace", "-f", o.Group, o.Old, nd)
		return err
	case "lvm":
		if err := ValidName(o.Group); err != nil {
			return err
		}
		if !devNameRe.MatchString(o.Old) || pvVG(o.Old) != o.Group {
			return fmt.Errorf("%s is not a physical volume of %s", o.Old, o.Group)
		}
		logf("Erasing %s", o.New)
		if err := Wipe(o.New); err != nil {
			return err
		}
		if _, err := sys.Run("pvcreate", "-ff", "-y", o.New); err != nil {
			return err
		}
		logf("vgextend %s %s", o.Group, o.New)
		if _, err := sys.Run("vgextend", o.Group, o.New); err != nil {
			_, _ = sys.Run("pvremove", "-ff", "-y", o.New)
			return err
		}
		// -b: the move runs in lvmpolld, and survives a restart of StrataOS.
		logf("pvmove -b %s %s", o.Old, o.New)
		if _, err := sys.Run("pvmove", "-b", "-i", "0", o.Old, o.New); err != nil {
			_, _ = sys.Run("vgreduce", o.Group, o.New)
			_, _ = sys.Run("pvremove", "-ff", "-y", o.New)
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown kind %q", o.Kind)
}

// Resume restarts what a reboot interrupted (an LVM move); RAID and ZFS
// continue by themselves.
func (o ReplaceOp) Resume() {
	if o.Kind == "lvm" && ValidName(o.Group) == nil {
		_, _ = sys.Run("pvmove", "-b", "-i", "0")
	}
}

// Poll reads the progress of the copy.
func (o ReplaceOp) Poll() (ReplaceStatus, error) {
	var st ReplaceStatus
	switch o.Kind {
	case "raid":
		a, err := findArray(o.Group)
		if err != nil {
			return st, err
		}
		var nm *RaidMember
		for i := range a.Members {
			if sameDev(a.Members[i].Device, o.New) {
				nm = &a.Members[i]
			}
		}
		if nm == nil {
			return st, fmt.Errorf("the new disk %s is no longer in %s", o.New, firstOf(a.Label, a.Name))
		}
		if strings.Contains(nm.State, "faulty") {
			return st, fmt.Errorf("the new disk %s failed during the rebuild", o.New)
		}
		if a.Health == "failed" {
			return st, fmt.Errorf("the array %s has failed", firstOf(a.Label, a.Name))
		}
		switch {
		case strings.HasPrefix(a.SyncAction, "recovery ("):
			st.Waiting = true
		case strings.HasPrefix(a.SyncAction, "recovery"):
			st.Progress, st.ETA = a.SyncProgress, ParseMdFinish(a.SyncFinish)
		case strings.Contains(nm.State, "active sync") || (strings.Contains(nm.State, "active") && !strings.Contains(nm.State, "rebuilding")):
			st.Done, st.Progress = true, 100
		default:
			// Spare not picked up yet, or a resync of the array comes first.
			st.Waiting = true
		}
		return st, nil
	case "zfs":
		if err := ValidName(o.Group); err != nil {
			return st, err
		}
		status, err := sys.Run("zpool", "status", "-P", o.Group)
		if err != nil {
			return st, err
		}
		disks, _ := ListDisks()
		scan := ParseZScan(status)
		var leaf *ZLeaf
		for _, v := range ParseZTopology(status) {
			for i, l := range v.Leaves {
				if d, _, ok := deviceOn(l.Name, disks); ok && sameDev(d.Path, o.New) {
					leaf = &v.Leaves[i]
				}
			}
		}
		if leaf == nil {
			return st, fmt.Errorf("the new disk %s is no longer in the pool %s", o.New, o.Group)
		}
		switch leaf.State {
		case "FAULTED", "UNAVAIL", "REMOVED":
			return st, fmt.Errorf("the new disk %s is %s", o.New, leaf.State)
		}
		if scan.Kind == "resilver" && scan.Running {
			st.Progress, st.ETA = ParseZScanProgress(status)
			return st, nil
		}
		if leaf.Replacing {
			st.Waiting = true // resilver queued, or the old disk not detached yet
			return st, nil
		}
		st.Done, st.Progress = true, 100
		if scan.Errors > 0 {
			st.Warning = fmt.Sprintf("the resilver found %d errors: run a scrub and check the pool status", scan.Errors)
		}
		return st, nil
	case "lvm":
		if err := ValidName(o.Group); err != nil {
			return st, err
		}
		out, err := sys.Run("lvs", "-a", "--noheadings", "--separator", "|", "-o", "lv_name,lv_attr,copy_percent", o.Group)
		if err != nil {
			return st, err
		}
		for _, l := range strings.Split(out, "\n") {
			f := strings.Split(strings.TrimSpace(l), "|")
			if len(f) == 3 && strings.HasPrefix(f[1], "p") {
				st.Progress, _ = strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
				return st, nil
			}
		}
		used, err := sys.Run("pvs", "--noheadings", "--units", "b", "--nosuffix", "-o", "pv_used", o.Old)
		if err != nil {
			return st, err
		}
		if n, _ := strconv.ParseInt(strings.TrimSpace(used), 10, 64); n > 0 {
			return st, fmt.Errorf("the move stopped before the end: %s still holds data", o.Old)
		}
		st.Done, st.Progress = true, 100
		return st, nil
	}
	return st, fmt.Errorf("unknown kind %q", o.Kind)
}

// Finish takes the old disk out once the copy is done.
func (o ReplaceOp) Finish(logf func(string, ...any)) error {
	Lock.Lock()
	defer Lock.Unlock()
	switch o.Kind {
	case "raid":
		a, err := findArray(o.Group)
		if err != nil {
			return err
		}
		// mdadm marks the replaced disk faulty at the end of --replace.
		for _, m := range a.Members {
			if m.Device != "" && strings.Contains(m.State, "faulty") {
				logf("Removing %s from %s", m.Device, a.Device)
				if _, err := sys.Run("mdadm", "--manage", a.Device, "--remove", m.Device); err != nil {
					return err
				}
				_, _ = sys.Run("mdadm", "--zero-superblock", m.Device)
			}
		}
		return SaveMdadmConf()
	case "zfs":
		// ZFS detached the old disk; clear its label so it is not taken for a pool member.
		if o.Mode == "add-first" && strings.HasPrefix(o.Old, "/dev/") && ValidateDevice(o.Old) == nil {
			if status, _ := sys.Run("zpool", "status", "-P"); !strings.Contains(status, o.Old) {
				logf("zpool labelclear %s", o.Old)
				_, _ = sys.Run("zpool", "labelclear", "-f", o.Old)
			}
		}
		return nil
	case "lvm":
		logf("vgreduce %s %s", o.Group, o.Old)
		if _, err := sys.Run("vgreduce", o.Group, o.Old); err != nil {
			return err
		}
		_, _ = sys.Run("pvremove", "-ff", "-y", o.Old)
		return nil
	}
	return nil
}

// Locate blinks (or stops blinking) the locate LED of a disk's bay with
// ledctl, when the enclosure supports it.
func Locate(dev string, on bool) error {
	if !sys.Has("ledctl") {
		return fmt.Errorf("ledctl is not installed (package ledmon)")
	}
	if err := validateBlockDevice(dev); err != nil {
		return err
	}
	pattern := "locate="
	if !on {
		pattern = "locate_off="
	}
	_, err := sys.Run("ledctl", pattern+dev)
	return err
}
