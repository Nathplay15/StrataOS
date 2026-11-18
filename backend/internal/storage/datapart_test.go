package storage

import "testing"

// A disk holding the system and a data partition (system on the data disks):
// the data partition is listed and usable, the system partitions are not.
func TestDataPartitions(t *testing.T) {
	root := "/"
	all := []BlockDevice{
		{Name: "sda", Path: "/dev/sda", Type: "disk", Model: "Disk A", Rotational: true, Children: []BlockDevice{
			{Name: "sda1", Path: "/dev/sda1", Type: "part", FSType: "vfat", Mountpoints: []string{"/boot/efi"}},
			{Name: "sda3", Path: "/dev/sda3", Type: "part", FSType: "linux_raid_member", Children: []BlockDevice{
				{Name: "md127", Path: "/dev/md127", Type: "raid1", Mountpoints: []string{root}},
			}},
			{Name: "sda4", Path: "/dev/sda4", Type: "part", PartLabel: DataPartLabel},
			// A data label on a partition that holds the system is ignored.
			{Name: "sda5", Path: "/dev/sda5", Type: "part", PartLabel: DataPartLabel, Mountpoints: []string{"/var"}},
		}},
		{Name: "sdb", Path: "/dev/sdb", Type: "disk", Children: []BlockDevice{
			{Name: "sdb1", Path: "/dev/sdb1", Type: "part", PartLabel: DataPartLabel},
		}},
	}
	none := func(string) []string { return nil }
	got := dataPartitions(all, none, func(string) bool { return false }, map[string]bool{})
	if len(got) != 1 || got[0].Path != "/dev/sda4" || got[0].DataOf != "sda" || got[0].Usage != "free" || got[0].Kind != "hdd" || got[0].System {
		t.Fatalf("data partitions: %+v", got)
	}
	// One holding an array is in use.
	all[0].Children[2].FSType = "linux_raid_member"
	all[0].Children[2].Children = []BlockDevice{{Name: "md0", Path: "/dev/md0", Type: "raid1"}}
	got = dataPartitions(all, none, func(string) bool { return false }, map[string]bool{})
	if len(got) != 1 || got[0].Usage != "raid" || got[0].ActiveUse != "raid" {
		t.Fatalf("data partition in an array: %+v", got)
	}
}
