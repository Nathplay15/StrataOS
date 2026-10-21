// Package vms manages KVM virtual machines through libvirt (virsh / virt-install).
package vms

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Installed reports whether libvirt and virt-install are available.
func Installed() bool { return sys.Has("virsh") && sys.Has("virt-install") }

// KVM reports whether hardware virtualisation is usable.
func KVM() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,62}$`)

func ValidName(n string) error {
	if !nameRe.MatchString(n) {
		return fmt.Errorf("invalid VM name (letters, digits, . - _)")
	}
	return nil
}

func virsh(args ...string) (string, error) {
	return sys.Run("virsh", append([]string{"--connect", "qemu:///system"}, args...)...)
}

type Disk struct {
	Device string `json:"device"` // disk | cdrom
	Target string `json:"target"` // vda, sda…
	Bus    string `json:"bus"`
	Source string `json:"source"`
	Size   int64  `json:"size,omitempty"` // virtual size in bytes
}

type NIC struct {
	Type   string `json:"type"`   // network | bridge
	Source string `json:"source"` // network name or bridge
	MAC    string `json:"mac"`
	Model  string `json:"model"`
	IP     string `json:"ip,omitempty"`
}

type VM struct {
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
	State     string `json:"state"` // running | shut off | paused…
	VCPUs     int    `json:"vcpus"`
	MemoryMB  int    `json:"memoryMb"`
	Autostart bool   `json:"autostart"`
	Firmware  string `json:"firmware"` // bios | uefi
	OS        string `json:"os,omitempty"`
	Disks     []Disk `json:"disks"`
	NICs      []NIC  `json:"nics"`
	VNC       int    `json:"vncPort"`
	Dir       string `json:"dir,omitempty"`
	// Template VMs cannot start: they are cloned into new VMs.
	Template bool `json:"template"`
	// Cloud: created from a cloud image (cloud-init seed attached).
	Cloud     string      `json:"cloud,omitempty"`
	USB       []USBDevice `json:"usb"`
	PCI       []string    `json:"pci"` // host PCI addresses passed through
	Snapshots int         `json:"snapshots"`
	// AutostartOrder and AutostartDelay are filled in by the API (StrataOS
	// starts these VMs itself, in order).
	AutostartOrder int `json:"autostartOrder,omitempty"`
	AutostartDelay int `json:"autostartDelay,omitempty"`
	// LibvirtAutostart: libvirt's own autostart flag.
	LibvirtAutostart bool `json:"-"`
}

type domainXML struct {
	Name   string `xml:"name"`
	UUID   string `xml:"uuid"`
	Memory struct {
		Value int64  `xml:",chardata"`
		Unit  string `xml:"unit,attr"`
	} `xml:"memory"`
	VCPU     int `xml:"vcpu"`
	Metadata struct {
		Libosinfo struct {
			OS struct {
				ID string `xml:"id,attr"`
			} `xml:"os"`
		} `xml:"libosinfo"`
	} `xml:"metadata"`
	OS struct {
		Loader string `xml:"loader"`
		Fw     string `xml:"firmware,attr"`
	} `xml:"os"`
	Devices struct {
		Disks []struct {
			Device string `xml:"device,attr"`
			Source struct {
				File string `xml:"file,attr"`
			} `xml:"source"`
			Target struct {
				Dev string `xml:"dev,attr"`
				Bus string `xml:"bus,attr"`
			} `xml:"target"`
		} `xml:"disk"`
		Interfaces []struct {
			Type   string `xml:"type,attr"`
			Source struct {
				Network string `xml:"network,attr"`
				Bridge  string `xml:"bridge,attr"`
			} `xml:"source"`
			MAC struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
		} `xml:"interface"`
		Graphics []struct {
			Type string `xml:"type,attr"`
			Port int    `xml:"port,attr"`
		} `xml:"graphics"`
		Hostdevs []struct {
			Type   string `xml:"type,attr"`
			Source struct {
				Vendor struct {
					ID string `xml:"id,attr"`
				} `xml:"vendor"`
				Product struct {
					ID string `xml:"id,attr"`
				} `xml:"product"`
				Address struct {
					Domain   string `xml:"domain,attr"`
					Bus      string `xml:"bus,attr"`
					Slot     string `xml:"slot,attr"`
					Function string `xml:"function,attr"`
					Device   string `xml:"device,attr"`
				} `xml:"address"`
			} `xml:"source"`
		} `xml:"hostdev"`
	} `xml:"devices"`
}

func memMB(v int64, unit string) int {
	switch strings.ToLower(unit) {
	case "b", "bytes":
		return int(v / 1024 / 1024)
	case "mib", "m":
		return int(v)
	case "gib", "g":
		return int(v * 1024)
	default: // KiB
		return int(v / 1024)
	}
}

// Get returns the details of one VM.
func Get(name string) (*VM, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	out, err := virsh("dumpxml", name)
	if err != nil {
		return nil, err
	}
	var d domainXML
	if err := xml.Unmarshal([]byte(out), &d); err != nil {
		return nil, err
	}
	vm := &VM{Name: d.Name, UUID: d.UUID, VCPUs: d.VCPU, MemoryMB: memMB(d.Memory.Value, d.Memory.Unit), Disks: []Disk{}, NICs: []NIC{}, Firmware: "bios", USB: []USBDevice{}, PCI: []string{}}
	if d.OS.Fw == "efi" || strings.Contains(strings.ToLower(d.OS.Loader), "ovmf") {
		vm.Firmware = "uefi"
	}
	switch id := d.Metadata.Libosinfo.OS.ID; {
	case strings.Contains(id, "/win"):
		vm.OS = "windows"
	case id == "", strings.Contains(id, "/unknown"), strings.HasSuffix(id, "/generic"):
		vm.OS = "other"
	default:
		vm.OS = "linux"
	}
	for _, dk := range d.Devices.Disks {
		disk := Disk{Device: dk.Device, Target: dk.Target.Dev, Bus: dk.Target.Bus, Source: dk.Source.File}
		if dk.Device == "disk" && dk.Source.File != "" {
			if info, err := sys.Run("qemu-img", "info", "--force-share", "--output=json", dk.Source.File); err == nil {
				if m := regexp.MustCompile(`"virtual-size":\s*(\d+)`).FindStringSubmatch(info); m != nil {
					disk.Size, _ = strconv.ParseInt(m[1], 10, 64)
				}
			}
			if vm.Dir == "" {
				vm.Dir = filepath.Dir(dk.Source.File)
			}
		}
		vm.Disks = append(vm.Disks, disk)
	}
	for _, n := range d.Devices.Interfaces {
		nic := NIC{Type: n.Type, MAC: n.MAC.Address, Model: n.Model.Type, Source: n.Source.Network}
		if n.Type == "bridge" {
			nic.Source = n.Source.Bridge
		}
		vm.NICs = append(vm.NICs, nic)
	}
	for _, g := range d.Devices.Graphics {
		if g.Type == "vnc" && g.Port > 0 {
			vm.VNC = g.Port
		}
	}
	meta := readMeta(name)
	vm.Template, vm.Cloud = meta.Template == "yes", meta.Cloud
	for _, h := range d.Devices.Hostdevs {
		src := h.Source
		switch h.Type {
		case "usb":
			u := USBDevice{Vendor: trimHex(src.Vendor.ID), Product: trimHex(src.Product.ID)}
			for _, m := range meta.USB {
				if m.Vendor == u.Vendor && m.Product == u.Product {
					u.Serial = m.Serial
				}
			}
			vm.USB = append(vm.USB, u)
		case "pci":
			a := src.Address
			vm.PCI = append(vm.PCI, pciAddr(a.Domain, a.Bus, a.Slot, a.Function))
		}
	}
	vm.Snapshots = snapshotCount(name)
	vm.State, _ = virsh("domstate", name)
	vm.State = strings.TrimSpace(vm.State)
	if info, err := virsh("dominfo", name); err == nil {
		for _, l := range strings.Split(info, "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "Autostart" {
				vm.LibvirtAutostart = strings.TrimSpace(v) == "enable"
				vm.Autostart = vm.LibvirtAutostart
			}
		}
	}
	if vm.State == "running" {
		if out, err := virsh("domifaddr", name, "--source", "lease"); err == nil {
			for _, l := range strings.Split(out, "\n") {
				f := strings.Fields(l)
				if len(f) >= 4 && strings.Contains(f[3], "/") {
					for i := range vm.NICs {
						if strings.EqualFold(vm.NICs[i].MAC, f[1]) {
							vm.NICs[i].IP = strings.Split(f[3], "/")[0]
						}
					}
				}
			}
		}
	}
	return vm, nil
}

// List returns every defined VM.
func List() ([]VM, error) {
	out, err := virsh("list", "--all", "--name")
	if err != nil {
		return nil, err
	}
	vms := []VM{}
	for _, n := range strings.Fields(out) {
		if vm, err := Get(n); err == nil {
			vms = append(vms, *vm)
		}
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
	return vms, nil
}

type CreateReq struct {
	Name      string `json:"name"`
	OS        string `json:"os"` // linux | windows | other
	VCPUs     int    `json:"vcpus"`
	MemoryMB  int    `json:"memoryMb"`
	DiskGB    int    `json:"diskGb"`
	ISO       string `json:"iso"`
	Storage   string `json:"storage"` // folder under /srv/strata holding the VM disks
	Network   string `json:"network"` // "nat" or a bridge name
	Firmware  string `json:"firmware"`
	Autostart bool   `json:"autostart"`
}

// insideStrata validates a path below /srv/strata (no traversal).
func insideStrata(p string) (string, error) {
	c := filepath.Clean(p)
	if !strings.HasPrefix(c, "/srv/strata/") || strings.ContainsAny(c, "\n,") {
		return "", fmt.Errorf("%s must be inside a volume (/srv/strata/...)", p)
	}
	return c, nil
}

func validISO(p string) (string, error) {
	c, err := insideStrata(p)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(c), ".iso") {
		return "", fmt.Errorf("choose an .iso file")
	}
	if fi, err := os.Stat(c); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("ISO not found")
	}
	return c, nil
}

// qemuUser is the account QEMU runs as under libvirt on Debian.
const qemuUser = "libvirt-qemu"

// grantQemu lets QEMU reach a file or folder inside a shared folder, whose
// permissions (ACLs managed by StrataOS) normally keep other accounts out:
// search on every parent below /srv/strata, then read (ISO) or read/write
// (VM folder, inherited by the files created in it).
func grantQemu(p string, write bool) error {
	if !sys.Has("setfacl") {
		return nil
	}
	if _, err := user.Lookup(qemuUser); err != nil {
		return nil
	}
	for d := filepath.Dir(p); strings.HasPrefix(d, "/srv/strata"); d = filepath.Dir(d) {
		if _, err := sys.Run("setfacl", "-m", "u:"+qemuUser+":x", d); err != nil {
			return err
		}
	}
	if write {
		_, err := sys.Run("setfacl", "-R", "-m", "u:"+qemuUser+":rwX,d:u:"+qemuUser+":rwX", p)
		return err
	}
	_, err := sys.Run("setfacl", "-m", "u:"+qemuUser+":r", p)
	return err
}

var bridgeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,14}$`)

// newVMDir validates the common fields of a creation request and creates the
// VM folder (storage/<name>), readable by QEMU.
func newVMDir(r *CreateReq) (string, error) {
	if !Installed() {
		return "", fmt.Errorf("virtualisation is not installed")
	}
	if err := ValidName(r.Name); err != nil {
		return "", err
	}
	if _, err := virsh("dominfo", r.Name); err == nil {
		return "", fmt.Errorf("a VM named %s already exists", r.Name)
	}
	if r.VCPUs < 1 || r.VCPUs > 64 || r.MemoryMB < 256 || r.MemoryMB > 1024*1024 || r.DiskGB < 0 || r.DiskGB > 64*1024 {
		return "", fmt.Errorf("invalid CPU, memory or disk size")
	}
	storage, err := insideStrata(r.Storage)
	if err != nil {
		return "", err
	}
	if r.Network != "" && r.Network != "nat" && !bridgeRe.MatchString(r.Network) {
		return "", fmt.Errorf("invalid network")
	}
	dir := filepath.Join(storage, r.Name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("%s already exists", dir)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	if err := grantQemu(dir, true); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// diskBus is the bus of the VM disks: Windows has no virtio drivers out of
// the box, SATA works everywhere.
func diskBus(os string) string {
	if os == "windows" {
		return "sata"
	}
	return "virtio"
}

// installSpec is what differs between an ISO install, a cloud image and an import.
type installSpec struct {
	disks   []string // virt-install --disk values
	cdrom   string   // installation ISO
	import_ bool     // boot from the existing disks
	extra   []string // more virt-install arguments
}

// Create defines a VM with virt-install and boots it on its ISO.
func Create(ctx context.Context, r CreateReq, logf func(string, ...any)) error {
	if r.DiskGB < 1 {
		return fmt.Errorf("invalid CPU, memory or disk size")
	}
	dir, err := newVMDir(&r)
	if err != nil {
		return err
	}
	spec := installSpec{import_: true}
	disk := filepath.Join(dir, "disk0.qcow2")
	spec.disks = []string{fmt.Sprintf("path=%s,size=%d,format=qcow2,bus=%s,cache=none,discard=unmap", disk, r.DiskGB, diskBus(r.OS))}
	if r.ISO != "" {
		iso, err := validISO(r.ISO)
		if err == nil {
			err = grantQemu(iso, false)
		}
		if err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
		spec.cdrom, spec.import_ = iso, false
	}
	if err := install(ctx, r, spec, logf); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	return nil
}

// install runs virt-install. The VM starts right away unless spec says otherwise.
func install(ctx context.Context, r CreateReq, spec installSpec, logf func(string, ...any)) error {
	args := []string{"--connect", "qemu:///system", "--name", r.Name, "--memory", strconv.Itoa(r.MemoryMB), "--vcpus", strconv.Itoa(r.VCPUs),
		"--graphics", "vnc,listen=127.0.0.1", "--video", "virtio", "--noautoconsole", "--wait", "0", "--channel", "unix,target.type=virtio,target.name=org.qemu.guest_agent.0"}
	variant := "generic"
	switch r.OS {
	case "linux":
		variant = "linux2022"
	case "windows":
		variant = "win10"
	}
	args = append(args, "--os-variant", variant)
	for _, d := range spec.disks {
		args = append(args, "--disk", d)
	}
	if spec.cdrom != "" {
		args = append(args, "--cdrom", spec.cdrom)
	} else if spec.import_ {
		args = append(args, "--import")
	}
	model := "virtio"
	if r.OS == "windows" {
		model = "e1000e"
	}
	switch {
	case r.Network == "" || r.Network == "nat":
		ensureDefaultNetwork()
		args = append(args, "--network", "network=default,model="+model)
	case bridgeRe.MatchString(r.Network):
		if _, err := os.Stat("/sys/class/net/" + r.Network + "/bridge"); err != nil {
			return fmt.Errorf("%s is not a bridge (create one in Network)", r.Network)
		}
		args = append(args, "--network", "bridge="+r.Network+",model="+model)
	default:
		return fmt.Errorf("invalid network")
	}
	if r.Firmware == "uefi" {
		args = append(args, "--boot", "uefi")
		if r.OS == "windows" && sys.Has("swtpm") {
			args = append(args, "--tpm", "backend.type=emulator,backend.version=2.0,model=tpm-crb")
		}
	}
	args = append(args, spec.extra...)
	// Autostart is handled by StrataOS itself (ordered start), not by libvirt.
	logf("virt-install %s", strings.Join(args, " "))
	_, err := sys.RunOpts(ctx, sys.Opts{Timeout: 10 * time.Minute, Output: &prefixWriter{logf}}, "virt-install", args...)
	return err
}

type prefixWriter struct{ logf func(string, ...any) }

func (p *prefixWriter) Write(b []byte) (int, error) {
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			p.logf("%s", l)
		}
	}
	return len(b), nil
}

// ensureDefaultNetwork starts libvirt's NAT network.
func ensureDefaultNetwork() {
	_, _ = virsh("net-autostart", "default")
	_, _ = virsh("net-start", "default")
}

// Action runs start, shutdown (ACPI), destroy (power off), reboot, suspend or resume.
func Action(name, action string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	switch action {
	case "start", "shutdown", "destroy", "reboot", "suspend", "resume":
	default:
		return fmt.Errorf("unknown action")
	}
	if action == "start" {
		ensureDefaultNetwork()
		// Permissions of the shares may have been reset since the VM was created.
		if vm, err := Get(name); err == nil {
			if vm.Template {
				return fmt.Errorf("%s is a template: create a VM from it instead", name)
			}
			refreshUSB(vm)
			if err := prepareVFIO(vm); err != nil {
				return err
			}
			for _, d := range vm.Disks {
				if d.Source == "" || !strings.HasPrefix(d.Source, "/srv/strata/") {
					continue
				}
				if d.Device == "cdrom" {
					_ = grantQemu(d.Source, false)
				} else {
					_ = grantQemu(filepath.Dir(d.Source), true)
				}
			}
		}
	}
	_, err := virsh(action, name)
	return err
}

// Update changes vCPUs and memory of a stopped VM.
func Update(name string, vcpus, memMB int) error {
	vm, err := Get(name)
	if err != nil {
		return err
	}
	if vm.State != "shut off" {
		return fmt.Errorf("shut the VM down first")
	}
	if vcpus < 1 || vcpus > 64 || memMB < 256 {
		return fmt.Errorf("invalid values")
	}
	kib := strconv.Itoa(memMB * 1024)
	for _, args := range [][]string{
		{"setvcpus", name, strconv.Itoa(vcpus), "--config", "--maximum"},
		{"setvcpus", name, strconv.Itoa(vcpus), "--config"},
		{"setmaxmem", name, kib, "--config"},
		{"setmem", name, kib, "--config"},
	} {
		if _, err := virsh(args...); err != nil {
			return err
		}
	}
	return nil
}

// SetISO inserts (iso != "") or ejects the ISO of the VM's CD-ROM drive.
func SetISO(name, iso string) error {
	vm, err := Get(name)
	if err != nil {
		return err
	}
	target := ""
	for _, d := range vm.Disks {
		if d.Device == "cdrom" {
			target = d.Target
		}
	}
	if target == "" {
		return fmt.Errorf("this VM has no CD-ROM drive")
	}
	flags := []string{"--config"}
	if vm.State == "running" {
		flags = append(flags, "--live")
	}
	if iso == "" {
		_, err = virsh(append([]string{"change-media", name, target, "--eject", "--force"}, flags...)...)
		return err
	}
	p, err := validISO(iso)
	if err != nil {
		return err
	}
	if err := grantQemu(p, false); err != nil {
		return err
	}
	_, err = virsh(append([]string{"change-media", name, target, p, "--insert", "--force"}, flags...)...)
	return err
}

// Delete removes a VM, and its folder of disks when removeDisks is set.
func Delete(name string, removeDisks bool) error {
	vm, err := Get(name)
	if err != nil {
		return err
	}
	if vm.State != "shut off" {
		_, _ = virsh("destroy", name)
	}
	if _, err := virsh("undefine", name, "--nvram", "--snapshots-metadata", "--tpm"); err != nil {
		if _, err2 := virsh("undefine", name, "--nvram"); err2 != nil {
			return err
		}
	}
	_ = os.Remove(consoleLogPath(name))
	if removeDisks && vm.Dir != "" && strings.HasPrefix(vm.Dir, "/srv/strata/") && filepath.Base(vm.Dir) == name {
		return os.RemoveAll(vm.Dir)
	}
	return nil
}

// ISOs lists .iso files found under /srv/strata (depth-limited).
func ISOs() []string { return findFiles(map[string]bool{".iso": true}) }

// ImportableFiles lists the disk images and appliances that can be imported.
func ImportableFiles() []string {
	return findFiles(map[string]bool{".ova": true, ".ovf": true, ".vmdk": true, ".qcow2": true, ".img": true, ".raw": true, ".vhd": true, ".vhdx": true, ".vdi": true})
}

// findFiles walks the volumes (depth-limited, hidden folders and VM disk
// folders included only through their own names) for files with these extensions.
func findFiles(exts map[string]bool) []string {
	out := []string{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 4 || len(out) > 200 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				walk(p, depth+1)
			} else if e.Type().IsRegular() && exts[strings.ToLower(filepath.Ext(e.Name()))] {
				out = append(out, p)
			}
		}
	}
	walk("/srv/strata", 0)
	sort.Strings(out)
	return out
}

// Stats returns CPU time (ns) and memory in use (KiB) of running VMs.
func Stats() map[string][2]int64 {
	out, err := virsh("domstats", "--state-running", "--cpu-total", "--balloon")
	stats := map[string][2]int64{}
	if err != nil {
		return stats
	}
	cur := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Domain:") {
			cur = strings.Trim(strings.TrimSpace(strings.TrimPrefix(l, "Domain:")), "'")
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || cur == "" {
			continue
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		s := stats[cur]
		switch k {
		case "cpu.time":
			s[0] = n
		case "balloon.rss", "balloon.current":
			if s[1] == 0 || k == "balloon.rss" {
				s[1] = n
			}
		}
		stats[cur] = s
	}
	return stats
}
