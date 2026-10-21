// Package iscsi serves block storage over iSCSI with the Linux kernel target (LIO).
//
// Why configfs rather than targetcli: StrataOS writes the LIO configfs tree
// (/sys/kernel/config/target) itself. Every write is a plain system call whose
// error is the kernel's own, a target is rebuilt in a few milliseconds without
// touching the other targets (only its initiators reconnect), and the plan of
// writes is pure data that unit tests check. targetcli runs one Python process
// per command, and its exit status does not reliably report failures.
// Persistence uses the standard Debian path: after every change `targetctl save`
// (python3-rtslib-fb) dumps the live tree to /etc/rtslib-fb-target/saveconfig.json,
// which rtslib-fb-targetctl.service restores at boot, before and independently
// of StrataOS. The daemon then checks each target against its state and
// rebuilds any that the boot restore missed (a zvol that appeared late…).
package iscsi

import (
	"crypto/rand"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"strataos/internal/store"
	"strataos/internal/sys"
)

var (
	ConfigFS    = "/sys/kernel/config/target"
	SaveFile    = "/etc/rtslib-fb-target/saveconfig.json"
	Service     = "rtslib-fb-targetctl.service"
	StorageRoot = "/srv/strata"
)

// DefaultPort is the registered iSCSI port.
const DefaultPort = 3260

// IQNDate is the date part of the names StrataOS generates.
const IQNDate = "2026-01"

const maxLUNs = 64

// Installed reports whether the persistence tools (targetcli-fb / rtslib) are present.
func Installed() bool {
	if sys.Has("targetctl") {
		return true
	}
	_, err := os.Stat("/usr/bin/targetctl")
	return err == nil
}

// Supported reports whether the running kernel ships the iSCSI target module.
func Supported() bool {
	if _, err := os.Stat(ConfigFS + "/iscsi"); err == nil {
		return true
	}
	_, err := sys.Run("modprobe", "-n", "iscsi_target_mod")
	return err == nil
}

// ---- Names ----

var (
	// RFC 3720 §3.2.6.3: iqn.yyyy-mm.reversed.domain[:anything], after the
	// stringprep normalisation of RFC 3722 (lower case); 223 bytes at most.
	iqnRe = regexp.MustCompile(`^iqn\.[0-9]{4}-(0[1-9]|1[0-2])\.[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[a-z0-9.:-]+)?$`)
	euiRe = regexp.MustCompile(`^eui\.[0-9a-f]{16}$`)
	naaRe = regexp.MustCompile(`^naa\.([0-9a-f]{16}|[0-9a-f]{32})$`)
	// Target names end up in IQNs and backstore names ("strata_<name>_<lun>"): no underscore.
	nameRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	chapRe   = regexp.MustCompile(`^[\x21-\x7e]+$`)
	serialRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
	// fileio paths go into the kernel's comma-separated control string.
	filePathRe = regexp.MustCompile(`^/[A-Za-z0-9._/@+-]+$`)
	vgLVRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}/[A-Za-z0-9_-]{1,32}$`)
	zvolRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*(/[A-Za-z0-9][A-Za-z0-9_.:-]*)+$`)
)

// Normalize returns the canonical (lower case) form of an iSCSI name.
func Normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidIQN checks an iSCSI name: iqn., eui. or naa. format (RFC 3720), lower case.
func ValidIQN(s string) error {
	if len(s) == 0 || len(s) > 223 {
		return fmt.Errorf("invalid iSCSI name %q", s)
	}
	// rtslib also wants a dot after the date part (iqn.yyyy-mm.x.y…): one name
	// it rejects makes targetctl fail on the whole configuration.
	if iqnRe.MatchString(s) && strings.Contains(s[len("iqn.2026-01."):], ".") || euiRe.MatchString(s) || naaRe.MatchString(s) {
		return nil
	}
	return fmt.Errorf("invalid iSCSI name %q (expected iqn.yyyy-mm.reversed.domain:name)", s)
}

// ValidName checks a target's short name.
func ValidName(s string) error {
	if !nameRe.MatchString(s) {
		return fmt.Errorf("invalid target name %q (lower-case letters, digits and -, 32 characters at most)", s)
	}
	return nil
}

// Authority turns a host name into the reversed domain of the IQNs:
// nas.home.lan → lan.home.nas, nas → local.nas.
func Authority(hostname string) string {
	var labels []string
	for _, l := range strings.Split(strings.ToLower(hostname), ".") {
		l = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
				return r
			}
			return -1
		}, l)
		if l = strings.Trim(l, "-"); l != "" {
			labels = append(labels, l)
		}
	}
	if len(labels) == 0 {
		labels = []string{"strataos"}
	}
	// A single-label host name gets the mDNS domain: rtslib (targetctl, which
	// saves and restores the configuration) rejects an IQN without a second dot.
	if len(labels) == 1 {
		labels = append(labels, "local")
	}
	slices.Reverse(labels)
	return strings.Join(labels, ".")
}

// Prefix is the start of the IQNs generated on this host.
func Prefix(hostname string) string { return "iqn." + IQNDate + "." + Authority(hostname) }

// GenerateIQN returns iqn.2026-01.<reversed hostname>:<name>.
func GenerateIQN(hostname, name string) string { return Prefix(hostname) + ":" + name }

// NewSerial returns a random unit serial (UUID form, as rtslib makes them).
func NewSerial() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ---- Validation ----

// ValidCHAP checks the credentials (all empty = no CHAP).
func ValidCHAP(c store.ISCSICHAP) error {
	if c.User == "" {
		if c.Secret != "" || c.MutualUser != "" || c.MutualSecret != "" {
			return fmt.Errorf("CHAP needs a user name")
		}
		return nil
	}
	if len(c.User) > 255 || !chapRe.MatchString(c.User) {
		return fmt.Errorf("invalid CHAP user name")
	}
	if err := validSecret(c.Secret); err != nil {
		return err
	}
	if c.MutualUser == "" && c.MutualSecret == "" {
		return nil
	}
	if c.MutualUser == "" || len(c.MutualUser) > 255 || !chapRe.MatchString(c.MutualUser) {
		return fmt.Errorf("invalid mutual CHAP user name")
	}
	if err := validSecret(c.MutualSecret); err != nil {
		return err
	}
	if c.MutualSecret == c.Secret {
		return fmt.Errorf("the mutual CHAP secret must differ from the initiator secret")
	}
	return nil
}

func validSecret(s string) error {
	// RFC 3720 asks for 96 bits at least; Windows accepts 12 to 16 characters.
	if len(s) < 12 || len(s) > 255 || !chapRe.MatchString(s) {
		return fmt.Errorf("CHAP secrets need 12 to 255 characters, without spaces")
	}
	return nil
}

// Portal returns the configfs name of the target's portal ("0.0.0.0:3260", "[::]:3260").
func Portal(t store.ISCSITarget) string {
	ip, port := t.PortalIP, t.Port
	if ip == "" {
		ip = "0.0.0.0"
	}
	if port == 0 {
		port = DefaultPort
	}
	if strings.Contains(ip, ":") {
		return "[" + ip + "]:" + strconv.Itoa(port)
	}
	return ip + ":" + strconv.Itoa(port)
}

func wildcard(ip string) bool { return ip == "" || ip == "0.0.0.0" || ip == "::" }

// Validate normalises a target and checks it against the other targets.
func Validate(t *store.ISCSITarget, others []*store.ISCSITarget) error {
	t.Name = Normalize(t.Name)
	t.IQN = Normalize(t.IQN)
	if err := ValidName(t.Name); err != nil {
		return err
	}
	if err := ValidIQN(t.IQN); err != nil {
		return err
	}
	if !strings.HasPrefix(t.IQN, "iqn.") {
		return fmt.Errorf("a target name must start with iqn.")
	}
	t.PortalIP = strings.TrimSpace(t.PortalIP)
	if t.PortalIP == "0.0.0.0" {
		t.PortalIP = ""
	}
	if t.PortalIP != "" {
		a, err := netip.ParseAddr(strings.Trim(t.PortalIP, "[]"))
		if err != nil || a.Zone() != "" {
			return fmt.Errorf("invalid portal address %q", t.PortalIP)
		}
		t.PortalIP = a.String()
	}
	if t.Port == 0 {
		t.Port = DefaultPort
	}
	if t.Port != DefaultPort && t.Port != 860 && (t.Port < 1024 || t.Port > 65535 || t.Port == 7070 || t.Port == 7443) {
		return fmt.Errorf("invalid portal port %d", t.Port)
	}
	var inits []string
	for _, i := range t.Initiators {
		i = Normalize(i)
		if i == "" {
			continue
		}
		if err := ValidIQN(i); err != nil {
			return err
		}
		if !slices.Contains(inits, i) {
			inits = append(inits, i)
		}
	}
	if len(inits) > 256 {
		return fmt.Errorf("too many initiators")
	}
	t.Initiators = inits
	if err := ValidCHAP(t.CHAP); err != nil {
		return err
	}
	if len(t.LUNs) > maxLUNs {
		return fmt.Errorf("a target holds %d LUNs at most", maxLUNs)
	}
	seen := map[int]bool{}
	backing := map[string]bool{}
	allRO, anyRO := len(t.LUNs) > 0, false
	for _, l := range t.LUNs {
		if l.LUN < 0 || l.LUN > 255 || seen[l.LUN] {
			return fmt.Errorf("invalid or duplicate LUN number %d", l.LUN)
		}
		seen[l.LUN] = true
		if backing[l.Kind+":"+l.Path] {
			return fmt.Errorf("%s is used by two LUNs", l.Path)
		}
		backing[l.Kind+":"+l.Path] = true
		if err := ValidLUN(l); err != nil {
			return err
		}
		allRO = allRO && l.ReadOnly
		anyRO = anyRO || l.ReadOnly
	}
	// Without ACLs the kernel only knows one write protection for the whole target.
	if t.AllowAny && anyRO && !allRO {
		return fmt.Errorf("read-only and writable LUNs can only be mixed with an initiator list")
	}
	for _, o := range others {
		if o.ID == t.ID {
			continue
		}
		if o.Name == t.Name {
			return fmt.Errorf("a target named %s already exists", t.Name)
		}
		if o.IQN == t.IQN {
			return fmt.Errorf("the name %s is already used", t.IQN)
		}
		// A wildcard and a precise address cannot both listen on one port.
		if o.Port == t.Port && wildcard(o.PortalIP) != wildcard(t.PortalIP) {
			return fmt.Errorf("port %d is already used by %s on another address", t.Port, o.Name)
		}
		for _, ol := range o.LUNs {
			for _, l := range t.LUNs {
				if ol.Kind == l.Kind && ol.Path == l.Path {
					return fmt.Errorf("%s is already exported by %s", l.Path, o.Name)
				}
			}
		}
	}
	return nil
}

// ValidLUN checks the backing store reference of a LUN (not its existence).
func ValidLUN(l *store.ISCSILUN) error {
	switch l.Kind {
	case "fileio":
		p := filepath.Clean(l.Path)
		if p != l.Path || !filePathRe.MatchString(p) || !strings.HasPrefix(p, StorageRoot+"/") || strings.Count(p, "/") < 4 {
			return fmt.Errorf("invalid image file %q (it must be inside a volume, e.g. %s/main/iscsi/disk.img)", l.Path, StorageRoot)
		}
		if l.Size < 1<<20 || l.Size > 1<<50 || l.Size%512 != 0 {
			return fmt.Errorf("invalid size (1 MiB to 1 PiB, a multiple of 512 bytes)")
		}
	case "lvm":
		if !vgLVRe.MatchString(l.Path) {
			return fmt.Errorf("invalid logical volume %q (group/volume)", l.Path)
		}
		l.WriteCache = false
	case "zvol":
		if !zvolRe.MatchString(l.Path) || len(l.Path) > 200 || strings.Contains(l.Path, "..") {
			return fmt.Errorf("invalid ZFS volume %q (pool/volume)", l.Path)
		}
		l.WriteCache = false
	default:
		return fmt.Errorf("unknown backing store %q", l.Kind)
	}
	if !serialRe.MatchString(l.Serial) {
		return fmt.Errorf("invalid unit serial")
	}
	return nil
}

// Device is what the kernel opens for a LUN: the image file or the block device.
func Device(l store.ISCSILUN) string {
	switch l.Kind {
	case "lvm":
		return "/dev/" + l.Path
	case "zvol":
		return "/dev/zvol/" + l.Path
	}
	return l.Path
}

// Backstore is the name of a LUN's LIO storage object.
func Backstore(t store.ISCSITarget, l store.ISCSILUN) string {
	return fmt.Sprintf("strata_%s_%d", t.Name, l.LUN)
}

func plugin(l store.ISCSILUN) string {
	if l.Kind == "fileio" {
		return "fileio"
	}
	return "iblock"
}

// ---- Configuration plan ----

// Op is one configfs operation, relative to ConfigFS.
type Op struct {
	Kind     string // mkdir | write | link
	Path     string
	Value    string // write: the content; link: what the link points to (relative to ConfigFS)
	Optional bool   // attributes that older kernels or some backstores lack
}

func (o Op) String() string {
	switch o.Kind {
	case "write":
		return fmt.Sprintf("echo %q > %s", o.Value, o.Path)
	case "link":
		return fmt.Sprintf("ln -s %s %s", o.Value, o.Path)
	}
	return o.Kind + " " + o.Path
}

// Plan lists the operations that create a target: its storage objects (in
// the HBA directories given by hba, e.g. "fileio_3"), then the target, its
// portal, LUNs, ACLs and authentication, enabled last.
func Plan(t store.ISCSITarget, hba func(l store.ISCSILUN) string) []Op {
	var ops []Op
	mkdir := func(p string) { ops = append(ops, Op{Kind: "mkdir", Path: p}) }
	write := func(p, v string) { ops = append(ops, Op{Kind: "write", Path: p, Value: v}) }
	opt := func(p, v string) { ops = append(ops, Op{Kind: "write", Path: p, Value: v, Optional: true}) }
	link := func(p, to string) { ops = append(ops, Op{Kind: "link", Path: p, Value: to}) }

	so := map[int]string{}
	for _, lp := range t.LUNs {
		l := *lp
		h := "core/" + hba(l)
		dir := h + "/" + Backstore(t, l)
		so[l.LUN] = dir
		mkdir(h)
		mkdir(dir)
		dev := Device(l)
		if l.Kind == "fileio" {
			write(dir+"/control", fmt.Sprintf("fd_dev_name=%s,fd_dev_size=%d", dev, l.Size))
			if l.WriteCache {
				write(dir+"/control", "fd_buffered_io=1")
			}
		} else {
			write(dir+"/control", "udev_path="+dev)
			if l.ReadOnly {
				write(dir+"/control", "readonly=1")
			}
		}
		write(dir+"/udev_path", dev)
		write(dir+"/enable", "1")
		write(dir+"/wwn/vpd_unit_serial", l.Serial)
		if l.WriteCache {
			opt(dir+"/attrib/emulate_write_cache", "1")
		}
		// UNMAP lets the initiator give freed blocks back (sparse files, zvols).
		opt(dir+"/attrib/emulate_tpu", "1")
	}

	tpg := "iscsi/" + t.IQN + "/tpgt_1"
	mkdir("iscsi/" + t.IQN)
	mkdir(tpg)
	for _, l := range t.LUNs {
		lun := fmt.Sprintf("%s/lun/lun_%d", tpg, l.LUN)
		mkdir(lun)
		link(lun+"/"+Backstore(t, *l), so[l.LUN])
	}
	mkdir(tpg + "/np/" + Portal(t))
	chap := t.CHAP.User != ""
	allRO := len(t.LUNs) > 0
	for _, l := range t.LUNs {
		allRO = allRO && l.ReadOnly
	}
	write(tpg+"/attrib/authentication", b01(chap))
	write(tpg+"/attrib/generate_node_acls", b01(t.AllowAny))
	write(tpg+"/attrib/demo_mode_write_protect", b01(t.AllowAny && allRO))
	if t.AllowAny {
		opt(tpg+"/attrib/cache_dynamic_acls", "1")
		if chap {
			ops = append(ops, auth(tpg+"/auth", t.CHAP)...)
		}
	} else {
		for _, i := range t.Initiators {
			acl := tpg + "/acls/" + i
			mkdir(acl)
			for _, l := range t.LUNs {
				ml := fmt.Sprintf("%s/lun_%d", acl, l.LUN)
				mkdir(ml)
				link(ml+"/link", fmt.Sprintf("%s/lun/lun_%d", tpg, l.LUN))
				if l.ReadOnly {
					write(ml+"/write_protect", "1")
				}
			}
			if chap {
				ops = append(ops, auth(acl+"/auth", t.CHAP)...)
			}
		}
	}
	write(tpg+"/enable", "1")
	return ops
}

func auth(dir string, c store.ISCSICHAP) []Op {
	ops := []Op{{Kind: "write", Path: dir + "/userid", Value: c.User}, {Kind: "write", Path: dir + "/password", Value: c.Secret}}
	if c.MutualUser != "" {
		ops = append(ops, Op{Kind: "write", Path: dir + "/userid_mutual", Value: c.MutualUser}, Op{Kind: "write", Path: dir + "/password_mutual", Value: c.MutualSecret})
	}
	return ops
}

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ---- Applying ----

// Load loads the kernel modules and makes sure the iSCSI fabric is registered.
func Load() error {
	for _, m := range []string{"target_core_mod", "target_core_file", "target_core_iblock", "iscsi_target_mod"} {
		_, _ = sys.Run("modprobe", m)
	}
	if _, err := os.Stat(ConfigFS + "/core"); err != nil {
		return fmt.Errorf("the kernel iSCSI target is not available (configfs, target_core_mod)")
	}
	if _, err := os.Stat(ConfigFS + "/iscsi"); os.IsNotExist(err) {
		// Creating the fabric directory registers iscsi_target_mod.
		if err := os.Mkdir(ConfigFS+"/iscsi", 0o755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("cannot start the iSCSI target: %v", err)
		}
	}
	return nil
}

func run(o Op) error {
	p := filepath.Join(ConfigFS, o.Path)
	switch o.Kind {
	case "mkdir":
		return os.Mkdir(p, 0o755)
	case "link":
		return os.Symlink(filepath.Join(ConfigFS, o.Value), p)
	case "write":
		f, err := os.OpenFile(p, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		_, err = f.WriteString(o.Value)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	return fmt.Errorf("unknown operation %s", o.Kind)
}

// allocHBAs gives each LUN a free HBA directory of its backstore type
// (one storage object per HBA, like rtslib).
func allocHBAs(t store.ISCSITarget) map[int]string {
	out := map[int]string{}
	used := map[string]bool{}
	for _, l := range t.LUNs {
		pl := plugin(*l)
		for n := 0; ; n++ {
			name := fmt.Sprintf("%s_%d", pl, n)
			if used[name] {
				continue
			}
			if _, err := os.Lstat(filepath.Join(ConfigFS, "core", name)); os.IsNotExist(err) {
				used[name] = true
				out[l.LUN] = name
				break
			}
		}
	}
	return out
}

// Build (re)creates a target in the kernel. On failure nothing of it is left.
func Build(t store.ISCSITarget) error {
	if err := Load(); err != nil {
		return err
	}
	Teardown(t)
	hbas := allocHBAs(t)
	for _, o := range Plan(t, func(l store.ISCSILUN) string { return hbas[l.LUN] }) {
		if err := run(o); err != nil && !o.Optional {
			Teardown(t)
			return fmt.Errorf("target %s: %s failed: %v", t.Name, o.Path, err)
		}
	}
	return nil
}

func dirs(p string) []string {
	ents, _ := os.ReadDir(p)
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, filepath.Join(p, e.Name()))
		}
	}
	return out
}

func removeLinks(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Type()&os.ModeSymlink != 0 {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// RemoveTarget deletes a target from the kernel (its sessions are dropped).
func RemoveTarget(iqn string) error {
	if ValidIQN(iqn) != nil {
		return fmt.Errorf("invalid iSCSI name")
	}
	root := filepath.Join(ConfigFS, "iscsi", iqn)
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	for _, tpg := range dirs(root) {
		if !strings.HasPrefix(filepath.Base(tpg), "tpgt_") {
			continue
		}
		_ = run(Op{Kind: "write", Path: strings.TrimPrefix(tpg, ConfigFS+"/") + "/enable", Value: "0"})
		for _, acl := range dirs(tpg + "/acls") {
			for _, ml := range dirs(acl) {
				if strings.HasPrefix(filepath.Base(ml), "lun_") {
					removeLinks(ml)
					_ = os.Remove(ml)
				}
			}
			_ = os.Remove(acl)
		}
		for _, lun := range dirs(tpg + "/lun") {
			removeLinks(lun)
			_ = os.Remove(lun)
		}
		for _, np := range dirs(tpg + "/np") {
			_ = os.Remove(np)
		}
		_ = os.Remove(tpg)
	}
	return os.Remove(root)
}

// removeBackstores deletes the storage objects whose name starts with prefix,
// and the HBAs they leave empty.
func removeBackstores(prefix string) {
	for _, h := range dirs(ConfigFS + "/core") {
		b := filepath.Base(h)
		if !strings.HasPrefix(b, "fileio_") && !strings.HasPrefix(b, "iblock_") {
			continue
		}
		left := 0
		for _, so := range dirs(h) {
			if strings.HasPrefix(filepath.Base(so), prefix) {
				if os.Remove(so) == nil {
					continue
				}
			}
			left++
		}
		if left == 0 {
			_ = os.Remove(h)
		}
	}
}

// Teardown removes a target and its storage objects from the kernel.
func Teardown(t store.ISCSITarget) {
	_ = RemoveTarget(t.IQN)
	if ValidName(t.Name) == nil {
		removeBackstores("strata_" + t.Name + "_")
	}
}

// Live reports whether the kernel runs the target with all its LUNs.
func Live(t store.ISCSITarget) bool {
	tpg := filepath.Join(ConfigFS, "iscsi", t.IQN, "tpgt_1")
	if b, err := os.ReadFile(tpg + "/enable"); err != nil || strings.TrimSpace(string(b)) != "1" {
		return false
	}
	if _, err := os.Stat(tpg + "/np/" + Portal(t)); err != nil {
		return false
	}
	for _, l := range t.LUNs {
		if _, err := os.Stat(fmt.Sprintf("%s/lun/lun_%d/%s", tpg, l.LUN, Backstore(t, *l))); err != nil {
			return false
		}
	}
	if !t.AllowAny {
		for _, i := range t.Initiators {
			if _, err := os.Stat(tpg + "/acls/" + i); err != nil {
				return false
			}
		}
	}
	return true
}

// Save writes the live configuration where rtslib-fb-targetctl restores it at boot.
// The file holds the CHAP secrets: root only.
func Save() error {
	if err := os.MkdirAll(filepath.Dir(SaveFile), 0o700); err != nil {
		return err
	}
	if _, err := sys.Run("targetctl", "save", SaveFile); err != nil {
		return err
	}
	return os.Chmod(SaveFile, 0o600)
}

// ---- Sessions ----

// Session is an initiator logged in to a target.
type Session struct {
	Initiator string `json:"initiator"`
	Address   string `json:"address,omitempty"`
}

// parseACLInfo reads the "info" file of an initiator ACL.
func parseACLInfo(s string) []Session {
	var out []Session
	name := ""
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "InitiatorName:"):
			name = strings.TrimSpace(strings.TrimPrefix(l, "InitiatorName:"))
		case strings.HasPrefix(l, "No active iSCSI Session"):
			return nil
		case strings.HasPrefix(l, "LIO Session ID:"):
			out = append(out, Session{Initiator: name})
		case strings.HasPrefix(l, "Address ") && len(out) > 0:
			if f := strings.Fields(l); len(f) > 1 && out[len(out)-1].Address == "" {
				out[len(out)-1].Address = f[1]
			}
		}
	}
	return out
}

// Sessions lists the initiators connected to a target.
func Sessions(iqn string) []Session {
	out := []Session{}
	if ValidIQN(iqn) != nil {
		return out
	}
	tpg := filepath.Join(ConfigFS, "iscsi", iqn, "tpgt_1")
	for _, acl := range dirs(tpg + "/acls") {
		if b, err := os.ReadFile(acl + "/info"); err == nil {
			out = append(out, parseACLInfo(string(b))...)
		}
	}
	// Initiators admitted without an ACL (any initiator allowed): names only.
	if b, err := os.ReadFile(tpg + "/dynamic_sessions"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, Session{Initiator: l})
			}
		}
	}
	return out
}
