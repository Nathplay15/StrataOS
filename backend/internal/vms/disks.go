package vms

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

// Extra disks: a qcow2 image is created in the VM's folder and attached to the
// definition (live too when the VM runs); detaching keeps the image file, so
// the disk can be attached back later.

// diskXML is the definition used to attach or detach a disk.
func diskXML(path, target, bus, format string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "'", "&apos;", `"`, "&quot;")
	if format == "" {
		format = "qcow2"
	}
	return fmt.Sprintf(`<disk type='file' device='disk'><driver name='qemu' type='%s' cache='none' discard='unmap'/><source file='%s'/><target dev='%s' bus='%s'/></disk>`,
		format, esc.Replace(path), target, bus)
}

// AttachDisk creates a sizeGB qcow2 disk in the VM's folder and attaches it.
func AttachDisk(name string, sizeGB int) error {
	if !Installed() {
		return fmt.Errorf("virtualisation is not installed")
	}
	if sizeGB < 1 || sizeGB > 64*1024 {
		return fmt.Errorf("invalid disk size")
	}
	vm, err := Get(name)
	if err != nil {
		return err
	}
	if vm.Dir == "" {
		return fmt.Errorf("the VM has no managed disk folder")
	}
	bus := "virtio"
	for _, d := range vm.Disks {
		if d.Device == "disk" {
			bus = d.Bus
			break
		}
	}
	prefix := "sd"
	switch bus {
	case "virtio":
		prefix = "vd"
	case "ide":
		prefix = "hd"
	}
	used := map[string]bool{}
	for _, d := range vm.Disks {
		used[d.Target] = true
	}
	target := ""
	for i := 0; i < 26; i++ {
		c := fmt.Sprintf("%s%c", prefix, rune('a'+i))
		if !used[c] {
			target = c
			break
		}
	}
	if target == "" {
		return fmt.Errorf("no free disk slot on the %s bus", bus)
	}
	n := 0
	for {
		if _, err := os.Stat(filepath.Join(vm.Dir, fmt.Sprintf("disk%d.qcow2", n))); err != nil {
			break
		}
		n++
	}
	disk := filepath.Join(vm.Dir, fmt.Sprintf("disk%d.qcow2", n))
	if _, err := sys.Run("qemu-img", "create", "-f", "qcow2", disk, strconv.Itoa(sizeGB)+"G"); err != nil {
		return err
	}
	if err := grantQemu(disk, true); err != nil {
		_ = os.Remove(disk)
		return err
	}
	if err := attachXML(name, diskXML(disk, target, bus, "qcow2"), deviceFlags(vm.State)); err != nil {
		_ = os.Remove(disk)
		return err
	}
	return nil
}

// DetachDisk removes a disk from the definition; the image file is kept in the
// VM's folder.
func DetachDisk(name, target string) error {
	vm, err := Get(name)
	if err != nil {
		return err
	}
	disks := 0
	var disk Disk
	for _, d := range vm.Disks {
		if d.Device != "disk" {
			continue
		}
		disks++
		if d.Target == target {
			disk = d
		}
	}
	if disk.Target == "" {
		return fmt.Errorf("no disk %s on this VM", target)
	}
	if disks == 1 {
		return fmt.Errorf("the last disk cannot be removed")
	}
	return detachXML(name, diskXML(disk.Source, disk.Target, disk.Bus, disk.Format), deviceFlags(vm.State))
}

// Rename renames a shut-off VM: the libvirt definition, the disk paths and the
// VM's folder (when it is managed).
func Rename(old, name string) error {
	if old == name {
		return fmt.Errorf("the VM already has this name")
	}
	if err := ValidName(name); err != nil {
		return err
	}
	vm, err := Get(old)
	if err != nil {
		return err
	}
	if vm.State != "shut off" {
		return fmt.Errorf("shut the VM down first")
	}
	if _, err := virsh("dominfo", name); err == nil {
		return fmt.Errorf("a VM named %s already exists", name)
	}
	oldDir, newDir := vm.Dir, ""
	if oldDir != "" && strings.HasPrefix(oldDir, "/srv/strata/") {
		newDir = filepath.Join(filepath.Dir(oldDir), name)
		if _, err := os.Stat(newDir); err == nil {
			return fmt.Errorf("%s already exists", newDir)
		}
	}
	if _, err := virsh("domrename", old, name); err != nil {
		return err
	}
	if newDir != "" {
		if err := os.Rename(oldDir, newDir); err != nil {
			_, _ = virsh("domrename", name, old)
			return err
		}
		// The definition still points at the old folder: rewrite its paths.
		out, err := virsh("dumpxml", name)
		if err != nil {
			return err
		}
		def := strings.ReplaceAll(out, oldDir+"/", newDir+"/")
		f, err := os.CreateTemp("", "strata-vm-*.xml")
		if err != nil {
			return err
		}
		_, _ = f.WriteString(def)
		f.Close()
		defer os.Remove(f.Name())
		if _, err := virsh("define", f.Name()); err != nil {
			return err
		}
	}
	return nil
}
