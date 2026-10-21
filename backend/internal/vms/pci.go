package vms

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

// PCI passthrough (GPU, HBA, NIC…). A device can only be given to a VM with
// its whole IOMMU group, bound to the vfio-pci driver: the host loses it.
// Devices the NAS itself needs (boot console, network in use, disks holding
// data) are refused, as is every device of their group.

type PCIDevice struct {
	Address   string `json:"address"` // 0000:01:00.0
	Vendor    string `json:"vendor"`
	Device    string `json:"device"`
	Class     string `json:"class"` // 6 hex digits
	ClassName string `json:"className,omitempty"`
	Name      string `json:"name,omitempty"`
	Driver    string `json:"driver,omitempty"`
	Group     int    `json:"group"`               // -1 without IOMMU
	Protected string `json:"protected,omitempty"` // why the NAS keeps it
	UsedBy    string `json:"usedBy,omitempty"`
}

type IOMMUStatus struct {
	Enabled  bool   `json:"enabled"` // groups exist
	Groups   int    `json:"groups"`
	CPU      string `json:"cpu"`      // intel | amd | other
	Params   string `json:"params"`   // kernel parameters to add
	Cmdline  bool   `json:"cmdline"`  // already on the kernel command line
	Grub     bool   `json:"grub"`     // GRUB can be configured from here
	Pending  bool   `json:"pending"`  // set in GRUB, reboot needed
	Supports bool   `json:"supports"` // CPU virtualisation flags present
}

// PCIRoot and the other paths are variables for the tests.
var (
	PCIRoot     = "/sys/bus/pci/devices"
	IOMMURoot   = "/sys/kernel/iommu_groups"
	GrubDefault = "/etc/default/grub"
)

var pciAddrRe = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

func pciAddr(domain, bus, slot, fn string) string {
	h := func(s string, w int) string {
		n, _ := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 32)
		return fmt.Sprintf("%0*x", w, n)
	}
	return h(domain, 4) + ":" + h(bus, 2) + ":" + h(slot, 2) + "." + h(fn, 1)
}

// pciHostdevXML is the libvirt definition of a PCI passthrough.
func pciHostdevXML(addr string) (string, error) {
	if !pciAddrRe.MatchString(addr) {
		return "", fmt.Errorf("invalid PCI address")
	}
	d, rest, _ := strings.Cut(addr, ":")
	b, rest, _ := strings.Cut(rest, ":")
	s, f, _ := strings.Cut(rest, ".")
	return fmt.Sprintf("<hostdev mode='subsystem' type='pci' managed='yes'><source><address domain='0x%s' bus='0x%s' slot='0x%s' function='0x%s'/></source></hostdev>", d, b, s, f), nil
}

var lspciField = regexp.MustCompile(`"([^"]*)"`)

// parseLspci reads `lspci -mm -D` lines into address -> [class, vendor, device] names.
func parseLspci(out string) map[string][3]string {
	m := map[string][3]string{}
	for _, l := range strings.Split(out, "\n") {
		addr, rest, ok := strings.Cut(strings.TrimSpace(l), " ")
		if !ok || !pciAddrRe.MatchString(addr) {
			continue
		}
		f := lspciField.FindAllStringSubmatch(rest, -1)
		if len(f) >= 3 {
			m[addr] = [3]string{f[0][1], f[1][1], f[2][1]}
		}
	}
	return m
}

func linkBase(p string) string {
	t, err := os.Readlink(p)
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

// IOMMU returns whether passthrough is possible and how to enable it.
func IOMMU() IOMMUStatus {
	st := IOMMUStatus{CPU: "other"}
	if e, err := os.ReadDir(IOMMURoot); err == nil {
		st.Groups = len(e)
		st.Enabled = len(e) > 0
	}
	cpu, _ := os.ReadFile("/proc/cpuinfo")
	switch {
	case strings.Contains(string(cpu), "GenuineIntel"):
		st.CPU, st.Params = "intel", "intel_iommu=on iommu=pt"
	case strings.Contains(string(cpu), "AuthenticAMD"):
		st.CPU, st.Params = "amd", "amd_iommu=on iommu=pt"
	}
	st.Supports = strings.Contains(string(cpu), " vmx") || strings.Contains(string(cpu), " svm")
	cmd, _ := os.ReadFile("/proc/cmdline")
	st.Cmdline = st.Params != "" && hasParams(string(cmd), st.Params)
	if _, err := os.Stat(GrubDefault); err == nil && sys.Has("update-grub") && st.Params != "" {
		st.Grub = true
		if g, err := os.ReadFile(GrubDefault); err == nil {
			_, changed := addKernelParams(string(g), st.Params)
			st.Pending = !changed && !st.Cmdline
		}
	}
	return st
}

func hasParams(cmdline, params string) bool {
	have := map[string]bool{}
	for _, f := range strings.Fields(cmdline) {
		have[f] = true
	}
	for _, p := range strings.Fields(params) {
		if !have[p] {
			return false
		}
	}
	return true
}

var grubLineRe = regexp.MustCompile(`(?m)^GRUB_CMDLINE_LINUX_DEFAULT=(["']?)(.*?)(["']?)\s*$`)

// addKernelParams adds parameters to GRUB_CMDLINE_LINUX_DEFAULT of /etc/default/grub.
func addKernelParams(grub, params string) (string, bool) {
	m := grubLineRe.FindStringSubmatchIndex(grub)
	if m == nil {
		return strings.TrimRight(grub, "\n") + "\nGRUB_CMDLINE_LINUX_DEFAULT=\"" + params + "\"\n", true
	}
	cur := grub[m[4]:m[5]]
	var add []string
	for _, p := range strings.Fields(params) {
		if !hasParams(cur, p) {
			add = append(add, p)
		}
	}
	if len(add) == 0 {
		return grub, false
	}
	val := strings.TrimSpace(cur + " " + strings.Join(add, " "))
	return grub[:m[0]] + `GRUB_CMDLINE_LINUX_DEFAULT="` + val + `"` + grub[m[1]:], true
}

// EnableIOMMU adds the IOMMU parameters to the kernel command line (GRUB).
// It takes effect at the next reboot.
func EnableIOMMU() (string, error) {
	st := IOMMU()
	if !st.Grub {
		return "", fmt.Errorf("the kernel command line cannot be changed here (no GRUB, or unknown CPU): enable the IOMMU (VT-d / AMD-Vi) in the firmware and add %q to the boot parameters by hand", st.Params)
	}
	g, err := os.ReadFile(GrubDefault)
	if err != nil {
		return "", err
	}
	out, changed := addKernelParams(string(g), st.Params)
	if !changed {
		return "The parameters are already set: reboot the NAS", nil
	}
	if err := os.WriteFile(GrubDefault+".strata-bak", g, 0o644); err != nil {
		return "", err
	}
	if err := sys.WriteFileAtomic(GrubDefault, []byte(out), 0o644); err != nil {
		return "", err
	}
	if _, err := sys.Run("update-grub"); err != nil {
		_ = os.WriteFile(GrubDefault, g, 0o644)
		_, _ = sys.Run("update-grub")
		return "", err
	}
	return "IOMMU parameters added (" + st.Params + "): reboot the NAS, and enable VT-d / AMD-Vi in the firmware if it is not", nil
}

// usedBlockDevices returns the disks (sda, nvme0n1…) holding a file system,
// a partition table, a RAID/LVM/ZFS member or a mount.
func blockInUse(dev string) bool {
	out, err := sys.Run("lsblk", "-nrpo", "FSTYPE,MOUNTPOINT,PTTYPE", "/dev/"+dev)
	if err != nil {
		return true // unknown: keep it
	}
	return strings.TrimSpace(out) != ""
}

// protection tells why the NAS must keep a PCI device ("" = it may be passed through).
func protection(addr, class string) string {
	dir := filepath.Join(PCIRoot, addr)
	if strings.HasPrefix(class, "06") {
		return "PCI bridge"
	}
	if readSys(dir, "boot_vga") == "1" {
		return "graphics card of the NAS console"
	}
	if nets, _ := os.ReadDir(filepath.Join(dir, "net")); len(nets) > 0 {
		for _, n := range nets {
			if s := readSys(filepath.Join("/sys/class/net", n.Name()), "operstate"); s != "down" {
				return "network interface " + n.Name() + " in use"
			}
		}
	}
	blocks, _ := os.ReadDir("/sys/block")
	for _, b := range blocks {
		t, err := filepath.EvalSymlinks(filepath.Join("/sys/block", b.Name()))
		if err != nil || !strings.Contains(t, "/"+addr+"/") {
			continue
		}
		if blockInUse(b.Name()) {
			return "controller of disk " + b.Name() + " (holds data)"
		}
	}
	return ""
}

// HostPCI lists the PCI devices with their IOMMU group and whether they may be passed through.
func HostPCI() []PCIDevice {
	names := map[string][3]string{}
	if out, err := sys.Run("lspci", "-mm", "-D"); err == nil {
		names = parseLspci(out)
	}
	out := []PCIDevice{}
	entries, _ := os.ReadDir(PCIRoot)
	for _, e := range entries {
		addr := e.Name()
		if !pciAddrRe.MatchString(addr) {
			continue
		}
		dir := filepath.Join(PCIRoot, addr)
		d := PCIDevice{Address: addr, Vendor: trimHex(readSys(dir, "vendor")), Device: trimHex(readSys(dir, "device")), Group: -1}
		d.Class = strings.TrimPrefix(readSys(dir, "class"), "0x")
		d.Driver = linkBase(filepath.Join(dir, "driver"))
		if g := linkBase(filepath.Join(dir, "iommu_group")); g != "" {
			d.Group, _ = strconv.Atoi(g)
		}
		if n, ok := names[addr]; ok {
			d.ClassName, d.Name = n[0], strings.TrimSpace(n[1]+" "+n[2])
		}
		if d.Driver != "vfio-pci" {
			d.Protected = protection(addr, d.Class)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

func isBridge(class string) bool {
	return strings.HasPrefix(class, "0604") || strings.HasPrefix(class, "0600")
}

// groupOf returns the devices sharing the IOMMU group of addr.
func groupOf(all []PCIDevice, addr string) ([]PCIDevice, error) {
	var dev *PCIDevice
	for i := range all {
		if all[i].Address == addr {
			dev = &all[i]
		}
	}
	if dev == nil {
		return nil, fmt.Errorf("PCI device %s not found", addr)
	}
	if dev.Group < 0 {
		return nil, fmt.Errorf("the IOMMU is off: enable it first")
	}
	var g []PCIDevice
	for _, d := range all {
		if d.Group == dev.Group {
			g = append(g, d)
		}
	}
	return g, nil
}

// checkGroup refuses a group holding a device the NAS needs (bridges excepted).
func checkGroup(g []PCIDevice) error {
	for _, d := range g {
		if d.Protected != "" && !isBridge(d.Class) {
			return fmt.Errorf("refused: %s (%s) is in the same IOMMU group and is needed by the NAS (%s)", d.Address, d.Name, d.Protected)
		}
	}
	return nil
}

func writeSys(path, v string) error {
	return os.WriteFile(path, []byte(v), 0o200)
}

// bindVFIO gives every device of the group (bridges excepted) to vfio-pci.
func bindVFIO(g []PCIDevice) error {
	_, _ = sys.Run("modprobe", "vfio-pci")
	for _, d := range g {
		if isBridge(d.Class) || d.Driver == "vfio-pci" {
			continue
		}
		dir := filepath.Join(PCIRoot, d.Address)
		if err := writeSys(filepath.Join(dir, "driver_override"), "vfio-pci"); err != nil {
			return err
		}
		if d.Driver != "" {
			if err := writeSys(filepath.Join(dir, "driver", "unbind"), d.Address); err != nil {
				return err
			}
		}
		if err := writeSys("/sys/bus/pci/drivers_probe", d.Address); err != nil {
			return err
		}
	}
	return nil
}

// releaseVFIO gives the devices of a group back to their host drivers.
func releaseVFIO(g []PCIDevice) {
	for _, d := range g {
		if isBridge(d.Class) {
			continue
		}
		dir := filepath.Join(PCIRoot, d.Address)
		_ = writeSys(filepath.Join(dir, "driver_override"), "\n")
		if d.Driver == "vfio-pci" {
			_ = writeSys(filepath.Join(dir, "driver", "unbind"), d.Address)
		}
		_ = writeSys("/sys/bus/pci/drivers_probe", d.Address)
	}
}

// prepareVFIO binds the IOMMU groups of a VM's PCI devices before it starts.
func prepareVFIO(vm *VM) error {
	if len(vm.PCI) == 0 {
		return nil
	}
	all := HostPCI()
	for _, a := range vm.PCI {
		g, err := groupOf(all, a)
		if err != nil {
			return err
		}
		if err := checkGroup(g); err != nil {
			return err
		}
		if err := bindVFIO(g); err != nil {
			return err
		}
	}
	return nil
}

// AttachPCI adds a PCI device to a stopped VM, its group bound to vfio-pci.
func AttachPCI(name, addr string) error {
	doc, err := pciHostdevXML(addr)
	if err != nil {
		return err
	}
	vm, err := Get(name)
	if err != nil {
		return err
	}
	if vm.State != "shut off" {
		return fmt.Errorf("shut the VM down first")
	}
	for _, a := range vm.PCI {
		if a == addr {
			return fmt.Errorf("this device is already given to the VM")
		}
	}
	g, err := groupOf(HostPCI(), addr)
	if err != nil {
		return err
	}
	if err := checkGroup(g); err != nil {
		return err
	}
	if err := bindVFIO(g); err != nil {
		return err
	}
	return attachXML(name, doc, []string{"--config"})
}

// DetachPCI removes a PCI device from a stopped VM and gives its group back to the host.
func DetachPCI(name, addr string) error {
	doc, err := pciHostdevXML(addr)
	if err != nil {
		return err
	}
	vm, err := Get(name)
	if err != nil {
		return err
	}
	if vm.State != "shut off" {
		return fmt.Errorf("shut the VM down first")
	}
	if err := detachXML(name, doc, []string{"--config"}); err != nil {
		return err
	}
	if g, err := groupOf(HostPCI(), addr); err == nil {
		releaseVFIO(g)
	}
	return nil
}
