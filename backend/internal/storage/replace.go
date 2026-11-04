package storage

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Guided disk replacement: what uses a disk (RAID array, ZFS vdev, LVM volume
// group), how much redundancy is left, and which new disk can take its place.
// PlanReplace is pure: the API gathers the facts (ReplaceGroups, SMART cache,
// backups) and the unit tests build them by hand.

// ReplaceMember is a member of a redundancy group.
type ReplaceMember struct {
	Name   string `json:"name"`             // as the tool names it: /dev/sda1, a by-id path, a ZFS GUID; "" for an empty RAID slot
	Disk   string `json:"disk,omitempty"`   // whole disk holding it ("" when missing)
	Serial string `json:"serial,omitempty"` // of that disk
	Model  string `json:"model,omitempty"`
	Size   int64  `json:"size,omitempty"` // of the member device
	Used   int64  `json:"used,omitempty"` // LVM: bytes allocated on the physical volume
	// State: active | spare | faulty | missing | rebuilding.
	State   string   `json:"state"`
	Detail  string   `json:"detail,omitempty"` // state as the tool writes it
	Was     string   `json:"was,omitempty"`    // ZFS: former path of a missing device
	Failing bool     `json:"failing,omitempty"`
	Weak    []string `json:"weak,omitempty"`
}

// ReplaceGroup is a set of disks that hold the same data: an mdadm array, a
// ZFS vdev or an LVM volume group (no redundancy).
type ReplaceGroup struct {
	ID     string `json:"id"`   // raid:<md name> | zfs:<pool>:<vdev> | lvm:<vg>
	Kind   string `json:"kind"` // raid | zfs | lvm
	Name   string `json:"name"` // array label, pool or volume group name
	Device string `json:"device,omitempty"`
	Level  string `json:"level"` // raid1, raid5…, mirror, raidz1…, stripe, linear
	Vdev   string `json:"vdev,omitempty"`
	// Tolerates is the number of members the group can lose when complete.
	Tolerates int             `json:"tolerates"`
	Members   []ReplaceMember `json:"members"`
	UsedBy    []string        `json:"usedBy,omitempty"` // what sits on top (volume group, volumes, datasets)
	// A rebuild (RAID recovery, ZFS resilver, pvmove) is running.
	Rebuilding bool    `json:"rebuilding,omitempty"`
	Progress   float64 `json:"progress,omitempty"`
	MinSize    int64   `json:"-"` // RAID: bytes a member needs (from Used Dev Size)
	// Unsupported: why StrataOS does not replace disks of this group.
	Unsupported string `json:"unsupported,omitempty"`
}

// Missing counts the members that hold no data now.
func (g ReplaceGroup) Missing() int {
	n := 0
	for _, m := range g.Members {
		if m.State == "missing" || m.State == "faulty" {
			n++
		}
	}
	return n
}

// DiskVerdict is the SMART verdict of a whole disk.
type DiskVerdict struct {
	Failing bool
	Weak    []string
}

// ReplaceFacts is what PlanReplace knows.
type ReplaceFacts struct {
	Disks      []BlockDevice
	Groups     []ReplaceGroup
	Smart      map[string]DiskVerdict // disk path → verdict (absent: unknown)
	Busy       map[string]string      // disk path → why it is reserved (burn-in…)
	Backups    int                    // enabled backup and cloud sync tasks
	LastBackup time.Time              // last success of any of them
	Now        time.Time
}

// ReplaceSel says what to replace and how.
type ReplaceSel struct {
	Disk   string `json:"disk,omitempty"`   // the disk to replace
	Group  string `json:"group,omitempty"`  // or a group (degraded array)…
	Member string `json:"member,omitempty"` // …and its missing member ("" = the first one)
	Mode   string `json:"mode,omitempty"`   // add-first | degraded ("" = recommended)
	New    string `json:"new,omitempty"`    // the new disk
}

// ReplaceMode is a way to replace the disk, possible or not.
type ReplaceMode struct {
	ID     string `json:"id"` // add-first | degraded
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// ReplaceCandidate is a disk that could be the new one.
type ReplaceCandidate struct {
	Path   string   `json:"path"`
	Name   string   `json:"name"`
	Model  string   `json:"model,omitempty"`
	Serial string   `json:"serial,omitempty"`
	Size   int64    `json:"size"`
	OK     bool     `json:"ok"`
	Reason string   `json:"reason,omitempty"` // why it cannot be used
	Erase  string   `json:"erase,omitempty"`  // what is on it now (erased by the replacement)
	Weak   []string `json:"weak,omitempty"`
}

// ReplacePlan is the answer of PlanReplace.
type ReplacePlan struct {
	Group *ReplaceGroup  `json:"group,omitempty"`
	Old   *ReplaceMember `json:"old,omitempty"`
	Mode  string         `json:"mode,omitempty"`
	Modes []ReplaceMode  `json:"modes"`
	// Need is the size the new disk must have at least.
	Need int64 `json:"need,omitempty"`
	// Left is the number of further failures the data survives now.
	Left       int                `json:"left"`
	Candidates []ReplaceCandidate `json:"candidates"`
	// Refusal: the replacement cannot be done, and why.
	Refusal    string     `json:"refusal,omitempty"`
	Warnings   []string   `json:"warnings,omitempty"`
	Backup     string     `json:"backup"` // none | old | recent
	LastBackup *time.Time `json:"lastBackup,omitempty"`
	Steps      []string   `json:"steps,omitempty"`
}

// Days after which the last backup is called old.
const backupRecentDays = 7

func disp(m ReplaceMember) string {
	switch {
	case m.Disk != "" && m.Serial != "":
		return fmt.Sprintf("%s (S/N %s)", m.Disk, m.Serial)
	case m.Disk != "":
		return m.Disk
	case m.Was != "":
		return m.Was
	}
	return m.Name
}

func levelName(g ReplaceGroup) string {
	switch g.Kind {
	case "raid":
		return strings.ToUpper(strings.Replace(g.Level, "raid", "RAID ", 1))
	case "zfs":
		return "ZFS " + g.Level
	}
	return "LVM"
}

// PlanReplace decides how a member of a group can be replaced. A plan with a
// Refusal must not be carried out; with sel.New set, the new disk is checked
// too and a bad choice is a refusal.
func PlanReplace(f ReplaceFacts, sel ReplaceSel) ReplacePlan {
	p := ReplacePlan{Modes: []ReplaceMode{}, Candidates: []ReplaceCandidate{}}
	p.Backup = "none"
	if f.Backups > 0 && !f.LastBackup.IsZero() {
		t := f.LastBackup
		p.LastBackup = &t
		p.Backup = "old"
		if f.Now.Sub(t) <= backupRecentDays*24*time.Hour {
			p.Backup = "recent"
		}
	}
	groups := make([]ReplaceGroup, len(f.Groups))
	for i, g := range f.Groups {
		groups[i] = annotate(g, f.Smart)
	}

	// Which group and member.
	var g *ReplaceGroup
	mi := -1
	if sel.Group != "" {
		for i := range groups {
			if groups[i].ID == sel.Group {
				g = &groups[i]
			}
		}
		if g == nil && strings.Count(sel.Group, ":") == 1 && strings.HasPrefix(sel.Group, "zfs:") {
			// A whole pool (from an alert): its vdev with a missing disk.
			for i := range groups {
				if strings.HasPrefix(groups[i].ID, sel.Group+":") && (g == nil || g.Missing() == 0 && groups[i].Missing() > 0) {
					g = &groups[i]
				}
			}
		}
		if g == nil {
			p.Refusal = "This array, pool or volume group no longer exists."
			return p
		}
		for i, m := range g.Members {
			if sel.Member != "" && (m.Name == sel.Member || (m.Disk != "" && m.Disk == sel.Member)) ||
				sel.Member == "" && sel.Disk != "" && m.Disk == sel.Disk ||
				sel.Member == "" && sel.Disk == "" && mi < 0 && (m.State == "missing" || m.State == "faulty") {
				mi = i
				break
			}
		}
		if mi < 0 {
			if sel.Member != "" || sel.Disk != "" {
				p.Refusal = "This disk is not a member of " + g.Name + " any more."
			} else {
				p.Refusal = g.Name + " has no missing member: choose the disk to replace on the Disks page."
			}
			p.Group = g
			return p
		}
	} else {
		var hits []int
		for i := range groups {
			for _, m := range groups[i].Members {
				if sel.Disk != "" && m.Disk == sel.Disk {
					hits = append(hits, i)
					break
				}
			}
		}
		switch len(hits) {
		case 0:
			p.Refusal = noGroupReason(f.Disks, sel.Disk)
			return p
		case 1:
		default:
			var names []string
			for _, i := range hits {
				names = append(names, groups[i].Name)
			}
			p.Refusal = fmt.Sprintf("This disk belongs to several arrays or pools (%s): replace it in each of them from the RAID or ZFS page.", strings.Join(names, ", "))
			return p
		}
		g = &groups[hits[0]]
		for i, m := range g.Members {
			if m.Disk == sel.Disk {
				mi = i
			}
		}
	}
	p.Group = g
	old := g.Members[mi]
	p.Old = &old
	missing := g.Missing()
	p.Left = g.Tolerates - missing

	// Possible modes.
	present := old.Disk != "" && old.State != "missing" && old.State != "faulty"
	addFirst := ReplaceMode{ID: "add-first", OK: present}
	if !present {
		addFirst.Reason = "The disk is already out of service: there is nothing left to copy from it."
	}
	degraded := ReplaceMode{ID: "degraded", OK: !present}
	if present {
		degraded.Reason = "The disk still works: add the new disk first, so that the data stays protected during the copy."
	}
	if g.Kind == "lvm" {
		degraded.OK, degraded.Reason = false, "A volume group without redundancy cannot rebuild a missing disk: its data is only on that disk."
	}
	p.Modes = []ReplaceMode{addFirst, degraded}
	p.Mode = sel.Mode
	if p.Mode == "" {
		p.Mode = "add-first"
		if !present {
			p.Mode = "degraded"
		}
	}
	var mode *ReplaceMode
	for i := range p.Modes {
		if p.Modes[i].ID == p.Mode {
			mode = &p.Modes[i]
		}
	}

	// Size the new disk needs.
	p.Need = needSize(*g, old)

	// Dangerous cases.
	var others []string
	for i, m := range g.Members {
		if i != mi && m.Failing {
			others = append(others, disp(m))
		}
	}
	switch {
	case mode == nil:
		p.Refusal = "Unknown replacement method."
	case g.Unsupported != "":
		p.Refusal = g.Unsupported
	case old.State == "spare":
		p.Refusal = "This disk is a hot spare: it holds no data. Remove it from the array and add the new disk as a spare instead."
	case g.Rebuilding:
		p.Refusal = fmt.Sprintf("A rebuild is already running on %s (%.1f %%): wait until it is finished.", g.Name, g.Progress)
	case g.Kind == "raid" && g.Tolerates == 0:
		p.Refusal = fmt.Sprintf("%s has no redundancy: mdadm cannot rebuild one of its disks. Copy the data to another volume, then recreate the array with the new disk.", levelName(*g))
	case p.Left < 0:
		p.Refusal = fmt.Sprintf("Too many disks of %s are missing (%d, it tolerates %d): the data cannot be rebuilt. Restore it from a backup.", g.Name, missing, g.Tolerates)
	case !mode.OK:
		p.Refusal = mode.Reason
	case g.Kind == "raid" && p.Mode == "add-first" && missing > 0:
		p.Refusal = fmt.Sprintf("%s is already degraded (%d disk(s) missing): rebuild the missing disk first, then replace this one.", g.Name, missing)
	case g.Kind == "lvm" && missing > 0:
		p.Refusal = fmt.Sprintf("The volume group %s has a missing disk: its volumes are incomplete and cannot be moved.", g.Name)
	case len(others) > 0:
		p.Refusal = fmt.Sprintf("%s also fails its SMART health check: rebuilding reads every disk and could lose it during the copy. Back up the data now, then replace that disk first.", strings.Join(others, ", "))
	}

	// Warnings.
	if p.Refusal == "" {
		if old.Failing && p.Mode == "add-first" {
			p.Warnings = append(p.Warnings, "The disk to replace fails its SMART check: it may stop during the copy. The data is then rebuilt from the other disks if there is redundancy.")
		}
		switch {
		case g.Tolerates == 0:
			p.Warnings = append(p.Warnings, "There is no redundant copy of this data: the new disk is filled from the old one only. Keep the old disk connected until the end, and check your backup first.")
		case p.Left == 0:
			p.Warnings = append(p.Warnings, "No redundancy is left until the rebuild ends: one more failing disk and the data is lost. Check your backup first.")
		}
		for i, m := range g.Members {
			if i != mi && len(m.Weak) > 0 {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s also shows signs of wear (%s): plan its replacement too.", disp(m), strings.Join(m.Weak, ", ")))
			}
		}
		switch p.Backup {
		case "none":
			p.Warnings = append(p.Warnings, "No successful backup is known: a rebuild stresses every disk. Back up the important data first if you can.")
		case "old":
			p.Warnings = append(p.Warnings, fmt.Sprintf("The last successful backup is %d days old.", int(f.Now.Sub(f.LastBackup).Hours()/24)))
		}
	}

	// Candidates.
	members := map[string]bool{}
	for _, m := range g.Members {
		if m.Disk != "" {
			members[m.Disk] = true
		}
	}
	for _, d := range f.Disks {
		if d.System || members[d.Path] {
			continue
		}
		c := ReplaceCandidate{Path: d.Path, Name: d.Name, Model: d.Model, Serial: d.Serial, Size: d.Size, OK: true}
		v := f.Smart[d.Path]
		c.Weak = v.Weak
		switch {
		case f.Busy[d.Path] != "":
			c.OK, c.Reason = false, "reserved: "+f.Busy[d.Path]
		case d.ActiveUse != "":
			c.OK, c.Reason = false, "in use: "+activeUseText(d)
		case d.Usage == "ceph":
			c.OK, c.Reason = false, "used by Ceph"
		case usedByGroup(groups, d.Path) != "":
			c.OK, c.Reason = false, "in use: member of "+usedByGroup(groups, d.Path)
		case v.Failing:
			c.OK, c.Reason = false, "fails its SMART health check"
		case d.Size < p.Need:
			c.OK, c.Reason = false, fmt.Sprintf("too small: %s needed", humanSize(p.Need))
		}
		if d.Usage != "free" && d.Usage != "" {
			c.Erase = strings.TrimSpace(d.Usage + " " + d.UsedBy)
		}
		p.Candidates = append(p.Candidates, c)
	}
	sort.SliceStable(p.Candidates, func(i, j int) bool {
		a, b := p.Candidates[i], p.Candidates[j]
		if a.OK != b.OK {
			return a.OK
		}
		return a.Size < b.Size // the smallest disk that fits first
	})
	if sel.New != "" && p.Refusal == "" {
		var c *ReplaceCandidate
		for i := range p.Candidates {
			if p.Candidates[i].Path == sel.New {
				c = &p.Candidates[i]
			}
		}
		switch {
		case c == nil && members[sel.New]:
			p.Refusal = "The new disk is already a member of " + g.Name + "."
		case c == nil:
			p.Refusal = "The new disk cannot be used (system disk or not found)."
		case !c.OK:
			p.Refusal = fmt.Sprintf("The new disk %s cannot be used: %s.", sel.New, c.Reason)
		}
	}
	if p.Refusal == "" {
		p.Steps = replaceSteps(*g, old, p.Mode)
	}
	return p
}

// annotate adds the SMART verdicts to the members of a group.
func annotate(g ReplaceGroup, smart map[string]DiskVerdict) ReplaceGroup {
	g.Members = append([]ReplaceMember(nil), g.Members...)
	for i := range g.Members {
		if v, ok := smart[g.Members[i].Disk]; ok && g.Members[i].Disk != "" {
			g.Members[i].Failing, g.Members[i].Weak = v.Failing, v.Weak
		}
	}
	return g
}

func usedByGroup(groups []ReplaceGroup, disk string) string {
	for _, g := range groups {
		for _, m := range g.Members {
			if m.Disk == disk && m.State != "missing" {
				return g.Name
			}
		}
	}
	return ""
}

func noGroupReason(disks []BlockDevice, path string) string {
	for _, d := range disks {
		if d.Path != path {
			continue
		}
		switch {
		case d.System:
			return "This is the system disk: StrataOS cannot replace it while it runs. Back up the configuration, install StrataOS on the new disk, then restore the configuration."
		case d.Usage == "btrfs":
			return "This disk belongs to a Btrfs volume: add the new disk to the volume, then remove this one (Volumes → the volume → Devices)."
		case d.Usage == "ceph":
			return "This disk is a Ceph OSD: replace it from the Ceph page."
		case d.Usage == "free" || d.Usage == "":
			return "This disk is not used by an array, a pool or a volume group: there is nothing to rebuild. You can simply remove it."
		}
		return "This disk holds a plain file system without redundancy: copy its data to another volume, there is nothing to rebuild."
	}
	return "Disk not found."
}

func activeUseText(d BlockDevice) string {
	switch d.ActiveUse {
	case "mounted":
		return "mounted on " + d.ActiveBy
	case "raid":
		return "member of " + d.ActiveBy
	case "lvm":
		return "LVM volume group " + d.ActiveBy
	case "zfs":
		return "ZFS pool " + d.ActiveBy
	}
	return strings.TrimSpace(d.ActiveUse + " " + d.ActiveBy)
}

// needSize is the smallest size the new disk may have.
func needSize(g ReplaceGroup, old ReplaceMember) int64 {
	switch g.Kind {
	case "lvm":
		// The extents allocated on the old physical volume, plus its metadata.
		return old.Used + 4<<20
	case "raid":
		need := int64(0)
		for _, m := range g.Members {
			if m.Size > 0 && (need == 0 || m.Size < need) {
				need = m.Size
			}
		}
		// The data area plus room for the superblock and data offset (128 MiB at most).
		if g.MinSize > 0 && (need == 0 || g.MinSize+128<<20 < need) {
			need = g.MinSize + 128<<20
		}
		return need
	}
	// ZFS: as large as the device it replaces (or the smallest of the vdev).
	if old.Size > 0 {
		return old.Size
	}
	need := int64(0)
	for _, m := range g.Members {
		if m.Size > 0 && (need == 0 || m.Size < need) {
			need = m.Size
		}
	}
	return need
}

func replaceSteps(g ReplaceGroup, old ReplaceMember, mode string) []string {
	o := disp(old)
	switch {
	case g.Kind == "raid" && mode == "add-first":
		return []string{
			"Erase the new disk",
			"Add it to " + g.Name + " as a spare",
			"Copy " + o + " onto it (mdadm --replace): the array keeps its redundancy during the copy",
			"Remove " + o + " from the array",
		}
	case g.Kind == "raid":
		s := []string{"Erase the new disk"}
		if old.Disk != "" {
			s = append(s, "Remove the failed disk "+o+" from the array")
		}
		return append(s, "Add the new disk to "+g.Name+" and rebuild the missing data onto it")
	case g.Kind == "zfs" && mode == "add-first":
		return []string{"Erase the new disk", "zpool replace: ZFS copies the data of " + o + " onto it (resilver), the pool stays usable", "ZFS detaches " + o + " at the end"}
	case g.Kind == "zfs":
		return []string{"Erase the new disk", "zpool replace: ZFS rebuilds the missing data onto it (resilver)"}
	}
	return []string{"Erase the new disk", "Add it to the volume group " + g.Name, "Move the data of " + o + " onto it (pvmove), the volumes stay usable", "Remove " + o + " from the volume group"}
}

func humanSize(b int64) string {
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
