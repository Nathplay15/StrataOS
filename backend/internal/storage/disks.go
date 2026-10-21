// Package storage manages disks, RAID (mdadm, btrfs, ZFS), LVM and filesystems.
package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"strataos/internal/sys"
)

// Lock serialises all destructive storage operations.
var Lock sync.Mutex

// flex decodes lsblk values that may be strings, numbers, booleans or null.
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		*f = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flex(s)
		return nil
	}
	*f = flex(string(b))
	return nil
}

func (f flex) Int() int64 {
	n, _ := strconv.ParseInt(string(f), 10, 64)
	return n
}

func (f flex) Bool() bool {
	return f == "true" || f == "1"
}

type lsblkDev struct {
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Type        string     `json:"type"`
	Size        flex       `json:"size"`
	Model       flex       `json:"model"`
	Serial      flex       `json:"serial"`
	Vendor      flex       `json:"vendor"`
	Rota        flex       `json:"rota"`
	Tran        flex       `json:"tran"`
	FSType      flex       `json:"fstype"`
	Label       flex       `json:"label"`
	UUID        flex       `json:"uuid"`
	Mountpoints []*string  `json:"mountpoints"`
	PKName      flex       `json:"pkname"`
	RM          flex       `json:"rm"`
	WWN         flex       `json:"wwn"`
	Children    []lsblkDev `json:"children"`
}

// BlockDevice is the API view of a block device.
type BlockDevice struct {
	Name        string        `json:"name"`
	Path        string        `json:"path"`
	Type        string        `json:"type"`
	Size        int64         `json:"size"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	Vendor      string        `json:"vendor"`
	Rotational  bool          `json:"rotational"`
	Transport   string        `json:"transport"`
	FSType      string        `json:"fsType"`
	Label       string        `json:"label"`
	UUID        string        `json:"uuid"`
	Mountpoints []string      `json:"mountpoints"`
	Removable   bool          `json:"removable"`
	Children    []BlockDevice `json:"children,omitempty"`
	// Computed fields (disks only).
	Kind   string `json:"kind,omitempty"`   // hdd | ssd | nvme | usb | virtual
	System bool   `json:"system,omitempty"` // hosts the running OS
	Usage  string `json:"usage,omitempty"`  // free | raid | lvm | zfs | btrfs | filesystem | partitioned | system
	UsedBy string `json:"usedBy,omitempty"` // name of the array/pool/volume group using it
}

func convert(d lsblkDev) BlockDevice {
	b := BlockDevice{
		Name: d.Name, Path: d.Path, Type: d.Type, Size: d.Size.Int(),
		Model: strings.TrimSpace(string(d.Model)), Serial: string(d.Serial), Vendor: strings.TrimSpace(string(d.Vendor)),
		Rotational: d.Rota.Bool(), Transport: string(d.Tran), FSType: string(d.FSType), Label: string(d.Label),
		UUID: string(d.UUID), Removable: d.RM.Bool(), Mountpoints: []string{},
	}
	for _, m := range d.Mountpoints {
		if m != nil && *m != "" {
			b.Mountpoints = append(b.Mountpoints, *m)
		}
	}
	for _, c := range d.Children {
		b.Children = append(b.Children, convert(c))
	}
	return b
}

const lsblkCols = "NAME,PATH,TYPE,SIZE,MODEL,SERIAL,VENDOR,ROTA,TRAN,FSTYPE,LABEL,UUID,MOUNTPOINTS,PKNAME,RM,WWN"

// ListBlock returns the full lsblk tree.
func ListBlock() ([]BlockDevice, error) {
	out, err := sys.Run("lsblk", "-J", "-b", "-o", lsblkCols)
	if err != nil {
		return nil, err
	}
	var res struct {
		Blockdevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("parse lsblk: %w", err)
	}
	devs := make([]BlockDevice, 0, len(res.Blockdevices))
	for _, d := range res.Blockdevices {
		devs = append(devs, convert(d))
	}
	return devs, nil
}

func walk(b BlockDevice, fn func(BlockDevice)) {
	fn(b)
	for _, c := range b.Children {
		walk(c, fn)
	}
}

var systemMounts = map[string]bool{"/": true, "/boot": true, "/boot/efi": true, "/usr": true, "/var": true, "[SWAP]": true}

// ListDisks returns physical disks with usage classification.
func ListDisks() ([]BlockDevice, error) {
	all, err := ListBlock()
	if err != nil {
		return nil, err
	}
	var disks []BlockDevice
	for _, d := range all {
		if d.Type != "disk" || strings.HasPrefix(d.Name, "zram") || strings.HasPrefix(d.Name, "loop") || d.Size == 0 {
			continue
		}
		classify(&d)
		disks = append(disks, d)
	}
	return disks, nil
}

func classify(d *BlockDevice) {
	switch {
	case d.Transport == "nvme" || strings.HasPrefix(d.Name, "nvme"):
		d.Kind = "nvme"
	case d.Transport == "usb":
		d.Kind = "usb"
	case strings.HasPrefix(d.Name, "vd") || strings.Contains(strings.ToLower(d.Model), "qemu"):
		if d.Rotational {
			d.Kind = "virtual"
		} else {
			d.Kind = "ssd"
		}
	case d.Rotational:
		d.Kind = "hdd"
	default:
		d.Kind = "ssd"
	}
	walk(*d, func(b BlockDevice) {
		for _, m := range b.Mountpoints {
			if systemMounts[m] {
				d.System = true
			}
		}
	})
	if d.System {
		d.Usage = "system"
		return
	}
	// Look at the disk itself first, then its partitions.
	usage := func(b BlockDevice) (string, string) {
		switch b.FSType {
		case "linux_raid_member":
			return "raid", b.Label
		case "LVM2_member":
			return "lvm", ""
		case "zfs_member":
			return "zfs", b.Label
		case "btrfs":
			return "btrfs", b.Label
		case "":
			return "", ""
		default:
			return "filesystem", b.Label
		}
	}
	if u, by := usage(*d); u != "" {
		d.Usage, d.UsedBy = u, by
	} else if len(d.Children) > 0 {
		d.Usage = "partitioned"
		for _, c := range d.Children {
			if u, by := usage(c); u != "" && u != "filesystem" {
				d.Usage, d.UsedBy = u, by
				break
			}
		}
	} else {
		d.Usage = "free"
	}
	// md arrays and LVM appear as children: name the consumer.
	for _, c := range d.Children {
		if strings.HasPrefix(c.Type, "raid") {
			d.UsedBy = c.Name
		}
	}
	if d.Usage == "lvm" {
		if vg := pvVG(d.Path); vg != "" {
			d.UsedBy = vg
		}
	}
}

var devNameRe = regexp.MustCompile(`^/dev/[A-Za-z0-9/_\-.:]+$`)

// ValidateDevice checks that path names an existing block device that is not the system disk.
func ValidateDevice(path string) error {
	if !devNameRe.MatchString(path) {
		return fmt.Errorf("invalid device path %q", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("device %s not found", path)
	}
	fi, err := os.Stat(real)
	if err != nil || fi.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("%s is not a block device", path)
	}
	disks, err := ListBlock()
	if err != nil {
		return err
	}
	for _, d := range disks {
		if d.Type != "disk" {
			continue
		}
		classify(&d)
		if !d.System {
			continue
		}
		var bad bool
		walk(d, func(b BlockDevice) {
			if b.Path == real || b.Path == path {
				bad = true
			}
		})
		if bad {
			return fmt.Errorf("%s belongs to the system disk", path)
		}
	}
	return nil
}

// ValidateFreeDisk ensures each path is a whole disk that is not used by anything.
func ValidateFreeDisks(paths []string, allowInUse bool) error {
	if len(paths) == 0 {
		return fmt.Errorf("no disk selected")
	}
	disks, err := ListDisks()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			return fmt.Errorf("disk %s selected twice", p)
		}
		seen[p] = true
		if err := ValidateDevice(p); err != nil {
			return err
		}
		if r := BusyReason(p); r != "" {
			return fmt.Errorf("disk %s is busy (%s)", p, r)
		}
		var found *BlockDevice
		for i := range disks {
			if disks[i].Path == p {
				found = &disks[i]
			}
		}
		if found == nil {
			// Not a whole disk (md device, LV, partition): accept, ValidateDevice already ran.
			continue
		}
		if found.Usage != "free" && !allowInUse {
			return fmt.Errorf("disk %s is in use (%s %s); wipe it first", p, found.Usage, found.UsedBy)
		}
		for _, c := range found.Children {
			if len(c.Mountpoints) > 0 {
				return fmt.Errorf("disk %s has mounted partitions", p)
			}
		}
	}
	return nil
}

// Wipe erases all signatures and partition tables from a disk.
func Wipe(path string) error {
	if err := ValidateDevice(path); err != nil {
		return err
	}
	if r := BusyReason(path); r != "" {
		return fmt.Errorf("%s is busy (%s)", path, r)
	}
	// Compare resolved paths: a /dev/disk/by-id link must not skip the checks below.
	real := resolve(path)
	disks, _ := ListDisks()
	for _, d := range disks {
		if d.Path != path && d.Path != real {
			continue
		}
		var mounted bool
		walk(d, func(b BlockDevice) {
			if len(b.Mountpoints) > 0 {
				mounted = true
			}
		})
		if mounted {
			return fmt.Errorf("%s has mounted filesystems", path)
		}
		for _, c := range d.Children {
			if strings.HasPrefix(c.Type, "raid") || c.Type == "lvm" {
				return fmt.Errorf("%s is an active member of %s; remove it first", path, c.Name)
			}
			_, _ = sys.Run("wipefs", "-a", c.Path)
		}
	}
	_, _ = sys.Run("mdadm", "--zero-superblock", "--force", path)
	if _, err := sys.Run("wipefs", "-a", "-f", path); err != nil {
		return err
	}
	_, _ = sys.Run("sgdisk", "--zap-all", path)
	// Clear the first and last MiB to remove leftover ZFS labels.
	_, _ = sys.Run("dd", "if=/dev/zero", "of="+path, "bs=1M", "count=4", "conv=fsync", "status=none")
	_, _ = sys.Run("partprobe", path)
	_, _ = sys.Run("udevadm", "settle")
	return nil
}

// DevicesByUUID returns every block device currently carrying a filesystem UUID
// (several for a multi-device btrfs). Kernel names like /dev/sdX change between
// boots, so stored device paths must never be trusted for destructive operations.
func DevicesByUUID(uuid string) []string {
	if uuid == "" || strings.ContainsAny(uuid, " \n=") {
		return nil
	}
	out, err := sys.Run("blkid", "-c", "/dev/null", "-t", "UUID="+uuid, "-o", "device")
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// FindByUUID returns the device path holding a filesystem UUID.
func FindByUUID(uuid string) string {
	out, err := sys.Run("blkid", "-U", uuid)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Probe returns fstype, uuid and label of a device.
func Probe(dev string) (fstype, uuid, label string) {
	out, _ := sys.Run("blkid", "-o", "export", "-p", dev)
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch k {
		case "TYPE":
			fstype = v
		case "UUID":
			uuid = v
		case "LABEL":
			label = v
		}
	}
	return
}
