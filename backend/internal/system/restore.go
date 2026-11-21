package system

// Restore points of the operating system: when "/" is a Btrfs subvolume (as
// installed from the StrataOS ISO), a read-only snapshot of it is taken
// before updates and can be put back (after a reboot). StrataOS settings
// (/var/lib/strata, subvolume @strata) and the data volumes are not part of
// it.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"strataos/internal/sys"
)

var (
	topMount = "/run/strata/btrfs-top"
	snapDir  = "@snapshots"
	keep     = 6
)

// RootFS describes the file system of "/".
type RootFS struct {
	FSType string `json:"fsType"`
	Device string `json:"device"`
	Subvol string `json:"subvol"` // e.g. @rootfs (Btrfs only)
}

// Root reads /proc/self/mountinfo.
func Root() RootFS {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return RootFS{}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// 25 1 0:23 /@rootfs / rw,relatime shared:1 - btrfs /dev/sda2 rw,...
		l := strings.Fields(sc.Text())
		if len(l) < 10 || l[4] != "/" {
			continue
		}
		sep := 0
		for i, x := range l {
			if x == "-" {
				sep = i
				break
			}
		}
		if sep == 0 || sep+2 >= len(l) {
			continue
		}
		r := RootFS{FSType: l[sep+1], Device: l[sep+2]}
		if r.FSType == "btrfs" {
			r.Subvol = strings.TrimPrefix(l[3], "/")
		}
		return r
	}
	return RootFS{}
}

// btrfsSupported tells whether restore points can be made, and why not.
func btrfsSupported() (bool, string) {
	r := Root()
	if r.FSType != "btrfs" {
		return false, fmt.Sprintf("the system is installed on %s: restore points need a Btrfs system (install from the StrataOS ISO)", r.FSType)
	}
	if r.Subvol == "" || strings.Contains(r.Subvol, "/") {
		return false, "the system is not on a top-level Btrfs subvolume"
	}
	return true, ""
}

// mountTop mounts the top level of the system file system.
func mountTop() (string, func(), error) {
	r := Root()
	if err := os.MkdirAll(topMount, 0o700); err != nil {
		return "", nil, err
	}
	if out, _ := sys.Run("findmnt", "-n", "-o", "TARGET", topMount); strings.TrimSpace(out) == "" {
		if _, err := sys.Run("mount", "-t", "btrfs", "-o", "subvolid=5", r.Device, topMount); err != nil {
			return "", nil, err
		}
	}
	return topMount, func() { _, _ = sys.Run("umount", topMount) }, nil
}

// RestorePoint is a snapshot of the system.
type RestorePoint struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Label   string    `json:"label"`
	Info    string    `json:"info,omitempty"`
	Invalid bool      `json:"invalid,omitempty"`
}

var pointRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})_([a-z0-9-]{1,40})$`)

// btrfsList returns the restore points, newest first.
func btrfsList() ([]RestorePoint, error) {
	if ok, why := btrfsSupported(); !ok {
		return nil, fmt.Errorf("%s", why)
	}
	top, done, err := mountTop()
	if err != nil {
		return nil, err
	}
	defer done()
	entries, _ := os.ReadDir(filepath.Join(top, snapDir))
	out := []RestorePoint{}
	for _, e := range entries {
		m := pointRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		t, _ := time.ParseInLocation("2006-01-02_15-04-05", m[1], time.Local)
		out = append(out, RestorePoint{Name: e.Name(), Created: t, Label: m[2]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// btrfsCreate snapshots the system (label: before-update, manual…)
// and keeps the most recent ones.
func btrfsCreate(label string) (string, error) {
	if ok, why := btrfsSupported(); !ok {
		return "", fmt.Errorf("%s", why)
	}
	if !regexp.MustCompile(`^[a-z0-9-]{1,40}$`).MatchString(label) {
		return "", fmt.Errorf("invalid label")
	}
	top, done, err := mountTop()
	if err != nil {
		return "", err
	}
	defer done()
	if err := os.MkdirAll(filepath.Join(top, snapDir), 0o700); err != nil {
		return "", err
	}
	name := time.Now().Format("2006-01-02_15-04-05") + "_" + label
	if _, err := sys.Run("btrfs", "subvolume", "snapshot", "-r", filepath.Join(top, Root().Subvol), filepath.Join(top, snapDir, name)); err != nil {
		return "", err
	}
	// Retention: the newest restore points only.
	entries, _ := os.ReadDir(filepath.Join(top, snapDir))
	var names []string
	for _, e := range entries {
		if pointRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, n := range names[min(keep, len(names)):] {
		_, _ = sys.Run("btrfs", "subvolume", "delete", filepath.Join(top, snapDir, n))
	}
	return name, nil
}

// btrfsDelete removes one restore point.
func btrfsDelete(name string) error {
	if !pointRe.MatchString(name) {
		return fmt.Errorf("invalid restore point")
	}
	top, done, err := mountTop()
	if err != nil {
		return err
	}
	defer done()
	_, err = sys.Run("btrfs", "subvolume", "delete", filepath.Join(top, snapDir, name))
	return err
}

// btrfsRollback replaces the system subvolume by a writable copy of the restore
// point; the current system is kept as <subvol>.before-<date>. A reboot is
// needed (the running system is unchanged until then).
func btrfsRollback(name string) error {
	if ok, why := btrfsSupported(); !ok {
		return fmt.Errorf("%s", why)
	}
	if !pointRe.MatchString(name) {
		return fmt.Errorf("invalid restore point")
	}
	top, done, err := mountTop()
	if err != nil {
		return err
	}
	defer done()
	src := filepath.Join(top, snapDir, name)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("restore point not found")
	}
	cur := filepath.Join(top, Root().Subvol)
	aside := cur + ".before-" + time.Now().Format("2006-01-02_15-04-05")
	if err := os.Rename(cur, aside); err != nil {
		return err
	}
	if _, err := sys.Run("btrfs", "subvolume", "snapshot", src, cur); err != nil {
		_ = os.Rename(aside, cur) // nothing changed
		return err
	}
	return nil
}

// btrfsSeparate moves /var/lib/strata to the top-level subvolume @strata
// (once, on Btrfs systems) so restore points leave the settings alone.
func btrfsSeparate(dataDir string) error {
	if ok, _ := btrfsSupported(); !ok {
		return nil
	}
	if out, _ := sys.Run("findmnt", "-n", "-o", "SOURCE", dataDir); strings.Contains(out, "@strata") {
		return nil // already done
	}
	top, done, err := mountTop()
	if err != nil {
		return err
	}
	defer done()
	vol := filepath.Join(top, "@strata")
	if _, err := os.Stat(vol); err != nil {
		if _, err := sys.Run("btrfs", "subvolume", "create", vol); err != nil {
			return err
		}
	}
	if _, err := sys.Run("cp", "-a", "--reflink=auto", dataDir+"/.", vol+"/"); err != nil {
		return err
	}
	_ = os.Chmod(vol, 0o700)
	uuid, err := sys.Run("findmnt", "-n", "-o", "UUID", "/")
	if err != nil || strings.TrimSpace(uuid) == "" {
		return fmt.Errorf("cannot read the UUID of the system")
	}
	line := fmt.Sprintf("UUID=%s %s btrfs subvol=@strata,defaults 0 0 # StrataOS settings (outside restore points)\n", strings.TrimSpace(uuid), dataDir)
	fstab, _ := os.ReadFile("/etc/fstab")
	if !strings.Contains(string(fstab), "subvol=@strata") {
		if err := sys.WriteFileAtomic("/etc/fstab", append(fstab, []byte(line)...), 0o644); err != nil {
			return err
		}
	}
	_, _ = sys.Run("systemctl", "daemon-reload")
	// Mount it now over the old folder (whose content was copied).
	_, err = sys.Run("mount", "-t", "btrfs", "-o", "subvol=@strata", "UUID="+strings.TrimSpace(uuid), dataDir)
	return err
}

// ReinstallBootLoader puts back the boot loader of the system subvolume
// after a rollback (btrfs only, before the restart): the restore point holds
// /boot and the GRUB modules of its time, but the EFI system partition and
// the boot sector hold what was installed since, which may not load them
// (after a release upgrade, for example). The GRUB package of the restored
// system is configured again in a chroot, which installs its own core.
func ReinstallBootLoader(logf func(string, ...any)) error {
	if m, _ := Mode(); m != "btrfs" {
		return nil
	}
	top, done, err := mountTop()
	if err != nil {
		return err
	}
	defer done()
	root := filepath.Join(top, Root().Subvol)
	var mounted []string
	defer func() {
		for i := len(mounted) - 1; i >= 0; i-- {
			_, _ = sys.Run("umount", "-l", mounted[i])
		}
	}()
	for _, d := range []string{"/dev", "/dev/pts", "/proc", "/sys", "/sys/firmware/efi/efivars", "/run", "/boot/efi"} {
		if out, _ := sys.Run("findmnt", "-n", "-o", "TARGET", d); strings.TrimSpace(out) != d {
			continue
		}
		dst := filepath.Join(root, d)
		_ = os.MkdirAll(dst, 0o755)
		if _, err := sys.Run("mount", "--bind", d, dst); err != nil {
			return err
		}
		mounted = append(mounted, dst)
	}
	var pkg string
	for _, p := range []string{"grub-efi-amd64", "grub-efi-arm64", "grub-pc"} {
		if out, err := sys.Run("chroot", root, "dpkg-query", "-W", "-f=${Status}", p); err == nil && strings.Contains(out, "ok installed") {
			pkg = p
			break
		}
	}
	if pkg == "" {
		return fmt.Errorf("no GRUB package in the restored system")
	}
	logf("Reinstalling the boot loader of the restored system (%s)", pkg)
	if out, err := sys.RunOpts(context.Background(), sys.Opts{Timeout: 10 * time.Minute, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}, "chroot", root, "dpkg-reconfigure", "-f", "noninteractive", pkg); err != nil {
		return fmt.Errorf("%v: %s", err, lastLines(out, 5))
	}
	return nil
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " | ")
}
