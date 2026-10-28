package ceph

import "strataos/internal/storage"

// MinOSDSize is the smallest disk offered for an OSD (BlueStore needs room
// for its database besides the data).
const MinOSDSize = 5 << 30

// Disk is a disk of a node as the cluster wizard shows it.
type Disk struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	Serial   string `json:"serial"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind"`
	Usage    string `json:"usage"`
	UsedBy   string `json:"usedBy,omitempty"`
	Eligible bool   `json:"eligible"`
	// Erase: the disk holds something (old partitions, a filesystem) that
	// creating the OSD erases.
	Erase  bool   `json:"erase,omitempty"`
	Reason string `json:"reason,omitempty"` // why it cannot be used
}

// Eligibility decides which disks may become OSDs. volumeDevs are the
// devices (resolved paths and their parents) of StrataOS volumes, pools and
// arrays in use; busy reports disks used by a running operation.
func Eligibility(disks []storage.BlockDevice, volumeDevs map[string]string, busy func(string) string) []Disk {
	out := []Disk{}
	for _, d := range disks {
		x := Disk{Path: d.Path, Name: d.Name, Model: d.Model, Serial: d.Serial, Size: d.Size, Kind: d.Kind, Usage: d.Usage, UsedBy: d.UsedBy}
		x.Eligible, x.Erase, x.Reason = eligible(d, volumeDevs, busy)
		out = append(out, x)
	}
	return out
}

func eligible(d storage.BlockDevice, volumeDevs map[string]string, busy func(string) string) (bool, bool, string) {
	if d.System {
		return false, false, "System disk"
	}
	if d.Usage == "ceph" {
		return false, false, "Already a Ceph disk"
	}
	if d.Removable || d.Kind == "usb" {
		return false, false, "USB and removable disks are not suitable for Ceph"
	}
	if d.Size < MinOSDSize {
		return false, false, "Too small (5 GB at least)"
	}
	if busy != nil {
		if r := busy(d.Path); r != "" {
			return false, false, "Busy: " + r
		}
	}
	mounted := false
	used := ""
	walkDisk(d, func(b storage.BlockDevice) {
		if len(b.Mountpoints) > 0 {
			mounted = true
		}
		if v := volumeDevs[b.Path]; v != "" && used == "" {
			used = v
		}
	})
	if used != "" {
		return false, false, "Used by " + used
	}
	if mounted {
		return false, false, "Has mounted partitions"
	}
	switch d.Usage {
	case "free":
		return true, false, ""
	// Members of arrays, pools and LVM groups are refused even when their
	// set is not in use here: they may belong to disks to import.
	case "raid":
		return false, false, "Member of a RAID array (wipe it in Disks first)"
	case "zfs":
		return false, false, "Member of a ZFS pool (wipe it in Disks first)"
	case "lvm":
		return false, false, "Member of an LVM group (wipe it in Disks first)"
	}
	// Old filesystem or partitions that StrataOS does not use.
	return true, true, ""
}

func walkDisk(b storage.BlockDevice, fn func(storage.BlockDevice)) {
	fn(b)
	for _, c := range b.Children {
		walkDisk(c, fn)
	}
}
