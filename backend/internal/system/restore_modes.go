package system

// Restore points by kind of system disk:
//   - btrfs:    "/" is a Btrfs sub-volume (StrataOS ISO): read-only snapshots;
//   - lvm:      "/" is an LVM logical volume (Debian "guided LVM" install):
//     snapshots merged back at the next start;
//   - packages: any other file system: before an update, the versions of the
//     packages about to change (and of StrataOS) are repackaged with
//     dpkg-repack, and reinstalled on rollback.
// In every case the StrataOS settings (/var/lib/strata) and the data volumes
// are left alone.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Mode returns the kind of restore points of this system, or "" with the reason.
func Mode() (string, string) {
	if ok, _ := btrfsSupported(); ok {
		return "btrfs", ""
	}
	if vg, lv := rootLV(); vg != "" && lv != "" {
		return "lvm", ""
	}
	if sys.Has("dpkg-repack") && sys.Has("dpkg-query") {
		return "packages", ""
	}
	return "", "dpkg-repack is not installed"
}

// RestoreSupported tells whether restore points can be made, and why not.
func RestoreSupported() (bool, string) {
	m, why := Mode()
	return m != "", why
}

func ListRestorePoints() ([]RestorePoint, error) {
	switch m, why := Mode(); m {
	case "btrfs":
		return btrfsList()
	case "lvm":
		return lvmList()
	case "packages":
		return pkgList()
	default:
		return nil, fmt.Errorf("%s", why)
	}
}

func CreateRestorePoint(label string) (string, error) {
	if !regexp.MustCompile(`^[a-z0-9-]{1,40}$`).MatchString(label) {
		return "", fmt.Errorf("invalid label")
	}
	switch m, why := Mode(); m {
	case "btrfs":
		return btrfsCreate(label)
	case "lvm":
		return lvmCreate(label)
	case "packages":
		return pkgCreate(label)
	default:
		return "", fmt.Errorf("%s", why)
	}
}

func DeleteRestorePoint(name string) error {
	if !pointRe.MatchString(name) {
		return fmt.Errorf("invalid restore point")
	}
	switch m, why := Mode(); m {
	case "btrfs":
		return btrfsDelete(name)
	case "lvm":
		vg, _ := rootLV()
		_, err := sys.Run("lvremove", "-f", vg+"/"+lvName(name))
		return err
	case "packages":
		return os.RemoveAll(filepath.Join(PkgPointsDir, name))
	default:
		return fmt.Errorf("%s", why)
	}
}

// RollbackTo prepares the return to a restore point; the caller reboots.
func RollbackTo(name string) error {
	if !pointRe.MatchString(name) {
		return fmt.Errorf("invalid restore point")
	}
	switch m, why := Mode(); m {
	case "btrfs":
		return btrfsRollback(name)
	case "lvm":
		vg, _ := rootLV()
		// The root volume is in use: the merge happens at the next start.
		_, err := sys.Run("lvconvert", "-y", "--merge", vg+"/"+lvName(name))
		return err
	case "packages":
		return pkgRollback(name)
	default:
		return fmt.Errorf("%s", why)
	}
}

// SeparateStrataData keeps the StrataOS settings outside of the restore
// points (once, at installation).
func SeparateStrataData(dataDir string) error {
	switch m, _ := Mode(); m {
	case "btrfs":
		return btrfsSeparate(dataDir)
	case "lvm":
		return lvmSeparate(dataDir)
	}
	return nil // packages: the settings are not part of the points anyway
}

// ---- LVM ----

// rootLV returns the volume group and logical volume holding "/".
func rootLV() (string, string) {
	r := Root()
	if r.Device == "" || !(strings.HasPrefix(r.Device, "/dev/mapper/") || strings.HasPrefix(r.Device, "/dev/dm-")) || !sys.Has("lvs") {
		return "", ""
	}
	out, err := sys.Run("lvs", "--noheadings", "-o", "vg_name,lv_name,segtype", r.Device)
	if err != nil {
		return "", ""
	}
	f := strings.Fields(out)
	if len(f) < 3 || f[2] == "snapshot" {
		return "", ""
	}
	return f[0], f[1]
}

// lvName turns a point name (2006-01-02_15-04-05_label) into an LV name.
func lvName(point string) string { return "strata-rp-" + point }

func lvmList() ([]RestorePoint, error) {
	vg, root := rootLV()
	out, err := sys.Run("lvs", "--noheadings", "--separator", "|", "-o", "lv_name,origin,snap_percent", vg)
	if err != nil {
		return nil, err
	}
	list := []RestorePoint{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(l), "|")
		if len(f) < 3 || f[1] != root || !strings.HasPrefix(f[0], "strata-rp-") {
			continue
		}
		name := strings.TrimPrefix(f[0], "strata-rp-")
		m := pointRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		t, _ := time.ParseInLocation("2006-01-02_15-04-05", m[1], time.Local)
		p := RestorePoint{Name: name, Created: t, Label: m[2]}
		if pct, err := strconv.ParseFloat(strings.TrimSpace(f[2]), 64); err == nil {
			p.Info = fmt.Sprintf("%.0f %%", pct)
			if pct >= 100 {
				p.Invalid = true // the snapshot overflowed: unusable
			}
		}
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	return list, nil
}

func vgFree(vg string) int64 {
	out, _ := sys.Run("vgs", "--noheadings", "--units", "b", "--nosuffix", "-o", "vg_free", vg)
	n, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	return n
}

func lvmCreate(label string) (string, error) {
	vg, root := rootLV()
	free := vgFree(vg)
	// The snapshot only keeps what changes: 15 % of the free space, 1 to 8 GiB.
	size := min(max(free*15/100, 1<<30), 8<<30)
	if free < size+(256<<20) {
		return "", fmt.Errorf("not enough free space in the volume group %s for a restore point (1 GiB needed)", vg)
	}
	name := time.Now().Format("2006-01-02_15-04-05") + "_" + label
	if _, err := sys.Run("lvcreate", "-y", "-s", "-L", fmt.Sprintf("%db", size), "-n", lvName(name), vg+"/"+root); err != nil {
		return "", err
	}
	// LVM snapshots slow writes down: keep the 3 newest.
	if list, err := lvmList(); err == nil {
		for _, p := range list[min(3, len(list)):] {
			_, _ = sys.Run("lvremove", "-f", vg+"/"+lvName(p.Name))
		}
	}
	return name, nil
}

// lvmSeparate moves /var/lib/strata to a logical volume of its own.
func lvmSeparate(dataDir string) error {
	if out, _ := sys.Run("findmnt", "-n", "-o", "SOURCE", dataDir); strings.TrimSpace(out) != "" {
		return nil // already a mount of its own
	}
	vg, _ := rootLV()
	dev := "/dev/" + vg + "/strata"
	if _, err := sys.Run("lvs", vg+"/strata"); err != nil { // not made yet
		if vgFree(vg) < 2<<30 {
			return nil // no room: the settings stay in the root volume
		}
		if _, err := sys.Run("lvcreate", "-y", "-L", "1g", "-n", "strata", vg); err != nil {
			return err
		}
	}
	_, _ = sys.Run("lvchange", "-ay", vg+"/strata")
	if fs, _ := sys.Run("blkid", "-s", "TYPE", "-o", "value", dev); strings.TrimSpace(fs) == "" {
		if _, err := sys.Run("mkfs.ext4", "-q", "-L", "strata", dev); err != nil {
			return err
		}
	}
	tmp, err := os.MkdirTemp("", "strata-data")
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := sys.Run("mount", dev, tmp); err != nil {
		return err
	}
	_, cpErr := sys.Run("cp", "-a", dataDir+"/.", tmp+"/")
	_, _ = sys.Run("umount", tmp)
	if cpErr != nil {
		return cpErr
	}
	uuid, err := sys.Run("blkid", "-s", "UUID", "-o", "value", dev)
	if err != nil || strings.TrimSpace(uuid) == "" {
		return fmt.Errorf("cannot read the UUID of %s", dev)
	}
	line := fmt.Sprintf("UUID=%s %s ext4 defaults 0 2 # StrataOS settings (outside restore points)\n", strings.TrimSpace(uuid), dataDir)
	fstab, _ := os.ReadFile("/etc/fstab")
	if err := sys.WriteFileAtomic("/etc/fstab", append(fstab, []byte(line)...), 0o644); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "daemon-reload")
	_, err = sys.Run("mount", dataDir)
	return err
}

// ---- Packages ----

// PkgPointsDir holds the package restore points (outside of the data volumes).
var PkgPointsDir = "/var/lib/strata-restore"

var upgradeRe = regexp.MustCompile(`(?m)^Inst (\S+) \[([^\]]+)\]`)

func pkgList() ([]RestorePoint, error) {
	ents, _ := os.ReadDir(PkgPointsDir)
	list := []RestorePoint{}
	for _, e := range ents {
		m := pointRe.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		t, _ := time.ParseInLocation("2006-01-02_15-04-05", m[1], time.Local)
		debs, _ := filepath.Glob(filepath.Join(PkgPointsDir, e.Name(), "debs", "*.deb"))
		list = append(list, RestorePoint{Name: e.Name(), Created: t, Label: m[2], Info: fmt.Sprintf("%d paquet(s)", len(debs))})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	return list, nil
}

// pkgCreate keeps the installed versions of the packages an update would
// change, and of StrataOS.
func pkgCreate(label string) (string, error) {
	name := time.Now().Format("2006-01-02_15-04-05") + "_" + label
	dir := filepath.Join(PkgPointsDir, name)
	if err := os.MkdirAll(filepath.Join(dir, "debs"), 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(PkgPointsDir, 0o700)
	if out, err := sys.Run("dpkg-query", "-W", "-f", "${Package}\t${Version}\t${Architecture}\n"); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "packages.list"), []byte(out), 0o600)
	}
	pkgs := map[string]bool{"strataos": true}
	if out, err := sys.Run("apt-get", "-s", "-o", "Debug::NoLocking=1", "dist-upgrade"); err == nil {
		for _, m := range upgradeRe.FindAllStringSubmatch(out, -1) {
			pkgs[m[1]] = true
		}
	}
	var names []string
	for p := range pkgs {
		names = append(names, p)
	}
	sort.Strings(names)
	args := append([]string{"--root=/"}, names...)
	if _, err := sys.RunOpts(context.Background(), sys.Opts{Dir: filepath.Join(dir, "debs"), Timeout: 30 * time.Minute}, "dpkg-repack", args...); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("dpkg-repack: %w", err)
	}
	// Repackaged files take room: keep the 3 newest points.
	if list, err := pkgList(); err == nil {
		for _, p := range list[min(3, len(list)):] {
			_ = os.RemoveAll(filepath.Join(PkgPointsDir, p.Name))
		}
	}
	return name, nil
}

// pkgRollback reinstalls the kept versions (downgrades included) and reboots.
// It runs in a transient systemd unit: reinstalling StrataOS restarts it.
func pkgRollback(name string) error {
	dir := filepath.Join(PkgPointsDir, name)
	debs, _ := filepath.Glob(filepath.Join(dir, "debs", "*.deb"))
	if len(debs) == 0 {
		return fmt.Errorf("restore point not found")
	}
	script := "#!/bin/sh\nexport DEBIAN_FRONTEND=noninteractive\n" +
		"dpkg -i --force-downgrade --force-confold " + filepath.Join(dir, "debs") + "/*.deb\n" +
		"apt-get -y -f install\n" +
		"systemctl reboot\n"
	sh := filepath.Join(dir, "rollback.sh")
	if err := os.WriteFile(sh, []byte(script), 0o700); err != nil {
		return err
	}
	_, err := sys.Run("systemd-run", "--unit=strata-rollback", "--collect", "--quiet", "/bin/sh", sh)
	return err
}

// RebootsItself tells whether RollbackTo reboots on its own.
func RebootsItself() bool { m, _ := Mode(); return m == "packages" }
