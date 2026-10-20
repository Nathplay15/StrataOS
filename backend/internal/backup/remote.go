package backup

// Helpers shared by the tasks that talk to another NAS: host keys, temporary
// credential files, and the snapshots that keep versions of what was copied.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strataos/internal/scheduler"
	"strataos/internal/storage"
	"strataos/internal/sys"
)

// SecretDir holds credential files while a task runs (root only, on tmpfs).
var SecretDir = "/run/strata/backup"

var (
	bwRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[KMG]?$`)
	// Sub-volumes whose path can name a snapshot (otherwise the volume is snapshotted).
	subNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-/]{0,90}$`)
)

// KnownHost records the server's SSH key the first time it is used (like
// StrictHostKeyChecking=accept-new) and returns the known_hosts file.
func KnownHost(ctx context.Context, host, port string) (string, error) {
	kh := knownHostsPath()
	if port == "" || port == "0" {
		port = "22"
	}
	name := host
	if port != "22" {
		name = "[" + host + "]:" + port
	}
	if out, _ := sys.Run("ssh-keygen", "-F", name, "-f", kh); strings.TrimSpace(out) != "" {
		return kh, nil
	}
	if strings.HasPrefix(host, "-") {
		return "", fmt.Errorf("invalid host")
	}
	keys, err := sys.RunOpts(ctx, sys.Opts{Timeout: 20 * time.Second}, "ssh-keyscan", "-p", port, "-T", "10", host)
	if err != nil || strings.TrimSpace(keys) == "" {
		return "", fmt.Errorf("cannot reach the SSH server %s:%s", host, port)
	}
	if err := os.MkdirAll(KeyDir, 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(kh, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.WriteString(keys)
	return kh, err
}

// secretFile writes content to a new root-only file and returns its path and
// a function removing it. Credentials go through such files, never argv.
func secretFile(content string) (string, func(), error) {
	if err := os.MkdirAll(SecretDir, 0o700); err != nil {
		return "", nil, err
	}
	_ = os.Chmod(SecretDir, 0o700)
	f, err := os.CreateTemp(SecretDir, "cred-")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return name, cleanup, nil
}

// mountInfo returns the mount point, file system type and source (the
// dataset name on ZFS) holding p.
func mountInfo(p string) (target, fstype, source string) {
	out, err := sys.Run("findmnt", "-n", "-o", "TARGET,FSTYPE,SOURCE", "-T", p)
	if err != nil {
		return "", "", ""
	}
	f := strings.Fields(strings.TrimSpace(strings.Split(out, "\n")[0]))
	if len(f) < 3 {
		return "", "", ""
	}
	unesc := func(s string) string { return strings.ReplaceAll(s, `\x20`, " ") }
	return unesc(f[0]), f[1], unesc(f[2])
}

// isSubvolume reports whether dir is the root of a Btrfs sub-volume (inode 256).
func isSubvolume(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Ino == 256
}

// prepareVersionedDir creates a missing destination folder as its own Btrfs
// sub-volume or ZFS dataset, so that its snapshots hold only the backup.
func prepareVersionedDir(dir string, p Progress) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	target, fstype, source := mountInfo(parent)
	switch {
	case fstype == "btrfs" && strings.HasPrefix(target, storage.MountRoot+"/"):
		if _, err := sys.Run("btrfs", "subvolume", "create", dir); err == nil {
			p.Log("Created the Btrfs sub-volume %s", dir)
			return nil
		}
	case fstype == "zfs" && target == parent:
		ds := source + "/" + filepath.Base(dir)
		if storage.ValidDS(ds) == nil {
			if _, err := sys.Run("zfs", "create", ds); err == nil {
				p.Log("Created the ZFS dataset %s", ds)
				return nil
			}
		}
	}
	return os.MkdirAll(dir, 0o755)
}

// snapshotFolder takes a read-only snapshot of the Btrfs sub-volume (or the
// whole volume) or ZFS dataset holding dir, with retention on the snapshots
// of tag. On other file systems it does nothing and says so.
func snapshotFolder(dir, tag string, keepLast, keepDays int, p Progress) (string, error) {
	target, fstype, source := mountInfo(dir)
	if !strings.HasPrefix(target, storage.MountRoot+"/") {
		return "", nil
	}
	switch fstype {
	case "btrfs":
		sub := ""
		if rel, err := filepath.Rel(target, dir); err == nil && dir != target && isSubvolume(dir) && subNameRe.MatchString(rel) {
			sub = rel
		}
		return scheduler.SnapshotBtrfs(target, sub, tag, keepLast, keepDays, 0, p.Log)
	case "zfs":
		if storage.ValidDS(source) != nil {
			return "", fmt.Errorf("cannot find the ZFS dataset of %s", dir)
		}
		return scheduler.SnapshotZFS(source, tag, false, keepLast, keepDays, 0, p.Log)
	}
	p.Log("No snapshot: %s is on %s (versions need Btrfs or ZFS)", dir, fstype)
	return "", nil
}

func portOr(port, def int) string {
	if port == 0 {
		port = def
	}
	return strconv.Itoa(port)
}

// sshHost puts an IPv6 address between brackets for rsync and rclone host specs.
func sshHost(h string) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		return "[" + h + "]"
	}
	return h
}
