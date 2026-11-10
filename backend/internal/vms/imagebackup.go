package vms

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
}

// Bundle is everything of a VM but its disks.
type Bundle struct {
	Manifest      BackupManifest `json:"manifest"`
	DomainXML     string         `json:"domainXml"`
	NVRAM         []byte         `json:"nvram,omitempty"`
	UserData      []byte         `json:"userData,omitempty"`
	NetworkConfig []byte         `json:"networkConfig,omitempty"`
	Disks         []ImageDisk    `json:"disks"`
}

// ImageSource is a VM held still for an image backup: Shared tells that the
// VM keeps its disks open (read them with locking off); Release lets it
// go on (merges the temporary snapshot, resumes or restarts it).
type ImageSource struct {
	Bundle  Bundle
	Shared  bool
	Release func()
}

// OpenForImage prepares an image backup of a VM.
func OpenForImage(ctx context.Context, name, mode string, p Progress) (*ImageSource, error) {
	vm, err := Get(name)
	if err != nil {
		return nil, err
	}
	doc, err := virsh("dumpxml", "--inactive", name)
	if err != nil {
		return nil, err
	}
	b := Bundle{DomainXML: doc, Manifest: BackupManifest{Name: vm.Name, UUID: vm.UUID, Dir: vm.Dir, Created: time.Now().UTC(), Firmware: vm.Firmware}}
	var disks []Disk
	for _, d := range vm.Disks {
		if d.Device != "disk" || d.Source == "" {
			continue
		}
		if strings.HasPrefix(d.Source, "rbd:") || !filepath.IsAbs(d.Source) || strings.ContainsAny(d.Source, ",\n\r") {
			return nil, fmt.Errorf("disk %s (%s) cannot be backed up as an image", d.Target, d.Source)
		}
		f := d.Format
		if f == "" {
			f = "raw" // libvirt's default
		}
		if f != "raw" && f != "qcow2" {
			return nil, fmt.Errorf("disk %s: unsupported format %s", d.Target, f)
		}
		disks = append(disks, d)
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
	release, consistency, shared, err := holdStill(vm, disks, mode, p)
	if err != nil {
		return nil, err
	}
	b.Manifest.Consistency = consistency
	return &ImageSource{Bundle: b, Shared: shared, Release: release}, nil
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
