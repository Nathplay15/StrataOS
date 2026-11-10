package backup

import "testing"

func TestParseBlkidAndPlan(t *testing.T) {
	kv := ParseBlkid("DEVNAME=/dev/loop3p1\nUUID=1234\nTYPE=ext4\nLABEL=root fs\nUSAGE=filesystem\n")
	if kv["TYPE"] != "ext4" || kv["LABEL"] != "root fs" {
		t.Fatalf("%v", kv)
	}
	cases := []struct {
		kv         map[string]string
		typ, opts  string
		lvm        bool
		skipPrefix string
	}{
		{map[string]string{"TYPE": "ext4"}, "ext4", "ro,nodev,nosuid,noexec,noload", false, ""},
		{map[string]string{"TYPE": "ext2"}, "ext2", "ro,nodev,nosuid,noexec,noload", false, ""},
		{map[string]string{"TYPE": "xfs"}, "xfs", "ro,nodev,nosuid,noexec,norecovery,nouuid", false, ""},
		{map[string]string{"TYPE": "btrfs"}, "btrfs", "ro,nodev,nosuid,noexec,rescue=nologreplay", false, ""},
		{map[string]string{"TYPE": "ntfs"}, "ntfs3", "ro,nodev,nosuid,noexec", false, ""},
		{map[string]string{"TYPE": "vfat"}, "vfat", "ro,nodev,nosuid,noexec", false, ""},
		{map[string]string{"TYPE": "LVM2_member"}, "", "", true, ""},
		{map[string]string{"TYPE": "swap"}, "", "", false, "swap"},
		{map[string]string{"TYPE": "crypto_LUKS"}, "", "", false, "encrypted"},
		{map[string]string{"PTTYPE": "dos"}, "", "", false, "partition table"},
		{map[string]string{}, "", "", false, "no file system"},
		{map[string]string{"TYPE": "zfs_member"}, "", "", false, "unsupported file system zfs_member"},
	}
	for _, c := range cases {
		p := PlanMount(c.kv)
		if p.Type != c.typ || p.Opts != c.opts || p.LVM != c.lvm || (c.skipPrefix == "") != (p.Skip == "") || len(p.Skip) < len(c.skipPrefix) || p.Skip[:len(c.skipPrefix)] != c.skipPrefix {
			t.Errorf("%v: %+v", c.kv, p)
		}
		if p.Type != "" && (p.Opts[:3] != "ro," || p.Skip != "") {
			t.Errorf("%v must be read-only: %+v", c.kv, p)
		}
	}
}

func TestPartName(t *testing.T) {
	if n := PartName("vda", "/dev/loop7", "/dev/loop7p2"); n != "vda2" {
		t.Fatal(n)
	}
	if n := PartName("sdb", "/dev/loop7", "/dev/loop7p12"); n != "sdb12" {
		t.Fatal(n)
	}
	if n := PartName("vdb", "/dev/loop7", "/dev/loop7"); n != "vdb" {
		t.Fatal(n)
	}
}
