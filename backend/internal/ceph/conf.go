// Package ceph drives Ceph with the tools of the Debian packages (ceph-mon,
// ceph-mgr, ceph-osd, ceph-mds, ceph-volume, ceph-common) and their systemd
// units, the way Proxmox's pveceph does: no cephadm, no containers. It builds
// the configuration and keys of a new cluster, runs the daemons of this node,
// reads the cluster state, and mounts or maps Ceph storage for StrataOS.
package ceph

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Mon is a monitor of the cluster: its daemon name and address.
type Mon struct {
	Name string
	Addr string
}

// AddrVec is the msgr2 + legacy address vector of a monitor.
func (m Mon) AddrVec() string {
	return fmt.Sprintf("[v2:%s/0,v1:%s/0]", net.JoinHostPort(m.Addr, "3300"), net.JoinHostPort(m.Addr, "6789"))
}

// ConfParams is what ceph.conf is made of.
type ConfParams struct {
	FSID       string
	Mons       []Mon
	PublicNet  string
	ClusterNet string
	Size       int
	MinSize    int
	// SingleHost puts the replicas on different disks instead of different
	// NAS (test clusters with one NAS).
	SingleHost bool
	// SmallMemory bounds the caches of the daemons for NAS with little RAM.
	SmallMemory bool
}

// Config renders /etc/ceph/ceph.conf. Everything else lives in the monitors'
// configuration database.
func Config(p ConfParams) string {
	var b strings.Builder
	b.WriteString("# Written by StrataOS: changes made here are overwritten.\n[global]\n")
	kv := func(k string, v any) { fmt.Fprintf(&b, "%s = %v\n", k, v) }
	kv("fsid", p.FSID)
	names := make([]string, 0, len(p.Mons))
	vecs := make([]string, 0, len(p.Mons))
	for _, m := range p.Mons {
		names = append(names, m.Name)
		vecs = append(vecs, m.AddrVec())
	}
	kv("mon_initial_members", strings.Join(names, ", "))
	kv("mon_host", strings.Join(vecs, " "))
	kv("public_network", p.PublicNet)
	if p.ClusterNet != "" && p.ClusterNet != p.PublicNet {
		kv("cluster_network", p.ClusterNet)
	}
	for _, k := range []string{"auth_cluster_required", "auth_service_required", "auth_client_required"} {
		kv(k, "cephx")
	}
	kv("auth_allow_insecure_global_id_reclaim", "false")
	// Only the manager modules StrataOS reads (the default restful and nfs
	// modules need packages it does not install, and would warn).
	kv("mgr_initial_modules", "iostat")
	size, minSize := p.Size, p.MinSize
	if size < 1 {
		size = 3
	}
	if minSize < 1 || minSize > size {
		minSize = DefaultMinSize(size)
	}
	kv("osd_pool_default_size", size)
	kv("osd_pool_default_min_size", minSize)
	kv("osd_pool_default_pg_autoscale_mode", "on")
	// Pools are deleted through StrataOS only, which checks they are unused.
	kv("mon_allow_pool_delete", "true")
	if size == 1 {
		kv("mon_allow_pool_size_one", "true")
	}
	if p.SingleHost {
		kv("osd_crush_chooseleaf_type", 0)
	}
	if p.SmallMemory {
		// Lowest values Ceph accepts or that still work: a NAS with a few GB
		// of RAM shares it with the rest of StrataOS.
		b.WriteString("\n[mon]\nmon_memory_target = 536870912\n")
		b.WriteString("\n[osd]\nosd_memory_target = 939524096\nbluestore_cache_autotune = false\nbluestore_cache_size = 134217728\nosd_memory_cache_min = 134217728\n")
		b.WriteString("\n[mds]\nmds_cache_memory_limit = 134217728\n")
	}
	return b.String()
}

// DefaultMinSize is the usual min_size for a replica count: writes go on
// while more than half the copies are there.
func DefaultMinSize(size int) int {
	if size <= 2 {
		return 1
	}
	return size - size/2
}

// NewKey returns a new cephx secret, encoded like ceph-authtool does: AES
// type, creation time and 16 random bytes, little endian, base64.
func NewKey() string {
	return encodeKey(time.Now(), randBytes(16))
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func encodeKey(t time.Time, secret []byte) string {
	buf := make([]byte, 12, 12+len(secret))
	binary.LittleEndian.PutUint16(buf[0:], 1) // CEPH_CRYPTO_AES
	binary.LittleEndian.PutUint32(buf[2:], uint32(t.Unix()))
	binary.LittleEndian.PutUint32(buf[6:], uint32(t.Nanosecond()))
	binary.LittleEndian.PutUint16(buf[10:], uint16(len(secret)))
	buf = append(buf, secret...)
	return base64.StdEncoding.EncodeToString(buf)
}

var keyRe = regexp.MustCompile(`^[A-Za-z0-9+/]{38,120}={0,2}$`)

// ValidKey checks a cephx secret as pasted by an administrator.
func ValidKey(k string) error {
	raw, err := base64.StdEncoding.DecodeString(k)
	if !keyRe.MatchString(k) || err != nil || len(raw) < 12+16 || binary.LittleEndian.Uint16(raw) != 1 || int(binary.LittleEndian.Uint16(raw[10:]))+12 != len(raw) {
		return fmt.Errorf("invalid Ceph key (it looks like AQ…==)")
	}
	return nil
}

// Entity is a cephx identity in a keyring.
type Entity struct {
	Name string            // mon., client.admin…
	Key  string            // secret
	Caps map[string]string // mon → "allow *"…
}

// Keyring renders entities as a keyring file.
func Keyring(entities ...Entity) string {
	var b strings.Builder
	for _, e := range entities {
		fmt.Fprintf(&b, "[%s]\n\tkey = %s\n", e.Name, e.Key)
		keys := make([]string, 0, len(e.Caps))
		for k := range e.Caps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "\tcaps %s = %s\n", k, strconv.Quote(e.Caps[k]))
		}
	}
	return b.String()
}

// KeyOf extracts the secret of entity from a keyring ("" when missing).
func KeyOf(keyring, entity string) string {
	in := false
	for _, l := range strings.Split(keyring, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			in = entity == "" || t[1:len(t)-1] == entity
			continue
		}
		if k, v, ok := strings.Cut(t, "="); in && ok && strings.TrimSpace(k) == "key" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Caps of the identities a new cluster starts with.
var (
	CapsAdmin        = map[string]string{"mon": "allow *", "osd": "allow *", "mds": "allow *", "mgr": "allow *"}
	CapsMon          = map[string]string{"mon": "allow *"}
	CapsBootstrapOSD = map[string]string{"mon": "profile bootstrap-osd", "mgr": "allow r"}
)

var monAddrRe = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)

// ParseMons validates monitor addresses typed for an external cluster
// (IP or name, optional port, separated by commas, spaces or new lines).
func ParseMons(s string) ([]string, error) {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == ';' || r == '\t' }) {
		if !monAddrRe.MatchString(f) {
			return nil, fmt.Errorf("invalid monitor address %q", f)
		}
		if i := strings.LastIndex(f, ":"); i > 0 && !strings.HasSuffix(f, "]") {
			if p, err := strconv.Atoi(f[i+1:]); err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("invalid monitor address %q", f)
			}
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("enter the address of at least one monitor")
	}
	if len(out) > 9 {
		return nil, fmt.Errorf("too many monitor addresses")
	}
	return out, nil
}

// withPort adds the legacy monitor port to an address without one.
func withPort(a string) string {
	if strings.HasPrefix(a, "[") {
		if strings.HasSuffix(a, "]") {
			return a + ":6789"
		}
		return a
	}
	if strings.Contains(a, ":") {
		return a
	}
	return a + ":6789"
}

// ClientConfig renders the ceph.conf of a client of an external cluster.
func ClientConfig(fsid string, mons []string) string {
	var b strings.Builder
	b.WriteString("# Written by StrataOS: changes made here are overwritten.\n[global]\n")
	if fsid != "" {
		fmt.Fprintf(&b, "fsid = %s\n", fsid)
	}
	fmt.Fprintf(&b, "mon_host = %s\n", strings.Join(mons, ","))
	return b.String()
}

// MountMonAddr is the mon_addr option of mount.ceph: legacy ports, slashes.
func MountMonAddr(mons []string) string {
	out := make([]string, len(mons))
	for i, m := range mons {
		out[i] = withPort(m)
	}
	return strings.Join(out, "/")
}

var (
	poolRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	userRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	imageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,94}$`)
	fsidRe  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	subRe   = regexp.MustCompile(`^(/[A-Za-z0-9_.-]+)*/?$`)
)

// ValidPool checks a pool or filesystem name.
func ValidPool(n string) error {
	if !poolRe.MatchString(n) || n == "." || n == ".." || strings.HasPrefix(n, ".") {
		return fmt.Errorf("invalid pool name %q", n)
	}
	return nil
}

// ValidUser checks a cephx user name (without "client.").
func ValidUser(n string) error {
	if !userRe.MatchString(n) {
		return fmt.Errorf("invalid Ceph user %q (without client.)", n)
	}
	return nil
}

// ValidImage checks an RBD image name.
func ValidImage(n string) error {
	if !imageRe.MatchString(n) {
		return fmt.Errorf("invalid image name %q", n)
	}
	return nil
}

// ValidFSID checks a cluster fsid.
func ValidFSID(f string) error {
	if !fsidRe.MatchString(f) {
		return fmt.Errorf("invalid cluster fsid %q", f)
	}
	return nil
}

// CleanSubPath validates a folder inside a CephFS filesystem ("/" for its root).
func CleanSubPath(p string) (string, error) {
	if p == "" {
		return "/", nil
	}
	if !subRe.MatchString(p) || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") || strings.Contains(p, "/./") {
		return "", fmt.Errorf("invalid folder %q in the filesystem", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." {
			return "", fmt.Errorf("invalid folder %q in the filesystem", p)
		}
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p, nil
}

// NewFSID returns a random UUID (version 4).
func NewFSID() string {
	b := randBytes(16)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
