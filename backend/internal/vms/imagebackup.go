package vms

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Image backups (backup datastores): instead of copying the disks into a
// folder, the backup reads each disk as a raw stream while the VM is held
// still, so that the repository can deduplicate blocks across VMs and
// backups. The rest of the VM (definition, UEFI variables, cloud-init
// seed) travels in a Bundle.

// ImageDisk is a disk to read.
type ImageDisk struct {
	Target string `json:"target"`
	Source string `json:"source"`
	Format string `json:"format"`
	Size   int64  `json:"size"` // virtual size in bytes
	// Snapshot: the RBD snapshot this backup read (kept as the base of the
	// next incremental backup of the task).
	Snapshot string `json:"snapshot,omitempty"`
}

// Bundle is everything of a VM but its disks.
type Bundle struct {
	Manifest      BackupManifest `json:"manifest"`
	DomainXML     string         `json:"domainXml"`
	NVRAM         []byte         `json:"nvram,omitempty"`
	UserData      []byte         `json:"userData,omitempty"`
	NetworkConfig []byte         `json:"networkConfig,omitempty"`
	Disks         []ImageDisk    `json:"disks"`
	// Checkpoint: the libvirt checkpoint matching the state backed up,
	// the base of the next incremental backup of the same task.
	Checkpoint string `json:"checkpoint,omitempty"`
}

// ImageSource is a VM held still for an image backup: Shared tells that the
// VM keeps its disks open (read them with locking off); Release lets it
// go on (merges the temporary snapshot, resumes or restarts it).
type ImageSource struct {
	Bundle  Bundle
	Shared  bool
	Release func()
}

// ImageBundle describes a VM for an image backup without holding it:
// its bundle (disks with their format, RBD disks as rbd:<pool>/<image>)
// and its current state.
func ImageBundle(name string) (*Bundle, *VM, error) {
	vm, err := Get(name)
	if err != nil {
		return nil, nil, err
	}
	doc, err := virsh("dumpxml", "--inactive", name)
	if err != nil {
		return nil, nil, err
	}
	b := Bundle{DomainXML: doc, Manifest: BackupManifest{Name: vm.Name, UUID: vm.UUID, Dir: vm.Dir, Created: time.Now().UTC(), Firmware: vm.Firmware}}
	for _, d := range vm.Disks {
		if d.Device != "disk" || d.Source == "" {
			continue
		}
		f := d.Format
		if f == "" {
			f = "raw" // libvirt's default
		}
		if img, ok := strings.CutPrefix(d.Source, RBDPrefix); ok {
			if !rbdImageRe.MatchString(img) {
				return nil, nil, fmt.Errorf("disk %s (%s) cannot be backed up as an image", d.Target, d.Source)
			}
			f = "raw"
		} else if !filepath.IsAbs(d.Source) || strings.ContainsAny(d.Source, ",\n\r") {
			return nil, nil, fmt.Errorf("disk %s (%s) cannot be backed up as an image", d.Target, d.Source)
		}
		if f != "raw" && f != "qcow2" {
			return nil, nil, fmt.Errorf("disk %s: unsupported format %s", d.Target, f)
		}
		b.Disks = append(b.Disks, ImageDisk{Target: d.Target, Source: d.Source, Format: f, Size: d.Size})
		b.Manifest.Disks = append(b.Manifest.Disks, BackupDiskInfo{Target: d.Target, Source: d.Source, File: d.Target + ".qcow2"})
	}
	if nv := nvramOf(doc); nv != "" {
		if data, err := os.ReadFile(nv); err == nil {
			b.NVRAM = data
			b.Manifest.NVRAM = true
		}
	}
	if vm.Dir != "" {
		b.UserData, _ = os.ReadFile(filepath.Join(vm.Dir, "cloud-init", "user-data"))
		b.NetworkConfig, _ = os.ReadFile(filepath.Join(vm.Dir, "cloud-init", "network-config"))
	}
	return &b, vm, nil
}

// rbdImageRe: <pool>/<image> of an RBD disk.
var rbdImageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}/[A-Za-z0-9][A-Za-z0-9_.-]{0,126}$`)

// OpenForImage prepares an image backup of a VM whose disks are files,
// holding it still (see holdStill).
func OpenForImage(ctx context.Context, name, mode string, p Progress) (*ImageSource, error) {
	b, vm, err := ImageBundle(name)
	if err != nil {
		return nil, err
	}
	var disks []Disk
	for _, d := range vm.Disks {
		if d.Device != "disk" || d.Source == "" {
			continue
		}
		if strings.HasPrefix(d.Source, RBDPrefix) {
			return nil, fmt.Errorf("disk %s (%s): a VM with disks on Ceph and in files cannot be backed up as an image", d.Target, d.Source)
		}
		disks = append(disks, d)
	}
	release, consistency, shared, err := holdStill(vm, disks, mode, p)
	if err != nil {
		return nil, err
	}
	b.Manifest.Consistency = consistency
	return &ImageSource{Bundle: *b, Shared: shared, Release: release}, nil
}

// HoldForSnapshots keeps a VM consistent while snapshots of its disks are
// taken elsewhere (RBD): the guest file systems frozen (snapshot mode),
// the VM suspended or shut down; release lets it go on.
func HoldForSnapshots(name, mode string, p Progress) (release func(), consistency string, err error) {
	vm, err := Get(name)
	if err != nil {
		return nil, "", err
	}
	if mode == "" || mode == BackupSnapshot {
		switch vm.State {
		case "running":
			thaw, c := Quiesce(name, p)
			return thaw, c, nil
		case "paused":
			return func() {}, "suspended", nil
		}
		return func() {}, "offline", nil
	}
	release, consistency, _, err = holdStill(vm, nil, mode, p)
	return release, consistency, err
}

// WriteBundle writes a bundle as a backup folder (domain.xml, manifest.json,
// nvram.fd, cloud-init/) whose disks the caller puts there, named after
// Manifest.Disks, for RestoreVM.
func WriteBundle(dir string, b *Bundle) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, d := range b.Manifest.Disks {
		if !safeBase(d.File) {
			return fmt.Errorf("invalid backup manifest")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "domain.xml"), []byte(b.DomainXML), 0o600); err != nil {
		return err
	}
	if b.Manifest.NVRAM {
		if err := os.WriteFile(filepath.Join(dir, "nvram.fd"), b.NVRAM, 0o600); err != nil {
			return err
		}
	}
	if len(b.UserData) > 0 {
		_ = os.MkdirAll(filepath.Join(dir, "cloud-init"), 0o700)
		if err := os.WriteFile(filepath.Join(dir, "cloud-init", "user-data"), b.UserData, 0o600); err != nil {
			return err
		}
		if len(b.NetworkConfig) > 0 {
			_ = os.WriteFile(filepath.Join(dir, "cloud-init", "network-config"), b.NetworkConfig, 0o600)
		}
	}
	data, err := json.MarshalIndent(b.Manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600)
}
