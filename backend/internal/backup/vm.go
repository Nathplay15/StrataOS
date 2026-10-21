package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/vms"
)

// Virtual machines in backup tasks. Each VM listed in t.VMs is exported
// (vms.ExportVM: definition, flattened disks, NVRAM) into a folder named after
// it, under "vms/" at the destination:
//   - rsync to a local folder: written in place (DestPath/vms/<vm>), the
//     previous copy replaced only once the new one is complete;
//   - rsync over SSH and restic: exported into a staging folder on the VM's
//     volume (.strata-vmbackup/<task>/vms), then sent like any other folder.

func validateVMs(t *store.BackupTask) error {
	if len(t.VMs) == 0 {
		t.VMMode = ""
		return nil
	}
	if t.Kind == "zfs" {
		return fmt.Errorf("ZFS replication cannot include virtual machines")
	}
	seen := map[string]bool{}
	for _, n := range t.VMs {
		if err := vms.ValidName(n); err != nil {
			return err
		}
		if seen[n] {
			return fmt.Errorf("%s is listed twice", n)
		}
		seen[n] = true
	}
	if !vms.ValidBackupMode(t.VMMode) {
		return fmt.Errorf("unknown VM backup mode %q", t.VMMode)
	}
	for _, s := range t.Sources {
		if t.Kind == "rsync" && filepath.Base(s) == "vms" {
			return fmt.Errorf("a source folder named \"vms\" would overwrite the VM backups")
		}
	}
	return nil
}

// stagingRoot is the folder VMs are exported to before being sent away.
func stagingRoot(t *store.BackupTask) (string, error) {
	for _, n := range t.VMs {
		if vm, err := vms.Get(n); err == nil && vm.Dir != "" {
			return filepath.Join(vms.VolumeRoot(vm.Dir), ".strata-vmbackup", t.ID, "vms"), nil
		}
	}
	return "", fmt.Errorf("none of the VMs of this backup has its disks in a volume")
}

func runWithVMs(ctx context.Context, t *store.BackupTask, p Progress) (string, error) {
	start := time.Now()
	vp := vms.Progress{Log: p.Log}
	share := 100.0
	if len(t.Sources) > 0 {
		share = 50
	}
	export := func(root string) error {
		for i, n := range t.VMs {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			from, to := share*float64(i)/float64(len(t.VMs)), share*float64(i+1)/float64(len(t.VMs))
			vp.Set = func(v float64) { p.Set(from + (to-from)*v/100) }
			p.Log("Backing up virtual machine %s", n)
			final := filepath.Join(root, n)
			part := final + ".partial"
			_ = os.RemoveAll(part)
			man, err := vms.ExportVM(ctx, n, part, t.VMMode, vp)
			if err != nil {
				_ = os.RemoveAll(part)
				return fmt.Errorf("VM %s: %w", n, err)
			}
			old := final + ".old"
			_ = os.RemoveAll(old)
			if _, err := os.Stat(final); err == nil {
				if err := os.Rename(final, old); err != nil {
					return err
				}
			}
			if err := os.Rename(part, final); err != nil {
				return err
			}
			_ = os.RemoveAll(old)
			p.Log("VM %s: %s copied (%s)", n, humanBytes(man.Size), man.Consistency)
		}
		return nil
	}
	sub := func() Progress {
		return Progress{Log: p.Log, Set: func(v float64) { p.Set(share + (100-share)*v/100) }}
	}
	var msg string
	var err error
	switch {
	case t.Kind == "rsync" && t.DestType == "local":
		if err = export(filepath.Join(t.DestPath, "vms")); err != nil {
			return "", err
		}
		if len(t.Sources) > 0 {
			if msg, err = runRsync(ctx, t, sub()); err != nil {
				return "", err
			}
		}
	case t.Kind == "rsync" || t.Kind == "restic":
		root, serr := stagingRoot(t)
		if serr != nil {
			return "", serr
		}
		defer os.RemoveAll(filepath.Dir(root))
		if err = export(root); err != nil {
			return "", err
		}
		c := *t
		c.Sources = append(append([]string(nil), t.Sources...), root)
		share = 100 - (100-share)/2
		if c.Kind == "rsync" {
			msg, err = runRsync(ctx, &c, sub())
		} else {
			msg, err = runRestic(ctx, &c, sub())
		}
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("virtual machines cannot be backed up with this backup type")
	}
	out := fmt.Sprintf("%d VM(s) backed up in %s", len(t.VMs), time.Since(start).Round(time.Second))
	if msg != "" {
		out += "; " + msg
	}
	return out, nil
}

// VMBackup is a VM backup found at a destination.
type VMBackup struct {
	Task     string    `json:"task"`
	TaskName string    `json:"taskName"`
	VM       string    `json:"vm"`
	Created  time.Time `json:"created"`
	Size     int64     `json:"size"`
	Mode     string    `json:"consistency"`
	Snapshot string    `json:"snapshot,omitempty"` // restic snapshot holding it
}

// VMBackups lists the VM backups of a task: the folders of a local rsync
// destination, or the VMs of the last restic snapshot.
func VMBackups(ctx context.Context, t store.BackupTask) ([]VMBackup, error) {
	out := []VMBackup{}
	switch {
	case t.Kind == "rsync" && t.DestType == "local":
		entries, _ := os.ReadDir(filepath.Join(t.DestPath, "vms"))
		for _, e := range entries {
			if !e.IsDir() || vms.ValidName(e.Name()) != nil || strings.HasSuffix(e.Name(), ".partial") || strings.HasSuffix(e.Name(), ".old") {
				continue
			}
			m, err := vms.ReadBackup(filepath.Join(t.DestPath, "vms", e.Name()))
			if err != nil {
				continue
			}
			out = append(out, VMBackup{Task: t.ID, TaskName: t.Name, VM: e.Name(), Created: m.Created, Size: m.Size, Mode: m.Consistency})
		}
	case t.Kind == "restic":
		snaps, err := Snapshots(ctx, t)
		if err != nil {
			return nil, err
		}
		for _, s := range snaps {
			if resticVMRoot(s.Paths, t.ID) == "" {
				continue
			}
			for _, n := range t.VMs {
				out = append(out, VMBackup{Task: t.ID, TaskName: t.Name, VM: n, Created: s.Time, Snapshot: s.ID})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// resticVMRoot finds the staging folder among the paths of a restic snapshot.
func resticVMRoot(paths []string, id string) string {
	for _, p := range paths {
		if strings.HasSuffix(p, "/.strata-vmbackup/"+id+"/vms") {
			return p
		}
	}
	return ""
}

// RestoreVM recreates a VM from a task's backup (local folder or restic).
func RestoreVM(ctx context.Context, t store.BackupTask, vm, snapshot string, r vms.RestoreReq, p Progress) (string, error) {
	if err := vms.ValidName(vm); err != nil {
		return "", err
	}
	vp := vms.Progress{Set: p.Set, Log: p.Log}
	switch {
	case t.Kind == "rsync" && t.DestType == "local":
		if err := vms.RestoreVM(ctx, filepath.Join(t.DestPath, "vms", vm), r, vp); err != nil {
			return "", err
		}
	case t.Kind == "restic":
		snaps, err := Snapshots(ctx, t)
		if err != nil {
			return "", err
		}
		root := ""
		for _, s := range snaps {
			if s.ID == snapshot || s.ShortID == snapshot {
				root, snapshot = resticVMRoot(s.Paths, t.ID), s.ID
			}
		}
		if root == "" {
			return "", fmt.Errorf("this snapshot holds no virtual machine")
		}
		storage := filepath.Clean(r.Storage)
		tmp := filepath.Join(vms.VolumeRoot(storage), ".strata-vmrestore-"+short(snapshot))
		_ = os.RemoveAll(tmp)
		defer os.RemoveAll(tmp)
		if _, err := Restore(ctx, t, snapshot, root+"/"+vm, tmp, Progress{Log: p.Log, Set: func(v float64) { p.Set(v / 2) }}); err != nil {
			return "", err
		}
		vp.Set = func(v float64) { p.Set(50 + v/2) }
		if err := vms.RestoreVM(ctx, filepath.Join(tmp, root, vm), r, vp); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("restore VMs from a local folder or a restic repository (copy the folder back from the server first)")
	}
	return "VM " + r.Name + " restored", nil
}
