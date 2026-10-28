package lxc

import (
	"crypto/rand"
	"fmt"
	"math"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// IDMap maps the ids of an unprivileged container to host ids: container id
// n is host id Base+n, except for the host ids listed in UIDs/GIDs, which keep
// their number inside (so that files of a StrataOS user or group in a mounted
// shared folder belong to the same id in the container).
type IDMap struct {
	Base  int   `json:"base"`
	Count int   `json:"count"`
	UIDs  []int `json:"uids,omitempty"`
	GIDs  []int `json:"gids,omitempty"`
}

// DefaultIDCount is the number of ids given to each container (uid 0-65535).
const DefaultIDCount = 65536

// validate checks the passthrough ids: never 0 (that would be the host's
// root), inside the container range, outside the shifted host range.
func (m IDMap) validate() error {
	if m.Base < 65536 || m.Count < 65536 || m.Count > 1<<24 {
		return fmt.Errorf("invalid id mapping")
	}
	for _, list := range [][]int{m.UIDs, m.GIDs} {
		for _, id := range list {
			if id < 1 || id >= m.Count || (id >= m.Base && id < m.Base+m.Count) {
				return fmt.Errorf("id %d cannot be mapped into the container", id)
			}
		}
	}
	return nil
}

// mapLines returns the lxc.idmap values of one kind (u or g).
func mapLines(kind string, base, count int, ids []int) []string {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	var out []string
	cur := 0
	for _, id := range ids {
		if id < cur || id >= count {
			continue
		}
		if id > cur {
			out = append(out, fmt.Sprintf("%s %d %d %d", kind, cur, base+cur, id-cur))
		}
		out = append(out, fmt.Sprintf("%s %d %d 1", kind, id, id))
		cur = id + 1
	}
	if cur < count {
		out = append(out, fmt.Sprintf("%s %d %d %d", kind, cur, base+cur, count-cur))
	}
	return out
}

// IDMapLines returns every lxc.idmap value of the mapping.
func (m IDMap) Lines() []string {
	return append(mapLines("u", m.Base, m.Count, m.UIDs), mapLines("g", m.Base, m.Count, m.GIDs)...)
}

// HostUID returns the host uid of a container uid.
func (m IDMap) HostUID(id int) int {
	if slices.Contains(m.UIDs, id) {
		return id
	}
	return m.Base + id
}

var (
	bridgeRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,14}$`)
	macRe     = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)
	targetRe  = regexp.MustCompile(`^/[A-Za-z0-9_.@+\-/ ]{1,200}$`)
	includeRe = regexp.MustCompile(`^/usr/share/lxc/config/[A-Za-z0-9_.\-]+\.conf$`)
	archRe    = regexp.MustCompile(`^[a-z0-9_]{1,16}$`)
)

// NewMAC returns a random address in the 00:16:3e range LXC uses.
func NewMAC() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("00:16:3e:%02x:%02x:%02x", b[0]&0x7f, b[1], b[2])
}

// Validate checks the parts of a spec the administrator can change.
func (s *Spec) Validate() error {
	if err := ValidName(s.Name); err != nil {
		return err
	}
	s.Description = cleanText(s.Description, 300)
	if s.CPUs < 0 || s.CPUs > 256 || math.IsNaN(s.CPUs) {
		return fmt.Errorf("invalid CPU limit")
	}
	if s.MemoryMB < 0 || (s.MemoryMB > 0 && s.MemoryMB < 64) || s.MemoryMB > 4<<20 || s.SwapMB < 0 || s.SwapMB > 4<<20 {
		return fmt.Errorf("invalid memory or swap limit (at least 64 MiB)")
	}
	if s.AutostartOrder < 0 || s.AutostartOrder > 999 || s.AutostartDelay < 0 || s.AutostartDelay > 3600 {
		return fmt.Errorf("invalid start order or delay")
	}
	if !s.Privileged {
		if err := s.IDMap.validate(); err != nil {
			return err
		}
	}
	if err := s.Network.validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range s.Mounts {
		m := &s.Mounts[i]
		src, err := insideStrata(m.Source)
		if err != nil {
			return err
		}
		m.Source = src
		m.Target = path.Clean(m.Target)
		if !targetRe.MatchString(m.Target) || m.Target == "/" || strings.Contains(m.Target, "..") {
			return fmt.Errorf("invalid mount point %q inside the container (an absolute path like /mnt/photos)", m.Target)
		}
		for _, sys := range []string{"/proc", "/sys", "/dev", "/run", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot"} {
			if m.Target == sys || strings.HasPrefix(m.Target, sys+"/") {
				return fmt.Errorf("%s cannot be used as a mount point", m.Target)
			}
		}
		if seen[m.Target] {
			return fmt.Errorf("%s is used twice", m.Target)
		}
		seen[m.Target] = true
	}
	for _, inc := range s.Includes {
		if !includeRe.MatchString(inc) {
			return fmt.Errorf("unexpected lxc.include %q", inc)
		}
	}
	if s.Arch != "" && !archRe.MatchString(s.Arch) {
		return fmt.Errorf("invalid architecture")
	}
	return nil
}

func (n *Network) validate() error {
	switch n.Mode {
	case "", "nat":
		n.Mode, n.Bridge = "nat", ""
	case "bridge":
		if !bridgeRe.MatchString(n.Bridge) {
			return fmt.Errorf("choose a bridge")
		}
	case "none":
		n.Bridge, n.Address, n.Gateway, n.DNS = "", "", "", nil
		return nil
	default:
		return fmt.Errorf("invalid network mode")
	}
	n.MAC = strings.ToLower(strings.TrimSpace(n.MAC))
	if n.MAC == "" {
		n.MAC = NewMAC()
	}
	if !macRe.MatchString(n.MAC) {
		return fmt.Errorf("invalid MAC address")
	}
	if b, _ := strconv.ParseUint(n.MAC[:2], 16, 8); b&1 == 1 {
		return fmt.Errorf("invalid MAC address (multicast)")
	}
	if n.Address == "" {
		n.Gateway, n.DNS = "", nil
		return nil
	}
	p, err := netip.ParsePrefix(n.Address)
	if err != nil || !p.Addr().Is4() || p.Bits() < 1 || p.Bits() > 32 {
		return fmt.Errorf("invalid static address (e.g. 192.168.1.50/24)")
	}
	n.Address = p.String()
	if n.Gateway != "" {
		g, err := netip.ParseAddr(n.Gateway)
		if err != nil || !g.Is4() {
			return fmt.Errorf("invalid gateway")
		}
		n.Gateway = g.String()
	}
	if len(n.DNS) > 3 {
		return fmt.Errorf("at most 3 DNS servers")
	}
	for i, d := range n.DNS {
		a, err := netip.ParseAddr(strings.TrimSpace(d))
		if err != nil {
			return fmt.Errorf("invalid DNS server %q", d)
		}
		n.DNS[i] = a.String()
	}
	return nil
}

func cleanText(t string, max int) string {
	t = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, t)
	if len(t) > max {
		t = t[:max]
	}
	return strings.TrimSpace(t)
}

// ConfigOpts are the host facts the config depends on.
type ConfigOpts struct {
	NATBridge string // lxcbr0
	// KeyctlSeccomp is the seccomp profile without the keyctl rule.
	KeyctlSeccomp string
}

// escapeMount escapes a path for an fstab-like lxc.mount.entry.
func escapeMount(p string) string {
	return strings.NewReplacer(`\`, `\134`, " ", `\040`, "\t", `\011`).Replace(p)
}

// GenerateConfig writes the lxc configuration of a spec. Every line comes from
// validated fields: nothing typed by the administrator reaches it unchecked.
func GenerateConfig(s *Spec, o ConfigOpts) string {
	var b strings.Builder
	kv := func(k, v string) { fmt.Fprintf(&b, "%s = %s\n", k, v) }
	b.WriteString("# Generated by StrataOS from container.json: change the container in\n# StrataOS (Virtual machines), edits made here are overwritten.\n")
	for _, inc := range s.Includes {
		kv("lxc.include", inc)
	}
	if s.Arch != "" {
		kv("lxc.arch", s.Arch)
	}
	kv("lxc.uts.name", s.Name)
	kv("lxc.rootfs.path", "dir:"+s.Storage.Rootfs())
	// StrataOS starts the containers itself (ordered, after the volumes).
	kv("lxc.start.auto", "0")
	if !s.Privileged {
		for _, l := range s.IDMap.Lines() {
			kv("lxc.idmap", l)
		}
	}

	switch s.Network.Mode {
	case "nat", "bridge":
		link := o.NATBridge
		if s.Network.Mode == "bridge" {
			link = s.Network.Bridge
		}
		kv("lxc.net.0.type", "veth")
		kv("lxc.net.0.link", link)
		kv("lxc.net.0.flags", "up")
		kv("lxc.net.0.name", "eth0")
		kv("lxc.net.0.hwaddr", s.Network.MAC)
		if s.Network.Address != "" {
			kv("lxc.net.0.ipv4.address", s.Network.Address)
			if s.Network.Gateway != "" {
				kv("lxc.net.0.ipv4.gateway", s.Network.Gateway)
			}
		}
	default:
		kv("lxc.net.0.type", "empty")
	}

	if s.CPUs > 0 {
		kv("lxc.cgroup2.cpu.max", fmt.Sprintf("%d 100000", int(math.Round(s.CPUs*100000))))
	}
	if s.MemoryMB > 0 {
		kv("lxc.cgroup2.memory.max", fmt.Sprintf("%dM", s.MemoryMB))
		kv("lxc.cgroup2.memory.swap.max", fmt.Sprintf("%dM", s.SwapMB))
	}

	f := s.Features
	if f.Nesting || !s.Privileged {
		// The generated AppArmor profile, with nesting: /proc and /sys are
		// also mounted uncovered for the inner containers. Always on for an
		// unprivileged container: the sandboxing of systemd ≥ 257 services
		// (networkd, resolved…) needs it, as on Proxmox.
		kv("lxc.apparmor.profile", "generated")
		kv("lxc.apparmor.allow_nesting", "1")
	}
	if f.Keyctl && !s.Privileged && o.KeyctlSeccomp != "" {
		kv("lxc.seccomp.profile", o.KeyctlSeccomp)
	}
	if f.FUSE {
		kv("lxc.cgroup2.devices.allow", "c 10:229 rwm")
		kv("lxc.mount.entry", "/dev/fuse dev/fuse none bind,create=file,optional 0 0")
	}
	if f.TUN {
		kv("lxc.cgroup2.devices.allow", "c 10:200 rwm")
		kv("lxc.mount.entry", "/dev/net/tun dev/net/tun none bind,create=file,optional 0 0")
	}
	for _, m := range s.Mounts {
		opts := "bind,create=dir"
		if m.ReadOnly {
			opts += ",ro"
		}
		kv("lxc.mount.entry", fmt.Sprintf("%s %s none %s 0 0", escapeMount(m.Source), escapeMount(strings.TrimPrefix(m.Target, "/")), opts))
	}
	return b.String()
}

// parseImageConfig keeps what the image's template wrote that StrataOS does
// not decide itself: its includes (distribution settings, userns.conf) and
// the architecture.
func parseImageConfig(conf string) (includes []string, arch string) {
	for _, l := range strings.Split(conf, "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "lxc.include":
			if includeRe.MatchString(v) && !slices.Contains(includes, v) {
				includes = append(includes, v)
			}
		case "lxc.arch":
			if archRe.MatchString(v) {
				arch = v
			}
		}
	}
	return includes, arch
}
