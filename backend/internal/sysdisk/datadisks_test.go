package sysdisk

import (
	"strings"
	"testing"

	"strataos/internal/storage"
)

// The system on two data disks: RAID 1 on partition 3 of each, a data array
// on partition 4 of each.
func onDataDisks() Facts {
	md := func(n string) Node { return Node{Name: n, Path: "/dev/" + n, Type: "raid1"} }
	root, data := md("md0"), md("md5")
	root.FSType, root.Mountpoints = "ext4", []string{"/"}
	data.FSType, data.Mountpoints = "ext4", []string{"/srv/strata/main"}
	disk := func(n string) Node {
		return Node{Name: n, Path: "/dev/" + n, Type: "disk", Size: 100 * gib, PTType: "gpt", Serial: strings.ToUpper(n), Children: []Node{
			{Name: n + "1", Path: "/dev/" + n + "1", Type: "part", PartType: TypeBIOSGrub},
			{Name: n + "3", Path: "/dev/" + n + "3", Type: "part", FSType: "linux_raid_member", Children: []Node{root}},
			{Name: n + "4", Path: "/dev/" + n + "4", Type: "part", FSType: "linux_raid_member", PartLabel: storage.DataPartLabel, Children: []Node{data}},
		}}
	}
	return Facts{Arch: "amd64", Disks: []Node{disk("sda"), disk("sdb"), {Name: "sdc", Path: "/dev/sdc", Type: "disk", Size: 50 * gib, Serial: "SDC"}},
		Mounts: []Mount{{Target: "/", Source: "/dev/md0", FSType: "ext4"}, {Target: "/srv/strata/main", Source: "/dev/md5", FSType: "ext4"}},
		Raids: []storage.RaidArray{
			{Name: "md0", Device: "/dev/md0", Level: "raid1", Health: "healthy", RaidDevices: 2, ActiveDevices: 2, Members: []storage.RaidMember{{Device: "/dev/sda3"}, {Device: "/dev/sdb3"}}},
			{Name: "md5", Device: "/dev/md5", Level: "raid1", Health: "healthy", RaidDevices: 2, ActiveDevices: 2, Members: []storage.RaidMember{{Device: "/dev/sda4"}, {Device: "/dev/sdb4"}}},
		},
		Used: map[string]int64{"/": 3 * gib}}
}

func TestLayoutDataDisks(t *testing.T) {
	l := ParseLayout(onDataDisks())
	if len(l.Problems) > 0 || l.Placement != "datadisks" || !l.Mirrored || len(l.DataParts) != 2 || len(l.Disks) != 2 {
		t.Fatalf("layout %+v", l)
	}
	// The data volume is neither copied with the system nor one of its arrays.
	if len(l.Mounts) != 1 || len(l.Arrays) != 1 || l.Arrays[0].Role != "root" {
		t.Fatalf("mounts %+v arrays %+v", l.Mounts, l.Arrays)
	}
	if l := ParseLayout(mirrored()); l.Placement != "mirror" {
		t.Fatalf("mirror placement %q", l.Placement)
	}
	if l := ParseLayout(cloudBIOS()); l.Placement != "own" {
		t.Fatalf("own placement %q", l.Placement)
	}
}

func TestPlanDataDisks(t *testing.T) {
	f := isoUEFI()
	f.Disks = append(f.Disks, Node{Name: "sdd", Path: "/dev/sdd", Type: "disk", Size: 60 * gib, Serial: "NEW2"})
	pf := facts(f)
	p := PlanDataDisks(pf, nil)
	if p.Refusal != "" || p.Kind != "datadisks" {
		t.Fatalf("plan %+v", p)
	}
	// One disk only: refused (the system is mirrored on all of them).
	if p := PlanDataDisks(pf, []string{"/dev/sdb"}); !strings.Contains(p.Refusal, "between 2") {
		t.Fatalf("one disk: %q", p.Refusal)
	}
	if p := PlanDataDisks(pf, []string{"/dev/sdb", "/dev/sdb"}); !strings.Contains(p.Refusal, "twice") {
		t.Fatalf("twice: %q", p.Refusal)
	}
	if p := PlanDataDisks(pf, []string{"/dev/sdb", "/dev/sdc"}); !strings.Contains(p.Refusal, "too small") {
		t.Fatalf("small: %q", p.Refusal)
	}
	if p := PlanDataDisks(pf, []string{"/dev/sdb", "/dev/sda"}); p.Refusal == "" {
		t.Fatal("the system disk must be refused")
	}
	p = PlanDataDisks(pf, []string{"/dev/sdb", "/dev/sdd"})
	if p.Refusal != "" || len(p.Targets) != 2 || p.Confirm != "sdb, sdd" {
		t.Fatalf("plan %+v", p)
	}
	// ESP, swap, system (20 GiB at least), data (the rest).
	if len(p.Parts) != 4 || p.Parts[0].Role != "esp" || p.Parts[1].Code != "FD00" || p.Parts[2].Code != "FD00" || p.Parts[2].MiB < minSysMiB ||
		p.Parts[3].Role != "data" || p.Parts[3].MiB != 0 || p.Parts[3].Name != storage.DataPartLabel {
		t.Fatalf("parts %+v", p.Parts)
	}
	if w := strings.Join(p.Warnings, " "); !strings.Contains(w, "different sizes") || !strings.Contains(w, "start empty") {
		t.Fatalf("warnings %v", p.Warnings)
	}
	if len(p.Steps) != 6 || !strings.Contains(p.Steps[0], "sdb, sdd") {
		t.Fatalf("steps %v", p.Steps)
	}
}

// Off the data disks or a mirror: a move onto one disk of its own.
func TestPlanMoveOffArrays(t *testing.T) {
	f := onDataDisks()
	pf := facts(f)
	if p := PlanDataDisks(pf, []string{"/dev/sdc"}); !strings.Contains(p.Refusal, "already on several disks") {
		t.Fatalf("datadisks again: %q", p.Refusal)
	}
	p := PlanMove(pf, "/dev/sdc")
	if p.Refusal != "" || len(p.OldDisks) != 2 || p.Confirm != "sdc" {
		t.Fatalf("move off the data disks: %+v", p)
	}
	if !strings.Contains(p.Steps[len(p.Steps)-1], "sda, sdb") {
		t.Fatalf("steps %v", p.Steps)
	}
	// A degraded mirror is repaired first.
	m := mirrored()
	m.Disks = append(m.Disks, Node{Name: "sdc", Path: "/dev/sdc", Type: "disk", Size: 64 * gib})
	if p := PlanMove(facts(m), "/dev/sdc"); !strings.Contains(p.Refusal, "degraded") {
		t.Fatalf("degraded mirror: %q", p.Refusal)
	}
}
