package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"strataos/internal/sys"
)

// MountRoot is where StrataOS mounts every data volume.
const MountRoot = "/srv/strata"

type Usage struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
	Free  int64 `json:"free"`
}

func StatFS(path string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	total := int64(st.Blocks) * st.Bsize
	free := int64(st.Bavail) * st.Bsize
	used := total - int64(st.Bfree)*st.Bsize
	return Usage{Total: total, Used: used, Free: free}, nil
}

// IsMountpoint reports whether path is currently a mount point.
func IsMountpoint(path string) bool {
	data, _ := os.ReadFile("/proc/self/mounts")
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && unescapeMount(f[1]) == path {
			return true
		}
	}
	return false
}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(s)
}

type CreateFSReq struct {
	Name        string   `json:"name"`
	FSType      string   `json:"fsType"` // ext4 | xfs | btrfs
	Devices     []string `json:"devices"`
	DataProfile string   `json:"dataProfile"` // btrfs: single raid0 raid1 raid1c3 raid1c4 raid10 raid5 raid6
	MetaProfile string   `json:"metaProfile"`
	Compression bool     `json:"compression"`
}

var btrfsProfiles = map[string]int{"single": 1, "dup": 1, "raid0": 2, "raid1": 2, "raid1c3": 3, "raid1c4": 4, "raid10": 4, "raid5": 2, "raid6": 3}

// CreateFilesystem formats devices and mounts the result under MountRoot/<name>.
func CreateFilesystem(ctx context.Context, r CreateFSReq, logf func(string, ...any)) (uuid, mountpoint string, err error) {
	if err := ValidName(r.Name); err != nil {
		return "", "", err
	}
	mountpoint = filepath.Join(MountRoot, r.Name)
	if _, err := os.Stat(mountpoint); err == nil && IsMountpoint(mountpoint) {
		return "", "", fmt.Errorf("a volume is already mounted at %s", mountpoint)
	}
	if len(r.Devices) == 0 {
		return "", "", fmt.Errorf("no device selected")
	}
	if r.FSType != "btrfs" && len(r.Devices) > 1 {
		return "", "", fmt.Errorf("%s supports a single device; use RAID, LVM or btrfs for several disks", r.FSType)
	}
	if err := ValidateFreeDisks(r.Devices, false); err != nil {
		return "", "", err
	}
	for _, d := range r.Devices {
		if fs, _, _ := Probe(d); fs != "" {
			return "", "", fmt.Errorf("%s already contains a %s signature; wipe it first", d, fs)
		}
		if mounted(resolve(d)) {
			return "", "", fmt.Errorf("%s is mounted", d)
		}
	}
	Lock.Lock()
	defer Lock.Unlock()
	var args []string
	var cmd string
	switch r.FSType {
	case "ext4":
		cmd = "mkfs.ext4"
		// User quotas are built in from the start (ext4 "quota" feature).
		args = []string{"-F", "-m", "1", "-L", trunc(r.Name, 16), "-O", "quota", "-E", "lazy_itable_init=1,lazy_journal_init=1,quotatype=usrquota", r.Devices[0]}
	case "xfs":
		cmd = "mkfs.xfs"
		args = []string{"-f", "-L", trunc(r.Name, 12), r.Devices[0]}
	case "btrfs":
		cmd = "mkfs.btrfs"
		args = []string{"-f", "-L", r.Name}
		dp := r.DataProfile
		if dp == "" {
			dp = "single"
			if len(r.Devices) > 1 {
				dp = "raid1"
			}
		}
		mp := r.MetaProfile
		if mp == "" {
			mp = dp
			if len(r.Devices) == 1 {
				mp = "dup"
			}
			if dp == "raid5" || dp == "raid6" || dp == "raid0" {
				mp = "raid1" // never put metadata on parity/striped profiles
				if len(r.Devices) >= 3 && dp == "raid6" {
					mp = "raid1c3"
				}
			}
		}
		for _, p := range []string{dp, mp} {
			min, ok := btrfsProfiles[p]
			if !ok {
				return "", "", fmt.Errorf("unknown btrfs profile %q", p)
			}
			if len(r.Devices) < min {
				return "", "", fmt.Errorf("btrfs profile %s needs %d devices", p, min)
			}
		}
		args = append(args, "-d", dp, "-m", mp)
		args = append(args, r.Devices...)
	default:
		return "", "", fmt.Errorf("unsupported filesystem %q", r.FSType)
	}
	logf("%s %s", cmd, strings.Join(args, " "))
	if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: -1}, cmd, args...); err != nil {
		return "", "", err
	}
	_, _ = sys.Run("udevadm", "settle")
	_, uuid, _ = Probe(r.Devices[0])
	if uuid == "" {
		return "", "", fmt.Errorf("could not read the UUID of the new filesystem")
	}
	opts := "defaults,noatime,nofail,x-systemd.device-timeout=30s"
	if r.FSType == "btrfs" && r.Compression {
		opts += ",compress=zstd:1"
	}
	switch r.FSType {
	case "xfs":
		opts += ",uquota"
	case "ext4":
		// The ext4 quota feature only tracks usage; this option makes limits enforced.
		opts += ",usrquota"
	}
	if err := mountAndPersist(uuid, mountpoint, r.FSType, opts); err != nil {
		return "", "", err
	}
	if r.FSType == "ext4" {
		_, _ = sys.Run("quotaon", "-u", mountpoint)
	}
	logf("Mounted at %s", mountpoint)
	return uuid, mountpoint, nil
}

// MountExisting mounts an already formatted device as a volume.
func MountExisting(name, dev string) (fstype, uuid, mountpoint string, err error) {
	if err := ValidName(name); err != nil {
		return "", "", "", err
	}
	if err := ValidateDevice(dev); err != nil {
		return "", "", "", err
	}
	fstype, uuid, _ = Probe(dev)
	switch fstype {
	case "ext4", "ext3", "ext2", "xfs", "btrfs", "exfat", "vfat", "ntfs", "ntfs3":
	case "":
		return "", "", "", fmt.Errorf("%s contains no filesystem", dev)
	default:
		return "", "", "", fmt.Errorf("filesystem %s cannot be mounted as a volume", fstype)
	}
	if fstype == "ntfs" {
		fstype = "ntfs3"
	}
	mountpoint = filepath.Join(MountRoot, name)
	Lock.Lock()
	defer Lock.Unlock()
	if err := mountAndPersist(uuid, mountpoint, fstype, "defaults,noatime,nofail,x-systemd.device-timeout=30s"); err != nil {
		return "", "", "", err
	}
	return fstype, uuid, mountpoint, nil
}

// fstabUUIDRe matches the UUID forms blkid reports (hex with dashes, or the
// short serials of FAT/exFAT/NTFS). The UUID comes from the device itself, so a
// crafted filesystem must not be able to add fields or lines to /etc/fstab.
var fstabUUIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

func fstabLine(uuid, mountpoint, fstype, opts string) (string, error) {
	if !fstabUUIDRe.MatchString(uuid) {
		return "", fmt.Errorf("unexpected filesystem UUID %q", uuid)
	}
	for _, f := range []string{mountpoint, fstype, opts} {
		if f == "" || strings.ContainsAny(f, " \t\n\r#\\") {
			return "", fmt.Errorf("invalid fstab field %q", f)
		}
	}
	return fmt.Sprintf("UUID=%s %s %s %s 0 %d", uuid, mountpoint, fstype, opts, passno(fstype)), nil
}

func mountAndPersist(uuid, mountpoint, fstype, opts string) error {
	line, err := fstabLine(uuid, mountpoint, fstype, opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		return err
	}
	if err := fstabSet(mountpoint, line); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "daemon-reload")
	if _, err := sys.Run("mount", mountpoint); err != nil {
		return err
	}
	return nil
}

func passno(fstype string) int {
	if strings.HasPrefix(fstype, "ext") {
		return 2
	}
	return 0
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// UnmountVolume unmounts a volume and removes it from fstab.
func UnmountVolume(mountpoint string) error {
	if !strings.HasPrefix(mountpoint, MountRoot+"/") {
		return fmt.Errorf("refusing to unmount %s", mountpoint)
	}
	Lock.Lock()
	defer Lock.Unlock()
	if IsMountpoint(mountpoint) {
		if _, err := sys.Run("umount", mountpoint); err != nil {
			return fmt.Errorf("%w (files may be in use by a container or share)", err)
		}
	}
	if err := fstabSet(mountpoint, ""); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "daemon-reload")
	_ = os.Remove(mountpoint)
	return nil
}

const fstabPath = "/etc/fstab"

// fstabSet replaces (or removes, when line is empty) the entry for mountpoint.
func fstabSet(mountpoint, line string) error {
	data, err := os.ReadFile(fstabPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var out []string
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "# strata:"+mountpoint {
			continue
		}
		f := strings.Fields(l)
		if len(f) > 1 && !strings.HasPrefix(f[0], "#") && f[1] == mountpoint {
			continue
		}
		out = append(out, l)
	}
	if line != "" {
		out = append(out, "# strata:"+mountpoint, line)
	}
	return sys.WriteFileAtomic(fstabPath, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// DeviceOf returns the source device of a mount point.
func DeviceOf(mountpoint string) string {
	out, err := sys.Run("findmnt", "-n", "-o", "SOURCE", "--target", mountpoint)
	if err != nil {
		return ""
	}
	src := strings.TrimSpace(out)
	if i := strings.Index(src, "["); i > 0 { // btrfs subvolume notation
		src = src[:i]
	}
	return src
}
