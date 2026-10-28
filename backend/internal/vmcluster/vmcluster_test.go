package vmcluster

import (
	"slices"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func view(self string, members []string, now time.Time, peers map[string]Peer) View {
	return View{Self: self, Members: members, Peers: peers, Start: now.Add(-10 * time.Minute), Now: now}
}

func TestQuorum(t *testing.T) {
	for _, c := range []struct {
		total, reach int
		ok           bool
	}{{1, 1, true}, {2, 1, false}, {2, 2, true}, {3, 1, false}, {3, 2, true}, {4, 2, false}, {4, 3, true}, {5, 3, true}, {0, 0, false}} {
		if HasQuorum(c.total, c.reach) != c.ok {
			t.Errorf("HasQuorum(%d, %d) != %v", c.total, c.reach, c.ok)
		}
	}
	now := time.Now()
	all := []string{"a", "b", "c"}
	v := view("b", all, now, map[string]Peer{"a": {LastSeen: now.Add(-2 * time.Second)}, "c": {LastSeen: now.Add(-time.Minute)}})
	if !v.Quorum() || v.Leader() != "a" || !slices.Equal(v.Online(), []string{"a", "b"}) {
		t.Errorf("view of b: quorum %v leader %q online %v", v.Quorum(), v.Leader(), v.Online())
	}
	alone := view("c", all, now, map[string]Peer{"a": {LastSeen: now.Add(-time.Minute)}, "b": {LastSeen: now.Add(-time.Minute)}})
	if alone.Quorum() || alone.Leader() != "" || alone.Dead() != nil {
		t.Error("an isolated member has no quorum, no leader and declares nobody dead")
	}
}

func TestDead(t *testing.T) {
	now := time.Now()
	all := []string{"a", "b", "c"}
	fresh := now.Add(-time.Second)
	// c stopped answering a minute ago, b does not see it either.
	v := view("a", all, now, map[string]Peer{"b": {LastSeen: fresh, Sees: []string{"a", "b"}}, "c": {LastSeen: now.Add(-time.Minute)}})
	if d := v.Dead(); !slices.Equal(d, []string{"c"}) {
		t.Errorf("dead = %v, want [c]", d)
	}
	// Too recent to be dead.
	v.Peers["c"] = Peer{LastSeen: now.Add(-20 * time.Second)}
	if d := v.Dead(); d != nil {
		t.Errorf("unreachable for 20 s only: dead = %v", d)
	}
	// b still sees c: a lone opinion (1 of 3) is not a majority.
	v.Peers["c"] = Peer{LastSeen: now.Add(-time.Minute)}
	v.Peers["b"] = Peer{LastSeen: fresh, Sees: []string{"a", "b", "c"}}
	if d := v.Dead(); d != nil {
		t.Errorf("b still sees c: dead = %v", d)
	}
	// Never seen since this member started, but it started only now.
	v2 := View{Self: "a", Members: all, Peers: map[string]Peer{"b": {LastSeen: fresh, Sees: []string{"a", "b"}}}, Start: now.Add(-10 * time.Second), Now: now}
	if d := v2.Dead(); d != nil {
		t.Errorf("just started: dead = %v", d)
	}
	v2.Start = now.Add(-time.Minute)
	if d := v2.Dead(); !slices.Equal(d, []string{"c"}) {
		t.Errorf("never seen for a minute: dead = %v", d)
	}
	// Five members, two down: the three others agree.
	five := []string{"a", "b", "c", "d", "e"}
	v5 := view("a", five, now, map[string]Peer{
		"b": {LastSeen: fresh, Sees: []string{"a", "b", "c"}}, "c": {LastSeen: fresh, Sees: []string{"a", "b", "c"}},
		"d": {LastSeen: now.Add(-time.Minute)}, "e": {LastSeen: now.Add(-2 * time.Minute)},
	})
	if d := v5.Dead(); !slices.Equal(d, []string{"d", "e"}) {
		t.Errorf("five members: dead = %v", d)
	}
}

func TestSelfFence(t *testing.T) {
	now := time.Now()
	all := []string{"a", "b", "c"}
	v := view("c", all, now, map[string]Peer{})
	if ShouldSelfFence(v, now.Add(-10*time.Second)) {
		t.Error("self-fenced too early")
	}
	if !ShouldSelfFence(v, now.Add(-SelfFenceAfter)) {
		t.Error("not self-fenced after losing quorum")
	}
	v.Peers["a"] = Peer{LastSeen: now}
	if ShouldSelfFence(v, now.Add(-time.Hour)) {
		t.Error("self-fenced with quorum")
	}
	if SelfFenceAfter >= DeadAfter {
		t.Error("a cut-off member must stop its VMs before the others restart them")
	}
}

func TestAcceptAndStale(t *testing.T) {
	local := store.VMCluster{Version: 5}
	if Accept(local, store.VMCluster{Version: 5}) || !Accept(local, store.VMCluster{Version: 6}) {
		t.Error("only newer tables are accepted")
	}
	tab := store.VMCluster{HA: []store.ClusterHAVM{{Name: "web", Owner: "b"}, {Name: "db", Owner: "a"}, {Name: "mig", Owner: "b", Migrating: "a"}}}
	if s := Stale(tab, "a", []string{"web", "db", "mig", "other"}); !slices.Equal(s, []string{"web"}) {
		t.Errorf("stale = %v", s)
	}
}

func TestPlacement(t *testing.T) {
	nodes := []Node{{ID: "a", FreeMB: 4000, OK: true}, {ID: "b", FreeMB: 8000, OK: true}, {ID: "c", FreeMB: 8000, OK: true}, {ID: "d", FreeMB: 99999, OK: false}}
	if got := ChooseTarget(nodes, "a", "", 1024); got != "a" {
		t.Errorf("preferred: %q", got)
	}
	if got := ChooseTarget(nodes, "a", "", 3900); got != "b" {
		t.Errorf("preferred full, most free (b before c): %q", got)
	}
	if got := ChooseTarget(nodes, "", "b", 1024); got != "c" {
		t.Errorf("excluded: %q", got)
	}
	if got := ChooseTarget(nodes, "d", "", 1024); got != "b" {
		t.Errorf("preferred not OK: %q", got)
	}
	if got := ChooseTarget(nodes, "", "", 9000); got != "" {
		t.Errorf("fits nowhere: %q", got)
	}
	tab := store.VMCluster{Maintenance: []string{"c"}, HA: []store.ClusterHAVM{
		{Name: "v1", Owner: "a", Running: true, Preferred: "c"},
		{Name: "v2", Owner: "a", Running: true},
		{Name: "v3", Owner: "a"},                // stopped: stays stopped
		{Name: "v4", Owner: "b", Running: true}, // owner alive
		{Name: "v5", Owner: "a", Recover: true}, // was being recovered on a, which died too
	}}
	plan, stuck := PlanRecovery(tab, []string{"a"}, []Node{{ID: "a", FreeMB: 9000, OK: true}, {ID: "b", FreeMB: 3000, OK: true}, {ID: "c", FreeMB: 9000, OK: true}}, func(h store.ClusterHAVM) int { return 1024 })
	want := []Recovery{{"v1", "a", "b"}, {"v2", "a", "b"}}
	if !slices.Equal(plan, want) || !slices.Equal(stuck, []string{"v5"}) {
		t.Errorf("plan = %v stuck = %v", plan, stuck)
	}
}

func TestFencing(t *testing.T) {
	if AddrIP("10.0.0.2:0/3416770527") != "10.0.0.2" || AddrIP("v1:10.0.0.2:6800/12") != "10.0.0.2" || AddrIP("[fd00::2]:0/7") != "fd00::2" || AddrIP("garbage") != "" {
		t.Error("AddrIP")
	}
	locks := []Lock{{ID: "auto 139", Locker: "client.4567", Address: "10.0.0.2:0/3416770527"}}
	watchers := []Watcher{{Address: "10.0.0.2:0/3416770527"}, {Address: "10.0.0.2:0/99"}, {Address: "10.0.0.3:0/5"}, {Address: "10.0.0.2:0/0"}}
	got := FenceAddrs(locks, watchers, []string{"10.0.0.2"})
	if !slices.Equal(got, []string{"10.0.0.2:0/3416770527", "10.0.0.2:0/99"}) {
		t.Errorf("fence = %v (never another NAS, never a whole address)", got)
	}
	if FenceAddrs(locks, watchers, []string{"10.0.0.9"}) != nil {
		t.Error("nothing of a live NAS is fenced")
	}
	if b := LocksToBreak(locks, []string{"10.0.0.2"}); len(b) != 1 || b[0].ID != "auto 139" {
		t.Errorf("locks to break = %v", b)
	}
	if SafeToStart(locks, nil, []string{"10.0.0.3"}) || !SafeToStart(nil, []Watcher{{Address: "10.0.0.3:0/5"}}, []string{"10.0.0.3"}) || !SafeToStart(nil, nil, nil) {
		t.Error("SafeToStart")
	}
}

func TestCPUModel(t *testing.T) {
	intel := []string{"qemu64", "Nehalem", "Westmere", "SandyBridge", "Haswell-noTSX", "Skylake-Client", "Opteron_G4"}
	older := []string{"qemu64", "Nehalem", "Westmere", "SandyBridge", "IvyBridge"}
	amd := []string{"qemu64", "Nehalem", "Opteron_G4", "EPYC", "EPYC-Rome"}
	if m := CommonModel([][]string{intel, older}); m != "SandyBridge" {
		t.Errorf("intel+older = %q", m)
	}
	if m := CommonModel([][]string{intel, amd}); m != "Opteron_G4" {
		t.Errorf("intel+amd = %q", m)
	}
	if m := CommonModel([][]string{amd}); m != "EPYC-Rome" {
		t.Errorf("amd = %q", m)
	}
	if m := CommonModel([][]string{{"Weird"}, {"Weird"}}); m != "" {
		t.Errorf("unknown models only = %q", m)
	}
	if !ValidModel("Haswell-noTSX") || ValidModel("a b") || ValidModel("") || ValidModel("x;y") {
		t.Error("ValidModel")
	}
	if ok, _ := CPUCompatible("custom", "Nehalem", older, "", ""); !ok {
		t.Error("named model usable on the target")
	}
	if ok, _ := CPUCompatible("custom", "EPYC", older, "", ""); ok {
		t.Error("named model missing on the target")
	}
	if ok, _ := CPUCompatible("host-passthrough", "", nil, "intel-6-85", "intel-6-85"); !ok {
		t.Error("same host CPU")
	}
	if ok, _ := CPUCompatible("host-passthrough", "", nil, "intel-6-85", "amd-25-1"); ok {
		t.Error("another host CPU")
	}
}

func TestPrecheck(t *testing.T) {
	base := func() Facts {
		return Facts{VM: "web", Running: true, MemoryMB: 1024, CPUMode: "custom", CPUModel: "Nehalem",
			Disks:     []DiskFact{{Device: "disk", Target: "vda", Source: "rbd:rbd/web-disk0", Ceph: "fsid/rbd"}, {Device: "cdrom", Target: "sda"}},
			NICs:      []NICFact{{Type: "bridge", Source: "br0"}, {Type: "network", Source: "default"}},
			DstOnline: true, DstLibvirt: true, DstFreeMB: 4096, DstModels: []string{"Nehalem"}, DstNetworks: []string{"default"},
			DstBridges: []string{"br0"}, DstCeph: []string{"fsid/rbd"}}
	}
	if p := Precheck(base()); !p.OK() || !p.Live || p.CopyStorage {
		t.Errorf("shared storage: %+v", p)
	}
	cases := []struct {
		name string
		edit func(*Facts)
		want string
	}{
		{"offline", func(f *Facts) { f.DstOnline = false }, "does not answer"},
		{"memory", func(f *Facts) { f.DstFreeMB = 1100 }, "not enough free memory"},
		{"cpu", func(f *Facts) { f.DstModels = nil }, "CPU model"},
		{"host cpu", func(f *Facts) { f.CPUMode, f.SrcCPU, f.DstCPU = "host-passthrough", "x", "y" }, "processor"},
		{"bridge", func(f *Facts) { f.DstBridges = []string{"br1"} }, "no network bridge br0"},
		{"network", func(f *Facts) { f.DstNetworks = nil }, "virtual network default"},
		{"ceph pool", func(f *Facts) { f.DstCeph = nil }, "no VM storage on the Ceph pool"},
		{"iso", func(f *Facts) { f.Disks[1].Source = "/srv/strata/main/iso/x.iso" }, "eject the CD-ROM"},
		{"usb", func(f *Facts) { f.HostDevs = 1 }, "USB and PCI"},
		{"snapshots", func(f *Facts) { f.Snapshots = 2 }, "snapshots"},
		{"maintenance", func(f *Facts) { f.DstMaintenance = true }, "maintenance"},
		{"name taken", func(f *Facts) { f.DstHasVM = true }, "already exists"},
		{"local disk unconfirmed", func(f *Facts) {
			f.Disks[0] = DiskFact{Device: "disk", Target: "vda", Source: "/srv/strata/main/vms/web/disk0.qcow2"}
		}, "confirm"},
		{"local disk stopped", func(f *Facts) {
			f.Running = false
			f.Disks[0] = DiskFact{Device: "disk", Target: "vda", Source: "/srv/strata/main/vms/web/disk0.qcow2"}
		}, "start it"},
	}
	for _, c := range cases {
		f := base()
		c.edit(&f)
		p := Precheck(f)
		if p.OK() || !strings.Contains(strings.Join(p.Problems, "; "), c.want) {
			t.Errorf("%s: problems %v, want %q", c.name, p.Problems, c.want)
		}
	}
	f := base()
	f.BridgeMap = map[string]string{"br0": "br1"}
	f.DstBridges = []string{"br1"}
	if p := Precheck(f); !p.OK() {
		t.Errorf("bridge mapping: %v", p.Problems)
	}
	f = base()
	f.Disks[1].Source = "/srv/strata/main/iso/x.iso"
	f.DstFiles = []string{"/srv/strata/main/iso/x.iso"}
	if p := Precheck(f); !p.OK() {
		t.Errorf("ISO present on the target: %v", p.Problems)
	}
	f = base()
	f.Disks[0] = DiskFact{Device: "disk", Target: "vda", Source: "/srv/strata/main/vms/web/disk0.qcow2"}
	f.CopyStorage = true
	if p := Precheck(f); !p.OK() || !p.CopyStorage || len(p.Warnings) == 0 || !slices.Equal(p.Copied, []string{"vda"}) {
		t.Errorf("copy storage: %+v", p)
	}
	f = base()
	f.Running = false
	f.DstFreeMB = 0
	f.DstModels = nil
	if p := Precheck(f); !p.OK() || p.Live {
		t.Errorf("stopped VM on Ceph needs neither memory nor CPU: %+v", p)
	}
}
