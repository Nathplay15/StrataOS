package vms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cloneOpts describes how a domain definition is rewritten for a copy.
type cloneOpts struct {
	Name    string
	OldDir  string            // folder of the source VM
	NewDir  string            // folder of the copy
	Disks   map[string]string // source disk path -> copy (qcow2)
	NVRAM   string            // NVRAM file of the copy ("" = let libvirt create one)
	Seed    string            // cloud-init seed of the copy ("" = eject seeds of the source)
	MAC     func() string
	KeepUSB bool // keep USB passthrough (VM restored from a backup)
	// KeepIdentity keeps UUID and MAC addresses (a VM restored in place of the original).
	KeepIdentity bool
}

// rewriteClone turns the inactive XML of a VM into the definition of a copy:
// new name, no UUID (libvirt generates one, hence a new TPM state too), new
// MAC addresses, copied disks (flattened qcow2, without the backing chain of
// external snapshots), own NVRAM, no PCI passthrough and no template flag.
func rewriteClone(doc string, o cloneOpts) (string, error) {
	root, err := parseXML(doc)
	if err != nil {
		return "", err
	}
	if root.name != "domain" {
		return "", fmt.Errorf("not a libvirt domain")
	}
	root.delAttr("id")
	root.ensure("name").setText(o.Name)
	if !o.KeepIdentity {
		root.remove(func(c *node) bool { return c.name == "uuid" })
	}
	if md := root.child("metadata"); md != nil {
		md.remove(func(c *node) bool {
			return strings.HasSuffix(c.name, ":vm") && c.attr("xmlns:"+strings.TrimSuffix(c.name, ":vm")) == StrataNS
		})
	}
	if osn := root.child("os"); osn != nil {
		if nv := osn.child("nvram"); nv != nil {
			if o.NVRAM != "" {
				nv.setText(o.NVRAM)
			} else {
				osn.remove(func(c *node) bool { return c == nv })
			}
		}
	}
	devs := root.child("devices")
	if devs == nil {
		return "", fmt.Errorf("domain without devices")
	}
	for _, d := range devs.children("disk") {
		src := d.child("source")
		file := ""
		if src != nil {
			file = src.attr("file")
		}
		switch {
		case d.attr("device") == "disk" && file != "":
			n, ok := o.Disks[file]
			if !ok {
				return "", fmt.Errorf("disk %s was not copied", file)
			}
			src.setAttr("file", n)
			d.setAttr("type", "file")
			if drv := d.child("driver"); drv != nil {
				drv.setAttr("type", "qcow2")
			}
			d.remove(func(c *node) bool { return c.name == "backingStore" })
		case d.attr("device") == "cdrom" && file != "" && o.OldDir != "" && strings.HasPrefix(file, o.OldDir+"/"):
			// The cloud-init seed of the source: the copy gets its own (new
			// instance id, so cloud-init renames it and regenerates its SSH host keys).
			if o.Seed != "" {
				src.setAttr("file", o.Seed)
			} else {
				d.remove(func(c *node) bool { return c.name == "source" })
			}
		}
	}
	for _, n := range devs.children("interface") {
		if !o.KeepIdentity || n.child("mac") == nil {
			n.ensure("mac").setAttr("address", o.MAC())
		}
		n.remove(func(c *node) bool { return c.name == "target" || c.name == "alias" })
	}
	devs.remove(func(c *node) bool {
		return c.name == "hostdev" && (c.attr("type") == "pci" || !o.KeepUSB)
	})
	// Runtime paths (guest agent socket) and files of the source folder (console log).
	root.walk(func(n *node) {
		if n.name == "channel" {
			if s := n.child("source"); s != nil {
				s.delAttr("path")
			}
		}
		for _, a := range n.attrs {
			if o.OldDir != "" && strings.HasPrefix(a.Value, o.OldDir+"/") && o.NewDir != "" {
				n.setAttr(rawName(a.Name), o.NewDir+strings.TrimPrefix(a.Value, o.OldDir))
			}
		}
	})
	return root.String(), nil
}

// nvramPath is where libvirt keeps the UEFI variables of a VM.
func nvramPath(name string) string {
	return filepath.Join("/var/lib/libvirt/qemu/nvram", name+"_VARS.fd")
}

// nvramOf returns the NVRAM file declared in a domain definition.
func nvramOf(doc string) string {
	root, err := parseXML(doc)
	if err != nil {
		return ""
	}
	for _, n := range root.path("os/nvram") {
		return n.text()
	}
	return ""
}

// CloneReq asks for a full copy of a stopped VM (or a template).
type CloneReq struct {
	Name    string `json:"name"`
	Storage string `json:"storage"` // folder that will hold the copy ("" = next to the source)
	Start   bool   `json:"start"`
}

// Clone copies a stopped VM: disks converted into the new folder, new identity.
// cloning a template gives an ordinary VM.
func Clone(ctx context.Context, src string, r CloneReq, p Progress) error {
	if err := ValidName(r.Name); err != nil {
		return err
	}
	vm, err := Get(src)
	if err != nil {
		return err
	}
	if vm.State != "shut off" {
		return fmt.Errorf("shut the VM down first")
	}
	if _, err := virsh("dominfo", r.Name); err == nil {
		return fmt.Errorf("a VM named %s already exists", r.Name)
	}
	storage := r.Storage
	if storage == "" {
		if vm.Dir == "" {
			return fmt.Errorf("choose where to store the copy")
		}
		storage = filepath.Dir(vm.Dir)
	}
	if storage, err = insideStrata(storage); err != nil {
		return err
	}
	doc, err := virsh("dumpxml", "--inactive", src)
	if err != nil {
		return err
	}
	dir := filepath.Join(storage, r.Name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s already exists", dir)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := grantQemu(dir, true); err != nil {
		return err
	}
	o := cloneOpts{Name: r.Name, OldDir: vm.Dir, NewDir: dir, Disks: map[string]string{}, MAC: randomMAC}
	var disks []Disk
	for _, d := range vm.Disks {
		if d.Device == "disk" && d.Source != "" {
			disks = append(disks, d)
		}
	}
	for i, d := range disks {
		dst := filepath.Join(dir, fmt.Sprintf("disk%d.qcow2", i))
		p.log("Copying %s", d.Source)
		if err := convertImage(ctx, d.Source, "", dst, false, p.scaled(float64(i)*95/float64(len(disks)), float64(i+1)*95/float64(len(disks)))); err != nil {
			return err
		}
		o.Disks[d.Source] = dst
	}
	if nv := nvramOf(doc); nv != "" {
		if _, err := os.Stat(nv); err == nil {
			o.NVRAM = nvramPath(r.Name)
			if err := copyFile(nv, o.NVRAM, 0o600); err != nil {
				return err
			}
		}
	}
	if vm.Dir != "" {
		if seed, err := cloneSeed(vm.Dir, dir, r.Name); err != nil {
			p.log("cloud-init seed not copied: %v", err)
		} else {
			o.Seed = seed
		}
	}
	out, err := rewriteClone(doc, o)
	if err != nil {
		return err
	}
	if err := defineXML(out); err != nil {
		if o.NVRAM != "" {
			_ = os.Remove(o.NVRAM)
		}
		return err
	}
	ok = true
	if vm.Cloud != "" {
		_ = writeMeta(r.Name, strataMeta{Cloud: vm.Cloud})
	}
	p.set(100)
	if r.Start {
		return Action(r.Name, "start")
	}
	return nil
}

// SetTemplate converts a stopped VM into a template (or back into a VM).
// Templates cannot start; their libvirt autostart is turned off.
func SetTemplate(name string, on bool) error {
	vm, err := Get(name)
	if err != nil {
		return err
	}
	if on && vm.State != "shut off" {
		return fmt.Errorf("shut the VM down first")
	}
	m := readMeta(name)
	m.Template = ""
	if on {
		m.Template = "yes"
		_, _ = virsh("autostart", name, "--disable")
	}
	return writeMeta(name, m)
}
