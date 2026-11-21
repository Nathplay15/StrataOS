package sysdisk

import (
	"strings"
	"testing"
	"time"

	"strataos/internal/storage"
)

const gib = int64(1) << 30

// An ISO installation in UEFI mode: ESP, Btrfs system (@rootfs, @strata), swap.
func isoUEFI() Facts {
	return Facts{
		EFI: true, Arch: "amd64",
		Disks: []Node{
			{Name: "sda", Path: "/dev/sda", Type: "disk", Size: 32 * gib, PTType: "gpt", Serial: "SYS1", Model: "SSD",
				Children: []Node{
					{Name: "sda1", Path: "/dev/sda1", Type: "part", Size: 512 << 20, FSType: "vfat", PartType: TypeESP, Mountpoints: []string{"/boot/efi"}},
					{Name: "sda2", Path: "/dev/sda2", Type: "part", Size: 30 * gib, FSType: "btrfs", UUID: "root-uuid", Mountpoints: []string{"/", "/var/lib/strata"}},
					{Name: "sda3", Path: "/dev/sda3", Type: "part", Size: 1 * gib, FSType: "swap", Mountpoints: []string{"[SWAP]"}},
				}},
			{Name: "sdb", Path: "/dev/sdb", Type: "disk", Size: 40 * gib, Serial: "NEW1"},
			{Name: "sdc", Path: "/dev/sdc", Type: "disk", Size: 4 * gib, Serial: "TINY"},
		},
		Mounts: []Mount{
			{Target: "/", Source: "/dev/sda2", FSType: "btrfs", Subvol: "@rootfs"},
			{Target: "/var/lib/strata", Source: "/dev/sda2", FSType: "btrfs", Subvol: "@strata"},
			{Target: "/boot/efi", Source: "/dev/sda1", FSType: "vfat"},
			{Target: "/proc", Source: "proc", FSType: "proc"},
			{Target: "/srv/strata/main", Source: "/dev/md127", FSType: "ext4"},
		},
		Swaps: []string{"/dev/sda3"},
		Used:  map[string]int64{"/": 3 * gib, "/boot/efi": 6 << 20},
	}
}

// The Debian cloud image (test VM): ext4 on partition 1, BIOS boot 14, ESP 15, booted in BIOS mode.
func cloudBIOS() Facts {
	return Facts{
		Arch: "amd64",
		Disks: []Node{
			{Name: "vda", Path: "/dev/vda", Type: "disk", Size: 20 * gib, PTType: "gpt", Children: []Node{
				{Name: "vda1", Path: "/dev/vda1", Type: "part", Size: 19 * gib, FSType: "ext4", Mountpoints: []string{"/"}},
				{Name: "vda14", Path: "/dev/vda14", Type: "part", Size: 3 << 20, PartType: TypeBIOSGrub},
				{Name: "vda15", Path: "/dev/vda15", Type: "part", Size: 124 << 20, FSType: "vfat", PartType: TypeESP, Mountpoints: []string{"/boot/efi"}},
			}},
			{Name: "sda", Path: "/dev/sda", Type: "disk", Size: 24 * gib, Serial: "STRATA-HDD-1", Rotational: true},
		},
		Mounts: []Mount{
			{Target: "/", Source: "/dev/vda1", FSType: "ext4"},
			{Target: "/boot/efi", Source: "/dev/vda15", FSType: "vfat"},
		},
		Used: map[string]int64{"/": 2 * gib},
	}
}

func mirrored() Facts {
	md := func(n string) Node { return Node{Name: n, Path: "/dev/" + n, Type: "raid1"} }
	root, swap := md("md0"), md("md1")
	root.FSType, root.Mountpoints = "btrfs", []string{"/"}
	swap.FSType, swap.Mountpoints = "swap", []string{"[SWAP]"}
	disk := func(n string) Node {
		return Node{Name: n, Path: "/dev/" + n, Type: "disk", Size: 32 * gib, PTType: "gpt", Children: []Node{
			{Name: n + "1", Path: "/dev/" + n + "1", Type: "part", FSType: "vfat", PartType: TypeESP},
			{Name: n + "2", Path: "/dev/" + n + "2", Type: "part", FSType: "linux_raid_member", Children: []Node{swap}},
			{Name: n + "3", Path: "/dev/" + n + "3", Type: "part", FSType: "linux_raid_member", Children: []Node{root}},
		}}
	}
	a, b := disk("sda"), disk("sdb")
	a.Children[0].Mountpoints = []string{"/boot/efi"}
	return Facts{EFI: true, Arch: "amd64", Disks: []Node{a, b},
		Mounts: []Mount{{Target: "/", Source: "/dev/md0", FSType: "btrfs", Subvol: "@rootfs"}, {Target: "/boot/efi", Source: "/dev/sda1", FSType: "vfat"}},
		Swaps:  []string{"/dev/md1"},
		Raids: []storage.RaidArray{
			{Name: "md0", Device: "/dev/md0", Level: "raid1", Health: "degraded", RaidDevices: 2, ActiveDevices: 1, Members: []storage.RaidMember{{Device: "/dev/sda3"}, {Device: ""}}},
			{Name: "md1", Device: "/dev/md1", Level: "raid1", Health: "healthy", RaidDevices: 2, ActiveDevices: 2, Members: []storage.RaidMember{{Device: "/dev/sda2"}, {Device: "/dev/sdb2"}}},
		},
		Used: map[string]int64{"/": 3 * gib}}
}

func disksOf(f Facts) []storage.BlockDevice {
	var out []storage.BlockDevice
	for _, d := range f.Disks {
		b := storage.BlockDevice{Name: d.Name, Path: d.Path, Size: d.Size, Serial: d.Serial, Usage: "free", Kind: "ssd"}
		if len(d.Children) > 0 {
			b.Usage = "system"
			b.System = true
		}
		out = append(out, b)
	}
	return out
}

func facts(f Facts) PlanFacts {
	return PlanFacts{Layout: ParseLayout(f), Disks: disksOf(f), Smart: map[string]storage.DiskVerdict{}, Busy: map[string]string{},
		LastConfigBackup: time.Now(), RestorePoints: true, Now: time.Now()}
}

func TestLayoutISO(t *testing.T) {
	l := ParseLayout(isoUEFI())
	if len(l.Problems) > 0 {
		t.Fatalf("problems: %v", l.Problems)
	}
	if l.Boot != "uefi" || l.RootOn != "part" || l.Root.FSType != "btrfs" || l.Root.Subvol != "@rootfs" {
		t.Fatalf("layout %+v", l)
	}
	if len(l.Disks) != 1 || l.Disks[0].ESP != "/dev/sda1" {
		t.Fatalf("disks %+v", l.Disks)
	}
	roles := map[string]string{}
	for _, m := range l.Mounts {
		roles[m.Target] = m.Role
	}
	if roles["/var/lib/strata"] != "subvol" || roles["/boot/efi"] != "esp" || roles["/srv/strata/main"] != "" {
		t.Fatalf("roles %v", roles)
	}
	if l.SwapSize != gib || len(l.Swap) != 1 || l.Mirrored {
		t.Fatalf("swap %v %d", l.Swap, l.SwapSize)
	}
}

func TestLayoutMirrored(t *testing.T) {
	l := ParseLayout(mirrored())
	if !l.Mirrored || !l.Degraded || len(l.Disks) != 2 || l.RootOn != "md" {
		t.Fatalf("layout %+v", l)
	}
	if ra := l.RootArray(); ra == nil || ra.Device != "/dev/md0" {
		t.Fatalf("root array %+v", ra)
	}
	if len(l.Problems) > 0 {
		t.Fatalf("problems: %v", l.Problems)
	}
	p := PlanMirror(facts(mirrored()), "")
	if !strings.Contains(p.Refusal, "already mirrored") {
		t.Fatalf("refusal %q", p.Refusal)
	}
	if p := PlanMove(facts(mirrored()), ""); p.Refusal == "" {
		t.Fatal("moving a mirrored system must be refused")
	}
}

func TestLayoutProblems(t *testing.T) {
	// LVM root.
	f := cloudBIOS()
	f.Disks[0].Children[0] = Node{Name: "vda1", Path: "/dev/vda1", Type: "part", FSType: "LVM2_member", Children: []Node{
		{Name: "vg-root", Path: "/dev/mapper/vg-root", Type: "lvm", FSType: "ext4", Mountpoints: []string{"/"}}}}
	f.Mounts[0].Source = "/dev/mapper/vg-root"
	if l := ParseLayout(f); l.RootOn != "lvm" || len(l.Problems) == 0 {
		t.Fatalf("LVM: %+v", l)
	}
	// /home on another disk.
	f = cloudBIOS()
	f.Disks[1].Children = []Node{{Name: "sda1", Path: "/dev/sda1", Type: "part", FSType: "ext4", Mountpoints: []string{"/home"}}}
	f.Mounts = append(f.Mounts, Mount{Target: "/home", Source: "/dev/sda1", FSType: "ext4"})
	if l := ParseLayout(f); len(l.Problems) == 0 || !strings.Contains(l.Problems[0], "/home") {
		t.Fatalf("home elsewhere: %v", l.Problems)
	}
	// A data partition on the system disk.
	f = isoUEFI()
	f.Disks[0].Children = append(f.Disks[0].Children, Node{Name: "sda4", Path: "/dev/sda4", Type: "part", Size: 100 * gib, FSType: "xfs"})
	if l := ParseLayout(f); len(l.Problems) == 0 || !strings.Contains(l.Problems[0], "sda4") {
		t.Fatalf("data partition: %v", l.Problems)
	}
	// A separate /boot partition is merged into the copy.
	f = cloudBIOS()
	f.Disks[0].Children = append(f.Disks[0].Children, Node{Name: "vda2", Path: "/dev/vda2", Type: "part", Size: gib, FSType: "ext4", Mountpoints: []string{"/boot"}})
	f.Mounts = append(f.Mounts, Mount{Target: "/boot", Source: "/dev/vda2", FSType: "ext4"})
	l := ParseLayout(f)
	if len(l.Problems) > 0 {
		t.Fatalf("separate /boot: %v", l.Problems)
	}
	merged := false
	for _, m := range l.Mounts {
		merged = merged || (m.Target == "/boot" && m.Role == "merge")
	}
	if !merged {
		t.Fatalf("mounts %+v", l.Mounts)
	}
	// Raspberry Pi.
	f = cloudBIOS()
	f.RPi = true
	if p := PlanMove(facts(f), ""); !strings.Contains(p.Refusal, "Raspberry") {
		t.Fatalf("rpi: %q", p.Refusal)
	}
}

func TestPlanMirrorUEFI(t *testing.T) {
	pf := facts(isoUEFI())
	p := PlanMirror(pf, "")
	if p.Refusal != "" {
		t.Fatal(p.Refusal)
	}
	ok := map[string]bool{}
	for _, c := range p.Candidates {
		ok[c.Path] = c.OK
	}
	if !ok["/dev/sdb"] || ok["/dev/sdc"] || len(p.Candidates) != 2 {
		t.Fatalf("candidates %+v", p.Candidates)
	}
	p = PlanMirror(pf, "/dev/sdb")
	if p.Refusal != "" || p.Target == nil {
		t.Fatalf("plan %+v", p)
	}
	// The mirror has the size of the smaller disk (the 32 GiB system disk).
	if len(p.Parts) != 3 || p.Parts[0].Code != "EF00" || p.Parts[1].Role != "swap" || p.Parts[1].Code != "FD00" || p.Parts[2].Code != "FD00" {
		t.Fatalf("parts %+v", p.Parts)
	}
	total := int64(reservedMiB)
	for _, x := range p.Parts {
		total += x.MiB
	}
	if total != 32*1024 {
		t.Fatalf("layout uses %d MiB of 32768", total)
	}
	if !strings.Contains(strings.Join(p.Warnings, " "), "larger") {
		t.Fatalf("warnings %v", p.Warnings)
	}
	if p := PlanMirror(pf, "/dev/sda"); p.Refusal == "" {
		t.Fatal("the system disk itself must be refused")
	}
	if p := PlanMirror(pf, "/dev/sdc"); !strings.Contains(p.Refusal, "too small") {
		t.Fatalf("small disk: %q", p.Refusal)
	}
	if p := PlanMirror(pf, "/dev/nope"); p.Refusal == "" {
		t.Fatal("unknown disk must be refused")
	}
}

func TestPlanMoveBIOS(t *testing.T) {
	pf := facts(cloudBIOS())
	pf.Disks[1].Kind = "hdd"
	p := PlanMove(pf, "/dev/sda")
	if p.Refusal != "" {
		t.Fatal(p.Refusal)
	}
	// No swap partition: boot + system, plain Linux partitions, the whole disk.
	if len(p.Parts) != 2 || p.Parts[0].Role != "biosgrub" || p.Parts[1].Code != "8300" || p.Parts[1].MiB != 24*1024-reservedMiB-1 {
		t.Fatalf("parts %+v", p.Parts)
	}
	if len(p.Steps) == 0 {
		t.Fatal("no steps")
	}
}

func TestPlanCandidates(t *testing.T) {
	pf := facts(isoUEFI())
	pf.Disks[1].Usage, pf.Disks[1].UsedBy = "raid", "data"
	pf.Disks[2].Size = 40 * gib
	pf.Busy["/dev/sdc"] = "burn-in"
	p := PlanMove(pf, "/dev/sdb")
	if !strings.Contains(p.Refusal, "holds data") {
		t.Fatalf("data disk: %q", p.Refusal)
	}
	if p := PlanMove(pf, "/dev/sdc"); !strings.Contains(p.Refusal, "reserved") {
		t.Fatalf("busy disk: %q", p.Refusal)
	}
	pf = facts(isoUEFI())
	pf.Smart["/dev/sdb"] = storage.DiskVerdict{Failing: true}
	if p := PlanMove(pf, "/dev/sdb"); !strings.Contains(p.Refusal, "SMART") {
		t.Fatalf("failing disk: %q", p.Refusal)
	}
	pf = facts(isoUEFI())
	pf.Disks[1].ActiveUse, pf.Disks[1].ActiveBy = "mounted", "/mnt/x"
	if p := PlanMove(pf, "/dev/sdb"); !strings.Contains(p.Refusal, "in use") {
		t.Fatalf("mounted disk: %q", p.Refusal)
	}
	pf = facts(isoUEFI())
	pf.Running = "An operation on the system disk is running."
	if p := PlanMove(pf, "/dev/sdb"); p.Refusal != pf.Running {
		t.Fatalf("running: %q", p.Refusal)
	}
	// Recommendations: no recent configuration backup, failing system disk.
	pf = facts(isoUEFI())
	pf.LastConfigBackup = time.Time{}
	pf.Smart["/dev/sda"] = storage.DiskVerdict{Failing: true}
	p = PlanMirror(pf, "/dev/sdb")
	w := strings.Join(p.Warnings, " ")
	if p.Refusal != "" || !strings.Contains(w, "Configuration backup") || !strings.Contains(w, "safer") {
		t.Fatalf("warnings %v (%s)", p.Warnings, p.Refusal)
	}
}

func TestParsers(t *testing.T) {
	mi := "25 1 0:23 /@rootfs / rw,relatime shared:1 - btrfs /dev/sda2 rw,space_cache=v2,subvolid=256,subvol=/@rootfs\n" +
		"26 25 0:23 /@strata /var/lib/strata rw - btrfs /dev/sda2 rw\n" +
		"27 25 8:1 / /boot/efi rw - vfat /dev/sda1 rw\n" +
		"28 25 8:1 / /mnt/with\\040space rw - ext4 /dev/sdb1 rw\n"
	m := ParseMountinfo(mi)
	if len(m) != 4 || m[0].Subvol != "@rootfs" || m[1].Target != "/var/lib/strata" || m[2].Subvol != "" || m[3].Target != "/mnt/with space" {
		t.Fatalf("mountinfo %+v", m)
	}
	if s := ParseSwaps("Filename Type Size Used Priority\n/dev/sda3 partition 1 0 -2\n/swapfile file 1 0 -3\n"); len(s) != 2 || s[1] != "/swapfile" {
		t.Fatalf("swaps %v", s)
	}
	js := `{"blockdevices":[{"name":"sda","path":"/dev/sda","type":"disk","size":34359738368,"fstype":null,"uuid":null,"partuuid":null,"parttype":null,"pttype":"gpt","serial":"S1","model":"QEMU HARDDISK   ","rota":true,"tran":"sata","mountpoints":[null],
	"children":[{"name":"sda1","path":"/dev/sda1","type":"part","size":536870912,"fstype":"vfat","uuid":"AB-CD","partuuid":"p1","parttype":"C12A7328-F81F-11D2-BA4B-00A0C93EC93B","pttype":"gpt","serial":null,"model":null,"rota":true,"tran":null,"mountpoints":["/boot/efi"]}]},
	{"name":"zram0","path":"/dev/zram0","type":"disk","size":1024,"mountpoints":["[SWAP]"]}]}`
	n, err := ParseLsblk(js)
	if err != nil || len(n) != 1 || n[0].Model != "QEMU HARDDISK" || !n[0].Rotational || n[0].Children[0].PartType != TypeESP || n[0].Children[0].Mountpoints[0] != "/boot/efi" {
		t.Fatalf("lsblk %+v %v", n, err)
	}
}

func TestRewriteFstab(t *testing.T) {
	in := `# /etc/fstab: static file system information.
UUID=old-root /               btrfs   defaults,subvol=@rootfs 0       0
UUID=old-boot /boot           ext4    defaults        0       2
UUID=AAAA-BBBB  /boot/efi       vfat    umask=0077      0       1
UUID=old-swap none            swap    sw              0       0
UUID=old-root /var/lib/strata btrfs subvol=@strata,defaults 0 0 # StrataOS settings
UUID=data-1 /srv/strata/main ext4 defaults,nofail 0 2
`
	out := RewriteFstab(in, FstabEdit{Root: "UUID=new-root", SameFS: []string{"/var/lib/strata"}, Merged: []string{"/boot"},
		ESP: "UUID=CCCC-DDDD", OldSwap: []string{"UUID=old-swap"}, Swap: "UUID=new-swap"})
	for _, want := range []string{
		"UUID=new-root /               btrfs   defaults,subvol=@rootfs 0       0",
		"# UUID=old-boot /boot",
		"UUID=CCCC-DDDD  /boot/efi       vfat    umask=0077",
		"UUID=new-swap none            swap",
		"UUID=new-root /var/lib/strata btrfs subvol=@strata",
		"UUID=data-1 /srv/strata/main ext4 defaults,nofail 0 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nUUID=old") {
		t.Errorf("old source left:\n%s", out)
	}
	// No swap line before: one is added; an ESP that goes is commented out.
	out = RewriteFstab("/dev/vda1 / ext4 rw 0 1\n/dev/vda15 /boot/efi vfat defaults 0 2\n", FstabEdit{Root: "UUID=r", Swap: "UUID=s"})
	if !strings.Contains(out, "UUID=r / ext4") || !strings.Contains(out, "# /dev/vda15") || !strings.HasSuffix(out, "UUID=s none swap sw 0 0\n") {
		t.Errorf("got\n%s", out)
	}
	if e := ParseFstab(in); len(e) != 6 || e[3].Type != "swap" || e[4].Opts != "subvol=@strata,defaults" {
		t.Errorf("parse %+v", e)
	}
}

func TestESPMkfsArgs(t *testing.T) {
	if got := strings.Join(espMkfsArgs("A1B2-C3D4"), " "); got != "-F 32 -n EFI -i A1B2C3D4" {
		t.Fatalf("got %q", got)
	}
	for _, u := range []string{"", "not-an-id", "A1B2-C3D4; rm", "0e3b8f2c-1111-2222-3333-444455556666"} {
		if got := strings.Join(espMkfsArgs(u), " "); got != "-F 32 -n EFI" {
			t.Fatalf("%q: got %q", u, got)
		}
	}
}
