package vms

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// USB passthrough. Devices are given to VMs by vendor:product (libvirt finds
// them again after they are unplugged and plugged back, whatever their new
// address); the serial number is remembered too, to tell identical devices
// apart (two identical Zigbee dongles) by pinning the current address at start.

type USBDevice struct {
	Vendor   string `json:"vendor"`  // 4 hex digits
	Product  string `json:"product"` // 4 hex digits
	Serial   string `json:"serial,omitempty"`
	Name     string `json:"name,omitempty"`
	Bus      int    `json:"bus,omitempty"`
	Device   int    `json:"device,omitempty"`
	Speed    string `json:"speed,omitempty"`
	UsedBy   string `json:"usedBy,omitempty"` // VM the device is given to
	Missing  bool   `json:"missing,omitempty"`
	Internal bool   `json:"internal,omitempty"` // hub, or the keyboard/disk the NAS may need
}

var usbIDRe = regexp.MustCompile(`^[0-9a-f]{4}$`)

func trimHex(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x"))
	for len(s) < 4 && s != "" {
		s = "0" + s
	}
	return s
}

// USBRoot is where the kernel lists USB devices (a variable for the tests).
var USBRoot = "/sys/bus/usb/devices"

func readSys(dir, f string) string {
	b, err := os.ReadFile(filepath.Join(dir, f))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// HostUSB lists the USB devices plugged into the NAS (hubs excluded).
func HostUSB() []USBDevice {
	out := []USBDevice{}
	entries, _ := os.ReadDir(USBRoot)
	for _, e := range entries {
		if strings.Contains(e.Name(), ":") { // interfaces
			continue
		}
		d := filepath.Join(USBRoot, e.Name())
		v, p := trimHex(readSys(d, "idVendor")), trimHex(readSys(d, "idProduct"))
		if !usbIDRe.MatchString(v) || !usbIDRe.MatchString(p) || readSys(d, "bDeviceClass") == "09" {
			continue
		}
		u := USBDevice{Vendor: v, Product: p, Serial: readSys(d, "serial"), Speed: readSys(d, "speed")}
		u.Bus, _ = strconv.Atoi(readSys(d, "busnum"))
		u.Device, _ = strconv.Atoi(readSys(d, "devnum"))
		u.Name = strings.TrimSpace(readSys(d, "manufacturer") + " " + readSys(d, "product"))
		if u.Name == "" {
			u.Name = "USB device " + v + ":" + p
		}
		// Mass storage (class 08 on an interface) may hold a volume of the NAS.
		if m, _ := filepath.Glob(filepath.Join(d, e.Name()+":*", "bInterfaceClass")); len(m) > 0 {
			for _, f := range m {
				if c, _ := os.ReadFile(f); strings.TrimSpace(string(c)) == "08" {
					u.Internal = true
				}
			}
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bus != out[j].Bus {
			return out[i].Bus < out[j].Bus
		}
		return out[i].Device < out[j].Device
	})
	return out
}

// usbHostdevXML is the libvirt definition of a USB passthrough. The address
// (bus > 0) pins one of several identical devices; startupPolicy lets the VM
// start while the device is unplugged.
func usbHostdevXML(vendor, product string, bus, dev int) string {
	addr := ""
	if bus > 0 && dev > 0 {
		addr = fmt.Sprintf("<address bus='%d' device='%d'/>", bus, dev)
	}
	return fmt.Sprintf("<hostdev mode='subsystem' type='usb' managed='yes'><source startupPolicy='optional'><vendor id='0x%s'/><product id='0x%s'/>%s</source></hostdev>", vendor, product, addr)
}

// pickUSB finds the plugged device to pin: by serial when several share the IDs.
func pickUSB(host []USBDevice, vendor, product, serial string) (bus, dev int) {
	var same []USBDevice
	for _, h := range host {
		if h.Vendor == vendor && h.Product == product {
			same = append(same, h)
		}
	}
	if len(same) < 2 {
		return 0, 0
	}
	for _, h := range same {
		if serial != "" && h.Serial == serial {
			return h.Bus, h.Device
		}
	}
	return same[0].Bus, same[0].Device
}

func deviceFlags(state string) []string {
	if state == "running" || state == "paused" {
		return []string{"--config", "--live"}
	}
	return []string{"--config"}
}

func attachXML(name, doc string, flags []string) error {
	f, err := os.CreateTemp("", "strata-dev-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, _ = f.WriteString(doc)
	f.Close()
	_, err = virsh(append([]string{"attach-device", name, f.Name()}, flags...)...)
	return err
}

func detachXML(name, doc string, flags []string) error {
	f, err := os.CreateTemp("", "strata-dev-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, _ = f.WriteString(doc)
	f.Close()
	_, err = virsh(append([]string{"detach-device", name, f.Name()}, flags...)...)
	return err
}

// AttachUSB gives a host USB device to a VM (hot-plugged when it runs, and kept
// in its definition).
func AttachUSB(name, vendor, product, serial string) error {
	vendor, product = trimHex(vendor), trimHex(product)
	if !usbIDRe.MatchString(vendor) || !usbIDRe.MatchString(product) {
		return fmt.Errorf("invalid USB device")
	}
	if len(serial) > 128 || strings.ContainsAny(serial, "<>&'\"\n") {
		return fmt.Errorf("invalid serial number")
	}
	vm, err := Get(name)
	if err != nil {
		return err
	}
	for _, u := range vm.USB {
		if u.Vendor == vendor && u.Product == product {
			return fmt.Errorf("this device is already given to the VM")
		}
	}
	bus, dev := pickUSB(HostUSB(), vendor, product, serial)
	if err := attachXML(name, usbHostdevXML(vendor, product, bus, dev), deviceFlags(vm.State)); err != nil {
		return err
	}
	m := readMeta(name)
	m.USB = append(m.USB, usbMeta{Vendor: vendor, Product: product, Serial: serial})
	return writeMeta(name, m)
}

// usbHostdevs returns the USB hostdev elements of a definition matching the IDs.
func usbHostdevs(doc, vendor, product string) []*node {
	root, err := parseXML(doc)
	if err != nil {
		return nil
	}
	var out []*node
	for _, h := range root.path("devices/hostdev") {
		if h.attr("type") != "usb" {
			continue
		}
		src := h.child("source")
		if src == nil || src.child("vendor") == nil || src.child("product") == nil {
			continue
		}
		if trimHex(src.child("vendor").attr("id")) == vendor && trimHex(src.child("product").attr("id")) == product {
			h.remove(func(c *node) bool { return c.name == "alias" || c.name == "address" })
			out = append(out, h)
		}
	}
	return out
}

// DetachUSB takes a USB device back from a VM.
func DetachUSB(name, vendor, product string) error {
	vendor, product = trimHex(vendor), trimHex(product)
	if !usbIDRe.MatchString(vendor) || !usbIDRe.MatchString(product) {
		return fmt.Errorf("invalid USB device")
	}
	vm, err := Get(name)
	if err != nil {
		return err
	}
	if vm.State == "running" || vm.State == "paused" {
		if live, err := virsh("dumpxml", name); err == nil {
			for _, x := range usbHostdevs(live, vendor, product) {
				if err := detachXML(name, x.String(), []string{"--live"}); err != nil {
					return err
				}
			}
		}
	}
	conf, err := virsh("dumpxml", "--inactive", name)
	if err != nil {
		return err
	}
	for _, x := range usbHostdevs(conf, vendor, product) {
		if err := detachXML(name, x.String(), []string{"--config"}); err != nil {
			return err
		}
	}
	m := readMeta(name)
	kept := m.USB[:0]
	for _, u := range m.USB {
		if u.Vendor != vendor || u.Product != product {
			kept = append(kept, u)
		}
	}
	m.USB = kept
	return writeMeta(name, m)
}

// refreshUSB re-pins identical devices by serial number before a VM starts
// (their bus address changes when they are plugged in again).
func refreshUSB(vm *VM) {
	m := readMeta(vm.Name)
	if len(m.USB) == 0 {
		return
	}
	host := HostUSB()
	conf, err := virsh("dumpxml", "--inactive", vm.Name)
	if err != nil {
		return
	}
	for _, u := range m.USB {
		if u.Serial == "" {
			continue
		}
		bus, dev := pickUSB(host, u.Vendor, u.Product, u.Serial)
		for _, x := range usbHostdevs(conf, u.Vendor, u.Product) {
			var cb, cd int
			if a := x.child("source").child("address"); a != nil {
				cb, _ = strconv.Atoi(a.attr("bus"))
				cd, _ = strconv.Atoi(a.attr("device"))
			}
			if cb == bus && cd == dev {
				continue
			}
			if detachXML(vm.Name, x.String(), []string{"--config"}) == nil {
				_ = attachXML(vm.Name, usbHostdevXML(u.Vendor, u.Product, bus, dev), []string{"--config"})
			}
		}
	}
}

// USBUsers maps "vendor:product" to the VM that has the device.
func USBUsers(list []VM) map[string]string {
	out := map[string]string{}
	for _, vm := range list {
		for _, u := range vm.USB {
			out[u.Vendor+":"+u.Product] = vm.Name
		}
	}
	return out
}
