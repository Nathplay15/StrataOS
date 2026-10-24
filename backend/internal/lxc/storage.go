package lxc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strataos/internal/sys"
)

// Root file systems. Every backend presents the root file system as a folder,
// <base>/rootfs (lxc.rootfs.path = dir:…), so lxc never depends on how it is
// stored; snapshots, clones, size limits and removal go through Backend.
// The interface is meant to take more backends later (Ceph RBD, …).

// StorageSpec says where and how a container's root file system is stored.
type StorageSpec struct {
	Backend  string `json:"backend"`  // dir | image | btrfs | zfs
	Location string `json:"location"` // volume root or ZFS dataset mount point
	Base     string `json:"base"`     // <location>/.strata-lxc/<name>
	Dataset  string `json:"dataset,omitempty"`
	SizeGB   int    `json:"sizeGb,omitempty"` // 0: no limit (dir only)
}

// Rootfs is the folder lxc boots.
func (s StorageSpec) Rootfs() string { return filepath.Join(s.Base, "rootfs") }

func (s StorageSpec) snapDir() string { return filepath.Join(s.Base, "snapshots") }

// Backend stores root file systems.
type Backend interface {
	// Create makes an empty root file system of st.SizeGB at st.Rootfs().
	Create(ctx context.Context, st *StorageSpec) error
	// Prepare makes the root file system available (mounted) before a start.
	Prepare(ctx context.Context, st *StorageSpec) error
	Resize(ctx context.Context, st *StorageSpec, sizeGB int) error
	// Usage returns the bytes used and the limit (0: none).
	Usage(st *StorageSpec) (used, limit int64)
	Snapshot(ctx context.Context, st *StorageSpec, snap string) error
	// Restore brings the stopped container's root file system back to snap.
	Restore(ctx context.Context, st *StorageSpec, snap string) error
	DeleteSnapshot(ctx context.Context, st *StorageSpec, snap string) error
	// SnapshotTree returns a folder holding the files of a snapshot ("" when
	// the backend cannot show one without copying it).
	SnapshotTree(st *StorageSpec, snap string) string
	// Clone copies a stopped container's root file system to dst (same location).
	Clone(ctx context.Context, src, dst *StorageSpec) error
	// Rename moves the storage to the base of another name.
	Rename(ctx context.Context, st *StorageSpec, newName string) error
	// Destroy removes the root file system and its snapshots.
	Destroy(ctx context.Context, st *StorageSpec) error
}

// BackendFor returns the implementation of a backend name.
func BackendFor(name string) (Backend, error) {
	switch name {
	case "dir":
		return dirBackend{}, nil
	case "image":
		return imageBackend{}, nil
	case "btrfs":
		return btrfsBackend{}, nil
	case "zfs":
		return zfsBackend{}, nil
	}
	return nil, fmt.Errorf("unknown storage backend %q", name)
}

// baseFor is the folder of a container on a location.
func baseFor(location, name string) string {
	return filepath.Join(location, ".strata-lxc", name)
}

// mkBase creates the container folder: traversable but not listable by the
// container's shifted root, which must reach rootfs.
func mkBase(base string) error {
	if err := os.MkdirAll(filepath.Dir(base), 0o711); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(base), 0o711)
	if err := os.Mkdir(base, 0o711); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists", base)
		}
		return err
	}
	return nil
}

func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	return sys.RunOpts(ctx, sys.Opts{Timeout: timeout}, name, args...)
}

// copyTree copies a root file system with its owners, modes, ACLs and
// extended attributes (file capabilities), sharing extents when it can.
func copyTree(ctx context.Context, src, dst string) error {
	_, err := run(ctx, 6*time.Hour, "cp", "-a", "--reflink=auto", "--no-target-directory", src, dst)
	return err
}

func duBytes(ctx context.Context, p string) int64 {
	out, err := run(ctx, 2*time.Minute, "du", "-sbx", p)
	if err != nil {
		return 0
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.ParseInt(f[0], 10, 64)
	return n
}

func statUsed(p string) (used, size int64) {
	var st syscall.Statfs_t
	if syscall.Statfs(p, &st) != nil {
		return 0, 0
	}
	size = int64(st.Blocks) * st.Bsize
	return size - int64(st.Bfree)*st.Bsize, size
}

// ---- dir: a plain folder (no size limit) ----

type dirBackend struct{}

func (dirBackend) Create(ctx context.Context, st *StorageSpec) error {
	st.SizeGB = 0
	return os.Mkdir(st.Rootfs(), 0o755)
}
func (dirBackend) Prepare(context.Context, *StorageSpec) error { return nil }
func (dirBackend) Resize(context.Context, *StorageSpec, int) error {
	return fmt.Errorf("a folder has no size limit: choose an image, Btrfs or ZFS storage for that")
}
func (dirBackend) Usage(st *StorageSpec) (int64, int64) {
	return duBytes(context.Background(), st.Rootfs()), 0
}
func (dirBackend) Snapshot(ctx context.Context, st *StorageSpec, snap string) error {
	if err := os.MkdirAll(st.snapDir(), 0o700); err != nil {
		return err
	}
	dst := filepath.Join(st.snapDir(), snap)
	if err := copyTree(ctx, st.Rootfs(), dst); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return nil
}
func (dirBackend) Restore(ctx context.Context, st *StorageSpec, snap string) error {
	return swapTree(ctx, st, filepath.Join(st.snapDir(), snap))
}

// swapTree replaces rootfs with a copy of src, the old one removed only once
// the copy is complete.
func swapTree(ctx context.Context, st *StorageSpec, src string) error {
	tmp := filepath.Join(st.Base, ".rootfs-restore")
	_ = os.RemoveAll(tmp)
	if err := copyTree(ctx, src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	old := filepath.Join(st.Base, ".rootfs-old")
	_ = os.RemoveAll(old)
	if err := os.Rename(st.Rootfs(), old); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.Rootfs()); err != nil {
		_ = os.Rename(old, st.Rootfs())
		return err
	}
	return os.RemoveAll(old)
}
func (dirBackend) DeleteSnapshot(ctx context.Context, st *StorageSpec, snap string) error {
	return os.RemoveAll(filepath.Join(st.snapDir(), snap))
}
func (dirBackend) SnapshotTree(st *StorageSpec, snap string) string {
	return filepath.Join(st.snapDir(), snap)
}
func (dirBackend) Clone(ctx context.Context, src, dst *StorageSpec) error {
	return copyTree(ctx, src.Rootfs(), dst.Rootfs())
}
func (dirBackend) Rename(ctx context.Context, st *StorageSpec, newName string) error {
	return renameBase(st, newName)
}
func (dirBackend) Destroy(ctx context.Context, st *StorageSpec) error {
	return removeBase(st)
}

func renameBase(st *StorageSpec, newName string) error {
	nb := baseFor(st.Location, newName)
	if _, err := os.Stat(nb); err == nil {
		return fmt.Errorf("%s already exists", nb)
	}
	if err := os.Rename(st.Base, nb); err != nil {
		return err
	}
	st.Base = nb
	return nil
}

// removeBase deletes a container folder, which must be one of ours.
func removeBase(st *StorageSpec) error {
	if !strings.HasPrefix(st.Base, MountRoot+"/") || filepath.Base(filepath.Dir(st.Base)) != ".strata-lxc" {
		return fmt.Errorf("refusing to remove %s", st.Base)
	}
	return os.RemoveAll(st.Base)
}

// ---- image: an ext4 file system in a sparse file, loop-mounted ----
// Hard size limit on any volume (ext4, XFS…). systemd mounts it before the
// container starts (a mount unit the container's unit requires).

type imageBackend struct{}

func imgPath(st *StorageSpec) string { return filepath.Join(st.Base, "rootfs.img") }

func (imageBackend) Create(ctx context.Context, st *StorageSpec) error {
	if st.SizeGB < 1 {
		return fmt.Errorf("choose a disk size")
	}
	img := imgPath(st)
	f, err := os.OpenFile(img, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = f.Truncate(int64(st.SizeGB) << 30)
	f.Close()
	if err != nil {
		return err
	}
	if _, err := run(ctx, 10*time.Minute, "mkfs.ext4", "-q", "-F", "-L", "strata-lxc", "-m", "0", img); err != nil {
		return err
	}
	if err := os.Mkdir(st.Rootfs(), 0o755); err != nil {
		return err
	}
	if err := writeMountUnit(st); err != nil {
		return err
	}
	return startMount(st)
}
func (imageBackend) Prepare(ctx context.Context, st *StorageSpec) error {
	if isMounted(st.Rootfs()) {
		return nil
	}
	if err := writeMountUnit(st); err != nil {
		return err
	}
	return startMount(st)
}
func (imageBackend) Resize(ctx context.Context, st *StorageSpec, sizeGB int) error {
	if sizeGB < st.SizeGB {
		return fmt.Errorf("the disk can only grow (%d GB now)", st.SizeGB)
	}
	if sizeGB == st.SizeGB {
		return nil
	}
	if err := os.Truncate(imgPath(st), int64(sizeGB)<<30); err != nil {
		return err
	}
	if isMounted(st.Rootfs()) {
		out, err := run(ctx, time.Minute, "losetup", "-j", imgPath(st))
		if err != nil {
			return err
		}
		dev, _, _ := strings.Cut(out, ":")
		if !strings.HasPrefix(dev, "/dev/loop") {
			return fmt.Errorf("loop device of %s not found", imgPath(st))
		}
		if _, err := run(ctx, time.Minute, "losetup", "-c", dev); err != nil {
			return err
		}
		if _, err := run(ctx, 30*time.Minute, "resize2fs", dev); err != nil {
			return err
		}
	} else {
		if _, err := run(ctx, 30*time.Minute, "e2fsck", "-f", "-p", imgPath(st)); err != nil {
			return err
		}
		if _, err := run(ctx, 30*time.Minute, "resize2fs", imgPath(st)); err != nil {
			return err
		}
	}
	st.SizeGB = sizeGB
	return nil
}
func (imageBackend) Usage(st *StorageSpec) (int64, int64) {
	limit := int64(st.SizeGB) << 30
	if isMounted(st.Rootfs()) {
		used, _ := statUsed(st.Rootfs())
		return used, limit
	}
	return 0, limit
}

// frozen runs fn with the image's file system frozen (consistent copy of a
// running container).
func frozen(ctx context.Context, st *StorageSpec, fn func() error) error {
	if isMounted(st.Rootfs()) {
		if _, err := run(ctx, time.Minute, "fsfreeze", "-f", st.Rootfs()); err == nil {
			defer func() { _, _ = run(context.Background(), time.Minute, "fsfreeze", "-u", st.Rootfs()) }()
		}
	}
	return fn()
}

func copySparse(ctx context.Context, src, dst string) error {
	_, err := run(ctx, 6*time.Hour, "cp", "--sparse=always", "--reflink=auto", src, dst)
	return err
}

func (imageBackend) Snapshot(ctx context.Context, st *StorageSpec, snap string) error {
	if err := os.MkdirAll(st.snapDir(), 0o700); err != nil {
		return err
	}
	dst := filepath.Join(st.snapDir(), snap+".img")
	err := frozen(ctx, st, func() error { return copySparse(ctx, imgPath(st), dst) })
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}
func (imageBackend) Restore(ctx context.Context, st *StorageSpec, snap string) error {
	src := filepath.Join(st.snapDir(), snap+".img")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("snapshot %s not found", snap)
	}
	tmp := imgPath(st) + ".restore"
	_ = os.Remove(tmp)
	if err := copySparse(ctx, src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := stopMount(st); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, imgPath(st)); err != nil {
		return err
	}
	return startMount(st)
}
func (imageBackend) DeleteSnapshot(ctx context.Context, st *StorageSpec, snap string) error {
	return os.Remove(filepath.Join(st.snapDir(), snap+".img"))
}
func (imageBackend) SnapshotTree(*StorageSpec, string) string { return "" }
func (imageBackend) Clone(ctx context.Context, src, dst *StorageSpec) error {
	if err := frozen(ctx, src, func() error { return copySparse(ctx, imgPath(src), imgPath(dst)) }); err != nil {
		return err
	}
	// A new file system UUID, so that both can be mounted at once.
	_, _ = run(ctx, time.Minute, "tune2fs", "-U", "random", imgPath(dst))
	dst.SizeGB = src.SizeGB
	if err := os.Mkdir(dst.Rootfs(), 0o755); err != nil {
		return err
	}
	if err := writeMountUnit(dst); err != nil {
		return err
	}
	return startMount(dst)
}
func (imageBackend) Rename(ctx context.Context, st *StorageSpec, newName string) error {
	if err := stopMount(st); err != nil {
		return err
	}
	removeMountUnit(st)
	if err := renameBase(st, newName); err != nil {
		return err
	}
	if err := writeMountUnit(st); err != nil {
		return err
	}
	return startMount(st)
}
func (imageBackend) Destroy(ctx context.Context, st *StorageSpec) error {
	if err := stopMount(st); err != nil {
		return err
	}
	removeMountUnit(st)
	return removeBase(st)
}

func isMounted(p string) bool {
	data, _ := os.ReadFile("/proc/self/mountinfo")
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) > 4 && unescapeMount(f[4]) == p {
			return true
		}
	}
	return false
}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

// ---- btrfs: a subvolume with a qgroup limit ----

type btrfsBackend struct{}

// ensureQuota turns quotas on for the file system (simple quotas when the
// kernel has them: they cost much less than full qgroup accounting).
func ensureQuota(ctx context.Context, location string) error {
	if _, err := run(ctx, time.Minute, "btrfs", "qgroup", "show", location); err == nil {
		return nil
	}
	if _, err := run(ctx, 10*time.Minute, "btrfs", "quota", "enable", "--simple", location); err == nil {
		return nil
	}
	_, err := run(ctx, 10*time.Minute, "btrfs", "quota", "enable", location)
	return err
}

func btrfsLimit(ctx context.Context, st *StorageSpec) error {
	if st.SizeGB < 1 {
		return nil
	}
	if err := ensureQuota(ctx, st.Location); err != nil {
		return fmt.Errorf("btrfs quotas: %w", err)
	}
	_, err := run(ctx, time.Minute, "btrfs", "qgroup", "limit", strconv.Itoa(st.SizeGB)+"G", st.Rootfs())
	return err
}

func (btrfsBackend) Create(ctx context.Context, st *StorageSpec) error {
	if st.SizeGB < 1 {
		return fmt.Errorf("choose a disk size")
	}
	if _, err := run(ctx, time.Minute, "btrfs", "subvolume", "create", st.Rootfs()); err != nil {
		return err
	}
	return btrfsLimit(ctx, st)
}
func (btrfsBackend) Prepare(context.Context, *StorageSpec) error { return nil }
func (b btrfsBackend) Resize(ctx context.Context, st *StorageSpec, sizeGB int) error {
	if sizeGB < 1 {
		return fmt.Errorf("invalid size")
	}
	old := st.SizeGB
	st.SizeGB = sizeGB
	if err := btrfsLimit(ctx, st); err != nil {
		st.SizeGB = old
		return err
	}
	return nil
}

var qgroupLine = regexp.MustCompile(`^0/\d+\s+(\d+)\s+(\d+)\s+(\S+)`)

func (btrfsBackend) Usage(st *StorageSpec) (int64, int64) {
	limit := int64(st.SizeGB) << 30
	out, err := sys.Run("btrfs", "qgroup", "show", "-rf", "--raw", st.Rootfs())
	if err == nil {
		for _, l := range strings.Split(out, "\n") {
			if m := qgroupLine.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
				used, _ := strconv.ParseInt(m[1], 10, 64)
				return used, limit
			}
		}
	}
	return duBytes(context.Background(), st.Rootfs()), limit
}
func (btrfsBackend) Snapshot(ctx context.Context, st *StorageSpec, snap string) error {
	if err := os.MkdirAll(st.snapDir(), 0o700); err != nil {
		return err
	}
	_, err := run(ctx, 10*time.Minute, "btrfs", "subvolume", "snapshot", "-r", st.Rootfs(), filepath.Join(st.snapDir(), snap))
	return err
}

// deleteSubvol removes a subvolume and the ones inside it (Docker's).
func deleteSubvol(ctx context.Context, p string) error {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return nil
	}
	if _, err := run(ctx, 30*time.Minute, "btrfs", "subvolume", "delete", "--recursive", p); err == nil {
		return nil
	}
	_, err := run(ctx, 30*time.Minute, "btrfs", "subvolume", "delete", p)
	return err
}

func (b btrfsBackend) Restore(ctx context.Context, st *StorageSpec, snap string) error {
	src := filepath.Join(st.snapDir(), snap)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("snapshot %s not found", snap)
	}
	old := filepath.Join(st.Base, ".rootfs-old")
	_ = deleteSubvol(ctx, old)
	if err := os.Rename(st.Rootfs(), old); err != nil {
		return err
	}
	if _, err := run(ctx, 10*time.Minute, "btrfs", "subvolume", "snapshot", src, st.Rootfs()); err != nil {
		_ = os.Rename(old, st.Rootfs())
		return err
	}
	if err := btrfsLimit(ctx, st); err != nil {
		return err
	}
	return deleteSubvol(ctx, old)
}
func (btrfsBackend) DeleteSnapshot(ctx context.Context, st *StorageSpec, snap string) error {
	return deleteSubvol(ctx, filepath.Join(st.snapDir(), snap))
}
func (btrfsBackend) SnapshotTree(st *StorageSpec, snap string) string {
	return filepath.Join(st.snapDir(), snap)
}
func (btrfsBackend) Clone(ctx context.Context, src, dst *StorageSpec) error {
	if _, err := run(ctx, 10*time.Minute, "btrfs", "subvolume", "snapshot", src.Rootfs(), dst.Rootfs()); err != nil {
		return err
	}
	dst.SizeGB = src.SizeGB
	return btrfsLimit(ctx, dst)
}
func (btrfsBackend) Rename(ctx context.Context, st *StorageSpec, newName string) error {
	return renameBase(st, newName)
}
func (btrfsBackend) Destroy(ctx context.Context, st *StorageSpec) error {
	entries, _ := os.ReadDir(st.snapDir())
	for _, e := range entries {
		if err := deleteSubvol(ctx, filepath.Join(st.snapDir(), e.Name())); err != nil {
			return err
		}
	}
	for _, p := range []string{st.Rootfs(), filepath.Join(st.Base, ".rootfs-old")} {
		if err := deleteSubvol(ctx, p); err != nil {
			return err
		}
	}
	return removeBase(st)
}

// ---- zfs: a dataset with a refquota ----
// <location dataset>/strata-lxc/<name>, mounted on <base>/rootfs.

type zfsBackend struct{}

var datasetRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:\-]*(/[A-Za-z0-9][A-Za-z0-9_.:\-]*)*$`)

// zfsDatasetAt returns the dataset mounted exactly at a location.
func zfsDatasetAt(location string) (string, error) {
	out, err := sys.Run("zfs", "list", "-H", "-o", "name,mountpoint", "-t", "filesystem")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		if len(f) == 2 && f[1] == location && datasetRe.MatchString(f[0]) {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("%s is not a ZFS dataset", location)
}

func zfsQuota(st *StorageSpec) string {
	if st.SizeGB < 1 {
		return "none"
	}
	return strconv.Itoa(st.SizeGB) + "G"
}

func (zfsBackend) Create(ctx context.Context, st *StorageSpec) error {
	if st.SizeGB < 1 {
		return fmt.Errorf("choose a disk size")
	}
	parent, err := zfsDatasetAt(st.Location)
	if err != nil {
		return err
	}
	group := parent + "/strata-lxc"
	if _, err := sys.Run("zfs", "list", "-H", "-o", "name", group); err != nil {
		if _, err := run(ctx, time.Minute, "zfs", "create", "-o", "canmount=off", "-o", "mountpoint=none", group); err != nil {
			return err
		}
	}
	st.Dataset = group + "/" + filepath.Base(st.Base)
	_, err = run(ctx, 5*time.Minute, "zfs", "create", "-o", "mountpoint="+st.Rootfs(), "-o", "refquota="+zfsQuota(st),
		"-o", "acltype=posix", "-o", "xattr=sa", st.Dataset)
	return err
}
func (zfsBackend) Prepare(ctx context.Context, st *StorageSpec) error {
	if isMounted(st.Rootfs()) {
		return nil
	}
	_, err := run(ctx, 5*time.Minute, "zfs", "mount", st.Dataset)
	return err
}
func (zfsBackend) Resize(ctx context.Context, st *StorageSpec, sizeGB int) error {
	if sizeGB < 1 {
		return fmt.Errorf("invalid size")
	}
	old := st.SizeGB
	st.SizeGB = sizeGB
	if _, err := run(ctx, time.Minute, "zfs", "set", "refquota="+zfsQuota(st), st.Dataset); err != nil {
		st.SizeGB = old
		return err
	}
	return nil
}
func (zfsBackend) Usage(st *StorageSpec) (int64, int64) {
	out, err := sys.Run("zfs", "get", "-Hp", "-o", "value", "referenced", st.Dataset)
	used, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		used = 0
	}
	return used, int64(st.SizeGB) << 30
}
func (zfsBackend) Snapshot(ctx context.Context, st *StorageSpec, snap string) error {
	_, err := run(ctx, 5*time.Minute, "zfs", "snapshot", st.Dataset+"@"+snap)
	return err
}

// Restore rolls back: ZFS can only go back to the latest snapshot, so the
// snapshots taken after snap are destroyed (-r), as the interface warns.
func (zfsBackend) Restore(ctx context.Context, st *StorageSpec, snap string) error {
	_, err := run(ctx, 30*time.Minute, "zfs", "rollback", "-r", st.Dataset+"@"+snap)
	return err
}
func (zfsBackend) DeleteSnapshot(ctx context.Context, st *StorageSpec, snap string) error {
	_, err := run(ctx, 30*time.Minute, "zfs", "destroy", st.Dataset+"@"+snap)
	return err
}
func (zfsBackend) SnapshotTree(st *StorageSpec, snap string) string {
	return filepath.Join(st.Rootfs(), ".zfs", "snapshot", snap)
}
func (zfsBackend) Clone(ctx context.Context, src, dst *StorageSpec) error {
	tmp := "strata-clone-" + strconv.FormatInt(time.Now().Unix(), 10)
	if _, err := run(ctx, 5*time.Minute, "zfs", "snapshot", src.Dataset+"@"+tmp); err != nil {
		return err
	}
	defer func() { _, _ = sys.Run("zfs", "destroy", src.Dataset+"@"+tmp) }()
	dst.Dataset = filepath.Dir(src.Dataset) + "/" + filepath.Base(dst.Base)
	dst.SizeGB = src.SizeGB
	// zfs send | zfs receive, without a shell: an independent copy (a zfs
	// clone would keep the source's snapshot alive).
	if err := pipeCmds(ctx, 6*time.Hour, []string{"zfs", "send", src.Dataset + "@" + tmp},
		[]string{"zfs", "receive", "-u", "-o", "mountpoint=" + dst.Rootfs(), "-o", "refquota=" + zfsQuota(dst), dst.Dataset}); err != nil {
		return err
	}
	_, _ = sys.Run("zfs", "destroy", dst.Dataset+"@"+tmp)
	_, err := run(ctx, 5*time.Minute, "zfs", "mount", dst.Dataset)
	return err
}
func (zfsBackend) Rename(ctx context.Context, st *StorageSpec, newName string) error {
	_, _ = sys.Run("zfs", "unmount", st.Dataset)
	nds := filepath.Dir(st.Dataset) + "/" + newName
	if _, err := run(ctx, 5*time.Minute, "zfs", "rename", st.Dataset, nds); err != nil {
		return err
	}
	st.Dataset = nds
	if err := renameBase(st, newName); err != nil {
		return err
	}
	_, err := run(ctx, 5*time.Minute, "zfs", "set", "mountpoint="+st.Rootfs(), st.Dataset)
	return err
}
func (zfsBackend) Destroy(ctx context.Context, st *StorageSpec) error {
	if st.Dataset != "" && datasetRe.MatchString(st.Dataset) && strings.Contains(st.Dataset, "/strata-lxc/") {
		if _, err := sys.Run("zfs", "list", "-H", "-o", "name", st.Dataset); err == nil {
			if _, err := run(ctx, 30*time.Minute, "zfs", "destroy", "-r", st.Dataset); err != nil {
				return err
			}
		}
	}
	return removeBase(st)
}

// pipeCmds runs a | b without a shell.
func pipeCmds(ctx context.Context, timeout time.Duration, a, b []string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ca := exec.CommandContext(ctx, a[0], a[1:]...)
	cb := exec.CommandContext(ctx, b[0], b[1:]...)
	r, err := ca.StdoutPipe()
	if err != nil {
		return err
	}
	cb.Stdin = r
	var ea, eb strings.Builder
	ca.Stderr, cb.Stderr = &ea, &eb
	if err := cb.Start(); err != nil {
		return err
	}
	if err := ca.Start(); err != nil {
		_ = cb.Process.Kill()
		_ = cb.Wait()
		return err
	}
	errA := ca.Wait()
	errB := cb.Wait()
	if errA != nil {
		return fmt.Errorf("%s: %v %s", a[0], errA, strings.TrimSpace(ea.String()))
	}
	if errB != nil {
		return fmt.Errorf("%s: %v %s", b[0], errB, strings.TrimSpace(eb.String()))
	}
	return nil
}
