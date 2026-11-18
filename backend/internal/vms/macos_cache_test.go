package vms

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMacOSCacheImages(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "vol1", cacheDirName)
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"OpenCore-abcdef012345.qcow2", "BaseSystem-sonoma.img", "BaseSystem-ventura.img", "BaseSystem-ventura.dmg", "debian-13.qcow2"} {
		if err := os.WriteFile(filepath.Join(cache, f), []byte("1234"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oc, son := filepath.Join(cache, "OpenCore-abcdef012345.qcow2"), filepath.Join(cache, "BaseSystem-sonoma.img")
	// mac1: its overlays read the cache; a snapshot overlay sits on top of one.
	backing := map[string]string{
		"/vm/mac1/opencore.qcow2":   oc,
		"/vm/mac1/snap.qcow2":       "/vm/mac1/basesystem.qcow2",
		"/vm/mac1/basesystem.qcow2": son,
	}
	list := []VM{{Name: "mac1", Disks: []Disk{{Device: "disk", Source: "/vm/mac1/opencore.qcow2"}, {Device: "disk", Source: "/vm/mac1/snap.qcow2"}, {Device: "cdrom", Source: "/x.iso"}}},
		{Name: "linux", Disks: []Disk{{Device: "disk", Source: "/vm/linux/disk0.qcow2"}}}}
	got := macosCacheImages(root, list, func(p string) string { return backing[p] })
	if len(got) != 4 {
		t.Fatalf("images: %+v", got)
	}
	by := map[string]MacOSCacheImage{}
	for _, im := range got {
		by[filepath.Base(im.Path)] = im
	}
	if u := by["OpenCore-abcdef012345.qcow2"]; u.Kind != "opencore" || len(u.UsedBy) != 1 || u.Volume != "vol1" || u.Size != 4 {
		t.Errorf("opencore %+v", u)
	}
	if u := by["BaseSystem-sonoma.img"]; u.Version != "sonoma" || len(u.UsedBy) != 1 {
		t.Errorf("sonoma %+v", u)
	}
	if u := by["BaseSystem-ventura.img"]; len(u.UsedBy) != 0 {
		t.Errorf("ventura %+v", u)
	}
	if _, ok := by["debian-13.qcow2"]; ok {
		t.Error("a cloud image listed as a macOS image")
	}
}
