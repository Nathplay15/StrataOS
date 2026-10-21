package vms

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/sys"
)

// VM backups: a folder holding the definition (domain.xml), a flattened qcow2
// copy of each disk, the UEFI variables and the cloud-init seed, plus a
// manifest. A running VM is copied consistently: by default a temporary
// external snapshot (file systems frozen by the guest agent when it answers)
// keeps the disks still during the copy, then is merged back (blockcommit);
// the VM can also be paused, or shut down, for the copy.

type BackupManifest struct {
	Name        string           `json:"name"`
	UUID        string           `json:"uuid"`
	Dir         string           `json:"dir"` // folder of the VM when it was backed up
	Created     time.Time        `json:"created"`
	Firmware    string           `json:"firmware"`
	Consistency string           `json:"consistency"` // offline | frozen | crash | suspended
	Disks       []BackupDiskInfo `json:"disks"`
	NVRAM       bool             `json:"nvram,omitempty"`
	Size        int64            `json:"size"`
}

type BackupDiskInfo struct {
	Target string `json:"target"`
	Source string `json:"source"` // path in the VM definition
	File   string `json:"file"`   // copy, relative to the backup folder
}

// Backup modes for running VMs.
const (
	BackupSnapshot = "snapshot" // live external snapshot + blockcommit (default)
	BackupSuspend  = "suspend"
	BackupShutdown = "shutdown"
)

func ValidBackupMode(m string) bool {
	return m == "" || m == BackupSnapshot || m == BackupSuspend || m == BackupShutdown
}

func waitState(name, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s, err := virsh("domstate", name); err == nil && strings.TrimSpace(s) == want {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// ExportVM writes a consistent backup of a VM into dest (created, must not exist).
func ExportVM(ctx context.Context, name, dest, mode string, p Progress) (*BackupManifest, error) {
	vm, err := Get(name)
	if err != nil {
		return nil, err
	}
	doc, err := virsh("dumpxml", "--inactive", name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return nil, err
	}
	man := &BackupManifest{Name: vm.Name, UUID: vm.UUID, Dir: vm.Dir, Created: time.Now().UTC(), Firmware: vm.Firmware, Consistency: "offline"}
	var disks []Disk
	for _, d := range vm.Disks {
		if d.Device == "disk" && d.Source != "" {
			if strings.ContainsAny(d.Source, ",\n") {
				return nil, fmt.Errorf("unsupported disk path %s", d.Source)
			}
			disks = append(disks, d)
		}
	}
	running := vm.State == "running" || vm.State == "paused"
	shared := false
	if running {
		if mode == "" {
			mode = BackupSnapshot
		}
		switch mode {
		case BackupSuspend:
			if vm.State == "running" {
				if _, err := virsh("suspend", name); err != nil {
					return nil, err
				}
				defer func() { _, _ = virsh("resume", name) }()
			}
			man.Consistency, shared = "suspended", true
		case BackupShutdown:
			p.log("Shutting %s down for the copy", name)
			if _, err := virsh("shutdown", name); err != nil {
				return nil, err
			}
			if !waitState(name, "shut off", 5*time.Minute) {
				return nil, fmt.Errorf("%s did not shut down within 5 minutes", name)
			}
			defer func() { _ = Action(name, "start") }()
			man.Consistency = "offline"
		default:
			commit, how, err := tempSnapshot(vm, disks, p)
			if err != nil {
				return nil, err
			}
			defer commit()
			man.Consistency, shared = how, true
		}
	}
	for i, d := range disks {
		file := fmt.Sprintf("%s.qcow2", d.Target)
		p.log("Copying disk %s (%s)", d.Target, d.Source)
		if err := convertImage(ctx, d.Source, "", filepath.Join(dest, file), shared, p.scaled(float64(i)*95/float64(len(disks)), float64(i+1)*95/float64(len(disks)))); err != nil {
			return nil, err
		}
		man.Disks = append(man.Disks, BackupDiskInfo{Target: d.Target, Source: d.Source, File: file})
	}
	if nv := nvramOf(doc); nv != "" {
		if err := copyFile(nv, filepath.Join(dest, "nvram.fd"), 0o600); err == nil {
			man.NVRAM = true
		}
	}
	if vm.Dir != "" {
		if user, err := os.ReadFile(filepath.Join(vm.Dir, "cloud-init", "user-data")); err == nil {
			_ = os.MkdirAll(filepath.Join(dest, "cloud-init"), 0o700)
			_ = os.WriteFile(filepath.Join(dest, "cloud-init", "user-data"), user, 0o600)
			if n, err := os.ReadFile(filepath.Join(vm.Dir, "cloud-init", "network-config")); err == nil {
				_ = os.WriteFile(filepath.Join(dest, "cloud-init", "network-config"), n, 0o600)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dest, "domain.xml"), []byte(doc), 0o600); err != nil {
		return nil, err
	}
	man.Size = dirSize(dest)
	b, _ := json.MarshalIndent(man, "", "  ")
	if err := os.WriteFile(filepath.Join(dest, "manifest.json"), b, 0o600); err != nil {
		return nil, err
	}
	p.set(100)
	return man, nil
}

// tempSnapshot puts a temporary overlay on every disk of a running VM, so that
// the disk files stop changing; commit merges the overlays back and removes them.
func tempSnapshot(vm *VM, disks []Disk, p Progress) (commit func(), consistency string, err error) {
	snap := fmt.Sprintf("strata-backup-%d", time.Now().Unix())
	args := []string{"snapshot-create-as", vm.Name, "--name", snap, "--disk-only", "--atomic", "--no-metadata"}
	overlays := map[string]string{}
	for _, d := range vm.Disks {
		if d.Device != "disk" || d.Source == "" {
			args = append(args, "--diskspec", d.Target+",snapshot=no")
			continue
		}
		o := filepath.Join(filepath.Dir(d.Source), "."+vm.Name+"-"+d.Target+"."+snap+".qcow2")
		overlays[d.Target] = o
		args = append(args, "--diskspec", d.Target+",snapshot=external,file="+o)
	}
	consistency = "frozen"
	if _, err = virsh(append(args, "--quiesce")...); err != nil {
		p.log("Guest agent unavailable (%v): crash-consistent copy", err)
		consistency = "crash"
		if _, err = virsh(args...); err != nil {
			return nil, "", fmt.Errorf("live snapshot failed: %v", err)
		}
	}
	commit = func() {
		for _, d := range disks {
			if _, err := sys.RunOpts(context.Background(), sys.Opts{Timeout: 6 * time.Hour}, "virsh", "--connect", "qemu:///system",
				"blockcommit", vm.Name, d.Target, "--active", "--pivot", "--wait"); err != nil {
				p.log("warning: merging the temporary snapshot of %s failed: %v (the VM keeps running on %s)", d.Target, err, overlays[d.Target])
				continue
			}
			_ = os.Remove(overlays[d.Target])
		}
	}
	return commit, consistency, nil
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// ReadBackup reads the manifest of a backup folder.
func ReadBackup(dir string) (*BackupManifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m BackupManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// RestoreReq recreates a VM from a backup folder.
type RestoreReq struct {
	Name    string `json:"name"`    // name of the restored VM
	Storage string `json:"storage"` // folder that will hold it
	Start   bool   `json:"start"`
}

// RestoreVM defines a new VM from a backup folder: disks copied into
// storage/<name>. It keeps the identity (UUID, MAC) when the original VM is gone
// and the name is the same; otherwise it gets a new one, like a clone.
func RestoreVM(ctx context.Context, dir string, r RestoreReq, p Progress) error {
	if err := ValidName(r.Name); err != nil {
		return err
	}
	man, err := ReadBackup(dir)
	if err != nil {
		return fmt.Errorf("not a VM backup: %v", err)
	}
	doc, err := os.ReadFile(filepath.Join(dir, "domain.xml"))
	if err != nil {
		return err
	}
	if _, err := virsh("dominfo", r.Name); err == nil {
		return fmt.Errorf("a VM named %s already exists", r.Name)
	}
	storage, err := insideStrata(r.Storage)
	if err != nil {
		return err
	}
	keep := r.Name == man.Name
	if keep {
		if _, err := virsh("dominfo", man.UUID); err == nil {
			keep = false
		}
	}
	vdir := filepath.Join(storage, r.Name)
	if _, err := os.Stat(vdir); err == nil {
		return fmt.Errorf("%s already exists", vdir)
	}
	if err := os.MkdirAll(vdir, 0o750); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(vdir)
		}
	}()
	if err := grantQemu(vdir, true); err != nil {
		return err
	}
	o := cloneOpts{Name: r.Name, OldDir: man.Dir, NewDir: vdir, Disks: map[string]string{}, MAC: randomMAC, KeepUSB: true, KeepIdentity: keep}
	for i, d := range man.Disks {
		if !safeBase(d.File) {
			return fmt.Errorf("invalid backup manifest")
		}
		dst := filepath.Join(vdir, fmt.Sprintf("disk%d.qcow2", i))
		p.log("Restoring disk %s", d.Target)
		if err := convertImage(ctx, filepath.Join(dir, d.File), "qcow2", dst, false, p.scaled(float64(i)*95/float64(len(man.Disks)), float64(i+1)*95/float64(len(man.Disks)))); err != nil {
			return err
		}
		o.Disks[d.Source] = dst
	}
	if man.NVRAM {
		o.NVRAM = nvramPath(r.Name)
		if err := copyFile(filepath.Join(dir, "nvram.fd"), o.NVRAM, 0o600); err != nil {
			return err
		}
	}
	if !keep {
		// A new identity: cloud-init runs again (host name, SSH host keys).
		if seed, err := cloneSeed(dir, vdir, r.Name); err == nil {
			o.Seed = seed
		}
	}
	out, err := rewriteClone(string(doc), o)
	if err != nil {
		return err
	}
	if err := defineXML(out); err != nil {
		if o.NVRAM != "" {
			_ = os.Remove(o.NVRAM)
		}
		return err
	}
	ok = true
	p.set(100)
	if r.Start {
		return Action(r.Name, "start")
	}
	return nil
}
