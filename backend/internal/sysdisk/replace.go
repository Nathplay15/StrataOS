package sysdisk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"strataos/internal/storage"
	"strataos/internal/sys"
)

// Replacing a disk of the system mirror (Storage → Disks → Replace): the
// mirror is one group made of whole disks, whatever the number of arrays on
// them (system, swap). The new disk gets the partitions of the disk that
// stays, each array its member, the EFI system partition a copy and GRUB is
// installed on it (BIOS).

// MirrorGroupID is the replacement group of the system mirror.
const MirrorGroupID = "sys:mirror"

// MirrorGroup describes the system mirror as a replacement group.
func MirrorGroup(l Layout, disks []storage.BlockDevice) (storage.ReplaceGroup, bool) {
	ra := l.RootArray()
	if !l.Mirrored || ra == nil {
		return storage.ReplaceGroup{}, false
	}
	g := storage.ReplaceGroup{ID: MirrorGroupID, Kind: "system", Name: "System mirror", Device: ra.Device, Level: "raid1",
		Tolerates: max(ra.Devices-1, 0), UsedBy: []string{"the operating system"}}
	state := map[string]string{} // disk → worst member state
	for _, a := range l.Arrays {
		if a.Role == "other" {
			continue
		}
		if a.Sync != "" {
			g.Rebuilding, g.Progress = true, a.Percent
		}
	}
	raids, _ := storage.ListRaid()
	for _, a := range raids {
		sys := false
		for _, x := range l.Arrays {
			if x.Device == a.Device && x.Role != "other" {
				sys = true
			}
		}
		if !sys {
			continue
		}
		for _, m := range a.Members {
			if m.Device == "" {
				continue
			}
			d := diskOf(disks, m.Device)
			st := "active"
			switch {
			case strings.Contains(m.State, "faulty"):
				st = "faulty"
			case strings.Contains(m.State, "rebuilding"):
				st = "rebuilding"
			case strings.Contains(m.State, "spare"):
				st = "spare"
			}
			if prev, ok := state[d]; !ok || rank(st) > rank(prev) {
				state[d] = st
			}
		}
	}
	present := 0
	for _, d := range disks {
		st, ok := state[d.Path]
		if !ok {
			continue
		}
		g.Members = append(g.Members, storage.ReplaceMember{Name: d.Path, Disk: d.Path, Serial: d.Serial, Model: d.Model, Size: d.Size, State: st, Detail: st})
		if st != "spare" {
			present++
		}
	}
	for i := present; i < ra.Devices; i++ {
		g.Members = append(g.Members, storage.ReplaceMember{State: "missing"})
	}
	return g, true
}

func rank(s string) int {
	return map[string]int{"active": 0, "spare": 1, "rebuilding": 2, "faulty": 3}[s]
}

func diskOf(disks []storage.BlockDevice, dev string) string {
	for _, d := range disks {
		if d.Path == dev {
			return d.Path
		}
		for _, c := range d.Children {
			if c.Path == dev {
				return d.Path
			}
		}
	}
	return ""
}

// MirrorReplace replaces a disk of the system mirror.
type MirrorReplace struct {
	Mode    string // add-first | degraded
	OldDisk string // "" when it is gone
	New     string
}

type sysPart struct {
	array string // array device ("" for the ESP)
	num   int
	old   string // partition of the old disk in that array ("" when gone)
}

// parts finds, on the disk that stays, the partition of each system array
// and the ESP.
func (o MirrorReplace) parts() (Layout, string, []sysPart, int, error) {
	l, _, err := Detect()
	if err != nil {
		return l, "", nil, 0, err
	}
	if !l.Mirrored {
		return l, "", nil, 0, errors.New("the system is not mirrored")
	}
	var keep string
	for _, d := range l.Disks {
		if d.Path != o.OldDisk && d.Path != o.New {
			keep = d.Path
		}
	}
	if keep == "" {
		return l, "", nil, 0, errors.New("no healthy disk of the system mirror left to copy from")
	}
	tree, _ := Tree()
	var out []sysPart
	esp := 0
	for _, d := range tree {
		if d.Path != keep {
			continue
		}
		for i, p := range d.Children {
			num := partNum(p.Path, i+1)
			if strings.ToLower(p.PartType) == TypeESP {
				esp = num
			}
			for _, a := range l.Arrays {
				if a.Role == "other" || !contains(a.Members, p.Path) {
					continue
				}
				sp := sysPart{array: a.Device, num: num}
				if o.OldDisk != "" {
					for _, m := range a.Members {
						if strings.HasPrefix(m, o.OldDisk) && m != o.OldDisk {
							sp.old = m
						}
					}
				}
				out = append(out, sp)
			}
		}
	}
	if len(out) == 0 {
		return l, "", nil, 0, fmt.Errorf("no system array found on %s", keep)
	}
	return l, keep, out, esp, nil
}

func partNum(path string, fallback int) int {
	b, err := sys.Run("cat", "/sys/class/block/"+strings.TrimPrefix(path, "/dev/")+"/partition")
	if err != nil {
		return fallback
	}
	n := 0
	_, _ = fmt.Sscan(strings.TrimSpace(b), &n)
	if n == 0 {
		return fallback
	}
	return n
}

func logRun(logf func(string, ...any), timeout time.Duration, stdin, name string, args ...string) (string, error) {
	logf("$ %s %s", name, strings.Join(args, " "))
	out, err := sys.RunOpts(context.Background(), sys.Opts{Timeout: timeout, Stdin: stdin}, name, args...)
	if err != nil {
		logf("  failed: %v", err)
	}
	return out, err
}

// Start partitions the new disk like the one that stays and hands its
// partitions to the arrays.
func (o MirrorReplace) Start(ctx context.Context, logf func(string, ...any)) error {
	l, keep, parts, esp, err := o.parts()
	if err != nil {
		return err
	}
	if l.IsSystemDisk(o.New) {
		return fmt.Errorf("%s is a disk of the running system", o.New)
	}
	storage.Lock.Lock()
	defer storage.Lock.Unlock()
	logf("Erasing %s", o.New)
	if err := storage.Wipe(o.New); err != nil {
		return err
	}
	dump, err := logRun(logf, time.Minute, "", "sfdisk", "--dump", keep)
	if err != nil {
		return err
	}
	// The identifiers of the disk and partitions must not be copied.
	var lines []string
	for _, ln := range strings.Split(dump, "\n") {
		if strings.HasPrefix(ln, "label-id:") || strings.HasPrefix(ln, "device:") || strings.HasPrefix(ln, "last-lba:") {
			continue
		}
		f := strings.Split(ln, ",")
		for i := range f {
			if strings.HasPrefix(strings.TrimSpace(f[i]), "uuid=") {
				f[i] = ""
			}
		}
		lines = append(lines, strings.ReplaceAll(strings.Join(f, ","), ",,", ","))
	}
	if _, err := logRun(logf, time.Minute, strings.Join(lines, "\n")+"\n", "sfdisk", "--no-reread", o.New); err != nil {
		return err
	}
	_, _ = logRun(logf, time.Minute, "", "partprobe", o.New)
	_, _ = logRun(logf, time.Minute, "", "udevadm", "settle")
	for _, p := range parts {
		np := PartPath(o.New, p.num)
		_, _ = logRun(logf, time.Minute, "", "wipefs", "-a", np)
		if o.Mode == "degraded" && p.old != "" {
			_, _ = logRun(logf, time.Minute, "", "mdadm", "--manage", p.array, "--fail", p.old)
			_, _ = logRun(logf, time.Minute, "", "mdadm", "--manage", p.array, "--remove", p.old)
		}
		if _, err := logRun(logf, time.Minute, "", "mdadm", "--manage", p.array, "--add", np); err != nil {
			return err
		}
		if o.Mode == "add-first" && p.old != "" {
			if _, err := logRun(logf, time.Minute, "", "mdadm", "--manage", p.array, "--replace", p.old, "--with", np); err != nil {
				return err
			}
		}
	}
	if esp > 0 {
		if _, err := logRun(logf, time.Minute, "", "mkfs.vfat", "-F", "32", "-n", "EFI", PartPath(o.New, esp)); err != nil {
			return err
		}
	}
	return storage.SaveMdadmConf()
}

// Resume: the kernel goes on rebuilding by itself.
func (o MirrorReplace) Resume() {}

// Poll follows the rebuild of every system array.
func (o MirrorReplace) Poll() (storage.ReplaceStatus, error) {
	var st storage.ReplaceStatus
	l, _, parts, _, err := o.parts()
	if err != nil {
		return st, err
	}
	_ = l
	raids, _ := storage.ListRaid()
	total := 0.0
	done := true
	for _, p := range parts {
		np := PartPath(o.New, p.num)
		var a *storage.RaidArray
		for i := range raids {
			if raids[i].Device == p.array {
				a = &raids[i]
			}
		}
		if a == nil {
			return st, fmt.Errorf("array %s not found", p.array)
		}
		var nm *storage.RaidMember
		for i := range a.Members {
			if sameDevice(a.Members[i].Device, np) {
				nm = &a.Members[i]
			}
		}
		if nm == nil {
			return st, fmt.Errorf("%s is no longer in %s", np, a.Device)
		}
		if strings.Contains(nm.State, "faulty") {
			return st, fmt.Errorf("%s failed during the rebuild", np)
		}
		switch {
		case strings.Contains(nm.State, "active sync") && a.SyncAction == "":
			total += 100
		case strings.HasPrefix(a.SyncAction, "recovery") && !strings.Contains(a.SyncAction, "("):
			total += a.SyncProgress
			done = false
		default:
			done = false
			st.Waiting = true
		}
	}
	st.Progress = total / float64(len(parts))
	if done {
		st.Done, st.Progress, st.Waiting = true, 100, false
	}
	return st, nil
}

// Finish takes the old partitions out and makes the new disk bootable.
func (o MirrorReplace) Finish(logf func(string, ...any)) error {
	raids, _ := storage.ListRaid()
	l, _, _, esp, err := o.parts()
	if err != nil {
		return err
	}
	storage.Lock.Lock()
	for _, a := range raids {
		sys := false
		for _, x := range l.Arrays {
			if x.Device == a.Device && x.Role != "other" {
				sys = true
			}
		}
		if !sys {
			continue
		}
		for _, m := range a.Members {
			if m.Device != "" && strings.Contains(m.State, "faulty") {
				_, _ = logRun(logf, time.Minute, "", "mdadm", "--manage", a.Device, "--remove", m.Device)
				_, _ = logRun(logf, time.Minute, "", "mdadm", "--zero-superblock", m.Device)
			}
		}
	}
	storage.Lock.Unlock()
	if err := storage.SaveMdadmConf(); err != nil {
		return err
	}
	if l.Boot == "uefi" && esp > 0 {
		if _, err := logRun(logf, 5*time.Minute, "", espSync, "--setup"); err != nil {
			return err
		}
		return nil
	}
	if _, err := logRun(logf, 5*time.Minute, "", "grub-install", "--target=i386-pc", o.New); err != nil {
		return err
	}
	var devs []string
	for _, d := range l.Disks {
		devs = append(devs, byIDPath(d.Path))
	}
	if !contains(l.DiskPaths(), o.New) {
		devs = append(devs, byIDPath(o.New))
	}
	_, _ = logRun(logf, time.Minute, "grub-pc grub-pc/install_devices multiselect "+strings.Join(devs, ", ")+"\n", "debconf-set-selections")
	return nil
}

// DiskPaths lists the paths of the system disks.
func (l Layout) DiskPaths() []string {
	var out []string
	for _, d := range l.Disks {
		out = append(out, d.Path)
	}
	return out
}
