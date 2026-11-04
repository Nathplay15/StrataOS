package storage

import (
	"fmt"
	"strings"
)

// deviceOn returns the whole disk holding dev and dev itself (the disk, or a
// partition), resolving /dev/disk/by-id links.
func deviceOn(dev string, disks []BlockDevice) (BlockDevice, BlockDevice, bool) {
	if dev == "" {
		return BlockDevice{}, BlockDevice{}, false
	}
	real := resolve(dev)
	for _, d := range disks {
		var hit BlockDevice
		found := false
		walk(d, func(b BlockDevice) {
			if !found && (b.Type == "disk" || b.Type == "part") && (b.Path == dev || b.Path == real) {
				hit, found = b, true
			}
		})
		if found {
			return d, hit, true
		}
	}
	return BlockDevice{}, BlockDevice{}, false
}

// raidTolerates is the number of members an mdadm array can lose.
func raidTolerates(level string, devices int) int {
	switch level {
	case "raid1":
		return max(devices-1, 0)
	case "raid4", "raid5", "raid10":
		return 1
	case "raid6":
		return 2
	}
	return 0
}

func zfsTolerates(v ZVdev) int {
	switch v.Type {
	case "mirror":
		n := 0
		for _, l := range v.Leaves {
			if !l.Replacing {
				n++
			}
		}
		// While a member is being replaced, the pair counts as one member.
		if len(v.Leaves) > n {
			n++
		}
		return max(n-1, 0)
	case "raidz1":
		return 1
	case "raidz2":
		return 2
	case "raidz3":
		return 3
	}
	return 0
}

func memberOf(name string, disks []BlockDevice) ReplaceMember {
	m := ReplaceMember{Name: name}
	if d, part, ok := deviceOn(name, disks); ok {
		m.Disk, m.Serial, m.Model, m.Size = d.Path, d.Serial, d.Model, part.Size
	}
	return m
}

func raidGroup(a RaidArray, disks []BlockDevice) ReplaceGroup {
	g := ReplaceGroup{ID: "raid:" + a.Name, Kind: "raid", Name: firstOf(a.Label, a.Name), Device: a.Device, Level: a.Level,
		Tolerates: raidTolerates(a.Level, a.RaidDevices), MinSize: a.UsedDevSize}
	if strings.HasPrefix(a.SyncAction, "recovery") || strings.HasPrefix(a.SyncAction, "resync") || strings.HasPrefix(a.SyncAction, "reshape") || a.Health == "rebuilding" {
		g.Rebuilding, g.Progress = true, a.SyncProgress
	}
	if a.UsedBy != "" {
		g.UsedBy = append(g.UsedBy, "LVM volume group "+a.UsedBy)
	} else if a.FSType != "" {
		g.UsedBy = append(g.UsedBy, a.FSType+" file system")
	}
	// A faulty member still listed also shows as a "removed" slot: keep the
	// faulty one (it names the disk to pull).
	faulty := 0
	for _, m := range a.Members {
		if strings.Contains(m.State, "faulty") {
			faulty++
		}
	}
	for _, m := range a.Members {
		st := m.State
		var rm ReplaceMember
		switch {
		case strings.Contains(st, "removed") || m.Device == "":
			if faulty > 0 {
				faulty--
				continue
			}
			rm = ReplaceMember{Name: "", State: "missing"}
		default:
			rm = memberOf(m.Device, disks)
			switch {
			case strings.Contains(st, "faulty"):
				rm.State = "faulty"
			case strings.Contains(st, "rebuilding"):
				rm.State = "rebuilding"
			case strings.Contains(st, "spare"):
				rm.State = "spare"
			default:
				rm.State = "active"
			}
		}
		rm.Detail = st
		g.Members = append(g.Members, rm)
	}
	return g
}

func zfsGroups(p ZPool, disks []BlockDevice) []ReplaceGroup {
	var out []ReplaceGroup
	scan := ParseZScan(p.Status)
	for _, v := range ParseZTopology(p.Status) {
		g := ReplaceGroup{ID: "zfs:" + p.Name + ":" + v.Name, Kind: "zfs", Name: p.Name, Level: v.Type, Vdev: v.Name, Tolerates: zfsTolerates(v)}
		if scan.Kind == "resilver" && scan.Running {
			g.Rebuilding = true
			g.Progress, _ = ParseZScanProgress(p.Status)
		}
		for _, l := range v.Leaves {
			rm := ReplaceMember{Name: l.Name, Was: l.Was, Detail: l.State}
			if strings.HasPrefix(l.Name, "/dev/") {
				rm = memberOf(l.Name, disks)
				rm.Was, rm.Detail = l.Was, l.State
			}
			switch l.State {
			case "ONLINE", "DEGRADED":
				rm.State = "active"
			case "UNAVAIL", "REMOVED", "OFFLINE":
				rm.State = "missing"
				if rm.Disk != "" {
					rm.State = "faulty"
				}
			case "FAULTED":
				rm.State = "faulty"
			default:
				rm.State = strings.ToLower(l.State)
			}
			if l.Replacing {
				g.Rebuilding = g.Rebuilding || scan.Running
			}
			g.Members = append(g.Members, rm)
		}
		if n := len(p.Datasets); n > 0 {
			g.UsedBy = append(g.UsedBy, fmt.Sprintf("%d ZFS dataset(s)", n))
		}
		out = append(out, g)
	}
	return out
}

func lvmGroup(vg VG, disks []BlockDevice) (ReplaceGroup, bool) {
	g := ReplaceGroup{ID: "lvm:" + vg.Name, Kind: "lvm", Name: vg.Name, Level: "linear"}
	onDisk := false
	for _, pv := range vg.PVs {
		var rm ReplaceMember
		if pv.Name == "" || strings.HasPrefix(pv.Name, "[unknown]") {
			rm = ReplaceMember{Name: pv.Name, State: "missing"}
		} else {
			rm = memberOf(pv.Name, disks)
			rm.State = "active"
			// A PV on an md array or another LV is not a disk: replace the disk at that level.
			if rm.Disk == "" {
				continue
			}
			onDisk = true
		}
		rm.Size, rm.Used = pv.Size, pv.Size-pv.Free
		g.Members = append(g.Members, rm)
	}
	for _, lv := range vg.LVs {
		g.UsedBy = append(g.UsedBy, "logical volume "+lv.Name)
		// Cached, RAID and thin volumes have sub-volumes pvmove handles badly.
		if lv.Type == "cache" || lv.Type == "writecache" || strings.HasPrefix(lv.Type, "raid") || strings.HasPrefix(lv.Type, "thin") || lv.Type == "mirror" {
			g.Unsupported = fmt.Sprintf("The volume group %s holds a %s volume (%s): replace its disks from the command line, or remove the cache first.", vg.Name, lv.Type, lv.Name)
		}
	}
	return g, onDisk
}

// ReplaceGroups lists the redundancy groups of the NAS with their members.
func ReplaceGroups(disks []BlockDevice) []ReplaceGroup {
	var out []ReplaceGroup
	if raids, err := ListRaid(); err == nil {
		for _, a := range raids {
			out = append(out, raidGroup(a, disks))
		}
	}
	if ZFSAvailable() {
		if pools, err := ListZPools(true); err == nil {
			for _, p := range pools {
				out = append(out, zfsGroups(p, disks)...)
			}
		}
	}
	if vgs, err := ListLVM(); err == nil {
		for _, vg := range vgs {
			if g, ok := lvmGroup(vg, disks); ok {
				out = append(out, g)
			}
		}
	}
	return out
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
