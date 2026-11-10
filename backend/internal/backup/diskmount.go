package backup

// Single files from a VM disk backup. The disk image of a snapshot is
// written to a sparse file on a volume (restic dump; the repository is only
// read), attached to a read-only loop device with its partitions, and each
// file system it holds is mounted read-only (ext4, XFS, Btrfs, NTFS,
// FAT/exFAT, and the logical volumes of an LVM group inside the guest) under
// /run/strata/diskmount/<id>/<name>. The guided restore then browses these
// mounts like a snapshot. A session closes after 30 idle minutes or 4 hours,
// and at most two are open at a time.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

const (
	MaxDiskSessions = 2
	DiskIdle        = 30 * time.Minute
	DiskMaxLife     = 4 * time.Hour
)

// DiskMountRoot holds the mount points of open disk backups.
var DiskMountRoot = "/run/strata/diskmount"

// diskTempPrefix names the folders holding the disk images, on a volume.
const diskTempPrefix = ".strata-diskmount-"

// MountedFS is a file system of an open disk, as the restore shows it.
type MountedFS struct {
	Path   string `json:"path"` // "/vda1", "/vg0-root"
	FSType string `json:"fsType"`
	Label  string `json:"label,omitempty"`
	Dir    string `json:"-"` // mount point
}

// DiskSession is a disk backup opened for browsing.
type DiskSession struct {
	ID       string      `json:"id"`
	Task     string      `json:"task"`
	Snapshot string      `json:"snapshot"`
	Disk     string      `json:"disk"`
	State    string      `json:"state"` // opening | open | failed
	Error    string      `json:"error,omitempty"`
	Size     int64       `json:"size"`
	Opened   time.Time   `json:"opened"`
	LastUsed time.Time   `json:"lastUsed"`
	Roots    []MountedFS `json:"roots"`
	Skipped  []string    `json:"skipped,omitempty"` // partitions not mounted, and why

	temp  string // folder of the image file, on a volume
	loop  string
	vgs   []lvmGroup
	mount string // DiskMountRoot/<id>
}

type lvmGroup struct {
	name string
	devs []string
}

var disks = struct {
	sync.Mutex
	m map[string]*DiskSession // task/snapshot → session
}{m: map[string]*DiskSession{}}

func diskKey(task, snap string) string { return task + "/" + snap }

// DiskSessionFor returns the session of a disk backup (nil: not open) and
// marks it used.
func DiskSessionFor(task, snap string) *DiskSession {
	disks.Lock()
	defer disks.Unlock()
	s := disks.m[diskKey(task, snap)]
	if s == nil {
		return nil
	}
	s.LastUsed = time.Now()
	c := *s
	c.Roots = slices.Clone(s.Roots)
	return &c
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// diskOf finds the disk snapshot of a task and its target.
func diskOf(ctx context.Context, t *store.BackupTask, snap string) (string, int64, error) {
	if !snapIDRe.MatchString(snap) {
		return "", 0, fmt.Errorf("invalid snapshot id")
	}
	all, err := taskSnapshots(ctx, t)
	if err != nil {
		return "", 0, err
	}
	for _, s := range all {
		if s.ID == snap || s.ShortID == snap {
			part := tagValue(s.Tags, partTag)
			target, ok := strings.CutPrefix(part, "disk-")
			if !ok || !diskTargetRe.MatchString(target) {
				return "", 0, fmt.Errorf("this snapshot is not a VM disk")
			}
			var size int64
			if s.Summary != nil {
				size = s.Summary.TotalBytesProcessed
			}
			return target, size, nil
		}
	}
	return "", 0, fmt.Errorf("disk backup not found")
}

// OpenDisk opens a disk backup of a task (snap: the snapshot of the disk).
// It returns once the file systems are mounted; the session stays open.
func OpenDisk(ctx context.Context, t store.BackupTask, snap string, p Progress) (*DiskSession, error) {
	if t.Datastore == "" {
		return nil, fmt.Errorf("only image backups of a backup repository can be opened")
	}
	target, size, err := diskOf(ctx, &t, snap)
	if err != nil {
		return nil, err
	}
	key := diskKey(t.ID, snap)
	disks.Lock()
	if s := disks.m[key]; s != nil {
		st := s.State
		disks.Unlock()
		if st == "failed" {
			CloseDisk(t.ID, snap)
			return OpenDisk(ctx, t, snap, p)
		}
		return DiskSessionFor(t.ID, snap), nil
	}
	if len(disks.m) >= MaxDiskSessions {
		disks.Unlock()
		return nil, fmt.Errorf("%d disk backups are open already: close one first", MaxDiskSessions)
	}
	s := &DiskSession{ID: newID(), Task: t.ID, Snapshot: snap, Disk: target, State: "opening", Size: size, Opened: time.Now(), LastUsed: time.Now()}
	disks.m[key] = s
	disks.Unlock()
	err = openDisk(ctx, &t, s, p)
	disks.Lock()
	if err != nil {
		s.State, s.Error = "failed", err.Error()
	} else {
		s.State = "open"
	}
	s.LastUsed = time.Now()
	disks.Unlock()
	if err != nil {
		closeSession(s)
		disks.Lock()
		if disks.m[key] == s {
			delete(disks.m, key)
		}
		disks.Unlock()
		return nil, err
	}
	return DiskSessionFor(t.ID, snap), nil
}

func openDisk(ctx context.Context, t *store.BackupTask, s *DiskSession, p Progress) error {
	for _, c := range []string{"losetup", "blkid", "mount"} {
		if !sys.Has(c) {
			return fmt.Errorf("%s is not installed", c)
		}
	}
	vol, err := stagingVolume("")
	if err != nil {
		return err
	}
	if f := freeBytes(vol); f >= 0 && f < 2<<30 {
		return fmt.Errorf("not enough free space on %s", vol)
	}
	s.temp = filepath.Join(vol, diskTempPrefix+s.ID)
	if err := os.Mkdir(s.temp, 0o700); err != nil {
		return err
	}
	img := filepath.Join(s.temp, s.Disk+".raw")
	f, err := os.OpenFile(img, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	p.Log("Reading disk %s from the backup (%s)", s.Disk, humanBytes(s.Size))
	zw := newZeroWriter(f)
	zw.check = func(n int64) error {
		if p.Set != nil && s.Size > 0 {
			p.Set(min(95, float64(n)*95/float64(s.Size)))
		}
		if fb := freeBytes(vol); fb >= 0 && fb < 1<<30 {
			return fmt.Errorf("less than 1 GB left on %s", vol)
		}
		return nil
	}
	derr := dumpTo(ctx, t, s.Snapshot, "/"+s.Disk+".raw", zw)
	n, cerr := zw.Close()
	if derr == nil {
		derr = cerr
	}
	if derr == nil {
		derr = f.Truncate(n) // trailing zeros were skipped
	}
	if cerr := f.Close(); derr == nil {
		derr = cerr
	}
	if derr != nil {
		return derr
	}
	out, err := sys.RunCtx(ctx, "losetup", "--find", "--show", "--read-only", "--partscan", "--", img)
	if err != nil {
		return err
	}
	s.loop = strings.TrimSpace(out)
	if !regexp.MustCompile(`^/dev/loop\d+$`).MatchString(s.loop) {
		return fmt.Errorf("unexpected loop device %q", s.loop)
	}
	if sys.Has("udevadm") {
		_, _ = sys.RunCtx(ctx, "udevadm", "settle", "--timeout=15")
	}
	s.mount = filepath.Join(DiskMountRoot, s.ID)
	if err := os.MkdirAll(s.mount, 0o700); err != nil {
		return err
	}
	var lvmDevs []string
	for _, dev := range partitions(s.loop) {
		kv := probe(ctx, dev)
		name := PartName(s.Disk, s.loop, dev)
		plan := PlanMount(kv)
		switch {
		case plan.LVM:
			lvmDevs = append(lvmDevs, dev)
		case plan.Skip != "":
			s.Skipped = append(s.Skipped, name+": "+plan.Skip)
		default:
			if err := s.mountFS(ctx, dev, name, plan, kv["LABEL"]); err != nil {
				s.Skipped = append(s.Skipped, name+": "+err.Error())
			}
		}
	}
	if len(lvmDevs) > 0 {
		s.openLVM(ctx, lvmDevs)
	}
	if len(s.Roots) == 0 {
		return fmt.Errorf("no file system could be opened on this disk (%s)", strings.Join(s.Skipped, "; "))
	}
	for _, r := range s.Roots {
		p.Log("%s: %s %s", r.Path, r.FSType, r.Label)
	}
	return nil
}

// partitions lists the partitions of a loop device (the device itself
// when it has none: a file system on the whole disk).
func partitions(loop string) []string {
	base := filepath.Base(loop)
	entries, _ := os.ReadDir("/sys/block/" + base)
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base+"p") {
			out = append(out, "/dev/"+e.Name())
		}
	}
	slices.SortFunc(out, func(a, b string) int { // loop0p2 before loop0p10
		if len(a) != len(b) {
			return len(a) - len(b)
		}
		return strings.Compare(a, b)
	})
	if len(out) == 0 {
		out = []string{loop}
	}
	return out
}

// probe reads the signature of a device (low-level, no cache).
func probe(ctx context.Context, dev string) map[string]string {
	out, _ := sys.RunOpts(ctx, sys.Opts{Timeout: 30 * time.Second, AllowCodes: []int{2}}, "blkid", "-p", "-o", "export", "--", dev)
	return ParseBlkid(out)
}

// ParseBlkid reads "blkid -o export" lines (KEY=value).
func ParseBlkid(out string) map[string]string {
	kv := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && k != "" {
			kv[k] = v
		}
	}
	return kv
}

// PartName names a partition for the restore: the guest's disk and the
// partition number (vda1), or the disk alone for a whole-disk file system.
func PartName(disk, loop, dev string) string {
	if dev == loop {
		return disk
	}
	return disk + strings.TrimPrefix(dev, loop+"p")
}

// FSPlan says how to mount a file system found on a guest disk.
type FSPlan struct {
	Type, Opts string
	LVM        bool
	Skip       string // why it is not mounted
}

// PlanMount decides how a probed partition is opened: read-only, without
// replaying any journal (the image must stay as backed up), never with
// devices, set-uid or executables.
func PlanMount(kv map[string]string) FSPlan {
	const safe = "ro,nodev,nosuid,noexec"
	switch t := kv["TYPE"]; t {
	case "ext2", "ext3", "ext4":
		return FSPlan{Type: t, Opts: safe + ",noload"}
	case "xfs":
		return FSPlan{Type: t, Opts: safe + ",norecovery,nouuid"}
	case "btrfs":
		return FSPlan{Type: t, Opts: safe + ",rescue=nologreplay"}
	case "vfat", "exfat":
		return FSPlan{Type: t, Opts: safe}
	case "ntfs":
		return FSPlan{Type: "ntfs3", Opts: safe}
	case "LVM2_member":
		return FSPlan{LVM: true}
	case "swap":
		return FSPlan{Skip: "swap"}
	case "crypto_LUKS":
		return FSPlan{Skip: "encrypted (LUKS)"}
	case "":
		if kv["PTTYPE"] != "" {
			return FSPlan{Skip: "partition table"}
		}
		return FSPlan{Skip: "no file system"}
	default:
		return FSPlan{Skip: "unsupported file system " + t}
	}
}

var safeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,120}$`)

func (s *DiskSession) mountFS(ctx context.Context, dev, name string, plan FSPlan, label string) error {
	if !safeNameRe.MatchString(name) {
		return fmt.Errorf("invalid name")
	}
	dir := filepath.Join(s.mount, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_, err := sys.RunOpts(ctx, sys.Opts{Timeout: 2 * time.Minute}, "mount", "-t", plan.Type, "-o", plan.Opts, "--", dev, dir)
	if err != nil && plan.Type == "ntfs3" && sys.Has("ntfs-3g") {
		_, err = sys.RunOpts(ctx, sys.Opts{Timeout: 2 * time.Minute}, "mount", "-t", "ntfs-3g", "-o", "ro,nodev,nosuid,noexec", "--", dev, dir)
	}
	if err != nil {
		_ = os.Remove(dir)
		return fmt.Errorf("cannot mount %s: %v", plan.Type, err)
	}
	s.Roots = append(s.Roots, MountedFS{Path: "/" + name, FSType: plan.Type, Label: label, Dir: dir})
	return nil
}

// openLVM activates the volume groups found on the guest's partitions
// (read-only loop: nothing can be written) and mounts their volumes. A
// group named like one of this NAS is left alone.
func (s *DiskSession) openLVM(ctx context.Context, devs []string) {
	if !sys.Has("vgchange") {
		s.Skipped = append(s.Skipped, "LVM: lvm2 is not installed")
		return
	}
	devArgs := func() []string {
		var a []string
		for _, d := range devs {
			a = append(a, "--devices", d)
		}
		return a
	}
	out, err := sys.RunCtx(ctx, "pvs", append([]string{"--noheadings", "-o", "vg_name,vg_uuid", "--readonly"}, devArgs()...)...)
	if err != nil {
		s.Skipped = append(s.Skipped, "LVM: "+err.Error())
		return
	}
	host, _ := sys.RunCtx(ctx, "vgs", "--noheadings", "-o", "vg_name,vg_uuid")
	hostVGs := map[string]string{}
	for _, l := range strings.Split(host, "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			hostVGs[f[0]] = f[1]
		}
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 || seen[f[0]] || !safeNameRe.MatchString(f[0]) {
			continue
		}
		vg, uuid := f[0], f[1]
		seen[vg] = true
		if u, ok := hostVGs[vg]; ok && u != uuid {
			s.Skipped = append(s.Skipped, "LVM "+vg+": a volume group of this NAS has the same name")
			continue
		}
		if _, err := sys.RunCtx(ctx, "vgchange", append(append([]string{"-ay"}, devArgs()...), vg)...); err != nil {
			s.Skipped = append(s.Skipped, "LVM "+vg+": "+err.Error())
			continue
		}
		s.vgs = append(s.vgs, lvmGroup{vg, devs})
		lvs, _ := sys.RunCtx(ctx, "lvs", append(append([]string{"--noheadings", "-o", "lv_name,lv_path"}, devArgs()...), vg)...)
		for _, ll := range strings.Split(lvs, "\n") {
			lf := strings.Fields(ll)
			if len(lf) != 2 || !safeNameRe.MatchString(lf[0]) || !strings.HasPrefix(lf[1], "/dev/") {
				continue
			}
			plan := PlanMount(probe(ctx, lf[1]))
			name := vg + "-" + lf[0]
			if plan.Skip != "" || plan.LVM {
				s.Skipped = append(s.Skipped, name+": "+firstNonEmpty(plan.Skip, "nested LVM"))
				continue
			}
			if err := s.mountFS(ctx, lf[1], name, plan, ""); err != nil {
				s.Skipped = append(s.Skipped, name+": "+err.Error())
			}
		}
	}
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// closeSession unmounts and removes everything of a session.
func closeSession(s *DiskSession) {
	for i := len(s.Roots) - 1; i >= 0; i-- {
		unmount(s.Roots[i].Dir)
	}
	for _, g := range s.vgs {
		args := []string{"-an"}
		for _, d := range g.devs {
			args = append(args, "--devices", d)
		}
		_, _ = sys.Run("vgchange", append(args, g.name)...)
	}
	if s.loop != "" {
		_, _ = sys.Run("losetup", "-d", s.loop)
	}
	if s.mount != "" {
		_ = os.RemoveAll(s.mount) // only empty folders are left once unmounted
	}
	if s.temp != "" {
		_ = os.RemoveAll(s.temp)
	}
}

func unmount(dir string) {
	if _, err := sys.Run("umount", dir); err != nil {
		_, _ = sys.Run("umount", "-l", dir)
	}
	_ = os.Remove(dir)
}

// CloseDisk closes the session of a disk backup.
func CloseDisk(task, snap string) bool {
	disks.Lock()
	s := disks.m[diskKey(task, snap)]
	if s != nil && s.State == "opening" {
		disks.Unlock()
		return false // its job cleans up when it ends or is cancelled
	}
	delete(disks.m, diskKey(task, snap))
	disks.Unlock()
	if s == nil {
		return false
	}
	closeSession(s)
	return true
}

// ExpireDisks closes the sessions idle or open for too long.
func ExpireDisks(now time.Time) {
	disks.Lock()
	var old []*DiskSession
	for k, s := range disks.m {
		if s.State != "opening" && (now.Sub(s.LastUsed) > DiskIdle || now.Sub(s.Opened) > DiskMaxLife) {
			old = append(old, s)
			delete(disks.m, k)
		}
	}
	disks.Unlock()
	for _, s := range old {
		closeSession(s)
	}
}

// CleanupDisks removes what sessions of an earlier run of the daemon left:
// mounts, loop devices and image files.
func CleanupDisks() {
	if out, err := sys.Run("findmnt", "-rn", "-o", "TARGET"); err == nil {
		lines := strings.Split(out, "\n")
		slices.Reverse(lines)
		for _, l := range lines {
			l = strings.ReplaceAll(strings.TrimSpace(l), `\x20`, " ")
			if strings.HasPrefix(l, DiskMountRoot+"/") {
				unmount(l)
			}
		}
	}
	if out, err := sys.Run("losetup", "-n", "-l", "-O", "NAME,BACK-FILE"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && strings.Contains(f[1], "/"+diskTempPrefix) {
				if vgs, err := sys.Run("pvs", "--noheadings", "-o", "pv_name,vg_name"); err == nil {
					for _, pl := range strings.Split(vgs, "\n") {
						if pf := strings.Fields(pl); len(pf) == 2 && strings.HasPrefix(pf[0], f[0]+"p") {
							_, _ = sys.Run("vgchange", "-an", pf[1])
						}
					}
				}
				_, _ = sys.Run("losetup", "-d", f[0])
			}
		}
	}
	_ = os.RemoveAll(DiskMountRoot)
	matches, _ := filepath.Glob(filepath.Join(storage.MountRoot, "*", diskTempPrefix+"*"))
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
}

// DiskSessions lists the open sessions.
func DiskSessions() []DiskSession {
	disks.Lock()
	defer disks.Unlock()
	out := []DiskSession{}
	for _, s := range disks.m {
		c := *s
		c.Roots = slices.Clone(s.Roots)
		out = append(out, c)
	}
	return out
}
