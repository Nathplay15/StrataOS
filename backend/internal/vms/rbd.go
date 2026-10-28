package vms

// VM disks on Ceph RBD: a libvirt storage pool of type rbd per Ceph pool,
// its cephx key in a libvirt secret. A VM created with Storage
// "rbd:<libvirt pool>" gets a raw RBD image as its disk.

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RBDPrefix marks a CreateReq.Storage that names a libvirt RBD pool.
const RBDPrefix = "rbd:"

var poolNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// ValidPoolName checks a libvirt pool name.
func ValidPoolName(n string) error {
	if !poolNameRe.MatchString(n) {
		return fmt.Errorf("invalid storage pool name %q", n)
	}
	return nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// DefineRBDPool creates the libvirt secret (from keyFile, which holds the
// base64 cephx key) and the storage pool, started and autostarted.
func DefineRBDPool(name, poolXML, secretXML, secretUUID, keyFile string) error {
	if !Installed() {
		return fmt.Errorf("virtualisation is not installed")
	}
	if err := ValidPoolName(name); err != nil {
		return err
	}
	if !uuidRe.MatchString(secretUUID) {
		return fmt.Errorf("invalid secret UUID")
	}
	if _, err := virsh("pool-info", name); err == nil {
		return fmt.Errorf("a storage pool named %s already exists", name)
	}
	dir, err := os.MkdirTemp("", "strata-rbdpool")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	sx, px := filepath.Join(dir, "secret.xml"), filepath.Join(dir, "pool.xml")
	if err := os.WriteFile(sx, []byte(secretXML), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(px, []byte(poolXML), 0o600); err != nil {
		return err
	}
	if _, err := virsh("secret-define", sx); err != nil {
		return err
	}
	if _, err := virsh("secret-set-value", "--secret", secretUUID, "--file", keyFile); err != nil {
		_, _ = virsh("secret-undefine", secretUUID)
		return err
	}
	if _, err := virsh("pool-define", px); err != nil {
		_, _ = virsh("secret-undefine", secretUUID)
		return err
	}
	if _, err := virsh("pool-start", name); err != nil {
		_, _ = virsh("pool-undefine", name)
		_, _ = virsh("secret-undefine", secretUUID)
		return err
	}
	_, _ = virsh("pool-autostart", name)
	return nil
}

// RemoveRBDPool removes a pool defined by DefineRBDPool (its images stay in
// Ceph) and its secret.
func RemoveRBDPool(name, secretUUID string) error {
	if err := ValidPoolName(name); err != nil {
		return err
	}
	if vols := PoolVolumes(name); len(vols) > 0 {
		return fmt.Errorf("the storage pool %s still holds VM disks: %s", name, strings.Join(vols, ", "))
	}
	_, _ = virsh("pool-destroy", name)
	if _, err := virsh("pool-undefine", name); err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	if uuidRe.MatchString(secretUUID) {
		_, _ = virsh("secret-undefine", secretUUID)
	}
	return nil
}

// PoolActive reports whether a libvirt pool is running.
func PoolActive(name string) bool {
	out, err := virsh("pool-info", name)
	return err == nil && strings.Contains(out, "running")
}

// PoolVolumes lists the volumes of a libvirt pool.
func PoolVolumes(name string) []string {
	if PoolActive(name) {
		_, _ = virsh("pool-refresh", name)
	}
	out, err := virsh("vol-list", name, "--name")
	if err != nil {
		return nil
	}
	var vols []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			vols = append(vols, l)
		}
	}
	return vols
}

// createOnPool creates a VM whose disk is an image of a libvirt RBD pool.
func createOnPool(ctx context.Context, r CreateReq, pool string, logf func(string, ...any)) error {
	if !Installed() {
		return fmt.Errorf("virtualisation is not installed")
	}
	if err := ValidName(r.Name); err != nil {
		return err
	}
	if err := ValidPoolName(pool); err != nil {
		return err
	}
	if !PoolActive(pool) {
		return fmt.Errorf("the storage pool %s is not running (check the Ceph cluster)", pool)
	}
	if _, err := virsh("dominfo", r.Name); err == nil {
		return fmt.Errorf("a VM named %s already exists", r.Name)
	}
	if r.VCPUs < 1 || r.VCPUs > 64 || r.MemoryMB < 256 || r.MemoryMB > 1024*1024 || r.DiskGB < 1 || r.DiskGB > 64*1024 {
		return fmt.Errorf("invalid CPU, memory or disk size")
	}
	if r.Network != "" && r.Network != "nat" && !bridgeRe.MatchString(r.Network) {
		return fmt.Errorf("invalid network")
	}
	vol := r.Name + "-disk0"
	if _, err := virsh("vol-create-as", pool, vol, fmt.Sprintf("%dG", r.DiskGB), "--format", "raw"); err != nil {
		return err
	}
	cleanup := func() { _, _ = virsh("vol-delete", "--pool", pool, vol) }
	spec := installSpec{import_: true}
	spec.disks = []string{fmt.Sprintf("vol=%s/%s,bus=%s,cache=none,discard=unmap", pool, vol, diskBus(r.OS))}
	if r.ISO != "" {
		iso, err := validISO(r.ISO)
		if err == nil {
			err = grantQemu(iso, false)
		}
		if err != nil {
			cleanup()
			return err
		}
		spec.cdrom, spec.import_ = iso, false
	}
	logf("Disk %s/%s on Ceph (%d GB)", pool, vol, r.DiskGB)
	if err := install(ctx, r, spec, logf); err != nil {
		cleanup()
		return err
	}
	return nil
}

// rbdDisks lists the RBD images used as disks by a VM (pool/image).
func rbdDisks(name string) []string {
	out, err := virsh("dumpxml", name)
	if err != nil {
		return nil
	}
	return parseRBDDisks(out)
}

func parseRBDDisks(domXML string) []string {
	var d struct {
		Disks []struct {
			Type   string `xml:"type,attr"`
			Device string `xml:"device,attr"`
			Source struct {
				Protocol string `xml:"protocol,attr"`
				Name     string `xml:"name,attr"`
				Pool     string `xml:"pool,attr"`
				Volume   string `xml:"volume,attr"`
			} `xml:"source"`
		} `xml:"devices>disk"`
	}
	if xml.Unmarshal([]byte(domXML), &d) != nil {
		return nil
	}
	var out []string
	for _, dk := range d.Disks {
		switch {
		case dk.Device != "disk":
		case dk.Type == "network" && dk.Source.Protocol == "rbd" && dk.Source.Name != "":
			out = append(out, "rbd:"+dk.Source.Name)
		case dk.Type == "volume" && dk.Source.Pool != "" && dk.Source.Volume != "":
			out = append(out, "vol:"+dk.Source.Pool+"/"+dk.Source.Volume)
		}
	}
	return out
}

// deleteRBDDisks deletes the Ceph disks of a deleted VM.
func deleteRBDDisks(disks []string) error {
	var errs []string
	for _, d := range disks {
		var err error
		switch {
		case strings.HasPrefix(d, "vol:"):
			p, v, _ := strings.Cut(strings.TrimPrefix(d, "vol:"), "/")
			_, err = virsh("vol-delete", "--pool", p, v)
		case strings.HasPrefix(d, "rbd:"):
			// The volume key of an RBD volume is pool/image.
			_, err = virsh("vol-delete", strings.TrimPrefix(d, "rbd:"))
		}
		if err != nil {
			errs = append(errs, d+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("some Ceph disks could not be deleted: %s", strings.Join(errs, "; "))
	}
	return nil
}
