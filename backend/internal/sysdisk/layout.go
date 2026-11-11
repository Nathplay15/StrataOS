// Package sysdisk protects the disk of the operating system: it describes
// how the system is laid out on its disk(s), plans and carries out the
// mirroring of the system disk (mdadm RAID 1) and the move of the system to
// another disk, and exports system backups. The planning is pure (facts in,
// plan out) and unit tested; the runner logs every command it runs.
package sysdisk

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"strataos/internal/storage"
	"strataos/internal/sys"
)

// GPT partition types.
const (
	TypeESP      = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	TypeBIOSGrub = "21686148-6449-6e6f-744e-656564454649"
)

// Node is a block device of the lsblk tree.
type Node struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Type        string   `json:"type"` // disk | part | raid1 | lvm | crypt…
	Size        int64    `json:"size"`
	FSType      string   `json:"fstype,omitempty"`
	UUID        string   `json:"uuid,omitempty"`
	PartUUID    string   `json:"partuuid,omitempty"`
	PartType    string   `json:"parttype,omitempty"`
	PTType      string   `json:"pttype,omitempty"`
	Serial      string   `json:"serial,omitempty"`
	Model       string   `json:"model,omitempty"`
	Rotational  bool     `json:"rota,omitempty"`
	Transport   string   `json:"tran,omitempty"`
	Mountpoints []string `json:"mountpoints,omitempty"`
	Children    []Node   `json:"children,omitempty"`
}

// Mount is a line of /proc/self/mountinfo.
type Mount struct {
	Target  string `json:"target"`
	Source  string `json:"source"` // as the kernel shows it, symlinks resolved
	FSType  string `json:"fstype"`
	Subvol  string `json:"subvol,omitempty"` // Btrfs: subvolume, without the leading /
	Options string `json:"options,omitempty"`
}

// Facts is what Detect reads from the running system.
type Facts struct {
	Disks  []Node              // lsblk tree, whole disks first
	Mounts []Mount             // mounted file systems
	Swaps  []string            // active swap devices and files
	Raids  []storage.RaidArray // mdadm arrays
	EFI    bool                // booted in UEFI mode
	RPi    bool                // Raspberry Pi (boots from its firmware partition)
	Arch   string              // amd64 | arm64…
	Used   map[string]int64    // bytes used, by mount point of the system
}

// DiskRef is a whole disk of the system.
type DiskRef struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Serial     string `json:"serial,omitempty"`
	Model      string `json:"model,omitempty"`
	Size       int64  `json:"size"`
	PTType     string `json:"ptType,omitempty"`
	Rotational bool   `json:"rotational,omitempty"`
	Transport  string `json:"transport,omitempty"`
	// ESP and BIOSGrub: its boot partitions ("" when none).
	ESP      string `json:"esp,omitempty"`
	BIOSGrub string `json:"biosGrub,omitempty"`
}

// SysMount is a file system of the system and how a copy handles it.
type SysMount struct {
	Mount
	// Role: root | subvol (another subvolume of the root Btrfs) | esp |
	// merge (a separate partition, e.g. /boot, copied into the root).
	Role   string `json:"role"`
	Device string `json:"device"` // the partition or array holding it
	Used   int64  `json:"used,omitempty"`
}

// SysArray is an mdadm array of the system.
type SysArray struct {
	Name    string   `json:"name"`   // md127
	Device  string   `json:"device"` // /dev/md127
	Label   string   `json:"label,omitempty"`
	Level   string   `json:"level"`
	Role    string   `json:"role"` // root | swap | boot | other
	Health  string   `json:"health"`
	Members []string `json:"members"` // partitions
	Disks   []string `json:"disks"`   // their whole disks
	Devices int      `json:"devices"` // raid devices
	Active  int      `json:"active"`
	Sync    string   `json:"sync,omitempty"`
	Percent float64  `json:"percent,omitempty"`
}

// Layout is how the system sits on its disk(s).
type Layout struct {
	Boot     string     `json:"boot"` // uefi | bios | rpi
	Arch     string     `json:"arch"`
	Root     SysMount   `json:"root"`
	RootOn   string     `json:"rootOn"` // part | md | lvm | crypt | other
	Disks    []DiskRef  `json:"disks"`
	Mounts   []SysMount `json:"mounts"`
	Swap     []string   `json:"swap,omitempty"` // swap partitions or arrays
	SwapSize int64      `json:"swapSize,omitempty"`
	// SwapFiles: swap files of the root file system (recreated on a copy).
	SwapFiles []string   `json:"swapFiles,omitempty"`
	Arrays    []SysArray `json:"arrays,omitempty"`
	Used      int64      `json:"used"` // bytes to copy
	// Mirrored: the system is on mdadm RAID 1; Degraded: a member is missing.
	Mirrored bool `json:"mirrored"`
	Degraded bool `json:"degraded,omitempty"`
	// Problems: what keeps StrataOS from mirroring or moving this system.
	Problems []string `json:"problems,omitempty"`
}

// Disk returns the system disk with that path.
func (l Layout) Disk(path string) *DiskRef {
	for i := range l.Disks {
		if l.Disks[i].Path == path {
			return &l.Disks[i]
		}
	}
	return nil
}

// IsSystemDisk tells whether a whole disk holds part of the system.
func (l Layout) IsSystemDisk(path string) bool { return l.Disk(path) != nil }

// RootArray returns the array holding "/" (nil when not on md).
func (l Layout) RootArray() *SysArray {
	for i := range l.Arrays {
		if l.Arrays[i].Role == "root" {
			return &l.Arrays[i]
		}
	}
	return nil
}

// walkNode calls fn with each node and the chain of its parents.
func walkNode(n Node, parents []Node, fn func(n Node, parents []Node)) {
	fn(n, parents)
	p := append(append([]Node(nil), parents...), n)
	for _, c := range n.Children {
		walkNode(c, p, fn)
	}
}

// holders finds the chains of a device path in the tree: an md array shows
// under each of its members, so there can be several.
func holders(disks []Node, path string) [][]Node {
	var out [][]Node
	for _, d := range disks {
		walkNode(d, nil, func(n Node, parents []Node) {
			if n.Path == path {
				out = append(out, append(append([]Node(nil), parents...), n))
			}
		})
	}
	return out
}

var systemTargets = map[string]bool{"/": true, "/boot": true, "/boot/efi": true, "/usr": true, "/var": true, "/home": true, "/opt": true, "/srv": true, "/tmp": true, "/var/log": true, "/var/lib": true, "/var/lib/strata": true}

// ParseLayout derives the layout of the system from the facts.
func ParseLayout(f Facts) Layout {
	l := Layout{Boot: "bios", Arch: f.Arch, Disks: []DiskRef{}, Mounts: []SysMount{}}
	if f.EFI {
		l.Boot = "uefi"
	}
	if f.RPi {
		l.Boot = "rpi"
	}
	var root *Mount
	for i := range f.Mounts {
		if f.Mounts[i].Target == "/" {
			root = &f.Mounts[i]
		}
	}
	if root == nil {
		l.Problems = append(l.Problems, "The device of the system (/) could not be found.")
		return l
	}
	chains := holders(f.Disks, root.Source)
	if len(chains) == 0 {
		l.Problems = append(l.Problems, fmt.Sprintf("The system is on %s, which is not a disk StrataOS knows (network or virtual device).", root.Source))
		return l
	}
	top := chains[0][len(chains[0])-1]
	l.Root = SysMount{Mount: *root, Role: "root", Device: root.Source, Used: f.Used["/"]}
	switch {
	case top.Type == "part":
		l.RootOn = "part"
	case strings.HasPrefix(top.Type, "raid"):
		l.RootOn = "md"
	case top.Type == "lvm":
		l.RootOn = "lvm"
	case top.Type == "crypt":
		l.RootOn = "crypt"
	default:
		l.RootOn = "other"
	}
	for _, c := range chains {
		for _, n := range c {
			if n.Type == "crypt" {
				l.RootOn = "crypt"
			}
		}
	}
	// System disks: those holding "/".
	diskSet := map[string]bool{}
	for _, c := range chains {
		d := c[0]
		if diskSet[d.Path] {
			continue
		}
		diskSet[d.Path] = true
		ref := DiskRef{Name: d.Name, Path: d.Path, Serial: d.Serial, Model: d.Model, Size: d.Size, PTType: d.PTType, Rotational: d.Rotational, Transport: d.Transport}
		for _, p := range d.Children {
			switch strings.ToLower(p.PartType) {
			case TypeESP:
				if ref.ESP == "" {
					ref.ESP = p.Path
				}
			case TypeBIOSGrub:
				ref.BIOSGrub = p.Path
			}
		}
		l.Disks = append(l.Disks, ref)
	}
	sort.Slice(l.Disks, func(i, j int) bool { return l.Disks[i].Path < l.Disks[j].Path })
	onSystem := func(path string) (bool, bool, string) {
		cs := holders(f.Disks, path)
		if len(cs) == 0 {
			return false, false, ""
		}
		in, out := false, false
		for _, c := range cs {
			if diskSet[c[0].Path] {
				in = true
			} else {
				out = true
			}
		}
		return in, out, cs[0][len(cs[0])-1].Type
	}
	l.Mounts = append(l.Mounts, l.Root)
	for _, m := range f.Mounts {
		if m.Target == "/" || !strings.HasPrefix(m.Source, "/dev/") {
			continue
		}
		in, out, typ := onSystem(m.Source)
		switch {
		case in && out:
			l.Problems = append(l.Problems, fmt.Sprintf("%s (%s) spans the system disk and another disk.", m.Target, m.Source))
			continue
		case !in:
			if systemTargets[m.Target] && m.Target != "/var/lib/strata" {
				l.Problems = append(l.Problems, fmt.Sprintf("%s is on another disk (%s): the system is spread over several disks.", m.Target, m.Source))
			}
			continue
		}
		sm := SysMount{Mount: m, Device: m.Source, Used: f.Used[m.Target]}
		switch {
		case m.Source == root.Source && m.FSType == "btrfs" && root.FSType == "btrfs":
			if m.Subvol == "" || m.Subvol == root.Subvol || strings.Contains(m.Subvol, "/") {
				continue // a bind mount, or a nested subvolume copied with its parent
			}
			sm.Role = "subvol"
		case m.Source == root.Source:
			continue // bind mount of the root file system
		case m.Target == "/boot/efi" && m.FSType == "vfat":
			sm.Role = "esp"
		case m.FSType == "ext4" || m.FSType == "ext3" || m.FSType == "ext2" || m.FSType == "xfs" || m.FSType == "btrfs":
			if typ == "lvm" || typ == "crypt" {
				l.Problems = append(l.Problems, fmt.Sprintf("%s is on an LVM or encrypted volume of the system disk.", m.Target))
				continue
			}
			sm.Role = "merge"
		default:
			l.Problems = append(l.Problems, fmt.Sprintf("%s (%s) is a %s file system on the system disk: StrataOS cannot copy it.", m.Target, m.Source, m.FSType))
			continue
		}
		// The same partition mounted twice (bind): keep the first.
		dup := false
		for _, x := range l.Mounts {
			if x.Source == sm.Source && x.Subvol == sm.Subvol {
				dup = true
			}
		}
		if !dup {
			l.Mounts = append(l.Mounts, sm)
		}
	}
	// Swap.
	for _, s := range f.Swaps {
		if !strings.HasPrefix(s, "/dev/") {
			l.SwapFiles = append(l.SwapFiles, s)
			continue
		}
		in, out, _ := onSystem(s)
		if in && out {
			l.Problems = append(l.Problems, fmt.Sprintf("The swap %s spans the system disk and another disk.", s))
		}
		if in {
			l.Swap = append(l.Swap, s)
			for _, c := range holders(f.Disks, s) {
				if diskSet[c[0].Path] {
					l.SwapSize += c[len(c)-1].Size
					break
				}
			}
		}
	}
	// Arrays of the system.
	for _, a := range f.Raids {
		role := ""
		switch {
		case a.Device == root.Source:
			role = "root"
		case contains(l.Swap, a.Device):
			role = "swap"
		default:
			for _, m := range l.Mounts {
				if m.Source == a.Device {
					role = "boot"
				}
			}
		}
		var members, disks []string
		for _, m := range a.Members {
			if m.Device == "" {
				continue
			}
			members = append(members, m.Device)
			for _, c := range holders(f.Disks, m.Device) {
				if !contains(disks, c[0].Path) {
					disks = append(disks, c[0].Path)
				}
			}
		}
		onSys := false
		for _, d := range disks {
			if diskSet[d] {
				onSys = true
			}
		}
		if role == "" && !onSys {
			continue
		}
		if role == "" {
			role = "other"
			l.Problems = append(l.Problems, fmt.Sprintf("The RAID array %s uses the system disk but holds no part of the system.", firstNonEmpty(a.Label, a.Name)))
		}
		l.Arrays = append(l.Arrays, SysArray{Name: a.Name, Device: a.Device, Label: a.Label, Level: a.Level, Role: role, Health: a.Health,
			Members: members, Disks: disks, Devices: a.RaidDevices, Active: a.ActiveDevices, Sync: a.SyncAction, Percent: a.SyncProgress})
	}
	if ra := l.RootArray(); ra != nil {
		if ra.Level == "raid1" && ra.Devices >= 2 {
			l.Mirrored = true
		} else if ra.Level != "raid1" {
			l.Problems = append(l.Problems, fmt.Sprintf("The system is on a %s array: only RAID 1 is handled.", strings.ToUpper(ra.Level)))
		}
		for _, a := range l.Arrays {
			if a.Health == "degraded" || a.Health == "failed" || (a.Devices > 0 && a.Active < a.Devices) {
				l.Degraded = true
			}
		}
	}
	// Partitions of the system disks that hold something else (data).
	for _, d := range f.Disks {
		if !diskSet[d.Path] {
			continue
		}
		for _, p := range d.Children {
			pt := strings.ToLower(p.PartType)
			if pt == TypeESP || pt == TypeBIOSGrub || p.FSType == "swap" {
				continue
			}
			used := false
			for _, m := range l.Mounts {
				if m.Source == p.Path {
					used = true
				}
			}
			walkNode(p, nil, func(n Node, _ []Node) {
				if n.Path == root.Source {
					used = true
				}
				for _, a := range l.Arrays {
					if n.Path == a.Device && a.Role != "other" {
						used = true
					}
				}
			})
			if !used && p.FSType != "" && p.Size > 64<<20 {
				l.Problems = append(l.Problems, fmt.Sprintf("The system disk also holds %s (%s, not part of the system): StrataOS cannot tell what it is for.", p.Path, p.FSType))
			}
		}
	}
	for _, m := range l.Mounts {
		l.Used += m.Used
	}
	switch l.RootOn {
	case "lvm":
		l.Problems = append(l.Problems, "The system is on an LVM logical volume: StrataOS mirrors and moves systems installed on a partition (StrataOS ISO, most Debian installations) only.")
	case "crypt":
		l.Problems = append(l.Problems, "The system is encrypted (LUKS): StrataOS does not mirror or move encrypted systems.")
	case "other":
		l.Problems = append(l.Problems, fmt.Sprintf("The system is on a %s device: StrataOS cannot copy it.", top.Type))
	}
	switch l.Root.FSType {
	case "ext4", "ext3", "ext2", "btrfs", "xfs":
	default:
		l.Problems = append(l.Problems, fmt.Sprintf("The system file system is %s: ext4, Btrfs and XFS only.", l.Root.FSType))
	}
	if l.Root.FSType == "btrfs" && strings.Contains(l.Root.Subvol, "/") {
		l.Problems = append(l.Problems, "The system is on a nested Btrfs subvolume.")
	}
	if l.Boot == "rpi" {
		l.Problems = append(l.Problems, "A Raspberry Pi starts from its SD card or USB disk through its own firmware: copy the SD card with the Raspberry Pi Imager instead.")
	}
	if len(l.Disks) > 1 && !l.Mirrored {
		l.Problems = append(l.Problems, "The system is spread over several disks.")
	}
	return l
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ParseMountinfo reads /proc/self/mountinfo lines.
func ParseMountinfo(text string) []Mount {
	var out []Mount
	for _, line := range strings.Split(text, "\n") {
		// 25 1 0:23 /@rootfs / rw,relatime shared:1 - btrfs /dev/sda2 rw,...
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		sep := 0
		for i, x := range f {
			if x == "-" {
				sep = i
				break
			}
		}
		if sep == 0 || sep+2 >= len(f) {
			continue
		}
		m := Mount{Target: unescape(f[4]), FSType: f[sep+1], Source: unescape(f[sep+2])}
		if sep+3 < len(f) {
			m.Options = f[sep+3]
		}
		if m.FSType == "btrfs" {
			m.Subvol = strings.TrimPrefix(unescape(f[3]), "/")
		}
		out = append(out, m)
	}
	return out
}

// unescape decodes the octal escapes of mountinfo (\040 = space).
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ParseSwaps reads /proc/swaps.
func ParseSwaps(text string) []string {
	var out []string
	for i, l := range strings.Split(text, "\n") {
		if f := strings.Fields(l); i > 0 && len(f) > 0 {
			out = append(out, unescape(f[0]))
		}
	}
	return out
}

const lsblkCols = "NAME,PATH,TYPE,SIZE,FSTYPE,UUID,PARTUUID,PARTTYPE,PTTYPE,SERIAL,MODEL,ROTA,TRAN,MOUNTPOINTS"

// ParseLsblk decodes lsblk -J -b output.
func ParseLsblk(out string) ([]Node, error) {
	var res struct {
		Blockdevices []json.RawMessage `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("parse lsblk: %w", err)
	}
	var nodes []Node
	for _, raw := range res.Blockdevices {
		n, err := decodeNode(raw)
		if err != nil {
			return nil, err
		}
		if n.Type == "disk" && !strings.HasPrefix(n.Name, "zram") && n.Size > 0 {
			nodes = append(nodes, n)
		}
	}
	return nodes, nil
}

func decodeNode(raw json.RawMessage) (Node, error) {
	var d map[string]json.RawMessage
	if err := json.Unmarshal(raw, &d); err != nil {
		return Node{}, err
	}
	str := func(k string) string {
		v, ok := d[k]
		if !ok || string(v) == "null" {
			return ""
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			return strings.TrimSpace(s)
		}
		return strings.TrimSpace(string(v))
	}
	n := Node{Name: str("name"), Path: str("path"), Type: str("type"), FSType: str("fstype"), UUID: str("uuid"),
		PartUUID: str("partuuid"), PartType: strings.ToLower(str("parttype")), PTType: str("pttype"),
		Serial: str("serial"), Model: str("model"), Transport: str("tran")}
	n.Size, _ = strconv.ParseInt(str("size"), 10, 64)
	n.Rotational = str("rota") == "true" || str("rota") == "1"
	if v, ok := d["mountpoints"]; ok {
		var mps []*string
		_ = json.Unmarshal(v, &mps)
		for _, m := range mps {
			if m != nil && *m != "" {
				n.Mountpoints = append(n.Mountpoints, *m)
			}
		}
	}
	if v, ok := d["children"]; ok {
		var kids []json.RawMessage
		_ = json.Unmarshal(v, &kids)
		for _, k := range kids {
			c, err := decodeNode(k)
			if err != nil {
				return n, err
			}
			n.Children = append(n.Children, c)
		}
	}
	return n, nil
}

// Tree reads the lsblk tree.
func Tree() ([]Node, error) {
	out, err := sys.Run("lsblk", "-J", "-b", "-o", lsblkCols)
	if err != nil {
		return nil, err
	}
	return ParseLsblk(out)
}

// Gather reads the facts of the running system.
func Gather() (Facts, error) {
	f := Facts{Arch: runtime.GOARCH, Used: map[string]int64{}}
	var err error
	if f.Disks, err = Tree(); err != nil {
		return f, err
	}
	mi, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return f, err
	}
	for _, m := range ParseMountinfo(string(mi)) {
		if strings.HasPrefix(m.Source, "/dev/") {
			if r, err := filepath.EvalSymlinks(m.Source); err == nil {
				m.Source = r
			}
		}
		f.Mounts = append(f.Mounts, m)
	}
	sw, _ := os.ReadFile("/proc/swaps")
	for _, s := range ParseSwaps(string(sw)) {
		if r, err := filepath.EvalSymlinks(s); err == nil && strings.HasPrefix(s, "/dev/") {
			s = r
		}
		f.Swaps = append(f.Swaps, s)
	}
	f.Raids, _ = storage.ListRaid()
	_, err = os.Stat("/sys/firmware/efi")
	f.EFI = err == nil
	if b, err := os.ReadFile("/proc/device-tree/model"); err == nil && strings.Contains(string(b), "Raspberry Pi") {
		f.RPi = true
	}
	// Space used by each file system of the system (once per file system).
	seen := map[string]bool{}
	for _, m := range f.Mounts {
		if !strings.HasPrefix(m.Source, "/dev/") || seen[m.Source] {
			continue
		}
		if out, err := sys.Run("df", "-B1", "--output=used", m.Target); err == nil {
			ls := strings.Fields(out)
			if len(ls) == 2 {
				n, _ := strconv.ParseInt(ls[1], 10, 64)
				f.Used[m.Target] = n
				seen[m.Source] = true
			}
		}
	}
	return f, nil
}

// Detect gathers the facts and derives the layout.
func Detect() (Layout, Facts, error) {
	f, err := Gather()
	if err != nil {
		return Layout{}, f, err
	}
	return ParseLayout(f), f, nil
}
