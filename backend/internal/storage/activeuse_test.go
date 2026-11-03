package storage

import "testing"

func TestActiveUse(t *testing.T) {
	none := func(string) []string { return nil }
	imported := func(p string) bool { return p == "tank" }
	md := BlockDevice{Name: "sda", Path: "/dev/sda", FSType: "linux_raid_member", Usage: "raid", Children: []BlockDevice{
		{Name: "md127", Path: "/dev/md127", Type: "raid1", Children: []BlockDevice{{Name: "vg-data", Type: "lvm", Mountpoints: []string{"/mnt/data"}}}},
	}}
	cases := []struct {
		name     string
		d        BlockDevice
		holders  func(string) []string
		swaps    map[string]bool
		kind, by string
	}{
		{"free disk", BlockDevice{Name: "sdc", Path: "/dev/sdc", Usage: "free"}, none, nil, "", ""},
		{"stale RAID signature", BlockDevice{Name: "sdc", Path: "/dev/sdc", FSType: "linux_raid_member", Usage: "raid", UsedBy: "nas:0"}, none, nil, "", ""},
		{"RAID member", md, none, nil, "raid", "md127"},
		{"PV with cache LV", BlockDevice{Name: "nvme0n1", Path: "/dev/nvme0n1", FSType: "LVM2_member", Usage: "lvm", UsedBy: "vg0", Children: []BlockDevice{{Name: "vg0-cache_cdata", Type: "lvm"}}}, none, nil, "lvm", "vg0"},
		{"mounted partition", BlockDevice{Name: "sdd", Path: "/dev/sdd", Children: []BlockDevice{{Name: "sdd1", Path: "/dev/sdd1", Type: "part", Mountpoints: []string{"/media/usb"}}}}, none, nil, "mounted", "/media/usb"},
		{"swap", BlockDevice{Name: "sde", Path: "/dev/sde", Children: []BlockDevice{{Name: "sde1", Path: "/dev/sde1", Type: "part"}}}, none, map[string]bool{"/dev/sde1": true}, "swap", "sde1"},
		{"imported ZFS pool", BlockDevice{Name: "sdf", Path: "/dev/sdf", Children: []BlockDevice{{Name: "sdf1", Type: "part", FSType: "zfs_member", Label: "tank"}}}, none, nil, "zfs", "tank"},
		{"exported ZFS pool", BlockDevice{Name: "sdf", Path: "/dev/sdf", Children: []BlockDevice{{Name: "sdf1", Type: "part", FSType: "zfs_member", Label: "old"}}}, none, nil, "", ""},
		{"device-mapper holder", BlockDevice{Name: "sdg", Path: "/dev/sdg"}, func(n string) []string {
			if n == "sdg" {
				return []string{"dm-3"}
			}
			return nil
		}, nil, "holder", "dm-3"},
	}
	for _, c := range cases {
		kind, by := activeUse(c.d, c.holders, imported, c.swaps)
		if kind != c.kind || by != c.by {
			t.Errorf("%s: got %q %q, want %q %q", c.name, kind, by, c.kind, c.by)
		}
	}
}
