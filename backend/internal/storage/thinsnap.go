package storage

// LVM thin snapshots: cheap like Btrfs/ZFS ones (no size to reserve). They
// are not mounted; browsing one mounts it read-only under ThinMountDir.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"strataos/internal/sys"
)

// ThinMountDir holds the read-only mounts of thin snapshots being browsed.
const ThinMountDir = "/run/strata/lvsnap"

// ThinLV is a thin logical volume (not a snapshot) and where it is mounted.
type ThinLV struct {
	VG         string `json:"vg"`
	LV         string `json:"lv"`
	MountPoint string `json:"mountPoint,omitempty"`
}

// ThinLVs lists the thin volumes that are not snapshots.
func ThinLVs() []ThinLV {
	rows, _ := lvmReport("lvs", "lv_name,vg_name,lv_path,segtype,origin")
	var out []ThinLV
	for _, r := range rows {
		if r["segtype"] != "thin" || r["origin"] != "" {
			continue
		}
		t := ThinLV{VG: r["vg_name"], LV: r["lv_name"]}
		if p := r["lv_path"]; p != "" {
			if mp, err := sys.Run("findmnt", "-n", "-o", "TARGET", "--source", p); err == nil {
				t.MountPoint = strings.ReplaceAll(strings.TrimSpace(strings.Split(mp, "\n")[0]), `\x20`, " ")
			}
		}
		out = append(out, t)
	}
	return out
}

// IsThinLV reports whether vg/lv is a thin volume.
func IsThinLV(vg, lv string) bool {
	for _, t := range ThinLVs() {
		if t.VG == vg && t.LV == lv {
			return true
		}
	}
	return false
}

// ThinSnapshot makes a thin snapshot of vg/lv.
func ThinSnapshot(vg, lv, name string) error {
	for _, n := range []string{vg, lv, name} {
		if err := ValidName(n); err != nil {
			return err
		}
	}
	if !IsThinLV(vg, lv) {
		return fmt.Errorf("%s/%s is not a thin volume", vg, lv)
	}
	Lock.Lock()
	defer Lock.Unlock()
	_, err := sys.Run("lvcreate", "-y", "-s", "-n", name, vg+"/"+lv)
	return err
}

// ThinSnapshotNames lists the snapshots of vg/lv.
func ThinSnapshotNames(vg, lv string) ([]string, error) {
	if err := ValidName(vg); err != nil {
		return nil, err
	}
	rows, err := lvmReport("lvs", "lv_name,vg_name,origin,segtype")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if r["vg_name"] == vg && r["origin"] == lv && r["segtype"] == "thin" {
			out = append(out, r["lv_name"])
		}
	}
	return out, nil
}

// thinMount is where a snapshot is mounted for browsing.
func thinMount(vg, name string) string { return filepath.Join(ThinMountDir, vg, name) }

// MountThinSnapshot mounts a snapshot read-only (once) and returns where.
func MountThinSnapshot(vg, name string) (string, error) {
	for _, n := range []string{vg, name} {
		if err := ValidName(n); err != nil {
			return "", err
		}
	}
	dir := thinMount(vg, name)
	if IsMountpoint(dir) {
		return dir, nil
	}
	// Thin snapshots skip activation by default (-K overrides it).
	if _, err := sys.Run("lvchange", "-ay", "-K", vg+"/"+name); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dev := "/dev/" + vg + "/" + name
	opts := "ro"
	fstype, _, _ := Probe(dev)
	switch fstype {
	case "xfs":
		opts += ",nouuid,norecovery" // same UUID as the mounted origin
	case "ext4", "ext3":
		opts += ",noload" // do not replay the journal of a read-only copy
	}
	if _, err := sys.Run("mount", "-o", opts, dev, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// RemoveThinSnapshot unmounts (when browsed) and removes a snapshot.
func RemoveThinSnapshot(vg, name string) error {
	for _, n := range []string{vg, name} {
		if err := ValidName(n); err != nil {
			return err
		}
	}
	if dir := thinMount(vg, name); IsMountpoint(dir) {
		if _, err := sys.Run("umount", dir); err != nil {
			return err
		}
		_ = os.Remove(dir)
	}
	Lock.Lock()
	defer Lock.Unlock()
	_, err := sys.Run("lvremove", "-f", vg+"/"+name)
	return err
}
