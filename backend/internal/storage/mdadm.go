package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

func nilCtx() context.Context { return context.Background() }

type RaidMember struct {
	Device     string `json:"device"`
	RaidDevice int    `json:"raidDevice"`
	State      string `json:"state"` // active sync | spare | faulty | rebuilding
}

type RaidArray struct {
	Name          string       `json:"name"`   // md127
	Device        string       `json:"device"` // /dev/md127
	Label         string       `json:"label"`  // array name given at creation
	Level         string       `json:"level"`
	Size          int64        `json:"size"`
	State         string       `json:"state"`
	Health        string       `json:"health"` // healthy | degraded | rebuilding | failed | inactive
	UUID          string       `json:"uuid"`
	RaidDevices   int          `json:"raidDevices"`
	ActiveDevices int          `json:"activeDevices"`
	FailedDevices int          `json:"failedDevices"`
	SpareDevices  int          `json:"spareDevices"`
	ChunkSize     string       `json:"chunkSize,omitempty"`
	Members       []RaidMember `json:"members"`
	SyncAction    string       `json:"syncAction,omitempty"` // resync | recovery | reshape | check
	SyncProgress  float64      `json:"syncProgress,omitempty"`
	SyncFinish    string       `json:"syncFinish,omitempty"`
	SyncSpeed     string       `json:"syncSpeed,omitempty"`
	FSType        string       `json:"fsType,omitempty"`
	UsedBy        string       `json:"usedBy,omitempty"`
}

var (
	mdstatArrayRe = regexp.MustCompile(`^(md[0-9a-z_]+)\s*:\s*(\w+)`)
	mdstatSyncRe  = regexp.MustCompile(`(resync|recovery|reshape|check|repair)\s*=\s*([0-9.]+)%.*?finish=([0-9.]+\w+)\s+speed=(\S+)`)
	mdDelayedRe   = regexp.MustCompile(`(resync|recovery|reshape)\s*=\s*(DELAYED|PENDING)`)
)

// ListRaid lists software RAID arrays managed by mdadm.
func ListRaid() ([]RaidArray, error) {
	data, err := os.ReadFile("/proc/mdstat")
	if err != nil {
		return []RaidArray{}, nil
	}
	arrays := []RaidArray{}
	var cur *RaidArray
	for _, line := range strings.Split(string(data), "\n") {
		if m := mdstatArrayRe.FindStringSubmatch(line); m != nil {
			if cur != nil {
				arrays = append(arrays, *cur)
			}
			cur = &RaidArray{Name: m[1], Device: "/dev/" + m[1], State: m[2], Members: []RaidMember{}}
			continue
		}
		if cur == nil {
			continue
		}
		if m := mdstatSyncRe.FindStringSubmatch(line); m != nil {
			cur.SyncAction = m[1]
			cur.SyncProgress, _ = strconv.ParseFloat(m[2], 64)
			cur.SyncFinish = m[3]
			cur.SyncSpeed = m[4]
		} else if m := mdDelayedRe.FindStringSubmatch(line); m != nil {
			cur.SyncAction = m[1] + " (" + strings.ToLower(m[2]) + ")"
		}
	}
	if cur != nil {
		arrays = append(arrays, *cur)
	}
	for i := range arrays {
		fillDetail(&arrays[i])
	}
	return arrays, nil
}

func fillDetail(a *RaidArray) {
	out, err := sys.Run("mdadm", "--detail", a.Device)
	if err != nil && out == "" {
		a.Health = "inactive"
		return
	}
	inTable := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "Number") && strings.Contains(line, "RaidDevice") {
			inTable = true
			continue
		}
		if inTable {
			f := strings.Fields(line)
			if len(f) < 5 {
				continue
			}
			// "0  8  16  0  active sync  /dev/sdb" or "-  0  0  1  removed"
			m := RaidMember{RaidDevice: -1}
			if rd, err := strconv.Atoi(f[3]); err == nil {
				m.RaidDevice = rd
			}
			last := f[len(f)-1]
			if strings.HasPrefix(last, "/dev/") {
				m.Device = last
				m.State = strings.Join(f[4:len(f)-1], " ")
			} else {
				m.State = strings.Join(f[4:], " ")
			}
			a.Members = append(a.Members, m)
			continue
		}
		k, v, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "Raid Level":
			a.Level = v
		case "Array Size":
			if n, err := strconv.ParseInt(strings.Fields(v)[0], 10, 64); err == nil {
				a.Size = n * 1024
			}
		case "State":
			a.State = v
		case "UUID":
			a.UUID = v
		case "Name":
			name := strings.Fields(v)[0]
			if _, after, ok := strings.Cut(name, ":"); ok {
				name = after
			}
			a.Label = name
		case "Raid Devices":
			a.RaidDevices, _ = strconv.Atoi(v)
		case "Active Devices":
			a.ActiveDevices, _ = strconv.Atoi(v)
		case "Failed Devices":
			a.FailedDevices, _ = strconv.Atoi(v)
		case "Spare Devices":
			a.SpareDevices, _ = strconv.Atoi(v)
		case "Chunk Size":
			a.ChunkSize = v
		}
	}
	st := strings.ToLower(a.State)
	switch {
	case strings.Contains(st, "fail") || (a.RaidDevices > 0 && a.ActiveDevices == 0):
		a.Health = "failed"
	case strings.Contains(st, "recovering") || strings.Contains(st, "resyncing") || strings.Contains(st, "reshaping"):
		a.Health = "rebuilding"
	case strings.Contains(st, "degraded"):
		a.Health = "degraded"
	case strings.Contains(st, "inactive"):
		a.Health = "inactive"
	default:
		a.Health = "healthy"
	}
	a.FSType, _, _ = Probe(a.Device)
	if a.FSType == "LVM2_member" {
		a.UsedBy = pvVG(a.Device)
	}
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-]{0,31}$`)

// ValidName checks names used for arrays, pools, volumes and shares.
func ValidName(n string) error {
	if !nameRe.MatchString(n) {
		return fmt.Errorf("invalid name %q (letters, digits, - and _, max 32 chars)", n)
	}
	return nil
}

type CreateRaidReq struct {
	Name    string   `json:"name"`
	Level   string   `json:"level"` // 0 1 4 5 6 10 linear
	Devices []string `json:"devices"`
	Spares  []string `json:"spares"`
	Chunk   int      `json:"chunk"` // KiB, 0 = default
}

var minDevices = map[string]int{"linear": 1, "0": 2, "1": 2, "4": 3, "5": 3, "6": 4, "10": 2}

// CreateRaid creates an mdadm array. Output of the long-running steps goes to w.
func CreateRaid(ctx context.Context, r CreateRaidReq, logf func(string, ...any)) (string, error) {
	if err := ValidName(r.Name); err != nil {
		return "", err
	}
	min, ok := minDevices[r.Level]
	if !ok {
		return "", fmt.Errorf("unsupported RAID level %q", r.Level)
	}
	if len(r.Devices) < min {
		return "", fmt.Errorf("RAID %s needs at least %d disks", r.Level, min)
	}
	all := append(append([]string{}, r.Devices...), r.Spares...)
	if err := ValidateFreeDisks(all, false); err != nil {
		return "", err
	}
	if _, err := os.Stat("/dev/md/" + r.Name); err == nil {
		return "", fmt.Errorf("an array named %s already exists", r.Name)
	}
	Lock.Lock()
	defer Lock.Unlock()
	for _, d := range all {
		logf("Wiping %s", d)
		if err := Wipe(d); err != nil {
			return "", err
		}
	}
	dev := "/dev/md/" + r.Name
	args := []string{"--create", dev, "--name=" + r.Name, "--level=" + r.Level, "--metadata=1.2", "--run", "--homehost=any",
		fmt.Sprintf("--raid-devices=%d", len(r.Devices))}
	if len(r.Spares) > 0 {
		args = append(args, fmt.Sprintf("--spare-devices=%d", len(r.Spares)))
	}
	if r.Chunk > 0 && r.Level != "1" && r.Level != "linear" {
		args = append(args, fmt.Sprintf("--chunk=%d", r.Chunk))
	}
	if r.Level == "1" || r.Level == "5" || r.Level == "6" || r.Level == "10" {
		args = append(args, "--bitmap=internal")
	}
	args = append(args, all...)
	logf("mdadm %s", strings.Join(args, " "))
	if _, err := sys.RunOpts(ctx, sys.Opts{Stdin: "y\n"}, "mdadm", args...); err != nil {
		return "", err
	}
	_, _ = sys.Run("udevadm", "settle")
	if err := SaveMdadmConf(); err != nil {
		logf("warning: mdadm.conf: %v", err)
	}
	real, _ := filepath.EvalSymlinks(dev)
	if real == "" {
		real = dev
	}
	return real, nil
}

// SaveMdadmConf rewrites ARRAY lines of /etc/mdadm/mdadm.conf from the running arrays.
func SaveMdadmConf() error {
	scan, err := sys.Run("mdadm", "--detail", "--scan")
	if err != nil {
		return err
	}
	path := "/etc/mdadm/mdadm.conf"
	old, _ := os.ReadFile(path)
	var keep []string
	for _, l := range strings.Split(string(old), "\n") {
		t := strings.TrimSpace(l)
		// ARRAY lines are regenerated; HOMEHOST is forced below; drop our old header.
		if strings.HasPrefix(t, "ARRAY") || strings.HasPrefix(t, "HOMEHOST") || t == "# Arrays managed by StrataOS" {
			continue
		}
		keep = append(keep, l)
	}
	// "spares=N" is a snapshot of the moment (e.g. a disk being rebuilt): if it is
	// wrong at boot, assembly is delayed. Arrays are identified by UUID only.
	var arrays []string
	for _, l := range strings.Split(scan, "\n") {
		var f []string
		for _, w := range strings.Fields(l) {
			if !strings.HasPrefix(w, "spares=") {
				f = append(f, w)
			}
		}
		if len(f) > 0 {
			arrays = append(arrays, strings.Join(f, " "))
		}
	}
	// HOMEHOST <ignore>: disks moved from another machine are treated as local, so
	// Debian's last-resort timer may start them degraded when a member is missing.
	content := "HOMEHOST <ignore>\n" + strings.Trim(strings.Join(keep, "\n"), "\n") +
		"\n\n# Arrays managed by StrataOS\n" + strings.Join(arrays, "\n") + "\n"
	_ = os.MkdirAll("/etc/mdadm", 0o755)
	if err := sys.WriteFileAtomic(path, []byte(content), 0o644); err != nil {
		return err
	}
	// Make the arrays available at boot; slow, so run detached.
	go func() { _, _ = sys.RunOpts(context.Background(), sys.Opts{Timeout: -1}, "update-initramfs", "-u") }()
	return nil
}

// StartDegraded starts an inactive array that still has enough members (mdadm --run).
func StartDegraded(a RaidArray) error {
	Lock.Lock()
	defer Lock.Unlock()
	_, err := sys.Run("mdadm", "--run", a.Device)
	return err
}

func findArray(name string) (*RaidArray, error) {
	arrays, err := ListRaid()
	if err != nil {
		return nil, err
	}
	for i := range arrays {
		if arrays[i].Name == name || arrays[i].Label == name {
			return &arrays[i], nil
		}
	}
	return nil, fmt.Errorf("array %s not found", name)
}

// RaidAction performs a management action on an array.
// Actions: add, add-spare, fail, remove, replace, grow, check, repair, stop-check.
func RaidAction(name, action, device string, count int) error {
	a, err := findArray(name)
	if err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	switch action {
	case "add", "add-spare":
		if err := ValidateFreeDisks([]string{device}, false); err != nil {
			return err
		}
		if err := Wipe(device); err != nil {
			return err
		}
		if _, err := sys.Run("mdadm", "--manage", a.Device, "--add", device); err != nil {
			return err
		}
	case "fail":
		if err := a.hasMember(device); err != nil {
			return err
		}
		if _, err := sys.Run("mdadm", "--manage", a.Device, "--fail", device); err != nil {
			return err
		}
	case "remove":
		if err := a.hasMember(device); err != nil {
			return err
		}
		_, _ = sys.Run("mdadm", "--manage", a.Device, "--fail", device)
		if _, err := sys.Run("mdadm", "--manage", a.Device, "--remove", device); err != nil {
			return err
		}
		_, _ = sys.Run("mdadm", "--zero-superblock", device)
	case "grow":
		// Turn spares into active members (expansion), e.g. RAID5 3 -> 4 disks.
		if count <= a.RaidDevices {
			return fmt.Errorf("new device count must be greater than %d", a.RaidDevices)
		}
		if _, err := sys.Run("mdadm", "--grow", a.Device, fmt.Sprintf("--raid-devices=%d", count)); err != nil {
			return err
		}
	case "check", "repair", "idle":
		p := filepath.Join("/sys/block", filepath.Base(resolve(a.Device)), "md/sync_action")
		if err := os.WriteFile(p, []byte(action), 0o644); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	return SaveMdadmConf()
}

// hasMember accepts only a device mdadm reports as a member of the array, so the
// value passed to --fail/--remove/--zero-superblock is never an option or an
// unrelated disk.
func (a *RaidArray) hasMember(device string) error {
	if devNameRe.MatchString(device) {
		for _, m := range a.Members {
			if m.Device == device {
				return nil
			}
		}
	}
	return fmt.Errorf("%q is not a member of array %s", device, a.Name)
}

func resolve(p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

// DeleteRaid stops an array and wipes its members' superblocks.
func DeleteRaid(name string) error {
	a, err := findArray(name)
	if err != nil {
		return err
	}
	if a.FSType != "" && a.FSType != "LVM2_member" {
		if mounted(resolve(a.Device)) {
			return fmt.Errorf("array %s holds a mounted filesystem; delete the volume first", name)
		}
	}
	if a.UsedBy != "" {
		return fmt.Errorf("array %s is used by volume group %s", name, a.UsedBy)
	}
	Lock.Lock()
	defer Lock.Unlock()
	_, _ = sys.Run("wipefs", "-a", a.Device)
	if _, err := sys.Run("mdadm", "--stop", a.Device); err != nil {
		return err
	}
	for _, m := range a.Members {
		if m.Device != "" {
			_, _ = sys.Run("mdadm", "--zero-superblock", m.Device)
			_, _ = sys.Run("wipefs", "-a", m.Device)
		}
	}
	return SaveMdadmConf()
}

func mounted(dev string) bool {
	data, _ := os.ReadFile("/proc/mounts")
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && resolve(f[0]) == dev {
			return true
		}
	}
	return false
}
