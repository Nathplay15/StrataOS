package vms

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Snapshots are taken by libvirt. Internal qcow2 snapshots (disk and, when the
// VM runs, memory, all inside the qcow2 file) are tried first. libvirt refuses
// them for UEFI VMs (the NVRAM is a raw pflash file that cannot hold a snapshot)
// and for raw disks: those get external snapshots instead, an overlay file next
// to each disk plus a memory file when the VM runs. libvirt ≥ 9.9 (Debian 13
// ships 11.x) can revert and delete external snapshots too.

type Snapshot struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Created     time.Time `json:"created"`
	State       string    `json:"state"`  // VM state when taken: running | shutoff | paused
	Memory      bool      `json:"memory"` // RAM saved: reverting resumes the running VM
	Kind        string    `json:"kind"`   // internal | external
	Parent      string    `json:"parent,omitempty"`
	Current     bool      `json:"current"`
}

var snapRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,62}$`)

func validSnap(s string) error {
	if !snapRe.MatchString(s) {
		return fmt.Errorf("invalid snapshot name (letters, digits, . - _)")
	}
	return nil
}

type snapshotXML struct {
	Name         string `xml:"name"`
	Description  string `xml:"description"`
	State        string `xml:"state"`
	CreationTime int64  `xml:"creationTime"`
	Memory       struct {
		Snapshot string `xml:"snapshot,attr"`
	} `xml:"memory"`
	Disks []struct {
		Name     string `xml:"name,attr"`
		Snapshot string `xml:"snapshot,attr"`
	} `xml:"disks>disk"`
	Parent struct {
		Name string `xml:"name"`
	} `xml:"parent"`
}

func parseSnapshotXML(s string) (Snapshot, error) {
	var x snapshotXML
	if err := xml.Unmarshal([]byte(s), &x); err != nil {
		return Snapshot{}, err
	}
	sn := Snapshot{Name: x.Name, Description: x.Description, State: x.State, Created: time.Unix(x.CreationTime, 0), Parent: x.Parent.Name, Kind: "internal"}
	sn.Memory = x.Memory.Snapshot == "internal" || x.Memory.Snapshot == "external"
	if x.Memory.Snapshot == "external" {
		sn.Kind = "external"
	}
	for _, d := range x.Disks {
		if d.Snapshot == "external" {
			sn.Kind = "external"
		}
	}
	// Internal checkpoints of a running VM have no <memory> element on older libvirt.
	if x.Memory.Snapshot == "" && x.State == "running" && sn.Kind == "internal" {
		sn.Memory = true
	}
	return sn, nil
}

// Snapshots lists the snapshots of a VM, oldest first.
func Snapshots(name string) ([]Snapshot, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	out, err := virsh("snapshot-list", name, "--name")
	if err != nil {
		return nil, err
	}
	cur, _ := virsh("snapshot-current", name, "--name")
	cur = strings.TrimSpace(cur)
	list := []Snapshot{}
	for _, n := range strings.Fields(out) {
		x, err := virsh("snapshot-dumpxml", name, n)
		if err != nil {
			continue
		}
		s, err := parseSnapshotXML(x)
		if err != nil {
			continue
		}
		s.Current = s.Name == cur
		list = append(list, s)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	return list, nil
}

// cleanDescription keeps a description on one line, without control characters.
func cleanDescription(d string) string {
	d = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, d)
	if len(d) > 500 {
		d = d[:500]
	}
	return strings.TrimSpace(d)
}

// externalSnapshotArgs builds the virsh arguments of an external snapshot:
// an overlay next to each disk, the CD-ROMs left out, the RAM in a file when running.
func externalSnapshotArgs(vm *VM, snap, desc string, running bool) []string {
	args := []string{"snapshot-create-as", vm.Name, "--name", snap, "--atomic"}
	if desc != "" {
		args = append(args, "--description", desc)
	}
	if running {
		dir := vm.Dir
		if dir == "" {
			dir = "/var/lib/libvirt/qemu/snapshot"
		}
		args = append(args, "--memspec", "file="+filepath.Join(dir, vm.Name+"."+snap+".mem")+",snapshot=external")
	} else {
		args = append(args, "--disk-only")
	}
	for _, d := range vm.Disks {
		if d.Device != "disk" || d.Source == "" {
			args = append(args, "--diskspec", d.Target+",snapshot=no")
			continue
		}
		base := strings.TrimSuffix(filepath.Base(d.Source), filepath.Ext(d.Source))
		// An overlay of an overlay: drop the previous snapshot suffix.
		if i := strings.Index(base, ".snap-"); i > 0 {
			base = base[:i]
		}
		overlay := filepath.Join(filepath.Dir(d.Source), base+".snap-"+snap+".qcow2")
		args = append(args, "--diskspec", d.Target+",snapshot=external,file="+overlay)
	}
	return args
}

// CreateSnapshot takes a snapshot and returns its kind (internal | external).
func CreateSnapshot(name, snap, desc string) (string, error) {
	if err := validSnap(snap); err != nil {
		return "", err
	}
	vm, err := Get(name)
	if err != nil {
		return "", err
	}
	if vm.Template {
		return "", fmt.Errorf("templates cannot have snapshots")
	}
	for _, d := range vm.Disks {
		if strings.ContainsAny(d.Source, ",\n") {
			return "", fmt.Errorf("unsupported disk path %s", d.Source)
		}
	}
	desc = cleanDescription(desc)
	running := vm.State == "running" || vm.State == "paused"
	args := []string{"snapshot-create-as", name, "--name", snap, "--atomic"}
	if desc != "" {
		args = append(args, "--description", desc)
	}
	_, ierr := virsh(args...)
	if ierr == nil {
		return "internal", nil
	}
	// pflash NVRAM (UEFI), raw disks… : external snapshot instead.
	if _, err := virsh(externalSnapshotArgs(vm, snap, desc, running)...); err != nil {
		return "", fmt.Errorf("snapshot failed: %v (internal snapshot: %v)", err, ierr)
	}
	return "external", nil
}

// RevertSnapshot brings the VM back to a snapshot (running again if RAM was saved).
func RevertSnapshot(name, snap string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := validSnap(snap); err != nil {
		return err
	}
	_, err := virsh("snapshot-revert", name, snap)
	if err != nil && strings.Contains(err.Error(), "--force") {
		_, err = virsh("snapshot-revert", name, snap, "--force")
	}
	return err
}

// DeleteSnapshot removes a snapshot (its changes are merged into the children).
func DeleteSnapshot(name, snap string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := validSnap(snap); err != nil {
		return err
	}
	_, err := virsh("snapshot-delete", name, snap)
	return err
}

// snapshotCount is cheap enough for the VM list.
func snapshotCount(name string) int {
	out, err := virsh("snapshot-list", name, "--name")
	if err != nil {
		return 0
	}
	return len(strings.Fields(out))
}
