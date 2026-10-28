package vms

// Moving VMs between the NAS of a cluster: what a migration needs to know of
// a VM and of each host, the definition rewritten for the target (Ceph
// secrets, bridges, disk paths), and `virsh migrate` over the TLS transport
// StrataOS sets up between members (internal/vmcluster/tls.go).

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// MigDisk is a disk of a VM as migration sees it.
type MigDisk struct {
	Device string `json:"device"`
	Target string `json:"target"`
	Source string `json:"source"`           // file path or rbd:<pool>/<image>
	Secret string `json:"secret,omitempty"` // UUID of the libvirt secret (RBD)
	Format string `json:"format,omitempty"` // qcow2 | raw (files)
	Size   int64  `json:"size,omitempty"`   // virtual size (files)
}

// MigInfo describes a VM for the migration pre-checks.
type MigInfo struct {
	Name      string    `json:"name"`
	State     string    `json:"state"`
	MemoryMB  int       `json:"memoryMb"`
	CPUMode   string    `json:"cpuMode"`
	CPUModel  string    `json:"cpuModel,omitempty"`
	Disks     []MigDisk `json:"disks"`
	NICs      []NIC     `json:"nics"`
	HostDevs  int       `json:"hostDevs"`
	Snapshots int       `json:"snapshots"`
	Template  bool      `json:"template"`
	Dir       string    `json:"dir,omitempty"`
	NVRAM     string    `json:"nvram,omitempty"`
}

type migXML struct {
	CPU struct {
		Mode  string `xml:"mode,attr"`
		Model string `xml:"model"`
	} `xml:"cpu"`
	Devices struct {
		Disks []struct {
			Type   string `xml:"type,attr"`
			Device string `xml:"device,attr"`
			Driver struct {
				Type string `xml:"type,attr"`
			} `xml:"driver"`
			Source struct {
				File     string `xml:"file,attr"`
				Dev      string `xml:"dev,attr"`
				Protocol string `xml:"protocol,attr"`
				Name     string `xml:"name,attr"`
			} `xml:"source"`
			Auth struct {
				Secret struct {
					UUID string `xml:"uuid,attr"`
				} `xml:"secret"`
			} `xml:"auth"`
			Target struct {
				Dev string `xml:"dev,attr"`
			} `xml:"target"`
		} `xml:"disk"`
		Hostdevs []struct{} `xml:"hostdev"`
	} `xml:"devices"`
}

// MigrationInfo gathers what the pre-checks need about a VM.
func MigrationInfo(name string) (*MigInfo, error) {
	vm, err := Get(name)
	if err != nil {
		return nil, err
	}
	out, err := virsh("dumpxml", name)
	if err != nil {
		return nil, err
	}
	var d migXML
	if err := xml.Unmarshal([]byte(out), &d); err != nil {
		return nil, err
	}
	mi := &MigInfo{Name: name, State: vm.State, MemoryMB: vm.MemoryMB, CPUMode: d.CPU.Mode, CPUModel: d.CPU.Model, NICs: vm.NICs,
		HostDevs: len(d.Devices.Hostdevs), Snapshots: vm.Snapshots, Template: vm.Template, Dir: vm.Dir, NVRAM: nvramOf(out)}
	if mi.CPUMode == "" && mi.CPUModel != "" {
		mi.CPUMode = "custom"
	}
	for _, dk := range d.Devices.Disks {
		md := MigDisk{Device: dk.Device, Target: dk.Target.Dev, Source: dk.Source.File, Format: dk.Driver.Type}
		switch {
		case dk.Source.Protocol == "rbd":
			md.Source, md.Secret = "rbd:"+dk.Source.Name, dk.Auth.Secret.UUID
		case dk.Type == "block":
			md.Source = dk.Source.Dev
		}
		if dk.Device == "disk" && md.Source != "" && dk.Type == "file" {
			if info, err := sys.Run("qemu-img", "info", "--force-share", "--output=json", md.Source); err == nil {
				if m := regexp.MustCompile(`"virtual-size":\s*(\d+)`).FindStringSubmatch(info); m != nil {
					md.Size, _ = strconv.ParseInt(m[1], 10, 64)
				}
			}
		}
		mi.Disks = append(mi.Disks, md)
	}
	return mi, nil
}

// CPUModelOf returns the named CPU model of a VM ("" for the host's CPU).
func CPUModelOf(name string) string {
	out, err := virsh("dumpxml", name, "--inactive")
	if err != nil {
		return ""
	}
	var d migXML
	if xml.Unmarshal([]byte(out), &d) != nil || (d.CPU.Mode != "custom" && d.CPU.Mode != "") {
		return ""
	}
	return d.CPU.Model
}

// HostCPU describes the processor of this NAS (vendor, family, model and
// flags, hashed): two NAS with the same signature run host-CPU VMs alike.
func HostCPU() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	keep := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		if strings.TrimSpace(l) == "" {
			break // first processor only
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "vendor_id", "cpu family", "model", "model name", "flags", "CPU implementer", "CPU part", "Features":
			keep[k] = v
		}
	}
	if len(keep) == 0 {
		return ""
	}
	keys := make([]string, 0, len(keep))
	for k := range keep {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, keep[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// UsableCPUModels lists the named CPU models KVM can run on this host.
func UsableCPUModels() []string {
	out, err := virsh("domcapabilities", "--virttype", "kvm")
	if err != nil {
		return nil
	}
	return parseUsableModels(out)
}

func parseUsableModels(doc string) []string {
	var d struct {
		Modes []struct {
			Name   string `xml:"name,attr"`
			Models []struct {
				Usable string `xml:"usable,attr"`
				Name   string `xml:",chardata"`
			} `xml:"model"`
		} `xml:"cpu>mode"`
	}
	if xml.Unmarshal([]byte(doc), &d) != nil {
		return nil
	}
	var out []string
	for _, m := range d.Modes {
		if m.Name != "custom" {
			continue
		}
		for _, x := range m.Models {
			if x.Usable == "yes" {
				out = append(out, strings.TrimSpace(x.Name))
			}
		}
	}
	return out
}

// ActiveNetworks lists the running libvirt networks.
func ActiveNetworks() []string {
	out, err := virsh("net-list", "--name")
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// Defined lists the names of every defined VM.
func Defined() []string {
	out, err := virsh("list", "--all", "--name")
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// FreeMemoryMB is the memory available for new VMs (MemAvailable).
func FreeMemoryMB() int {
	data, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(data), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemAvailable:" {
			kb, _ := strconv.Atoi(f[1])
			return kb / 1024
		}
	}
	return 0
}

// Rewrite says how a definition changes on its way to another NAS.
type Rewrite struct {
	Secrets map[string]string `json:"secrets,omitempty"` // libvirt secret UUID here → there
	Bridges map[string]string `json:"bridges,omitempty"`
	Paths   map[string]string `json:"paths,omitempty"` // disk file here → there
	// EjectMissing empties a CD-ROM drive whose image is not on this NAS.
	EjectMissing bool `json:"-"`
}

// rewriteDomain applies a Rewrite to a domain definition.
func rewriteDomain(doc string, rw Rewrite) (string, error) {
	root, err := parseXML(doc)
	if err != nil {
		return "", err
	}
	if root.name != "domain" {
		return "", fmt.Errorf("not a libvirt domain")
	}
	devs := root.child("devices")
	if devs == nil {
		return "", fmt.Errorf("domain without devices")
	}
	for _, d := range devs.children("disk") {
		if src := d.child("source"); src != nil {
			if p := rw.Paths[src.attr("file")]; p != "" {
				src.setAttr("file", p)
			}
			if f := src.attr("file"); rw.EjectMissing && d.attr("device") == "cdrom" && f != "" {
				if _, err := os.Stat(f); err != nil {
					d.remove(func(c *node) bool { return c == src })
				}
			}
		}
		for _, s := range d.path("auth/secret") {
			if u := rw.Secrets[s.attr("uuid")]; u != "" {
				s.setAttr("uuid", u)
			}
		}
		// Older libvirt puts the auth inside the source.
		for _, s := range d.path("source/auth/secret") {
			if u := rw.Secrets[s.attr("uuid")]; u != "" {
				s.setAttr("uuid", u)
			}
		}
	}
	for _, n := range devs.children("interface") {
		if n.attr("type") != "bridge" {
			continue
		}
		if src := n.child("source"); src != nil {
			if b := rw.Bridges[src.attr("bridge")]; b != "" {
				src.setAttr("bridge", b)
			}
		}
	}
	return root.String(), nil
}

// MigrationXML returns the definition of a VM (active, or the persistent
// one with inactive) rewritten for the target.
func MigrationXML(name string, inactive bool, rw Rewrite) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	args := []string{"dumpxml", name, "--migratable", "--security-info"}
	if inactive {
		args = append(args, "--inactive")
	}
	out, err := virsh(args...)
	if err != nil {
		return "", err
	}
	return rewriteDomain(out, rw)
}

// RewriteXML applies a Rewrite to a definition (an HA copy defined on
// another member).
func RewriteXML(doc string, rw Rewrite) (string, error) { return rewriteDomain(doc, rw) }

// DomainName returns the name in a definition, checked.
func DomainName(doc string) (string, error) {
	var d struct {
		XMLName xml.Name `xml:"domain"`
		Name    string   `xml:"name"`
	}
	if err := xml.Unmarshal([]byte(doc), &d); err != nil {
		return "", err
	}
	return d.Name, ValidName(d.Name)
}

// DomainMemoryMB returns the memory of a definition.
func DomainMemoryMB(doc string) int {
	var d domainXML
	if xml.Unmarshal([]byte(doc), &d) != nil {
		return 0
	}
	return memMB(d.Memory.Value, d.Memory.Unit)
}

// DomainSecrets lists the libvirt secrets a definition uses (RBD disks).
func DomainSecrets(doc string) []string {
	var d migXML
	if xml.Unmarshal([]byte(doc), &d) != nil {
		return nil
	}
	var out []string
	for _, dk := range d.Devices.Disks {
		if u := dk.Auth.Secret.UUID; u != "" && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}

// DomainRBD lists the RBD images ("pool/image") a definition uses.
func DomainRBD(doc string) []string {
	var out []string
	for _, d := range parseRBDDisks(doc) {
		if x, ok := strings.CutPrefix(d, "rbd:"); ok {
			out = append(out, x)
		}
	}
	return out
}

// RBDSecret is an RBD disk of a definition: its Ceph pool and secret.
type RBDSecret struct {
	Pool, Secret string
}

// DomainRBDSecrets lists the Ceph pools and secrets of the RBD disks.
func DomainRBDSecrets(doc string) []RBDSecret {
	var d migXML
	if xml.Unmarshal([]byte(doc), &d) != nil {
		return nil
	}
	var out []RBDSecret
	for _, dk := range d.Devices.Disks {
		if dk.Source.Protocol != "rbd" || dk.Auth.Secret.UUID == "" {
			continue
		}
		pool, _, _ := strings.Cut(dk.Source.Name, "/")
		out = append(out, RBDSecret{Pool: pool, Secret: dk.Auth.Secret.UUID})
	}
	return out
}

// DefineDoc defines a VM from a definition received from another member.
func DefineDoc(doc string) error {
	name, err := DomainName(doc)
	if err != nil {
		return err
	}
	if _, err := virsh("dominfo", name); err == nil {
		return fmt.Errorf("a VM named %s already exists", name)
	}
	return defineXML(doc)
}

// InactiveXML is the persistent definition of a VM (for its HA copies).
func InactiveXML(name string) (string, error) {
	return virsh("dumpxml", name, "--inactive", "--migratable", "--security-info")
}

// Undefine removes the definition of a VM, keeping its disks (it now lives
// on another member). A running copy is powered off first.
func Undefine(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if st, _ := virsh("domstate", name); strings.TrimSpace(st) != "shut off" {
		_, _ = virsh("destroy", name)
	}
	if _, err := virsh("undefine", name, "--keep-nvram", "--snapshots-metadata"); err != nil {
		if _, err2 := virsh("undefine", name); err2 != nil {
			return err
		}
	}
	return nil
}

// State returns the state of a VM ("" when it does not exist).
func State(name string) string {
	out, err := virsh("domstate", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// SetCPUModel changes the CPU of a stopped VM: a named model (cluster-safe)
// or "" for the processor of the host (host-passthrough).
func SetCPUModel(name, model string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if State(name) != "shut off" {
		return fmt.Errorf("shut the VM down first")
	}
	out, err := virsh("dumpxml", name, "--inactive", "--security-info")
	if err != nil {
		return err
	}
	doc, err := setCPU(out, model)
	if err != nil {
		return err
	}
	return defineXML(doc)
}

func setCPU(doc, model string) (string, error) {
	root, err := parseXML(doc)
	if err != nil {
		return "", err
	}
	if root.name != "domain" {
		return "", fmt.Errorf("not a libvirt domain")
	}
	cpu := el(`<cpu mode='host-passthrough' check='none' migratable='on'/>`)
	if model != "" {
		cpu = el(`<cpu mode='custom' match='exact' check='partial'><model fallback='forbid'></model></cpu>`)
		cpu.child("model").setText(model)
	}
	// Keep the topology the VM had.
	if old := root.child("cpu"); old != nil {
		if t := old.child("topology"); t != nil {
			cpu.kids = append(cpu.kids, t)
		}
		for i, k := range root.kids {
			if k == old {
				root.kids[i] = cpu
			}
		}
	} else {
		root.kids = append(root.kids, cpu)
	}
	return root.String(), nil
}

// DiskSpec is a disk the target creates before a migration with copy.
type DiskSpec struct {
	Path   string `json:"path"`
	Format string `json:"format"`
	Size   int64  `json:"size"`
}

// PrepareDisks creates empty disks for an incoming migration that copies
// storage, in a new VM folder.
func PrepareDisks(dir string, disks []DiskSpec) error {
	d, err := insideStrata(dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(d); err == nil {
		return fmt.Errorf("%s already exists on the target NAS", d)
	}
	if fi, err := os.Stat(filepath.Dir(d)); err != nil || !fi.IsDir() {
		return fmt.Errorf("the folder %s does not exist on the target NAS", filepath.Dir(d))
	}
	if err := os.MkdirAll(d, 0o750); err != nil {
		return err
	}
	if err := grantQemu(d, true); err != nil {
		_ = os.RemoveAll(d)
		return err
	}
	for _, x := range disks {
		p := filepath.Clean(x.Path)
		if filepath.Dir(p) != d || (x.Format != "qcow2" && x.Format != "raw") || x.Size <= 0 {
			_ = os.RemoveAll(d)
			return fmt.Errorf("invalid disk %s", x.Path)
		}
		if _, err := sys.Run("qemu-img", "create", "-f", x.Format, p, strconv.FormatInt(x.Size, 10)); err != nil {
			_ = os.RemoveAll(d)
			return err
		}
	}
	return nil
}

// DiscardDisks removes a folder made by PrepareDisks after a failed migration.
func DiscardDisks(dir string, name string) error {
	d, err := insideStrata(dir)
	if err != nil || filepath.Base(d) != name {
		return fmt.Errorf("invalid folder")
	}
	if slices.Contains(Defined(), name) {
		return fmt.Errorf("%s is defined here", name)
	}
	return os.RemoveAll(d)
}

// RemoveDir deletes the disk folder of a VM that moved to another NAS with
// its disks copied.
func RemoveDir(dir, name string) error {
	d, err := insideStrata(dir)
	if err != nil || filepath.Base(d) != name {
		return fmt.Errorf("invalid folder")
	}
	return os.RemoveAll(d)
}

// MigrateOpts is a live migration to another member.
type MigrateOpts struct {
	Dest        string   // address of the target in the cluster network
	XML         string   // active definition for the target
	Persistent  string   // persistent definition for the target
	CopyStorage bool     // copy the local disks
	CopyDisks   []string // their targets (vda…)
}

var percentRe = regexp.MustCompile(`\[\s*(\d+)\s*%\]`)

// progressWriter turns `virsh migrate --verbose` output into progress.
type progressWriter struct {
	set  func(float64)
	logf func(string, ...any)
	last int
}

func (w *progressWriter) Write(b []byte) (int, error) {
	for _, part := range strings.FieldsFunc(string(b), func(r rune) bool { return r == '\r' || r == '\n' }) {
		if m := percentRe.FindStringSubmatch(part); m != nil {
			p, _ := strconv.Atoi(m[1])
			if p != w.last {
				w.last = p
				w.set(float64(p))
				if p%25 == 0 {
					w.logf("Migration: %d %%", p)
				}
			}
		} else if s := strings.TrimSpace(part); s != "" {
			w.logf("%s", s)
		}
	}
	return len(b), nil
}

// Migrate moves a running VM to another member, live: libvirt connects to
// the target over TLS (qemu+tls), and the memory (and disks when copied)
// travel encrypted (--tls). The VM is defined there and undefined here
// once it runs there. Cancelling ctx aborts the migration (the VM keeps
// running here).
func Migrate(ctx context.Context, name string, o MigrateOpts, setProgress func(float64), logf func(string, ...any)) error {
	if err := ValidName(name); err != nil {
		return err
	}
	ip := net.ParseIP(o.Dest)
	if ip == nil {
		return fmt.Errorf("invalid target address")
	}
	host := ip.String()
	if ip.To4() == nil {
		host = "[" + host + "]"
	}
	dir, err := os.MkdirTemp("", "strata-migrate")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	xf, pf := filepath.Join(dir, "active.xml"), filepath.Join(dir, "persistent.xml")
	if err := os.WriteFile(xf, []byte(o.XML), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(pf, []byte(o.Persistent), 0o600); err != nil {
		return err
	}
	args := []string{"--connect", "qemu:///system", "migrate", "--live", "--p2p", "--tls", "--persistent", "--undefinesource", "--verbose", "--auto-converge",
		"--xml", xf, "--persistent-xml", pf, "--migrateuri", "tcp://" + host, "--tls-destination", ip.String()}
	if o.CopyStorage {
		args = append(args, "--copy-storage-all")
		if len(o.CopyDisks) > 0 {
			args = append(args, "--migrate-disks", strings.Join(o.CopyDisks, ","))
		}
	}
	args = append(args, name, "qemu+tls://"+host+"/system")
	logf("virsh %s", strings.Join(args[2:], " "))
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			logf("Cancelling the migration")
			_, _ = virsh("domjobabort", name)
			time.Sleep(10 * time.Second)
			cancel()
		case <-done:
		}
	}()
	w := &progressWriter{set: setProgress, logf: logf, last: -1}
	_, err = sys.RunOpts(runCtx, sys.Opts{Timeout: 6 * time.Hour, Output: w}, "virsh", args...)
	close(done)
	if ctx.Err() != nil {
		return fmt.Errorf("migration cancelled: the VM keeps running on this NAS")
	}
	return err
}

// ReadNVRAM returns the UEFI variables of a VM (copied with an offline move).
func ReadNVRAM(path string) ([]byte, error) {
	if path == "" || !strings.HasPrefix(filepath.Clean(path), "/var/lib/libvirt/qemu/nvram/") {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 16<<20))
}

// WriteNVRAM stores the UEFI variables of a VM received from another NAS.
func WriteNVRAM(path string, data []byte) error {
	p := filepath.Clean(path)
	if !strings.HasPrefix(p, "/var/lib/libvirt/qemu/nvram/") || len(data) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return err
	}
	if u, err := userLookup(qemuUser); err == nil {
		_ = os.Chown(p, u[0], u[1])
	}
	return nil
}

// userLookup returns the uid and gid of an account.
func userLookup(name string) ([2]int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return [2]int{}, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return [2]int{uid, gid}, nil
}

// QemuGID is the group of the account QEMU runs as (-1 when unknown).
func QemuGID() int {
	if ids, err := userLookup(qemuUser); err == nil {
		return ids[1]
	}
	return -1
}
