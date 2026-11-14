package sysdisk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/storage"
	"strataos/internal/sys"
)

// System backup: an archive of the whole system (root file system, StrataOS
// settings, separate /boot) on a data volume, which the installer's "Restore
// a StrataOS system backup" entry puts back on a new disk. Layout on the
// volume (restore.sh of the ISO looks for it):
//
//	<folder>/strataos-system-backup/<host>-<date>/system.tar.zst
//	                                              manifest.json

// BackupDir is the folder name the restore looks for.
const BackupDir = "strataos-system-backup"

// Manifest describes a system backup.
type Manifest struct {
	Format   int       `json:"format"` // 1
	Version  string    `json:"version"`
	Hostname string    `json:"hostname"`
	Created  time.Time `json:"created"`
	Arch     string    `json:"arch"`
	Boot     string    `json:"boot"`
	FSType   string    `json:"fsType"`
	Kernel   string    `json:"kernel,omitempty"`
	Archive  string    `json:"archive"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256"`
	// Mounts: the file systems the archive holds besides "/".
	Mounts []string `json:"mounts,omitempty"`
}

const backupRoot = work + "/backup"

// CheckBackupFolder refuses a destination on the system disk or outside the
// data volumes.
func CheckBackupFolder(l Layout, folder string) (string, error) {
	folder = filepath.Clean(folder)
	if !strings.HasPrefix(folder, storage.MountRoot+"/") || strings.Contains(folder, "..") {
		return "", fmt.Errorf("choose a folder of a data volume (under %s)", storage.MountRoot)
	}
	real, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return "", fmt.Errorf("folder %s not found", folder)
	}
	if !strings.HasPrefix(real, storage.MountRoot+"/") {
		return "", errors.New("the folder must be on a data volume")
	}
	out, err := sys.Run("findmnt", "-n", "-o", "SOURCE,TARGET", "-T", real)
	if err != nil {
		return "", err
	}
	f := strings.Fields(out)
	if len(f) < 2 || f[1] == "/" || !strings.HasPrefix(f[1], storage.MountRoot+"/") {
		return "", errors.New("this folder is on the system disk: choose a data volume, so that the backup survives the loss of the system disk")
	}
	src := f[0]
	if i := strings.Index(src, "["); i > 0 {
		src = src[:i]
	}
	if strings.HasPrefix(src, "/dev/") {
		for _, c := range holders(mustTree(), resolveDev(src)) {
			if l.IsSystemDisk(c[0].Path) {
				return "", errors.New("this volume is on the system disk: choose a volume on another disk")
			}
		}
	}
	return real, nil
}

func mustTree() []Node { t, _ := Tree(); return t }

func resolveDev(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// Backup writes a system backup into folder (a data volume).
func Backup(ctx context.Context, logf func(string, ...any), l Layout, folder, version string) (Manifest, string, error) {
	m := Manifest{Format: 1, Version: version, Created: time.Now().UTC(), Arch: l.Arch, Boot: l.Boot, FSType: l.Root.FSType}
	m.Hostname, _ = os.Hostname()
	if k, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		m.Kernel = strings.TrimSpace(string(k))
	}
	if len(l.Problems) > 0 && l.RootOn != "md" {
		for _, p := range l.Problems {
			if strings.Contains(p, "LVM") || strings.Contains(p, "encrypted") || strings.Contains(p, "Raspberry") {
				return m, "", errors.New(p)
			}
		}
	}
	r := &Run{Ctx: ctx, Logf: logf}
	dir := filepath.Join(folder, BackupDir, fmt.Sprintf("%s-%s", safeName(m.Hostname), time.Now().Format("2006-01-02_15-04-05")))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return m, "", err
	}
	// The archive holds every secret of the NAS (password hashes, keys, the
	// StrataOS state) and sits in a shared folder: root only, without the
	// default ACLs the share would pass down.
	for _, d := range []string{filepath.Join(folder, BackupDir), dir} {
		_ = os.Chmod(d, 0o700)
		if sys.Has("setfacl") {
			if _, err := sys.Run("setfacl", "-b", d); err != nil {
				return m, "", fmt.Errorf("could not restrict the access to %s: %v", d, err)
			}
		}
	}
	_ = os.MkdirAll(backupRoot, 0o700)
	if mountSource(backupRoot) != "" {
		_, _ = r.run("umount", "-R", backupRoot)
	}
	var cleanup []func()
	defer func() {
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
	}()
	extra := []string{}
	if l.Root.FSType == "btrfs" {
		// Read-only snapshots: the archive is the system at one moment.
		top := work + "/backup-top"
		_ = os.MkdirAll(top, 0o700)
		if _, err := r.run("mount", "-t", "btrfs", "-o", "subvolid=5", l.Root.Source, top); err != nil {
			return m, "", err
		}
		cleanup = append(cleanup, func() { _, _ = r.run("umount", top) })
		stamp := time.Now().Format("20060102150405")
		snap := func(sub string) (string, error) {
			name := filepath.Join(top, ".strata-backup-"+stamp+"-"+strings.ReplaceAll(sub, "/", "_"))
			if _, err := r.run("btrfs", "subvolume", "snapshot", "-r", filepath.Join(top, sub), name); err != nil {
				return "", err
			}
			cleanup = append(cleanup, func() { _, _ = r.run("btrfs", "subvolume", "delete", name) })
			return name, nil
		}
		s, err := snap(l.Root.Subvol)
		if err != nil {
			return m, "", err
		}
		if _, err := r.run("mount", "--bind", "-o", "ro", s, backupRoot); err != nil {
			return m, "", err
		}
		cleanup = append(cleanup, func() { _, _ = r.run("umount", "-R", backupRoot) })
		for _, x := range l.Mounts {
			if x.Role != "subvol" {
				continue
			}
			s, err := snap(x.Subvol)
			if err != nil {
				return m, "", err
			}
			if _, err := r.run("mount", "--bind", "-o", "ro", s, filepath.Join(backupRoot, x.Target)); err != nil {
				return m, "", err
			}
			extra = append(extra, "."+x.Target)
		}
	} else {
		if _, err := r.run("mount", "--bind", "-o", "ro", "/", backupRoot); err != nil {
			return m, "", err
		}
		cleanup = append(cleanup, func() { _, _ = r.run("umount", "-R", backupRoot) })
	}
	for _, x := range l.Mounts {
		if x.Role == "merge" {
			if _, err := r.run("mount", "--bind", "-o", "ro", x.Target, filepath.Join(backupRoot, x.Target)); err != nil {
				return m, "", err
			}
			extra = append(extra, "."+x.Target)
		}
	}
	for _, e := range extra {
		m.Mounts = append(m.Mounts, strings.TrimPrefix(e, "."))
	}
	comp, ext := "--gzip", "tar.gz"
	if sys.Has("zstd") {
		comp, ext = "--zstd", "tar.zst"
	}
	m.Archive = "system." + ext
	archive := filepath.Join(dir, m.Archive)
	args := []string{"--create", "--file", archive, comp, "--one-file-system", "--numeric-owner", "--acls", "--xattrs", "--xattrs-include=*", "--sparse",
		"-C", backupRoot, "--exclude=./var/cache/apt/archives/*.deb", "--exclude=./run/*", "--exclude=./tmp/*"}
	for _, f := range l.SwapFiles {
		args = append(args, "--exclude=."+f)
	}
	args = append(args, ".")
	args = append(args, extra...)
	logf("$ tar %s", strings.Join(args, " "))
	if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: -1, AllowCodes: []int{1}}, "tar", args...); err != nil {
		logf("  failed: %v", err)
		_ = os.RemoveAll(dir)
		return m, "", err
	}
	_ = os.Chmod(archive, 0o600)
	out, err := r.cmd(-1, "sha256sum", archive)
	if err != nil {
		_ = os.RemoveAll(dir)
		return m, "", err
	}
	m.SHA256 = strings.Fields(out)[0]
	if fi, err := os.Stat(archive); err == nil {
		m.Size = fi.Size()
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o600); err != nil {
		return m, "", err
	}
	logf("System backup written to %s (%s)", dir, human(m.Size))
	return m, dir, nil
}

func safeName(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 {
		return "strataos"
	}
	return b.String()
}
