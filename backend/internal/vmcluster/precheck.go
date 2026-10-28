package vmcluster

import (
	"fmt"
	"slices"
)

// DiskFact describes a disk of a VM for migration.
type DiskFact struct {
	Device string // disk | cdrom
	Target string // vda…
	Source string // file path, "rbd:<pool>/<image>" or ""
	// Ceph: the key of its Ceph pool ("<fsid>/<pool>") for RBD disks.
	Ceph string
	// Shared: a file the target sees at the same path (CephFS volume).
	Shared bool
}

// NICFact is a network interface of a VM.
type NICFact struct {
	Type   string // network | bridge
	Source string
}

// Facts is what the pre-checks of a migration need, gathered from both NAS.
type Facts struct {
	VM        string
	Running   bool
	MemoryMB  int
	CPUMode   string // host-passthrough | host-model | custom | ""
	CPUModel  string
	Disks     []DiskFact
	NICs      []NICFact
	HostDevs  int // USB or PCI devices passed through
	Snapshots int
	Template  bool

	SrcCPU, DstCPU string // host processor signatures
	DstModels      []string
	DstOnline      bool
	DstMaintenance bool
	DstLibvirt     bool
	DstFreeMB      int
	DstNetworks    []string // active libvirt networks
	DstBridges     []string
	DstCeph        []string // Ceph pool keys with a libvirt pool on the target
	DstFiles       []string // CD-ROM images present at the same path on the target
	DstHasVM       bool     // a VM with this name exists there

	BridgeMap   map[string]string // bridge here → bridge there
	CopyStorage bool              // the administrator accepts copying local disks
}

// Plan is the outcome of the pre-checks.
type Plan struct {
	Live        bool     `json:"live"`        // the VM runs: live migration
	CopyStorage bool     `json:"copyStorage"` // local disks are copied (slower)
	Copied      []string `json:"copied,omitempty"`
	Problems    []string `json:"problems,omitempty"` // block the migration
	Warnings    []string `json:"warnings,omitempty"`
}

// OK reports whether the migration may start.
func (p Plan) OK() bool { return len(p.Problems) == 0 }

// Precheck decides whether and how a VM can move to the target.
func Precheck(f Facts) Plan {
	p := Plan{Live: f.Running}
	bad := func(format string, a ...any) { p.Problems = append(p.Problems, fmt.Sprintf(format, a...)) }
	if !f.DstOnline {
		bad("the target NAS does not answer")
		return p
	}
	if !f.DstLibvirt {
		bad("virtualization is not installed on the target NAS")
		return p
	}
	if f.DstMaintenance {
		bad("the target NAS is in maintenance")
	}
	if f.DstHasVM {
		bad("a VM named %s already exists on the target NAS", f.VM)
	}
	if f.Template {
		bad("a template cannot be migrated")
	}
	if f.HostDevs > 0 {
		bad("detach the USB and PCI devices passed through to the VM first")
	}
	if f.Snapshots > 0 {
		bad("the VM has snapshots: delete them first (libvirt cannot migrate them)")
	}
	if f.Running && f.DstFreeMB < f.MemoryMB+marginMB {
		bad("not enough free memory on the target NAS (%d MB free, the VM needs %d MB)", f.DstFreeMB, f.MemoryMB)
	}
	if f.Running {
		if ok, why := CPUCompatible(f.CPUMode, f.CPUModel, f.DstModels, f.SrcCPU, f.DstCPU); !ok {
			bad("%s", why)
		}
	}
	for _, n := range f.NICs {
		switch n.Type {
		case "network":
			if !slices.Contains(f.DstNetworks, n.Source) {
				bad("the virtual network %s does not exist on the target NAS", n.Source)
			}
		case "bridge":
			b := n.Source
			if m := f.BridgeMap[n.Source]; m != "" {
				b = m
			}
			if !slices.Contains(f.DstBridges, b) {
				bad("the target NAS has no network bridge %s (create it, or choose another bridge)", b)
			}
		default:
			bad("the network interface of type %s cannot be migrated", n.Type)
		}
	}
	for _, d := range f.Disks {
		switch {
		case d.Device == "cdrom":
			if d.Source != "" && !slices.Contains(f.DstFiles, d.Source) {
				bad("eject the CD-ROM image %s first (the target NAS does not have it at the same place)", d.Source)
			}
		case d.Ceph != "":
			if !slices.Contains(f.DstCeph, d.Ceph) {
				bad("the target NAS has no VM storage on the Ceph pool of the disk %s (add it in Storage → Ceph)", d.Target)
			}
		case d.Shared:
		case d.Source == "":
		default:
			p.Copied = append(p.Copied, d.Target)
		}
	}
	if len(p.Copied) > 0 {
		p.CopyStorage = true
		switch {
		case !f.Running:
			bad("the disks of this VM are on this NAS only: start it to migrate it with its disks, or move it with a backup")
		case !f.CopyStorage:
			bad("the disks of this VM are on this NAS only: confirm that they are copied to the target (slower)")
		default:
			p.Warnings = append(p.Warnings, "the local disks are copied over the network while the VM runs: this takes a while and loads both NAS")
		}
	}
	return p
}
