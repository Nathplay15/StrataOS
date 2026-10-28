package lxc

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, n := range []string{"web", "dns-1", "a", "0db", strings.Repeat("a", 63)} {
		if err := ValidName(n); err != nil {
			t.Errorf("%q refused: %v", n, err)
		}
	}
	for _, n := range []string{"", "Web", "-web", "web-", "a_b", "a.b", "../x", "a b", strings.Repeat("a", 64), "templates", "import", "x\n"} {
		if ValidName(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestIDMapLines(t *testing.T) {
	cases := []struct {
		m    IDMap
		want []string
	}{
		{IDMap{Base: 100000, Count: 65536}, []string{"u 0 100000 65536", "g 0 100000 65536"}},
		{IDMap{Base: 100000, Count: 65536, UIDs: []int{1001}, GIDs: []int{100, 1001}}, []string{
			"u 0 100000 1001", "u 1001 1001 1", "u 1002 101002 64534",
			"g 0 100000 100", "g 100 100 1", "g 101 100101 900", "g 1001 1001 1", "g 1002 101002 64534"}},
		// Unsorted and duplicated ids, one next to another.
		{IDMap{Base: 200000, Count: 65536, UIDs: []int{1002, 1001, 1002}}, []string{
			"u 0 200000 1001", "u 1001 1001 1", "u 1002 1002 1", "u 1003 201003 64533", "g 0 200000 65536"}},
	}
	for _, c := range cases {
		if got := c.m.Lines(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v:\n got %q\nwant %q", c.m, got, c.want)
		}
	}
	m := IDMap{Base: 100000, Count: 65536, UIDs: []int{1001}}
	if m.HostUID(0) != 100000 || m.HostUID(1001) != 1001 || m.HostUID(33) != 100033 {
		t.Error("HostUID")
	}
	for _, bad := range []IDMap{
		{Base: 100000, Count: 65536, UIDs: []int{0}},     // the NAS's root
		{Base: 100000, Count: 65536, GIDs: []int{70000}}, // outside the container
		{Base: 1000, Count: 65536},                       // overlaps the NAS's own ids
	} {
		if bad.validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestPickBase(t *testing.T) {
	uids := parseSubIDs("alice:100000:65536\nbob:165536:65536\n# comment\nbroken\n")
	gids := parseSubIDs("alice:100000:65536\nbob:165536:65536\n")
	if b := pickBase(uids, gids, 65536); b != 262144 {
		t.Errorf("after the users' ranges: %d", b)
	}
	if b := pickBase(nil, nil, 65536); b != 131072 {
		t.Errorf("empty files: %d", b)
	}
	// root already has a range: kept.
	uids = append(uids, subRange{"root", 1000000, 65536})
	gids = append(gids, subRange{"root", 1000000, 65536})
	if b := pickBase(uids, gids, 65536); b != 1000000 {
		t.Errorf("root's range: %d", b)
	}
	if !hasRange(uids, "root", 1000000, 65536) || hasRange(uids, "root", 1000001, 65536) || hasRange(uids, "alice", 1000000, 1) {
		t.Error("hasRange")
	}
}

func testSpec() *Spec {
	return &Spec{
		Version: 1, Name: "web", Image: Image{Distro: "debian", Release: "trixie", Arch: "amd64"},
		IDMap:    IDMap{Base: 100000, Count: 65536},
		Storage:  StorageSpec{Backend: "btrfs", Location: "/srv/strata/fast", Base: "/srv/strata/fast/.strata-lxc/web", SizeGB: 8},
		CPUs:     1.5,
		MemoryMB: 512, SwapMB: 256,
		Network:  Network{Mode: "nat", MAC: "00:16:3e:01:02:03"},
		Includes: []string{"/usr/share/lxc/config/common.conf", "/usr/share/lxc/config/userns.conf"},
		Arch:     "amd64",
	}
}

func TestGenerateConfig(t *testing.T) {
	s := testSpec()
	s.Mounts = []Mount{{Share: "Photos", Source: "/srv/strata/main/Photos", Target: "/mnt/photos", ReadOnly: true}, {Share: "My Docs", Source: "/srv/strata/main/My Docs", Target: "/mnt/my docs"}}
	s.Features = Features{Nesting: true, FUSE: true, TUN: true, Keyctl: true}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	got := GenerateConfig(s, ConfigOpts{NATBridge: "lxcbr0", KeyctlSeccomp: "/var/lib/strata/lxc-cache/keyctl.seccomp"})
	for _, want := range []string{
		"lxc.include = /usr/share/lxc/config/common.conf\nlxc.include = /usr/share/lxc/config/userns.conf\nlxc.arch = amd64\n",
		"lxc.uts.name = web\n",
		"lxc.rootfs.path = dir:/srv/strata/fast/.strata-lxc/web/rootfs\n",
		"lxc.start.auto = 0\n",
		"lxc.idmap = u 0 100000 65536\nlxc.idmap = g 0 100000 65536\n",
		"lxc.net.0.type = veth\nlxc.net.0.link = lxcbr0\nlxc.net.0.flags = up\nlxc.net.0.name = eth0\nlxc.net.0.hwaddr = 00:16:3e:01:02:03\n",
		"lxc.cgroup2.cpu.max = 150000 100000\n",
		"lxc.cgroup2.memory.max = 512M\nlxc.cgroup2.memory.swap.max = 256M\n",
		"lxc.apparmor.profile = generated\nlxc.apparmor.allow_nesting = 1\n",
		"lxc.seccomp.profile = /var/lib/strata/lxc-cache/keyctl.seccomp\n",
		"lxc.cgroup2.devices.allow = c 10:229 rwm\nlxc.mount.entry = /dev/fuse dev/fuse none bind,create=file,optional 0 0\n",
		"lxc.cgroup2.devices.allow = c 10:200 rwm\nlxc.mount.entry = /dev/net/tun dev/net/tun none bind,create=file,optional 0 0\n",
		"lxc.mount.entry = /srv/strata/main/Photos mnt/photos none bind,create=dir,ro 0 0\n",
		`lxc.mount.entry = /srv/strata/main/My\040Docs mnt/my\040docs none bind,create=dir 0 0` + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ipv4") {
		t.Error("DHCP: no static address expected")
	}

	// Privileged, bridged, static address, no limits.
	p := testSpec()
	p.Privileged, p.CPUs, p.MemoryMB, p.SwapMB = true, 0, 0, 0
	p.Includes = []string{"/usr/share/lxc/config/debian.common.conf"}
	p.Network = Network{Mode: "bridge", Bridge: "br0", MAC: "00:16:3E:0A:0B:0C", Address: "192.168.1.50/24", Gateway: "192.168.1.1", DNS: []string{"1.1.1.1"}}
	p.Features = Features{Keyctl: true}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	got = GenerateConfig(p, ConfigOpts{NATBridge: "lxcbr0", KeyctlSeccomp: "/x.seccomp"})
	for _, want := range []string{"lxc.net.0.link = br0\n", "lxc.net.0.hwaddr = 00:16:3e:0a:0b:0c\n", "lxc.net.0.ipv4.address = 192.168.1.50/24\nlxc.net.0.ipv4.gateway = 192.168.1.1\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, no := range []string{"lxc.idmap", "cgroup2.cpu", "memory.max", "seccomp", "allow_nesting"} {
		if strings.Contains(got, no) {
			t.Errorf("%q not expected in:\n%s", no, got)
		}
	}
	n := testSpec()
	n.Network = Network{Mode: "none"}
	if err := n.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := GenerateConfig(n, ConfigOpts{NATBridge: "lxcbr0"}); !strings.Contains(got, "lxc.net.0.type = empty\n") || strings.Contains(got, "veth") {
		t.Errorf("no network:\n%s", got)
	}
}

func TestValidateSpec(t *testing.T) {
	bad := map[string]func(s *Spec){
		"traversal mount": func(s *Spec) { s.Mounts = []Mount{{Share: "x", Source: "/srv/strata/../etc", Target: "/mnt/x"}} },
		"outside volumes": func(s *Spec) { s.Mounts = []Mount{{Share: "x", Source: "/etc", Target: "/mnt/x"}} },
		"system target":   func(s *Spec) { s.Mounts = []Mount{{Share: "x", Source: "/srv/strata/main/x", Target: "/etc/x"}} },
		"relative target": func(s *Spec) { s.Mounts = []Mount{{Share: "x", Source: "/srv/strata/main/x", Target: "mnt"}} },
		"root target":     func(s *Spec) { s.Mounts = []Mount{{Share: "x", Source: "/srv/strata/main/x", Target: "/"}} },
		"newline target": func(s *Spec) {
			s.Mounts = []Mount{{Share: "x", Source: "/srv/strata/main/x", Target: "/mnt/a\nlxc.x = 1"}}
		},
		"twice": func(s *Spec) {
			m := Mount{Share: "x", Source: "/srv/strata/main/x", Target: "/mnt/x"}
			s.Mounts = []Mount{m, m}
		},
		"container folder": func(s *Spec) {
			s.Mounts = []Mount{{Share: "x", Source: "/srv/strata/main/.strata-lxc/db", Target: "/mnt/x"}}
		},
		"bridge name":  func(s *Spec) { s.Network = Network{Mode: "bridge", Bridge: "br0 x"} },
		"mac":          func(s *Spec) { s.Network.MAC = "01:00:5e:00:00:01" },
		"mac format":   func(s *Spec) { s.Network.MAC = "00:16:3e:01:02" },
		"address":      func(s *Spec) { s.Network.Address = "192.168.1.300/24" },
		"ipv6 address": func(s *Spec) { s.Network.Address = "fd00::5/64" },
		"gateway":      func(s *Spec) { s.Network.Address, s.Network.Gateway = "10.0.3.9/24", "x" },
		"dns":          func(s *Spec) { s.Network.Address, s.Network.DNS = "10.0.3.9/24", []string{"a;b"} },
		"memory":       func(s *Spec) { s.MemoryMB = 10 },
		"cpus":         func(s *Spec) { s.CPUs = -1 },
		"include":      func(s *Spec) { s.Includes = []string{"/etc/shadow"} },
		"order":        func(s *Spec) { s.AutostartOrder = 5000 },
		"idmap root":   func(s *Spec) { s.IDMap.UIDs = []int{0} },
	}
	for name, f := range bad {
		s := testSpec()
		f(s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := testSpec()
	s.Network.MAC = ""
	s.Description = "line\none"
	s.Mounts = []Mount{{Share: "x", Source: "/srv/strata/main/x/", Target: "/mnt/x/"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if !macRe.MatchString(s.Network.MAC) || !strings.HasPrefix(s.Network.MAC, "00:16:3e:") {
		t.Errorf("generated MAC %q", s.Network.MAC)
	}
	if s.Description != "line one" || s.Mounts[0].Source != "/srv/strata/main/x" || s.Mounts[0].Target != "/mnt/x" {
		t.Errorf("not normalised: %+v", s)
	}
}

func TestParseImageConfig(t *testing.T) {
	conf := `# Template used to create this container: /usr/share/lxc/templates/lxc-download
lxc.include = /usr/share/lxc/config/common.conf
lxc.include = /usr/share/lxc/config/userns.conf
lxc.arch = linux64
lxc.idmap = u 0 100000 65536
lxc.include = /etc/evil.conf
lxc.rootfs.path = dir:/x
`
	inc, arch := parseImageConfig(conf)
	if !reflect.DeepEqual(inc, []string{"/usr/share/lxc/config/common.conf", "/usr/share/lxc/config/userns.conf"}) || arch != "linux64" {
		t.Errorf("got %v %q", inc, arch)
	}
}

func TestParseIndex(t *testing.T) {
	index := `alpine;3.21;amd64;default;20251022_13:00;/images/alpine/3.21/amd64/default/20251022_13:00/
alpine;3.21;arm64;default;20251022_13:00;/images/alpine/3.21/arm64/default/20251022_13:00/
archlinux;current;amd64;default;20251022_04:18;/images/archlinux/current/amd64/default/20251022_04:18/
debian;bookworm;amd64;default;20251022_05:24;/images/debian/bookworm/amd64/default/20251022_05:24/
debian;bookworm;amd64;cloud;20251022_05:24;/images/debian/bookworm/amd64/cloud/20251022_05:24/
debian;trixie;amd64;default;20251022_05:24;/images/debian/trixie/amd64/default/20251022_05:24/
fedora;42;amd64;default;20251022_20:33;/images/fedora/42/amd64/default/20251022_20:33/
rockylinux;9;amd64;default;20251022_02:06;/images/rockylinux/9/amd64/default/20251022_02:06/
ubuntu;noble;amd64;default;20251022_07:42;/images/ubuntu/noble/amd64/default/20251022_07:42/
evil;$(reboot);amd64;default;x;/
`
	list := ParseIndex(index, "amd64")
	var got []string
	for _, i := range list {
		got = append(got, i.Distro+"/"+i.Release+"="+i.Label)
	}
	want := []string{
		"debian/trixie=Debian 13 (trixie)", "debian/bookworm=Debian 12 (bookworm)", "ubuntu/noble=Ubuntu 24.04 LTS (noble)", "archlinux/current=Arch Linux",
		"alpine/3.21=Alpine 3.21", "fedora/42=Fedora 42", "rockylinux/9=Rocky Linux 9",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if !list[0].Recommended || list[4].Recommended || list[0].Build != "20251022_05:24" {
		t.Errorf("flags: %+v", list[:5])
	}
	if len(ParseIndex(index, "arm64")) != 1 {
		t.Error("arm64 filter")
	}
}

func TestParseInfo(t *testing.T) {
	out := "State:          RUNNING\nPID:            4242\nIP:             10.0.3.57\nIP:             172.17.0.1\nIP:             10.0.3.57\n"
	st, pid, ips := parseInfo(out)
	if st != "running" || pid != 4242 || !reflect.DeepEqual(ips, []string{"10.0.3.57", "172.17.0.1"}) {
		t.Errorf("got %s %d %v", st, pid, ips)
	}
	if st, _, ips := parseInfo("State:          STOPPED\n"); st != "stopped" || len(ips) != 0 {
		t.Errorf("stopped: %s %v", st, ips)
	}
}

func TestCgroupAndCounters(t *testing.T) {
	cg := "0::/system.slice/system-strata\\x2dlxc.slice/strata-lxc@web.service/lxc.payload.web/init.scope\n"
	if got := payloadCgroup(cg, "web"); got != `/sys/fs/cgroup/system.slice/system-strata\x2dlxc.slice/strata-lxc@web.service/lxc.payload.web` {
		t.Errorf("cgroup %q", got)
	}
	if payloadCgroup("0::/lxc.payload.webx/init.scope\n", "web") != "" {
		t.Error("another container's cgroup")
	}
	if got := payloadCgroup("0::/lxc.payload.web\n", "web"); got != "/sys/fs/cgroup/lxc.payload.web" {
		t.Errorf("top-level cgroup %q", got)
	}
	rd, wr := parseIOStat("8:0 rbytes=1000 wbytes=200 rios=3 wios=4 dbytes=0 dios=0\n254:0 rbytes=24 wbytes=6 rios=1 wios=1\n")
	if rd != 1024 || wr != 206 {
		t.Errorf("io %d %d", rd, wr)
	}
	netdev := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:     500       5    0    0    0     0          0         0      500       5    0    0    0     0       0          0
  eth0:   12345      20    0    0    0     0          0         0     6789      10    0    0    0     0       0          0
`
	if rx, tx := parseNetDev(netdev); rx != 12345 || tx != 6789 {
		t.Errorf("net %d %d", rx, tx)
	}
}

func TestGuestNetworkFiles(t *testing.T) {
	n := Network{Mode: "nat", Address: "10.0.3.50/24", Gateway: "10.0.3.1", DNS: []string{"10.0.3.1", "9.9.9.9"}}
	if got := networkdConfig(n); !strings.Contains(got, "[Match]\nName=eth0\n\n[Network]\nAddress=10.0.3.50/24\nGateway=10.0.3.1\nDNS=10.0.3.1\nDNS=9.9.9.9\n") {
		t.Errorf("networkd:\n%s", got)
	}
	if got := netplanConfig(n); !strings.Contains(got, "dhcp4: false") || !strings.Contains(got, "addresses: [10.0.3.50/24]") || !strings.Contains(got, "via: 10.0.3.1") || !strings.Contains(got, "addresses: [10.0.3.1, 9.9.9.9]") {
		t.Errorf("netplan:\n%s", got)
	}
	ifaces := "auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet dhcp\n\thostname web\n"
	want := "auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet static\n\taddress 10.0.3.50/24\n\tgateway 10.0.3.1\n"
	if got := rewriteInterfaces(ifaces, n); got != want {
		t.Errorf("interfaces:\n%q\nwant\n%q", got, want)
	}
	if got := rewriteInterfaces(want, Network{Mode: "nat"}); got != "auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet dhcp\n" {
		t.Errorf("back to DHCP:\n%q", got)
	}
	if got := rewriteInterfaces("auto lo\niface lo inet loopback\n", n); !strings.Contains(got, "auto eth0\niface eth0 inet static\n") {
		t.Errorf("no eth0 stanza yet:\n%q", got)
	}
	hosts := "127.0.0.1\tlocalhost\n127.0.1.1\tweb\n# web\n::1 localhost web-2\n"
	if got := renameHost(hosts, "web", "db"); got != "127.0.0.1\tlocalhost\n127.0.1.1\tdb\n# web\n::1 localhost web-2\n" {
		t.Errorf("hosts:\n%q", got)
	}
}

func TestArchiveNames(t *testing.T) {
	m := archiveRe.FindStringSubmatch("web-db-20261023-141500.tar.zst")
	if m == nil || m[1] != "web-db" || m[3] != "20261023-141500" {
		t.Errorf("got %q", m)
	}
	for _, bad := range []string{"web.tar.zst", "Web-20261023-141500.tar.zst", "web-20261023-141500.tar.zst.partial", "../web-20261023-141500.tar.zst"} {
		if archiveRe.MatchString(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if !ValidMode("") || !ValidMode("stop") || ValidMode("pause") {
		t.Error("modes")
	}
	s := testSpec()
	s.IDMap.UIDs = []int{1001}
	if got := strings.Join(usernsArgs(s), " "); got != "lxc-usernsexec -m u:0:100000:1001 -m u:1001:1001:1 -m u:1002:101002:64534 -m g:0:100000:65536 --" {
		t.Errorf("usernsexec: %s", got)
	}
	s.Privileged = true
	if usernsArgs(s) != nil {
		t.Error("privileged: no user namespace")
	}
}

func TestSnapshotNames(t *testing.T) {
	for _, n := range []string{"before-upgrade", "s1", "2026.10.23"} {
		if validSnap(n) != nil {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{"", "../x", "a/b", "x.json", "x.img", "strata-backup", "-x"} {
		if validSnap(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestStartOrder(t *testing.T) {
	list := []Spec{{Name: "c", Autostart: true, AutostartOrder: 2}, {Name: "a", Autostart: true, AutostartOrder: 2}, {Name: "b", Autostart: true, AutostartOrder: 1}, {Name: "off"}}
	var got []string
	for _, s := range startOrder(list) {
		got = append(got, s.Name)
	}
	if !reflect.DeepEqual(got, []string{"b", "a", "c"}) {
		t.Errorf("got %v", got)
	}
}

func TestEscapes(t *testing.T) {
	if got := escapeMount(`/srv/strata/main/a b\c`); got != `/srv/strata/main/a\040b\134c` {
		t.Errorf("mount %q", got)
	}
	if got := systemdQuote("/srv/strata/main/50% off/x y"); got != `/srv/strata/main/50%%\x20off/x\x20y` {
		t.Errorf("systemd %q", got)
	}
	if !strings.Contains(unitText(), "ExecStart=/usr/bin/lxc-start -F -P /var/lib/strata/lxc -n %i\n") || !strings.Contains(unitText(), "Delegate=yes") {
		t.Errorf("unit:\n%s", unitText())
	}
}

func TestACLParsing(t *testing.T) {
	masked := "user::rwx\nuser:327680:--x\t#effective:---\ngroup::---\nmask::---\nother::---\n"
	ok := "user::rwx\nuser:327680:--x\ngroup::r-x\nmask::r-x\nother::---\n"
	if aclSearchable(masked, "327680") || !aclSearchable(ok, "327680") || aclSearchable(ok, "100000") {
		t.Error("aclSearchable")
	}
	if aclMask(masked) != "---" || aclMask(ok) != "r-x" || aclMask("user::rwx\n") != "" {
		t.Error("aclMask")
	}
}
