package backup

import (
	"testing"

	"strataos/internal/store"
)

func TestValidateVMs(t *testing.T) {
	ok := store.BackupTask{Name: "vms", Kind: "rsync", DestType: "local", DestPath: "/srv/strata/usb/backups", VMs: []string{"web", "db"}, VMMode: "suspend"}
	if err := Validate(&ok); err != nil {
		t.Fatal(err)
	}
	for i, b := range []store.BackupTask{
		{Name: "x", Kind: "rsync", DestType: "local", DestPath: "/srv/strata/usb/b", VMs: []string{"../etc"}},
		{Name: "x", Kind: "rsync", DestType: "local", DestPath: "/srv/strata/usb/b", VMs: []string{"web", "web"}},
		{Name: "x", Kind: "rsync", DestType: "local", DestPath: "/srv/strata/usb/b", VMs: []string{"web"}, VMMode: "yolo"},
		{Name: "x", Kind: "rsync", DestType: "local", DestPath: "/srv/strata/usb/b", VMs: []string{"web"}, Sources: []string{"/srv/strata/main/vms"}},
		{Name: "x", Kind: "zfs", DestType: "local", DestPath: "tank/b", VMs: []string{"web"}, Sources: []string{"tank/data"}},
		{Name: "x", Kind: "rsync", DestType: "local", DestPath: "/srv/strata/usb/b"},
	} {
		if err := Validate(&b); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if resticVMRoot([]string{"/srv/strata/main/Photos", "/srv/strata/main/.strata-vmbackup/abc/vms"}, "abc") != "/srv/strata/main/.strata-vmbackup/abc/vms" || resticVMRoot([]string{"/x"}, "abc") != "" {
		t.Fatal("resticVMRoot")
	}
}
