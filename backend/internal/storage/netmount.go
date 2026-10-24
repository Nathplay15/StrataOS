package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"strataos/internal/sys"
)

// netSourceRe is what a network filesystem source may look like in fstab
// (CephFS: user@fsid.fsname=/path).
var netSourceRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+@[0-9a-f-]{36}\.[A-Za-z0-9_.-]+=/[A-Za-z0-9_./-]*$`)

// PersistNetMount records a network filesystem in fstab, without mounting
// it at boot (noauto): StrataOS mounts it once the network and the cluster
// are there, so a missing cluster never delays the boot.
func PersistNetMount(source, mountpoint, fstype, opts string) error {
	if !netSourceRe.MatchString(source) {
		return fmt.Errorf("invalid network filesystem source %q", source)
	}
	if !strings.HasPrefix(mountpoint, MountRoot+"/") || filepath.Clean(mountpoint) != mountpoint {
		return fmt.Errorf("invalid mount point %s", mountpoint)
	}
	for _, f := range []string{fstype, opts} {
		if f == "" || strings.ContainsAny(f, " \t\n\r#\\") {
			return fmt.Errorf("invalid fstab field %q", f)
		}
	}
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	if err := fstabSet(mountpoint, fmt.Sprintf("%s %s %s %s 0 0", source, mountpoint, fstype, opts)); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "daemon-reload")
	return nil
}

// MountPoint mounts the fstab entry of mountpoint.
func MountPoint(mountpoint string) error {
	if !strings.HasPrefix(mountpoint, MountRoot+"/") {
		return fmt.Errorf("refusing to mount %s", mountpoint)
	}
	if IsMountpoint(mountpoint) {
		return nil
	}
	_, err := sys.Run("mount", mountpoint)
	return err
}
