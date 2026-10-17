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

type BtrfsDevice struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	Used int64  `json:"used"`
}

type BtrfsSubvol struct {
	ID       int    `json:"id"`
	Path     string `json:"path"`
	Snapshot bool   `json:"snapshot"`
	ReadOnly bool   `json:"readOnly"`
}

type BtrfsInfo struct {
	MountPoint  string        `json:"mountPoint"`
	Label       string        `json:"label"`
	UUID        string        `json:"uuid"`
	Devices     []BtrfsDevice `json:"devices"`
	DataProfile string        `json:"dataProfile"`
	MetaProfile string        `json:"metaProfile"`
	Size        int64         `json:"size"`
	Used        int64         `json:"used"`
	FreeEst     int64         `json:"freeEstimated"`
	Subvolumes  []BtrfsSubvol `json:"subvolumes"`
	Scrub       string        `json:"scrub"`
	Balance     string        `json:"balance"`
}

func checkBtrfsMount(mp string) error {
	if !strings.HasPrefix(mp, MountRoot+"/") || !IsMountpoint(mp) {
		return fmt.Errorf("%s is not a mounted StrataOS volume", mp)
	}
	return nil
}

var (
	btrfsDevRe     = regexp.MustCompile(`devid\s+(\d+)\s+size\s+(\d+)\s+used\s+(\d+)\s+path\s+(\S+)`)
	btrfsProfileRe = regexp.MustCompile(`^(Data|Metadata),(\w+):`)
)

// BtrfsDetail describes a mounted btrfs volume.
func BtrfsDetail(mp string) (*BtrfsInfo, error) {
	if err := checkBtrfsMount(mp); err != nil {
		return nil, err
	}
	info := &BtrfsInfo{MountPoint: mp, Devices: []BtrfsDevice{}, Subvolumes: []BtrfsSubvol{}}
	if out, err := sys.Run("btrfs", "filesystem", "show", "--raw", mp); err == nil {
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "Label:") {
				// Label: 'name'  uuid: xxx
				if i := strings.Index(l, "uuid:"); i > 0 {
					info.UUID = strings.TrimSpace(l[i+5:])
					info.Label = strings.Trim(strings.TrimSpace(l[6:i]), "'")
				}
			}
			if m := btrfsDevRe.FindStringSubmatch(l); m != nil {
				id, _ := strconv.Atoi(m[1])
				info.Devices = append(info.Devices, BtrfsDevice{ID: id, Size: num(m[2]), Used: num(m[3]), Path: m[4]})
			}
		}
	}
	if out, err := sys.Run("btrfs", "filesystem", "df", "--raw", mp); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if m := btrfsProfileRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
				if m[1] == "Data" {
					info.DataProfile = strings.ToLower(m[2])
				} else {
					info.MetaProfile = strings.ToLower(m[2])
				}
			}
		}
	}
	if out, err := sys.Run("btrfs", "filesystem", "usage", "-b", mp); err == nil {
		for _, l := range strings.Split(out, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
			if !ok {
				continue
			}
			f := strings.Fields(v)
			if len(f) == 0 {
				continue
			}
			switch strings.TrimSpace(k) {
			case "Device size":
				info.Size = num(f[0])
			case "Used":
				if info.Used == 0 {
					info.Used = num(f[0])
				}
			case "Free (estimated)":
				info.FreeEst = num(f[0])
			}
		}
	}
	if out, err := sys.Run("btrfs", "subvolume", "list", "-o", "--sort=path", mp); err == nil {
		ro := map[string]bool{}
		if r, err := sys.Run("btrfs", "subvolume", "list", "-r", mp); err == nil {
			for _, l := range strings.Split(r, "\n") {
				if i := strings.Index(l, " path "); i > 0 {
					ro[strings.TrimSpace(l[i+6:])] = true
				}
			}
		}
		for _, l := range strings.Split(out, "\n") {
			// ID 256 gen 10 top level 5 path data
			f := strings.Fields(l)
			if len(f) < 9 {
				continue
			}
			id, _ := strconv.Atoi(f[1])
			p := strings.Join(f[8:], " ")
			info.Subvolumes = append(info.Subvolumes, BtrfsSubvol{ID: id, Path: p, ReadOnly: ro[p], Snapshot: strings.HasPrefix(p, ".snapshots/")})
		}
	}
	if out, err := sys.Run("btrfs", "scrub", "status", mp); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && strings.TrimSpace(k) == "Status" {
				info.Scrub = strings.TrimSpace(v)
			}
		}
	}
	if out, _ := sys.RunOpts(context.Background(), sys.Opts{AllowCodes: []int{1}}, "btrfs", "balance", "status", mp); out != "" {
		info.Balance = strings.TrimSpace(strings.Split(out, "\n")[0])
	}
	return info, nil
}

var snapNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_@.\-]{0,127}$`)

// BtrfsSnapshotNames lists snapshot directory names under mp/.snapshots.
func BtrfsSnapshotNames(mp string) ([]string, error) {
	if err := checkBtrfsMount(mp); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(mp, ".snapshots"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func subvolPath(mp, sub string) (string, error) {
	clean := filepath.Clean("/" + sub)[1:]
	if clean == "" || strings.Contains(clean, "..") {
		return "", fmt.Errorf("invalid subvolume path")
	}
	return filepath.Join(mp, clean), nil
}

func BtrfsCreateSubvol(mp, name string) error {
	if err := checkBtrfsMount(mp); err != nil {
		return err
	}
	if err := ValidName(name); err != nil {
		return err
	}
	_, err := sys.Run("btrfs", "subvolume", "create", filepath.Join(mp, name))
	return err
}

func BtrfsDeleteSubvol(mp, sub string) error {
	if err := checkBtrfsMount(mp); err != nil {
		return err
	}
	p, err := subvolPath(mp, sub)
	if err != nil {
		return err
	}
	_, err = sys.Run("btrfs", "subvolume", "delete", p)
	return err
}

// BtrfsSnapshot creates a read-only snapshot of sub (or of the whole volume when empty) in .snapshots.
func BtrfsSnapshot(mp, sub, name string) (string, error) {
	if err := checkBtrfsMount(mp); err != nil {
		return "", err
	}
	src := mp
	if sub != "" {
		p, err := subvolPath(mp, sub)
		if err != nil {
			return "", err
		}
		src = p
	}
	if name == "" {
		base := filepath.Base(src)
		if src == mp {
			base = "root"
		}
		name = base + "@" + time.Now().Format("2006-01-02_15-04-05")
	} else if !snapNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid snapshot name %q", name)
	}
	dir := filepath.Join(mp, ".snapshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)
	_, err := sys.Run("btrfs", "subvolume", "snapshot", "-r", src, dst)
	return ".snapshots/" + name, err
}

func BtrfsScrub(mp string) error {
	if err := checkBtrfsMount(mp); err != nil {
		return err
	}
	_, err := sys.Run("btrfs", "scrub", "start", mp)
	return err
}

func BtrfsBalance(ctx context.Context, mp, dataProfile, metaProfile string, w func(string, ...any)) error {
	if err := checkBtrfsMount(mp); err != nil {
		return err
	}
	args := []string{"balance", "start", "--full-balance"}
	if dataProfile != "" {
		if _, ok := btrfsProfiles[dataProfile]; !ok {
			return fmt.Errorf("unknown profile %s", dataProfile)
		}
		args = append(args, "-dconvert="+dataProfile)
	}
	if metaProfile != "" {
		if _, ok := btrfsProfiles[metaProfile]; !ok {
			return fmt.Errorf("unknown profile %s", metaProfile)
		}
		args = append(args, "-mconvert="+metaProfile)
	}
	args = append(args, mp)
	w("btrfs %s", strings.Join(args, " "))
	_, err := sys.RunOpts(ctx, sys.Opts{Timeout: -1}, "btrfs", args...)
	return err
}

func BtrfsAddDevice(mp, dev string) error {
	if err := checkBtrfsMount(mp); err != nil {
		return err
	}
	if err := ValidateFreeDisks([]string{dev}, false); err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	if err := Wipe(dev); err != nil {
		return err
	}
	_, err := sys.Run("btrfs", "device", "add", "-f", dev, mp)
	return err
}

func BtrfsRemoveDevice(ctx context.Context, mp, dev string) error {
	if err := checkBtrfsMount(mp); err != nil {
		return err
	}
	// A device path, a devid, or "missing" (btrfs keywords); never an option.
	if !devNameRe.MatchString(dev) && dev != "missing" && !isDigits(dev) {
		return fmt.Errorf("invalid device %q", dev)
	}
	_, err := sys.RunOpts(ctx, sys.Opts{Timeout: -1}, "btrfs", "device", "remove", dev, mp)
	return err
}
