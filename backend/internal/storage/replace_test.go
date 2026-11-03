package storage

import (
	"strings"
	"testing"
	"time"
)

const gib = int64(1) << 30

var now = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

func disk(name string, size int64, usage string) BlockDevice {
	return BlockDevice{Name: name, Path: "/dev/" + name, Type: "disk", Size: size, Serial: "SN-" + name, Model: "HDD", Usage: usage}
}

func member(name, state string) ReplaceMember {
	if name == "" {
		return ReplaceMember{State: state}
	}
	return ReplaceMember{Name: "/dev/" + name, Disk: "/dev/" + name, Serial: "SN-" + name, Size: 4 * gib, State: state}
}

// The case that started it all: sda of a RAID 1 under LVM shows reallocated sectors.
func raid1Facts() ReplaceFacts {
	return ReplaceFacts{
		Disks: []BlockDevice{
			{Name: "vda", Path: "/dev/vda", Type: "disk", Size: 20 * gib, System: true, Usage: "system"},
			disk("sda", 4*gib, "raid"), disk("sdb", 4*gib, "raid"), disk("sdc", 4*gib, "free"), disk("sdd", 2*gib, "free"),
		},
		Groups: []ReplaceGroup{{ID: "raid:md127", Kind: "raid", Name: "data", Device: "/dev/md127", Level: "raid1", Tolerates: 1,
			Members: []ReplaceMember{member("sda", "active"), member("sdb", "active")}, UsedBy: []string{"LVM volume group vg0"}}},
		Smart:      map[string]DiskVerdict{"/dev/sda": {Weak: []string{"24 reallocated sector(s)"}}},
		Backups:    1,
		LastBackup: now.Add(-24 * time.Hour),
		Now:        now,
	}
}

func candidate(p ReplacePlan, path string) ReplaceCandidate {
	for _, c := range p.Candidates {
		if c.Path == path {
			return c
		}
	}
	return ReplaceCandidate{}
}

func TestPlanRaid1AddFirst(t *testing.T) {
	p := PlanReplace(raid1Facts(), ReplaceSel{Disk: "/dev/sda"})
	if p.Refusal != "" {
		t.Fatalf("refused: %s", p.Refusal)
	}
	if p.Group.ID != "raid:md127" || p.Old.Disk != "/dev/sda" || p.Mode != "add-first" || p.Left != 1 {
		t.Fatalf("unexpected plan: %+v", p)
	}
	if !p.Modes[0].OK || p.Modes[1].OK {
		t.Errorf("modes: %+v", p.Modes)
	}
	if p.Backup != "recent" || len(p.Warnings) != 0 {
		t.Errorf("backup %s, warnings %v", p.Backup, p.Warnings)
	}
	if c := candidate(p, "/dev/sdc"); !c.OK {
		t.Errorf("sdc refused: %+v", c)
	}
	if c := candidate(p, "/dev/sdd"); c.OK || !strings.Contains(c.Reason, "too small") {
		t.Errorf("sdd accepted: %+v", c)
	}
	if c := candidate(p, "/dev/sdb"); c.Path != "" {
		t.Error("a member is offered as the new disk")
	}
	if c := candidate(p, "/dev/vda"); c.Path != "" {
		t.Error("the system disk is offered")
	}
	if p.Candidates[0].Path != "/dev/sdc" {
		t.Errorf("usable disks must come first: %+v", p.Candidates)
	}
	if len(p.Steps) != 4 || !strings.Contains(p.Steps[2], "mdadm --replace") {
		t.Errorf("steps: %v", p.Steps)
	}
	if p := PlanReplace(raid1Facts(), ReplaceSel{Disk: "/dev/sda", New: "/dev/sdc"}); p.Refusal != "" {
		t.Errorf("good new disk refused: %s", p.Refusal)
	}
	if p := PlanReplace(raid1Facts(), ReplaceSel{Disk: "/dev/sda", New: "/dev/sdd"}); !strings.Contains(p.Refusal, "too small") {
		t.Errorf("small disk: %q", p.Refusal)
	}
	if p := PlanReplace(raid1Facts(), ReplaceSel{Disk: "/dev/sda", New: "/dev/sdb"}); !strings.Contains(p.Refusal, "already a member") {
		t.Errorf("member as new disk: %q", p.Refusal)
	}
	if p := PlanReplace(raid1Facts(), ReplaceSel{Disk: "/dev/sda", Mode: "degraded"}); p.Refusal == "" {
		t.Error("degraded mode accepted for a working disk")
	}
}

func TestPlanNewDiskChecks(t *testing.T) {
	f := raid1Facts()
	f.Disks[3].ActiveUse, f.Disks[3].ActiveBy = "mounted", "/mnt/usb"
	if p := PlanReplace(f, ReplaceSel{Disk: "/dev/sda", New: "/dev/sdc"}); !strings.Contains(p.Refusal, "in use: mounted on /mnt/usb") {
		t.Errorf("disk in use: %q", p.Refusal)
	}
	f = raid1Facts()
	f.Busy = map[string]string{"/dev/sdc": "burn-in test"}
	if c := candidate(PlanReplace(f, ReplaceSel{Disk: "/dev/sda"}), "/dev/sdc"); c.OK || !strings.Contains(c.Reason, "burn-in") {
		t.Errorf("busy disk: %+v", c)
	}
	f = raid1Facts()
	f.Smart["/dev/sdc"] = DiskVerdict{Failing: true}
	if c := candidate(PlanReplace(f, ReplaceSel{Disk: "/dev/sda"}), "/dev/sdc"); c.OK {
		t.Error("failing new disk accepted")
	}
	// Leftover signatures are fine (erased), an active member of another group is not.
	f = raid1Facts()
	f.Disks[3].Usage, f.Disks[3].UsedBy = "zfs", "old"
	f.Groups = append(f.Groups, ReplaceGroup{ID: "raid:md1", Kind: "raid", Name: "other", Level: "raid1", Tolerates: 1, Members: []ReplaceMember{member("sdx", "active")}})
	c := candidate(PlanReplace(f, ReplaceSel{Disk: "/dev/sda"}), "/dev/sdc")
	if !c.OK || c.Erase != "zfs old" {
		t.Errorf("leftover signature: %+v", c)
	}
	f.Groups[1].Members = append(f.Groups[1].Members, member("sdc", "active"))
	if c := candidate(PlanReplace(f, ReplaceSel{Disk: "/dev/sda"}), "/dev/sdc"); c.OK || !strings.Contains(c.Reason, "member of other") {
		t.Errorf("member of another array: %+v", c)
	}
}

func TestPlanRaidDegraded(t *testing.T) {
	f := raid1Facts()
	f.Groups[0].Members = []ReplaceMember{member("", "missing"), member("sdb", "active")}
	f.Disks[1].Usage = "free" // sda was pulled and a new disk took its place
	p := PlanReplace(f, ReplaceSel{Group: "raid:md127"})
	if p.Refusal != "" || p.Mode != "degraded" || p.Left != 0 || p.Old.Disk != "" {
		t.Fatalf("unexpected plan: %+v", p)
	}
	if p.Modes[0].OK || !p.Modes[1].OK {
		t.Errorf("modes: %+v", p.Modes)
	}
	if !strings.Contains(strings.Join(p.Warnings, " "), "No redundancy is left") {
		t.Errorf("warnings: %v", p.Warnings)
	}
	if p.Need != 4*gib {
		t.Errorf("need %d", p.Need)
	}
	if len(p.Steps) != 2 {
		t.Errorf("steps: %v", p.Steps)
	}
	// The other disk fails too: refuse, the rebuild reads all of it.
	f.Smart["/dev/sdb"] = DiskVerdict{Failing: true}
	if p := PlanReplace(f, ReplaceSel{Group: "raid:md127"}); !strings.Contains(p.Refusal, "/dev/sdb (S/N SN-sdb) also fails") {
		t.Errorf("other failing: %q", p.Refusal)
	}
	// A disk already gone cannot be copied from.
	if p := PlanReplace(f, ReplaceSel{Group: "raid:md127", Mode: "add-first"}); p.Refusal == "" {
		t.Error("add-first accepted for a missing disk")
	}
}

func TestPlanRaidRefusals(t *testing.T) {
	f := raid1Facts()
	f.Groups[0].Level, f.Groups[0].Tolerates = "raid0", 0
	if p := PlanReplace(f, ReplaceSel{Disk: "/dev/sda"}); !strings.Contains(p.Refusal, "no redundancy") {
		t.Errorf("raid0: %q", p.Refusal)
	}
	f = raid1Facts()
	f.Groups[0].Rebuilding, f.Groups[0].Progress = true, 42
	if p := PlanReplace(f, ReplaceSel{Disk: "/dev/sda"}); !strings.Contains(p.Refusal, "already running") {
		t.Errorf("rebuilding: %q", p.Refusal)
	}
	// RAID 5 with a missing disk: replacing another one first would leave no copy.
	f = raid1Facts()
	f.Groups[0] = ReplaceGroup{ID: "raid:md0", Kind: "raid", Name: "r5", Level: "raid5", Tolerates: 1,
		Members: []ReplaceMember{member("sda", "active"), member("sdb", "active"), member("", "missing")}}
	if p := PlanReplace(f, ReplaceSel{Disk: "/dev/sda"}); !strings.Contains(p.Refusal, "already degraded") {
		t.Errorf("degraded add-first: %q", p.Refusal)
	}
	f.Groups[0].Members[1] = member("", "missing")
	if p := PlanReplace(f, ReplaceSel{Group: "raid:md0"}); !strings.Contains(p.Refusal, "Too many disks") {
		t.Errorf("two missing: %q", p.Refusal)
	}
	f = raid1Facts()
	f.Groups[0].Members[1].State = "spare"
	f.Groups[0].Members = append(f.Groups[0].Members, member("sde", "active"))
	if p := PlanReplace(f, ReplaceSel{Disk: "/dev/sdb"}); !strings.Contains(p.Refusal, "hot spare") {
		t.Errorf("spare: %q", p.Refusal)
	}
	// Not in any group.
	for d, want := range map[string]string{"/dev/vda": "system disk", "/dev/sdc": "nothing to rebuild", "/dev/zzz": "not found"} {
		if p := PlanReplace(raid1Facts(), ReplaceSel{Disk: d}); !strings.Contains(p.Refusal, want) {
			t.Errorf("%s: %q", d, p.Refusal)
		}
	}
}

func TestPlanBackupAndWear(t *testing.T) {
	f := raid1Facts()
	f.Groups[0].Members = append(f.Groups[0].Members, member("sde", "active"))
	f.Groups[0].Tolerates = 2
	f.Smart["/dev/sde"] = DiskVerdict{Weak: []string{"2 sector(s) pending reallocation"}}
	f.LastBackup = now.Add(-30 * 24 * time.Hour)
	p := PlanReplace(f, ReplaceSel{Disk: "/dev/sda"})
	w := strings.Join(p.Warnings, " | ")
	if p.Backup != "old" || !strings.Contains(w, "30 days old") || !strings.Contains(w, "sde (S/N SN-sde) also shows signs of wear") {
		t.Errorf("backup %s, warnings %s", p.Backup, w)
	}
	f.Backups = 0
	if p := PlanReplace(f, ReplaceSel{Disk: "/dev/sda"}); p.Backup != "none" || !strings.Contains(strings.Join(p.Warnings, " "), "No successful backup") {
		t.Errorf("no backup: %s %v", p.Backup, p.Warnings)
	}
}

func TestRaidGroupFaultyMember(t *testing.T) {
	// After --fail, mdadm lists the empty slot and the faulty disk.
	a := RaidArray{Name: "md127", Label: "data", Device: "/dev/md127", Level: "raid1", RaidDevices: 2, ActiveDevices: 1, UsedDevSize: 3 * gib,
		Members: []RaidMember{{RaidDevice: 0, State: "removed"}, {Device: "/dev/sdb", RaidDevice: 1, State: "active sync"}, {Device: "/dev/sda", RaidDevice: -1, State: "faulty"}}}
	disks := []BlockDevice{disk("sda", 4*gib, "raid"), disk("sdb", 4*gib, "raid")}
	g := raidGroup(a, disks)
	if len(g.Members) != 2 || g.Missing() != 1 || g.Members[1].State != "faulty" || g.Members[1].Serial != "SN-sda" {
		t.Fatalf("members: %+v", g.Members)
	}
	f := ReplaceFacts{Disks: append(disks, disk("sdc", 4*gib, "free")), Groups: []ReplaceGroup{g}, Now: now}
	p := PlanReplace(f, ReplaceSel{Group: "raid:md127"})
	if p.Refusal != "" || p.Mode != "degraded" || p.Old.Disk != "/dev/sda" {
		t.Fatalf("plan: %+v", p)
	}
	if p.Need != 3*gib+128<<20 {
		t.Errorf("need %d", p.Need)
	}
	if !strings.Contains(strings.Join(p.Steps, " "), "Remove the failed disk /dev/sda") {
		t.Errorf("steps: %v", p.Steps)
	}
}

const zMirror = `  pool: tank
 state: DEGRADED
  scan: resilvered 1.50G in 00:01:02 with 0 errors on Tue Oct 20 10:00:00 2026
config:

	NAME                                      STATE     READ WRITE CKSUM
	tank                                      DEGRADED     0     0     0
	  mirror-0                                DEGRADED     0     0     0
	    934868875068009117                    UNAVAIL      0     0     0  was /dev/disk/by-id/ata-HDD_4-part1
	    /dev/sdb1                             ONLINE       0     0     0
	  mirror-1                                ONLINE       0     0     0
	    /dev/sdc1                             ONLINE       0     0     0
	    /dev/sdd1                             ONLINE       0     0     0
	  /dev/sdf1                               ONLINE       0     0     0
	logs
	  /dev/sdg1                               ONLINE       0     0     0
	spares
	  /dev/sdh1                               AVAIL

errors: No known data errors
`

const zReplacing = `  pool: tank
 state: ONLINE
  scan: resilver in progress since Tue Nov  3 10:00:00 2026
	1.23G / 4.56G scanned at 100M/s, 800M / 4.56G issued at 50M/s
	800M resilvered, 17.52% done, 00:01:15 to go
config:

	NAME             STATE     READ WRITE CKSUM
	tank             ONLINE       0     0     0
	  mirror-0       ONLINE       0     0     0
	    replacing-0  ONLINE       0     0     0
	      /dev/sda1  ONLINE       0     0     0
	      /dev/sde1  ONLINE       0     0     0  (resilvering)
	    /dev/sdb1    ONLINE       0     0     0
	  replacing-1    ONLINE       0     0     0
	    /dev/sdf1    ONLINE       0     0     0
	    /dev/sdg1    ONLINE       0     0     0

errors: No known data errors
`

func TestParseZTopology(t *testing.T) {
	v := ParseZTopology(zMirror)
	if len(v) != 3 || v[0].Type != "mirror" || len(v[0].Leaves) != 2 || v[0].Leaves[0].Was != "/dev/disk/by-id/ata-HDD_4-part1" ||
		v[1].Name != "mirror-1" || v[2].Type != "stripe" || v[2].Leaves[0].Name != "/dev/sdf1" {
		t.Fatalf("topology: %+v", v)
	}
	v = ParseZTopology(zReplacing)
	if len(v) != 2 || len(v[0].Leaves) != 3 || !v[0].Leaves[0].Replacing || v[0].Leaves[2].Replacing ||
		v[1].Type != "stripe" || len(v[1].Leaves) != 2 || !v[1].Leaves[1].Replacing {
		t.Fatalf("replacing: %+v", v)
	}
	if n := zfsTolerates(v[0]); n != 1 {
		t.Errorf("mirror being replaced tolerates %d", n)
	}
	pct, eta := ParseZScanProgress(zReplacing)
	if pct != 17.52 || eta != 75 {
		t.Errorf("progress %v %v", pct, eta)
	}
	if _, eta := ParseZScanProgress("12.5% done, 1 days 02:00:00 to go"); eta != 93600 {
		t.Errorf("eta with days %d", eta)
	}
}

func TestPlanZfs(t *testing.T) {
	disks := []BlockDevice{disk("sdb", 4*gib, "zfs"), disk("sdc", 4*gib, "zfs"), disk("sdd", 4*gib, "zfs"), disk("sdf", 4*gib, "zfs"), disk("sdx", 8*gib, "free")}
	for i := range disks[:4] {
		disks[i].Children = []BlockDevice{{Name: disks[i].Name + "1", Path: disks[i].Path + "1", Type: "part", Size: 4*gib - 8<<20}}
	}
	groups := zfsGroups(ZPool{Name: "tank", Status: zMirror}, disks)
	if len(groups) != 3 || groups[0].ID != "zfs:tank:mirror-0" || groups[0].Missing() != 1 || groups[0].Members[1].Disk != "/dev/sdb" {
		t.Fatalf("groups: %+v", groups)
	}
	f := ReplaceFacts{Disks: disks, Groups: groups, Now: now}
	// From the pool alert: the vdev with the missing disk.
	p := PlanReplace(f, ReplaceSel{Group: "zfs:tank"})
	if p.Refusal != "" || p.Group.Vdev != "mirror-0" || p.Mode != "degraded" || p.Old.Name != "934868875068009117" || p.Need != 4*gib-8<<20 {
		t.Fatalf("degraded: %+v", p)
	}
	// Add-first on the healthy mirror.
	p = PlanReplace(f, ReplaceSel{Disk: "/dev/sdc"})
	if p.Refusal != "" || p.Mode != "add-first" || p.Old.Name != "/dev/sdc1" || p.Left != 1 {
		t.Fatalf("add-first: %+v", p)
	}
	// A single-disk vdev: allowed, with a warning.
	p = PlanReplace(f, ReplaceSel{Disk: "/dev/sdf"})
	if p.Refusal != "" || !strings.Contains(strings.Join(p.Warnings, " "), "no redundant copy") {
		t.Fatalf("stripe: %+v", p)
	}
	p = PlanReplace(f, ReplaceSel{Group: "zfs:tank:mirror-1", Member: "/dev/sdc1"})
	if p.Refusal != "" || p.Old.Disk != "/dev/sdc" {
		t.Fatalf("by member: %+v", p)
	}
	// A resilver runs: wait for it.
	groups = zfsGroups(ZPool{Name: "tank", Status: zReplacing}, disks)
	if p := PlanReplace(ReplaceFacts{Disks: disks, Groups: groups, Now: now}, ReplaceSel{Disk: "/dev/sdb"}); !strings.Contains(p.Refusal, "already running") {
		t.Errorf("resilvering: %q", p.Refusal)
	}
}

func TestPlanLvm(t *testing.T) {
	disks := []BlockDevice{disk("sdb", 4*gib, "lvm"), disk("sdc", 4*gib, "free"), disk("sdd", 1*gib, "free")}
	g, ok := lvmGroup(VG{Name: "vg0", PVs: []PV{{Name: "/dev/sdb", Size: 4 * gib, Free: 3 * gib}}, LVs: []LV{{Name: "lv0", Type: "linear"}}}, disks)
	if !ok || g.Members[0].Used != gib || g.Tolerates != 0 {
		t.Fatalf("group: %+v", g)
	}
	f := ReplaceFacts{Disks: disks, Groups: []ReplaceGroup{g}, Now: now}
	p := PlanReplace(f, ReplaceSel{Disk: "/dev/sdb"})
	if p.Refusal != "" || p.Mode != "add-first" || p.Need != gib+4<<20 || p.Modes[1].OK {
		t.Fatalf("plan: %+v", p)
	}
	// 1 GiB is too small for 1 GiB of data plus metadata.
	if c := candidate(p, "/dev/sdd"); c.OK {
		t.Errorf("small disk: %+v", c)
	}
	if !strings.Contains(p.Steps[2], "pvmove") {
		t.Errorf("steps: %v", p.Steps)
	}
	// A volume group on an md array is replaced at the RAID level.
	if _, ok := lvmGroup(VG{Name: "vg1", PVs: []PV{{Name: "/dev/md127"}}}, disks); ok {
		t.Error("a VG on md offered for pvmove")
	}
	// SSD cache: refused.
	g, _ = lvmGroup(VG{Name: "vol_x", PVs: []PV{{Name: "/dev/sdb", Size: 4 * gib}}, LVs: []LV{{Name: "data", Type: "cache"}}}, disks)
	if p := PlanReplace(ReplaceFacts{Disks: disks, Groups: []ReplaceGroup{g}, Now: now}, ReplaceSel{Disk: "/dev/sdb"}); !strings.Contains(p.Refusal, "cache") {
		t.Errorf("cache: %q", p.Refusal)
	}
	// A missing physical volume.
	g, _ = lvmGroup(VG{Name: "vg0", PVs: []PV{{Name: "/dev/sdb", Size: 4 * gib}, {Name: "[unknown]"}}}, disks)
	if p := PlanReplace(ReplaceFacts{Disks: disks, Groups: []ReplaceGroup{g}, Now: now}, ReplaceSel{Disk: "/dev/sdb"}); p.Refusal == "" {
		t.Error("VG with a missing PV accepted")
	}
}

func TestParseMdFinish(t *testing.T) {
	for in, want := range map[string]int64{"12.3min": 738, "45sec": 45, "2.5hour": 9000, "": 0, "x": 0} {
		if got := ParseMdFinish(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}
