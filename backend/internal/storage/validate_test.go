package storage

import "testing"

func TestValidVdev(t *testing.T) {
	for _, ok := range []string{"/dev/sdb", "/dev/disk/by-id/ata-HDD_4-part1", "934868875068009117", "sdb", "ata-QEMU_HARDDISK_QM00005"} {
		if err := validVdev(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-t", "--force", "-s", "sdb c", "a\nb"} {
		if validVdev(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRaidHasMember(t *testing.T) {
	a := &RaidArray{Name: "md127", Members: []RaidMember{{Device: "/dev/sdb"}, {Device: "/dev/sdc"}, {Device: ""}}}
	if err := a.hasMember("/dev/sdb"); err != nil {
		t.Errorf("member rejected: %v", err)
	}
	for _, bad := range []string{"", "/dev/sda", "--stop", "-f"} {
		if a.hasMember(bad) == nil {
			t.Errorf("%q accepted as member", bad)
		}
	}
}

func TestFstabLine(t *testing.T) {
	l, err := fstabLine("3f1c2a9e-1b2c-4d5e-8f90-123456789abc", "/srv/strata/main", "ext4", "defaults,noatime")
	if err != nil || l != "UUID=3f1c2a9e-1b2c-4d5e-8f90-123456789abc /srv/strata/main ext4 defaults,noatime 0 2" {
		t.Fatalf("got %q, %v", l, err)
	}
	if _, err := fstabLine("1A2B-3C4D", "/srv/strata/usb", "vfat", "defaults"); err != nil {
		t.Errorf("FAT serial rejected: %v", err)
	}
	for _, uuid := range []string{"", "x /etc ext4 defaults 0 0\nUUID=y", "a b", "-x", "a#b"} {
		if _, err := fstabLine(uuid, "/srv/strata/v", "ext4", "defaults"); err == nil {
			t.Errorf("UUID %q accepted", uuid)
		}
	}
	if _, err := fstabLine("abcd", "/srv/strata/a b", "ext4", "defaults"); err == nil {
		t.Error("mount point with a space accepted")
	}
}
