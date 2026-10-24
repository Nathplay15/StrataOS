package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"strataos/internal/lxc"
	"strataos/internal/store"
	"strataos/internal/vms"
)

// LXC containers in backup tasks, next to the VMs: each container listed in
// t.Containers is written as lxc/<name>.tar.zst at the destination (local
// rsync), or into the staging folder sent with the other folders (rsync over
// SSH, restic). Restore one with Virtual machines → Create → Container →
// Import (a restic or remote copy is copied back to a volume first).

func validateContainers(t *store.BackupTask) error {
	if len(t.Containers) == 0 {
		return nil
	}
	if t.Kind == "zfs" || t.Kind == "sync" {
		return fmt.Errorf("this backup type cannot include containers")
	}
	seen := map[string]bool{}
	for _, n := range t.Containers {
		if err := lxc.ValidName(n); err != nil {
			return err
		}
		if seen[n] {
			return fmt.Errorf("%s is listed twice", n)
		}
		seen[n] = true
	}
	for _, s := range t.Sources {
		if t.Kind == "rsync" && filepath.Base(s) == "lxc" {
			return fmt.Errorf("a source folder named \"lxc\" would overwrite the container backups")
		}
	}
	return nil
}

// containerVolume is a volume to stage the backups of a task's containers on.
func containerVolume(t *store.BackupTask) string {
	for _, n := range t.Containers {
		if s, err := lxc.Load(n); err == nil {
			if _, err := os.Stat(s.Storage.Location); err == nil {
				return vms.VolumeRoot(s.Storage.Location)
			}
		}
	}
	return ""
}

// stagedFolders are the staging folders that hold something.
func stagedFolders(t *store.BackupTask, root string) []string {
	var out []string
	if len(t.VMs) > 0 {
		out = append(out, root)
	}
	if len(t.Containers) > 0 {
		out = append(out, filepath.Join(filepath.Dir(root), "lxc"))
	}
	return out
}

// exportContainers writes the containers of a task into parent/lxc.
func exportContainers(ctx context.Context, t *store.BackupTask, parent string, p Progress) error {
	if len(t.Containers) == 0 {
		return nil
	}
	dir := filepath.Join(parent, "lxc")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, n := range t.Containers {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.Log("Backing up container %s", n)
		file, size, err := lxc.Export(ctx, n, lxc.ExportOpts{Dir: dir, File: n + ".tar.zst", AnyDir: true}, p.Log)
		if err != nil {
			return fmt.Errorf("container %s: %w", n, err)
		}
		p.Log("Container %s: %s written (%s)", n, filepath.Base(file), humanBytes(size))
	}
	return nil
}
