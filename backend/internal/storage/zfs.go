package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

type ZPool struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Alloc  int64  `json:"alloc"`
	Free   int64  `json:"free"`
	Frag   string `json:"fragmentation"`
	Cap    string `json:"capacity"`
	Dedup  string `json:"dedup"`
	Health string `json:"health"`
	Status string `json:"status,omitempty"` // raw `zpool status` text
	// Problems lists leaf devices that are not ONLINE (to offer a replacement).
	Problems []ZProblem `json:"problems,omitempty"`
	Datasets []ZDataset `json:"datasets,omitempty"`
}

type ZProblem struct {
	Device string `json:"device"` // name or GUID as zpool reports it
	State  string `json:"state"`
	Was    string `json:"was,omitempty"`
}

// parseProblems extracts non-ONLINE leaf vdevs from `zpool status -P` output.
func parseProblems(status string) []ZProblem {
	var out []ZProblem
	inConfig := false
	for _, l := range strings.Split(status, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "NAME") && strings.Contains(t, "STATE") {
			inConfig = true
			continue
		}
		if !inConfig || t == "" {
			if strings.HasPrefix(t, "errors:") {
				inConfig = false
			}
			continue
		}
		f := strings.Fields(t)
		if len(f) < 2 {
			continue
		}
		name, state := f[0], f[1]
		leaf := strings.HasPrefix(name, "/dev/") || isDigits(name)
		if !leaf || state == "ONLINE" || state == "AVAIL" || state == "INUSE" {
			continue
		}
		p := ZProblem{Device: name, State: state}
		if i := strings.Index(t, "was "); i >= 0 {
			p.Was = strings.TrimSpace(t[i+4:])
		}
		out = append(out, p)
	}
	return out
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

type ZDataset struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Used        int64  `json:"used"`
	Avail       int64  `json:"avail"`
	Refer       int64  `json:"refer"`
	Mountpoint  string `json:"mountpoint"`
	Compression string `json:"compression"`
	Quota       int64  `json:"quota"`
	Creation    int64  `json:"creation,omitempty"`
	LockedUntil int64  `json:"lockedUntil,omitempty"` // snapshots: not deletable before (Unix)
}

// ZFSBroken reports ZFS tools installed without a loadable kernel module, which
// typically happens after a kernel upgrade without matching headers.
func ZFSBroken() bool {
	return sys.Has("zpool") && !ZFSAvailable()
}

// ZFSAvailable reports whether the ZFS tools and kernel module are usable.
func ZFSAvailable() bool {
	if !sys.Has("zpool") {
		return false
	}
	if _, err := os.Stat("/sys/module/zfs"); err != nil {
		_, _ = sys.Run("modprobe", "zfs")
		_, err = os.Stat("/sys/module/zfs")
		return err == nil
	}
	return true
}

func tabRows(out string) [][]string {
	var rows [][]string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		rows = append(rows, strings.Split(l, "\t"))
	}
	return rows
}

func ListZPools(withDetail bool) ([]ZPool, error) {
	if !ZFSAvailable() {
		return []ZPool{}, nil
	}
	out, err := sys.Run("zpool", "list", "-Hp", "-o", "name,size,alloc,free,frag,cap,dedup,health")
	if err != nil {
		return nil, err
	}
	pools := []ZPool{}
	for _, f := range tabRows(out) {
		if len(f) < 8 {
			continue
		}
		p := ZPool{Name: f[0], Size: num(f[1]), Alloc: num(f[2]), Free: num(f[3]), Frag: f[4], Cap: f[5], Dedup: f[6], Health: f[7]}
		if withDetail {
			p.Status, _ = sys.Run("zpool", "status", "-P", p.Name)
			p.Problems = parseProblems(p.Status)
			p.Datasets, _ = ListZDatasets(p.Name, "filesystem,volume")
		}
		pools = append(pools, p)
	}
	return pools, nil
}

func ListZDatasets(pool, types string) ([]ZDataset, error) {
	args := []string{"list", "-Hp", "-t", types, "-o", "name,type,used,avail,refer,mountpoint,compression,quota,creation"}
	if pool != "" {
		if err := validDS(pool); err != nil {
			return nil, err
		}
		args = append(args, "-r", pool)
	}
	out, err := sys.Run("zfs", args...)
	if err != nil {
		return nil, err
	}
	ds := []ZDataset{}
	for _, f := range tabRows(out) {
		if len(f) < 9 {
			continue
		}
		ds = append(ds, ZDataset{Name: f[0], Type: f[1], Used: num(f[2]), Avail: num(f[3]), Refer: num(f[4]), Mountpoint: f[5], Compression: f[6], Quota: num(f[7]), Creation: num(f[8])})
	}
	return ds, nil
}

type VDev struct {
	Type  string   `json:"type"` // stripe | mirror | raidz1 | raidz2 | raidz3
	Disks []string `json:"disks"`
}

type CreatePoolReq struct {
	Name        string   `json:"name"`
	VDevs       []VDev   `json:"vdevs"`
	Cache       []string `json:"cache"`
	Log         []string `json:"log"`
	Spares      []string `json:"spares"`
	Compression string   `json:"compression"` // lz4 | zstd | off
	Ashift      int      `json:"ashift"`
	Encryption  bool     `json:"encryption"`
	Passphrase  string   `json:"passphrase,omitempty"`
}

var vdevMin = map[string]int{"stripe": 1, "mirror": 2, "raidz1": 2, "raidz2": 3, "raidz3": 4}

var reservedPool = map[string]bool{"mirror": true, "raidz": true, "raidz1": true, "raidz2": true, "raidz3": true, "spare": true, "log": true, "cache": true, "draid": true}

func CreateZPool(ctx context.Context, r CreatePoolReq, logf func(string, ...any)) error {
	if !ZFSAvailable() {
		return fmt.Errorf("ZFS is not installed")
	}
	if err := ValidName(r.Name); err != nil {
		return err
	}
	if reservedPool[r.Name] || strings.HasPrefix(r.Name, "c") && len(r.Name) > 1 && r.Name[1] >= '0' && r.Name[1] <= '9' {
		return fmt.Errorf("%s is a reserved ZFS pool name", r.Name)
	}
	if len(r.VDevs) == 0 {
		return fmt.Errorf("at least one vdev is required")
	}
	var all []string
	args := []string{"create", "-f", "-m", filepath.Join(MountRoot, r.Name)}
	ashift := r.Ashift
	if ashift == 0 {
		ashift = 12
	}
	args = append(args, "-o", fmt.Sprintf("ashift=%d", ashift), "-O", "acltype=posixacl", "-O", "xattr=sa", "-O", "atime=off")
	comp := r.Compression
	if comp == "" {
		comp = "lz4"
	}
	if comp != "lz4" && comp != "zstd" && comp != "off" && comp != "gzip" {
		return fmt.Errorf("unsupported compression %q", comp)
	}
	args = append(args, "-O", "compression="+comp)
	if r.Encryption {
		if len(r.Passphrase) < 8 {
			return fmt.Errorf("the encryption passphrase must be at least 8 characters")
		}
		args = append(args, "-O", "encryption=aes-256-gcm", "-O", "keyformat=passphrase", "-O", "keylocation=prompt")
	}
	args = append(args, r.Name)
	for _, v := range r.VDevs {
		min, ok := vdevMin[v.Type]
		if !ok {
			return fmt.Errorf("unknown vdev type %q", v.Type)
		}
		if len(v.Disks) < min {
			return fmt.Errorf("%s vdev needs at least %d disks", v.Type, min)
		}
		if v.Type != "stripe" {
			args = append(args, v.Type)
		}
		args = append(args, v.Disks...)
		all = append(all, v.Disks...)
	}
	for _, sec := range []struct {
		kw    string
		disks []string
	}{{"cache", r.Cache}, {"log", r.Log}, {"spare", r.Spares}} {
		if len(sec.disks) > 0 {
			args = append(args, sec.kw)
			args = append(args, sec.disks...)
			all = append(all, sec.disks...)
		}
	}
	if err := ValidateFreeDisks(all, false); err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	for _, d := range all {
		logf("Wiping %s", d)
		if err := Wipe(d); err != nil {
			return err
		}
	}
	// Use stable by-id paths when they exist so the pool survives device renaming.
	for i, a := range args {
		if strings.HasPrefix(a, "/dev/") {
			args[i] = byID(a)
		}
	}
	logf("zpool %s", strings.Join(args, " "))
	stdin := ""
	if r.Encryption {
		stdin = r.Passphrase + "\n" + r.Passphrase + "\n"
	}
	_, err := sys.RunOpts(ctx, sys.Opts{Stdin: stdin, Timeout: 10 * time.Minute}, "zpool", args...)
	return err
}

func byID(dev string) string {
	entries, err := os.ReadDir("/dev/disk/by-id")
	if err != nil {
		return dev
	}
	real := resolve(dev)
	best := ""
	for _, e := range entries {
		n := e.Name()
		if strings.Contains(n, "-part") {
			continue
		}
		if resolve(filepath.Join("/dev/disk/by-id", n)) == real {
			// Prefer ata-/nvme-/scsi- names over wwn- names.
			if best == "" || strings.HasPrefix(best, "wwn-") {
				best = n
			}
		}
	}
	if best == "" {
		return dev
	}
	return filepath.Join("/dev/disk/by-id", best)
}

var vdevNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:\-]{0,254}$`)

// validVdev accepts the ways zpool names an existing leaf vdev: a device path, a
// GUID, or a short name such as "sdb" or an /dev/disk/by-id entry. A leading "-"
// would be parsed by zpool as an option.
func validVdev(d string) error {
	if devNameRe.MatchString(d) || vdevNameRe.MatchString(d) {
		return nil
	}
	return fmt.Errorf("invalid device %q", d)
}

var dsNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-.:/]{0,200}$`)

// ValidDS validates a ZFS dataset or snapshot name.
func ValidDS(n string) error { return validDS(n) }

// validDS accepts a dataset name, or a snapshot name "dataset@snap".
func validDS(n string) error {
	ds, snap, isSnap := strings.Cut(n, "@")
	if !dsNameRe.MatchString(ds) || strings.Contains(ds, "..") || strings.HasSuffix(ds, "/") ||
		(isSnap && !snapRe.MatchString(snap)) {
		return fmt.Errorf("invalid dataset name %q", n)
	}
	return nil
}

var snapRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-.:]{0,200}$`)

func DestroyZPool(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if ZFSPoolHasLocks(name) {
		return fmt.Errorf("pool %s holds locked snapshots: it cannot be destroyed before they expire", name)
	}
	Lock.Lock()
	defer Lock.Unlock()
	_, err := sys.Run("zpool", "destroy", "-f", name)
	return err
}

// ZPoolAction runs scrub, scrub-stop, export, clear, or attaches/replaces devices.
func ZPoolAction(name, action, device, newDevice string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	var err error
	switch action {
	case "scrub":
		_, err = sys.Run("zpool", "scrub", name)
	case "scrub-stop":
		_, err = sys.Run("zpool", "scrub", "-s", name)
	case "clear":
		_, err = sys.Run("zpool", "clear", name)
	case "export":
		_, err = sys.Run("zpool", "export", name)
	case "trim":
		_, err = sys.Run("zpool", "trim", name)
	case "add-spare", "add-cache", "add-log":
		if err := ValidateFreeDisks([]string{device}, false); err != nil {
			return err
		}
		if err := Wipe(device); err != nil {
			return err
		}
		kw := strings.TrimPrefix(action, "add-")
		_, err = sys.Run("zpool", "add", "-f", name, kw, byID(device))
	case "replace":
		if err := validVdev(device); err != nil {
			return err
		}
		if err := ValidateFreeDisks([]string{newDevice}, false); err != nil {
			return err
		}
		if err := Wipe(newDevice); err != nil {
			return err
		}
		_, err = sys.Run("zpool", "replace", "-f", name, device, byID(newDevice))
	case "offline", "online":
		if err := validVdev(device); err != nil {
			return err
		}
		_, err = sys.Run("zpool", action, name, device)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	return err
}

type ImportablePool struct {
	Name  string `json:"name"`
	ID    string `json:"id"`
	State string `json:"state"`
}

func ListImportable() ([]ImportablePool, error) {
	if !ZFSAvailable() {
		return []ImportablePool{}, nil
	}
	out, _ := sys.RunOpts(context.Background(), sys.Opts{AllowCodes: []int{1}}, "zpool", "import")
	pools := []ImportablePool{}
	var cur *ImportablePool
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "pool":
			if cur != nil {
				pools = append(pools, *cur)
			}
			cur = &ImportablePool{Name: v}
		case "id":
			if cur != nil {
				cur.ID = v
			}
		case "state":
			if cur != nil {
				cur.State = v
			}
		}
	}
	if cur != nil {
		pools = append(pools, *cur)
	}
	return pools, nil
}

func ImportZPool(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	_, err := sys.Run("zpool", "import", "-f", name)
	return err
}

type CreateDatasetReq struct {
	Name        string `json:"name"` // full name, e.g. tank/media
	Compression string `json:"compression"`
	Quota       int64  `json:"quota"`
	RecordSize  string `json:"recordSize"`
	Volume      bool   `json:"volume"` // create a zvol
	VolSize     int64  `json:"volSize"`
}

func CreateDataset(r CreateDatasetReq) error {
	if err := validDS(r.Name); err != nil {
		return err
	}
	args := []string{"create", "-p"}
	if r.Compression != "" {
		args = append(args, "-o", "compression="+r.Compression)
	}
	if r.Volume {
		if r.VolSize <= 0 {
			return fmt.Errorf("a zvol needs a size")
		}
		args = append(args, "-V", fmt.Sprintf("%d", r.VolSize))
	} else {
		if r.Quota > 0 {
			args = append(args, "-o", fmt.Sprintf("quota=%d", r.Quota))
		}
		if r.RecordSize != "" {
			args = append(args, "-o", "recordsize="+r.RecordSize)
		}
	}
	_, err := sys.Run("zfs", append(args, r.Name)...)
	return err
}

func SetDatasetProps(name string, props map[string]string) error {
	if err := validDS(name); err != nil {
		return err
	}
	allowed := map[string]bool{"compression": true, "quota": true, "refquota": true, "reservation": true, "recordsize": true, "atime": true, "sync": true, "dedup": true, "readonly": true}
	for k, v := range props {
		if !allowed[k] || strings.ContainsAny(v, " \t\n") {
			return fmt.Errorf("property %s cannot be set", k)
		}
		if _, err := sys.Run("zfs", "set", k+"="+v, name); err != nil {
			return err
		}
	}
	return nil
}

func DestroyDataset(name string, recursive bool) error {
	if err := validDS(name); err != nil {
		return err
	}
	if !strings.Contains(name, "/") && !strings.Contains(name, "@") {
		return fmt.Errorf("use pool deletion to destroy a pool's root dataset")
	}
	if strings.Contains(name, "@") {
		if err := unlockZFSIfExpired(name, recursive); err != nil {
			return err
		}
	} else if ZFSPoolHasLocks(name) {
		return fmt.Errorf("%s holds locked snapshots: it cannot be destroyed before they expire", name)
	}
	args := []string{"destroy"}
	if recursive {
		args = append(args, "-r")
	}
	_, err := sys.Run("zfs", append(args, name)...)
	return err
}

func Snapshot(dataset, name string, recursive bool) error {
	if err := validDS(dataset); err != nil {
		return err
	}
	if name == "" {
		name = "strata-" + time.Now().Format("2006-01-02_15-04-05")
	}
	if err := validDS(name); err != nil || strings.Contains(name, "/") {
		return fmt.Errorf("invalid snapshot name")
	}
	args := []string{"snapshot"}
	if recursive {
		args = append(args, "-r")
	}
	_, err := sys.Run("zfs", append(args, dataset+"@"+name)...)
	return err
}

type ZSnapshot struct {
	Name     string
	Creation time.Time
}

// SnapshotsOf lists the direct snapshots of a dataset.
func SnapshotsOf(dataset string) ([]ZSnapshot, error) {
	if err := validDS(dataset); err != nil {
		return nil, err
	}
	out, err := sys.Run("zfs", "list", "-Hp", "-t", "snapshot", "-d", "1", "-o", "name,creation", dataset)
	if err != nil {
		return nil, err
	}
	var snaps []ZSnapshot
	for _, f := range tabRows(out) {
		if len(f) < 2 {
			continue
		}
		snaps = append(snaps, ZSnapshot{Name: f[0], Creation: time.Unix(num(f[1]), 0)})
	}
	return snaps, nil
}

func Rollback(snapshot string) error {
	if err := validDS(snapshot); err != nil || !strings.Contains(snapshot, "@") {
		return fmt.Errorf("invalid snapshot")
	}
	_, err := sys.Run("zfs", "rollback", "-r", snapshot)
	return err
}

// MarkLocks fills LockedUntil of the snapshots in list (one zfs call).
func MarkLocks(list []ZDataset) []ZDataset {
	out, err := sys.Run("zfs", "get", "-Hp", "-t", "snapshot", "-o", "name,value", "strata:lockuntil")
	if err != nil {
		return list
	}
	m := map[string]int64{}
	for _, f := range tabRows(out) {
		if len(f) >= 2 {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil && time.Now().Before(time.Unix(n, 0)) {
				m[f[0]] = n
			}
		}
	}
	for i := range list {
		list[i].LockedUntil = m[list[i].Name]
	}
	return list
}
