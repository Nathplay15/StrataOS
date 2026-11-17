package vms

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Linked clones are qcow2 overlays whose backing file is a disk of another VM
// (the source). Anything that writes the source's disks (starting it, a
// snapshot, a live backup, a disk change) silently corrupts the clones, so
// they are made from templates only, which refuse all of these. Clones made
// by earlier versions from ordinary VMs are reported (LinkedClones), never
// changed.

// LinkedClone is a VM disk that reads another VM's disk.
type LinkedClone struct {
	Clone  string `json:"clone"`  // the VM that holds the overlay
	Disk   string `json:"disk"`   // its disk (the overlay)
	Source string `json:"source"` // the VM whose disk it reads
	Base   string `json:"base"`   // that disk (the backing file)
	// SourceTemplate: the source is a template (the safe case).
	SourceTemplate bool `json:"sourceTemplate"`
}

// backingOf returns the absolute backing file of a disk image, "" when it has none.
func backingOf(path string) string {
	info, err := imageInfo(path)
	if err != nil {
		return ""
	}
	b := info.FullBacking
	if b == "" {
		b = info.BackingFile
	}
	if b == "" {
		return ""
	}
	if !filepath.IsAbs(b) {
		b = filepath.Join(filepath.Dir(path), b)
	}
	return filepath.Clean(b)
}

// ownsDir reports whether vm.Dir is the VM's own folder (created for it,
// named after it), not a shared one: the macOS image cache, a shared folder
// an imported disk sits in, a volume. [others] are the other VMs: none may
// have a disk in it.
func ownsDir(vm VM, name string, others []VM) bool {
	d := filepath.Clean(vm.Dir)
	if vm.Dir == "" || filepath.Base(d) != name || strings.Contains(d, cacheDirName) {
		return false
	}
	rel, ok := strings.CutPrefix(d, "/srv/strata/")
	// At least <volume>/<folder>/<name>: never a volume or a shared folder itself.
	if !ok || strings.Count(rel, "/") < 2 {
		return false
	}
	for _, o := range others {
		if o.Name == vm.Name {
			continue
		}
		for _, k := range o.Disks {
			if k.Source != "" && (k.Source == d || strings.HasPrefix(k.Source, d+"/")) {
				return false
			}
		}
	}
	return true
}

// findLinked lists the disks of [list] that read a disk of another VM of the
// list: the backing file is one of its disks, or lies in its own folder (an
// older state of its disks: external snapshots).
func findLinked(list []VM, backing func(string) string) []LinkedClone {
	var out []LinkedClone
	for _, v := range list {
		for _, d := range v.Disks {
			if d.Device != "disk" || d.Source == "" || strings.HasPrefix(d.Source, "rbd:") {
				continue
			}
			b := backing(d.Source)
			// The shared images of the cache (macOS) are no VM's disk.
			if b == "" || strings.Contains(b, "/"+cacheDirName+"/") {
				continue
			}
			for _, o := range list {
				if o.Name == v.Name {
					continue
				}
				hit := false
				for _, k := range o.Disks {
					hit = hit || (k.Device == "disk" && k.Source == b)
				}
				if !hit && ownsDir(o, o.Name, list) && strings.HasPrefix(b, filepath.Clean(o.Dir)+"/") {
					hit = true
				}
				if hit {
					out = append(out, LinkedClone{Clone: v.Name, Disk: d.Source, Source: o.Name, Base: b, SourceTemplate: o.Template})
					break
				}
			}
		}
	}
	return out
}

// LinkedClones lists the linked clones of every VM.
func LinkedClones() ([]LinkedClone, error) {
	list, err := List()
	if err != nil {
		return nil, err
	}
	return findLinked(list, backingOf), nil
}

// clonesOf returns the names of the linked clones of a VM.
func clonesOf(name string) ([]string, error) {
	all, err := LinkedClones()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range all {
		if c.Source == name && !slices.Contains(out, c.Clone) {
			out = append(out, c.Clone)
		}
	}
	return out, nil
}

// errTemplate is the answer to a change a template refuses.
func errTemplate(name string) error {
	return fmt.Errorf("%s is a template: its disks are the base of linked clones and cannot change (convert it back to a VM first)", name)
}
