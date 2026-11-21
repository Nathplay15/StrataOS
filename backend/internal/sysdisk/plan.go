package sysdisk

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
)

// PartSpec is a partition of the new layout. Both disks of a mirror get the
// same partitions, numbered 1 (boot), 2 (swap, when the system has one) and
// 3 (system): the ISO installer's mirror layout is the same.
type PartSpec = store.SysDiskPart

// Sizes of the layout.
const (
	espMiB     = 512
	biosMiB    = 1
	maxSwapMiB = 8192
	// The first MiB holds the partition table and alignment, the last one
	// the backup GPT.
	reservedMiB = 2
)

// Sizes of the system on the data disks: the system partition is at least
// minSysMiB, and each disk keeps at least minDataMiB for data.
const (
	minSysMiB    = 20 << 10
	minDataMiB   = 1 << 10
	maxDataDisks = 8
)

// sysPartMiB is the system partition of the data disks layout: room for the
// system as it is now and half again, 20 GiB at least, whole GiB.
func sysPartMiB(used int64) int64 {
	mib := (rootNeed(used) + rootNeed(used)/2) >> 20
	mib = (mib + 1023) / 1024 * 1024
	return max(mib, minSysMiB)
}

// dataDisksLayout: boot, swap and system as on a mirror, then a data
// partition with the rest of each disk (storage.DataPartLabel).
func dataDisksLayout(boot string, used, swap int64) []PartSpec {
	parts := partLayout(boot, 0, swap, true)
	parts[len(parts)-1].MiB = sysPartMiB(used)
	return append(parts, PartSpec{Num: 4, Role: "data", MiB: 0, Code: "8300", Name: storage.DataPartLabel})
}

// Layout of a new system disk of size bytes (for a mirror: the smaller disk).
func partLayout(boot string, size, swap int64, mirror bool) []PartSpec {
	var parts []PartSpec
	if boot == "uefi" {
		parts = append(parts, PartSpec{Num: 1, Role: "esp", MiB: espMiB, Code: "EF00", Name: "EFI system"})
	} else {
		parts = append(parts, PartSpec{Num: 1, Role: "biosgrub", MiB: biosMiB, Code: "EF02", Name: "BIOS boot"})
	}
	used := parts[0].MiB
	if swap > 0 {
		mib := min((swap+(1<<20)-1)>>20, maxSwapMiB)
		code := "8200"
		if mirror {
			code = "FD00"
		}
		parts = append(parts, PartSpec{Num: 2, Role: "swap", MiB: mib, Code: code, Name: "swap"})
		used += mib
	}
	code := "8300"
	if mirror {
		code = "FD00"
	}
	parts = append(parts, PartSpec{Num: 3, Role: "root", MiB: size>>20 - reservedMiB - used, Code: code, Name: "StrataOS"})
	return parts
}

// Part returns the partition with that role (nil when none).
func Part(parts []PartSpec, role string) *PartSpec {
	for i := range parts {
		if parts[i].Role == role {
			return &parts[i]
		}
	}
	return nil
}

// Candidate is a disk that could receive the system.
type Candidate struct {
	Path   string   `json:"path"`
	Name   string   `json:"name"`
	Model  string   `json:"model,omitempty"`
	Serial string   `json:"serial,omitempty"`
	Size   int64    `json:"size"`
	Kind   string   `json:"kind,omitempty"`
	OK     bool     `json:"ok"`
	Reason string   `json:"reason,omitempty"`
	Weak   []string `json:"weak,omitempty"`
}

// PlanFacts is what the planning knows besides the layout.
type PlanFacts struct {
	Layout Layout
	Disks  []storage.BlockDevice          // storage.ListDisks
	Smart  map[string]storage.DiskVerdict // disk path → verdict
	Busy   map[string]string              // disk path → why it is reserved
	// Running: an operation on the system disk is not finished.
	Running string
	// LastConfigBackup: last export of the StrataOS configuration (zero: none).
	LastConfigBackup time.Time
	// RestorePoints: restore points can be made on this system.
	RestorePoints bool
	Now           time.Time
}

// Plan says whether and how the system can be mirrored or moved.
type Plan struct {
	Kind       string      `json:"kind"` // mirror | move | datadisks
	Boot       string      `json:"boot"`
	FSType     string      `json:"fsType"`
	Old        *DiskRef    `json:"old,omitempty"`
	Need       int64       `json:"need"` // smallest disk that fits
	Used       int64       `json:"used"`
	Candidates []Candidate `json:"candidates"`
	Target     *Candidate  `json:"target,omitempty"`
	// Targets: the disks of a datadisks plan (Target is the first).
	Targets []Candidate `json:"targets,omitempty"`
	// OldDisks: every disk of the system (several when it is mirrored).
	OldDisks []DiskRef `json:"oldDisks,omitempty"`
	// Confirm: what the user types to confirm (the names of the disks erased).
	Confirm  string     `json:"confirm,omitempty"`
	Parts    []PartSpec `json:"parts,omitempty"`
	Refusal  string     `json:"refusal,omitempty"`
	Warnings []string   `json:"warnings,omitempty"`
	Steps    []string   `json:"steps,omitempty"`
}

// Room the copy needs beyond the space used now: 10 % and 1 GiB.
func rootNeed(used int64) int64 { return used + used/10 + 1<<30 }

// PlanMirror plans the mirroring of a single-disk system onto target ("":
// only the candidates).
func PlanMirror(f PlanFacts, target string) Plan { return plan(f, "mirror", one(target)) }

// PlanMove plans the move of the system onto target (also off a mirror or
// the data disks: the system then lives on one disk of its own).
func PlanMove(f PlanFacts, target string) Plan { return plan(f, "move", one(target)) }

// PlanDataDisks plans the move of the system onto several data disks: each
// gets a copy of the system (RAID 1 over all of them) and a data partition
// with the rest of its space.
func PlanDataDisks(f PlanFacts, targets []string) Plan { return plan(f, "datadisks", targets) }

func one(t string) []string {
	if t == "" {
		return nil
	}
	return []string{t}
}

func plan(f PlanFacts, kind string, targets []string) Plan {
	l := f.Layout
	p := Plan{Kind: kind, Boot: l.Boot, FSType: l.Root.FSType, Used: l.Used, Candidates: []Candidate{}}
	if len(l.Disks) > 0 {
		d := l.Disks[0]
		p.Old = &d
		p.OldDisks = l.Disks
	}
	swap := l.SwapSize
	bootMiB := int64(biosMiB)
	if l.Boot == "uefi" {
		bootMiB = espMiB
	}
	p.Need = (reservedMiB+bootMiB+min((swap+(1<<20)-1)>>20, maxSwapMiB))<<20 + rootNeed(l.Used)
	if kind == "datadisks" {
		p.Need = (reservedMiB + bootMiB + min((swap+(1<<20)-1)>>20, maxSwapMiB) + sysPartMiB(l.Used) + minDataMiB) << 20
	}

	switch {
	case len(l.Problems) > 0:
		p.Refusal = l.Problems[0]
	case f.Running != "":
		p.Refusal = f.Running
	case kind == "mirror" && l.Mirrored:
		p.Refusal = "The system is already mirrored. To change one of its disks, use Replace on the Disks page."
	case kind == "datadisks" && l.Mirrored:
		p.Refusal = "The system is already on several disks: move it to one disk first, then onto the data disks."
	case kind == "move" && l.Degraded:
		p.Refusal = "The system mirror is degraded: replace its missing disk first (Disks → Replace)."
	case len(l.Disks) == 0 || (len(l.Disks) != 1 && !l.Mirrored):
		p.Refusal = "The system disk could not be identified."
	case l.Arch != "amd64" && l.Arch != "arm64":
		p.Refusal = fmt.Sprintf("Unsupported architecture %s.", l.Arch)
	case l.Arch == "arm64" && l.Boot != "uefi":
		p.Refusal = "This ARM machine does not start through UEFI: StrataOS cannot install a boot loader on another disk."
	}

	// Candidates.
	for _, d := range f.Disks {
		if d.System || l.IsSystemDisk(d.Path) {
			continue
		}
		c := Candidate{Path: d.Path, Name: d.Name, Model: d.Model, Serial: d.Serial, Size: d.Size, Kind: d.Kind, OK: true}
		v := f.Smart[d.Path]
		c.Weak = v.Weak
		size := d.Size
		if kind == "mirror" && p.Old != nil {
			size = min(size, p.Old.Size)
		}
		switch {
		case f.Busy[d.Path] != "":
			c.OK, c.Reason = false, "reserved: "+f.Busy[d.Path]
		case d.ActiveUse != "":
			c.OK, c.Reason = false, "in use ("+strings.TrimSpace(d.ActiveUse+" "+d.ActiveBy)+")"
		case d.Usage == "ceph":
			c.OK, c.Reason = false, "used by Ceph"
		case d.Usage != "free" && d.Usage != "":
			c.OK, c.Reason = false, "holds data ("+strings.TrimSpace(d.Usage+" "+d.UsedBy)+"): erase it first in Storage → Disks if you are sure"
		case d.Removable && d.Kind != "usb":
			c.OK, c.Reason = false, "removable media"
		case v.Failing:
			c.OK, c.Reason = false, "fails its SMART health check"
		case size < p.Need:
			c.OK, c.Reason = false, "too small: "+human(p.Need)+" needed"
		}
		p.Candidates = append(p.Candidates, c)
	}
	sort.SliceStable(p.Candidates, func(i, j int) bool {
		a, b := p.Candidates[i], p.Candidates[j]
		if a.OK != b.OK {
			return a.OK
		}
		return a.Size < b.Size
	})
	if p.Refusal != "" {
		return p
	}

	// Warnings that hold whatever the target.
	if p.Old != nil {
		if v, ok := f.Smart[p.Old.Path]; ok {
			switch {
			case v.Failing && kind == "mirror":
				p.Warnings = append(p.Warnings, "The system disk fails its SMART health check: moving the system to a new disk is safer than mirroring it (the mirror would start from a failing disk).")
			case v.Failing:
				p.Warnings = append(p.Warnings, "The system disk fails its SMART health check: files that cannot be read are listed at the end of the copy.")
			case len(v.Weak) > 0:
				p.Warnings = append(p.Warnings, fmt.Sprintf("The system disk shows signs of wear (%s).", strings.Join(v.Weak, ", ")))
			}
		}
	}
	if f.LastConfigBackup.IsZero() || f.Now.Sub(f.LastConfigBackup) > 7*24*time.Hour {
		p.Warnings = append(p.Warnings, "Export the StrataOS configuration first (System → Configuration backup) and keep it off the NAS: it brings the settings back on a new installation if anything goes wrong.")
	}
	if !f.RestorePoints {
		p.Warnings = append(p.Warnings, "This system has no restore points: a system backup (below) is the only way back besides the old disk.")
	}
	if len(l.SwapFiles) > 0 {
		p.Warnings = append(p.Warnings, "The swap file is created again on the new disk.")
	}

	if kind == "datadisks" {
		p.Warnings = append(p.Warnings, "The data partitions start empty: create a volume or a RAID array on them in Storage once the system runs from the data disks.")
	}
	if len(targets) == 0 {
		return p
	}
	if kind == "datadisks" && (len(targets) < 2 || len(targets) > maxDataDisks) {
		p.Refusal = fmt.Sprintf("Choose between 2 and %d disks: the system is mirrored on all of them.", maxDataDisks)
		return p
	}
	seen := map[string]bool{}
	for _, target := range targets {
		var c *Candidate
		for i := range p.Candidates {
			if p.Candidates[i].Path == target {
				c = &p.Candidates[i]
			}
		}
		switch {
		case seen[target]:
			p.Refusal = fmt.Sprintf("%s is chosen twice.", target)
			return p
		case c == nil:
			p.Refusal = "This disk cannot receive the system (system disk, or not found)."
			return p
		case !c.OK:
			p.Refusal = fmt.Sprintf("%s cannot receive the system: %s.", target, c.Reason)
			return p
		}
		seen[target] = true
		p.Targets = append(p.Targets, *c)
	}
	var names []string
	for _, c := range p.Targets {
		names = append(names, c.Name)
	}
	p.Confirm = strings.Join(names, ", ")
	c := &p.Targets[0]
	p.Target = c
	if kind == "datadisks" {
		smallest, largest := c.Size, c.Size
		for _, t := range p.Targets {
			smallest, largest = min(smallest, t.Size), max(largest, t.Size)
			if t.Kind == "usb" {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s is a USB disk: it can be unplugged by mistake, and some machines do not start from USB.", t.Name))
			}
			if len(t.Weak) > 0 {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s shows signs of wear (%s).", t.Name, strings.Join(t.Weak, ", ")))
			}
		}
		if largest > smallest+smallest/10 {
			p.Warnings = append(p.Warnings, "The disks have different sizes: their data partitions too. A RAID array on them has the size of the smallest one.")
		}
		p.Parts = dataDisksLayout(l.Boot, l.Used, swap)
		p.Steps = steps(p, l)
		return p
	}
	size := c.Size
	if kind == "mirror" {
		size = min(size, p.Old.Size)
		if c.Size < p.Old.Size {
			p.Warnings = append(p.Warnings, fmt.Sprintf("The new disk is smaller than the system disk: the mirror has the size of the smaller one (%s), the rest of the system disk stays unused.", human(c.Size)))
		} else if c.Size > p.Old.Size+p.Old.Size/10 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("The new disk is larger: the mirror has the size of the system disk (%s).", human(p.Old.Size)))
		}
		if c.Kind != "" && p.Old.Rotational != (c.Kind == "hdd") && (c.Kind == "hdd" || c.Kind == "ssd" || c.Kind == "nvme") {
			p.Warnings = append(p.Warnings, "One disk is an SSD and the other a hard disk: the mirror writes at the speed of the slower one.")
		}
	}
	if c.Kind == "usb" {
		p.Warnings = append(p.Warnings, "The new disk is a USB disk: it can be unplugged by mistake, and some machines do not start from USB.")
	}
	if len(c.Weak) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The new disk shows signs of wear (%s).", strings.Join(c.Weak, ", ")))
	}
	p.Parts = partLayout(l.Boot, size, swap, kind == "mirror")
	p.Steps = steps(p, l)
	return p
}

// sub fills {0}, {1}… (the web interface translates the texts with the
// same placeholders).
func sub(format string, args ...any) string {
	for i, a := range args {
		format = strings.ReplaceAll(format, fmt.Sprintf("{%d}", i), fmt.Sprint(a))
	}
	return format
}

func steps(p Plan, l Layout) []string {
	nd := p.Target.Name
	od := p.Old.Name
	if len(p.OldDisks) > 1 {
		var n []string
		for _, d := range p.OldDisks {
			n = append(n, d.Name)
		}
		od = strings.Join(n, ", ")
	}
	if p.Kind == "datadisks" {
		boot := sub("GRUB on {0}", p.Confirm)
		if l.Boot == "uefi" {
			boot = sub("an EFI system partition on {0} and a UEFI boot entry for each", p.Confirm)
		}
		return []string{
			sub("Erase {0} and partition each one ({1})", p.Confirm, partSummary(p.Parts)),
			sub("Create a RAID 1 array over the system partitions of the {0} disks, and a {1} file system", len(p.Targets), p.FSType),
			sub("Copy the running system onto it ({0} is only read)", od),
			sub("When you click Restart: copy the last changes, point the copy's fstab, initramfs and boot loader to the array, install {0}", boot),
			sub("Restart once from {0}: if it does not start, the next restart uses {1} unchanged", nd, od),
			sub("Once the system runs from the data disks: power off, then remove {0} or erase it to use it for data", od),
		}
	}
	boot := sub("GRUB on {0}", nd)
	if l.Boot == "uefi" {
		boot = sub("an EFI system partition and a UEFI boot entry for {0}", nd)
	}
	first := sub("Erase {0} and partition it ({1})", nd, partSummary(p.Parts))
	restart := sub("Restart once from {0}: if it does not start, the next restart uses {1} unchanged", nd, od)
	if p.Kind == "mirror" {
		return []string{
			first,
			sub("Create the RAID 1 arrays with {0} only (degraded), and a {1} file system", nd, p.FSType),
			sub("Copy the running system onto them ({0} is only read)", od),
			sub("When you click Restart: copy the last changes, point the copy's fstab, initramfs and boot loader to the arrays, install {0}", boot),
			restart,
			sub("Once the system runs from the mirror and you confirm: erase {0}, partition it the same way and add it to the arrays (rebuild)", od),
		}
	}
	return []string{
		first,
		sub("Create a {0} file system and copy the running system onto it ({1} is only read; unreadable files are listed)", p.FSType, od),
		sub("When you click Restart: copy the last changes, point the copy's fstab, initramfs and boot loader to {0}, install {1}", nd, boot),
		restart,
		sub("Once the system runs from {0}: power off, then remove {1} or erase it to use it for data", nd, od),
	}
}

func partSummary(parts []PartSpec) string {
	var s []string
	for _, p := range parts {
		switch p.Role {
		case "esp":
			s = append(s, fmt.Sprintf("EFI %d MiB", p.MiB))
		case "biosgrub":
			s = append(s, "BIOS boot 1 MiB")
		case "swap":
			s = append(s, "swap "+human(p.MiB<<20))
		case "root":
			s = append(s, "system "+human(p.MiB<<20))
		case "data":
			s = append(s, "data: the rest")
		}
	}
	return strings.Join(s, ", ")
}

func human(b int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	f := float64(b)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", b)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
