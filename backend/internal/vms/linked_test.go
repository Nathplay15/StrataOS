package vms

import (
	"context"
	"strings"
	"testing"
	"time"
)

func disk(src string) Disk { return Disk{Device: "disk", Source: src, Format: "qcow2"} }

func TestOwnsDir(t *testing.T) {
	vm := func(name, dir string, disks ...string) VM {
		v := VM{Name: name, Dir: dir}
		for _, d := range disks {
			v.Disks = append(v.Disks, disk(d))
		}
		return v
	}
	own := vm("web", "/srv/strata/vol1/vms/web", "/srv/strata/vol1/vms/web/disk0.qcow2")
	mac1 := vm("mac1", "/srv/strata/vol1/.strata-cloud-images", "/srv/strata/vol1/.strata-cloud-images/BaseSystem.img")
	share := vm("media", "/srv/strata/vol1/media", "/srv/strata/vol1/media/media.qcow2")
	volume := vm("vol1", "/srv/strata/vol1", "/srv/strata/vol1/disk.img")
	other := vm("db", "/srv/strata/vol1/vms/db", "/srv/strata/vol1/vms/web/extra.qcow2")
	named := vm("old", "/srv/strata/vol1/imports/disks", "/srv/strata/vol1/imports/disks/a.qcow2")
	all := []VM{own, mac1, share, volume}
	for _, c := range []struct {
		vm   VM
		list []VM
		want bool
	}{
		{own, all, true},
		{mac1, all, false},   // the shared image cache
		{share, all, false},  // a shared folder named like the VM
		{volume, all, false}, // a volume
		{named, all, false},  // a folder not named after the VM
		{own, append(all, other), false},
	} {
		if got := ownsDir(c.vm, c.vm.Name, c.list); got != c.want {
			t.Errorf("ownsDir(%s, %s) = %v", c.vm.Name, c.vm.Dir, got)
		}
	}
}

func TestFindLinked(t *testing.T) {
	tpl := VM{Name: "tpl", Dir: "/srv/strata/v/vms/tpl", Template: true, Disks: []Disk{disk("/srv/strata/v/vms/tpl/disk0.qcow2")}}
	src := VM{Name: "src", Dir: "/srv/strata/v/vms/src", Disks: []Disk{disk("/srv/strata/v/vms/src/disk0.snap1")}}
	c1 := VM{Name: "c1", Dir: "/srv/strata/v/vms/c1", Disks: []Disk{disk("/srv/strata/v/vms/c1/disk0.qcow2")}}
	c2 := VM{Name: "c2", Dir: "/srv/strata/v/vms/c2", Disks: []Disk{disk("/srv/strata/v/vms/c2/disk0.qcow2"), {Device: "cdrom", Source: "/x.iso"}}}
	snap := VM{Name: "snap", Dir: "/srv/strata/v/vms/snap", Disks: []Disk{disk("/srv/strata/v/vms/snap/disk0.s1")}}
	backing := map[string]string{
		"/srv/strata/v/vms/c1/disk0.qcow2": "/srv/strata/v/vms/tpl/disk0.qcow2",
		"/srv/strata/v/vms/c2/disk0.qcow2": "/srv/strata/v/vms/src/disk0.qcow2",  // an older state of src (external snapshot)
		"/srv/strata/v/vms/snap/disk0.s1":  "/srv/strata/v/vms/snap/disk0.qcow2", // its own snapshot chain
		"/x.iso":                           "/srv/strata/v/vms/tpl/disk0.qcow2",
	}
	got := findLinked([]VM{tpl, src, c1, c2, snap}, func(p string) string { return backing[p] })
	if len(got) != 2 {
		t.Fatalf("findLinked = %+v", got)
	}
	if got[0].Clone != "c1" || got[0].Source != "tpl" || !got[0].SourceTemplate {
		t.Errorf("c1: %+v", got[0])
	}
	if got[1].Clone != "c2" || got[1].Source != "src" || got[1].SourceTemplate {
		t.Errorf("c2: %+v", got[1])
	}
}

func TestMovePaths(t *testing.T) {
	doc := `<domain type="kvm"><name>new</name><devices>` +
		`<disk type="file" device="disk"><source file="/srv/strata/v/vms/old/disk0.qcow2"></source></disk>` +
		`<disk type="file" device="cdrom"><source file="/srv/strata/v/vms/old/seed.iso"></source></disk>` +
		`<disk type="file" device="cdrom"><source file="/srv/strata/v/vms/older/x.iso"></source></disk>` +
		`</devices></domain>`
	out, err := movePaths(doc, "/srv/strata/v/vms/old", "/srv/strata/v/vms/new")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/srv/strata/v/vms/new/disk0.qcow2", "/srv/strata/v/vms/new/seed.iso", "/srv/strata/v/vms/older/x.iso"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing in %s", want, out)
		}
	}
	if strings.Contains(out, "/vms/old/") {
		t.Errorf("old path left: %s", out)
	}
}

func TestLockDownload(t *testing.T) {
	ctx := context.Background()
	unlock, err := lockDownload(ctx, "/srv/strata/v/ISOs/win.iso")
	if err != nil {
		t.Fatal(err)
	}
	// Another file is not blocked.
	other, err := lockDownload(ctx, "/srv/strata/v/ISOs/other.iso")
	if err != nil {
		t.Fatal(err)
	}
	other()
	// The same file waits, and gives up with its context.
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := lockDownload(short, "/srv/strata/v/ISOs/win.iso"); err == nil {
		t.Fatal("second download of the same file not blocked")
	}
	got := make(chan error, 1)
	go func() {
		u, err := lockDownload(ctx, "/srv/strata/v/ISOs/win.iso")
		if err == nil {
			u()
		}
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	unlock()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting download never released")
	}
}
