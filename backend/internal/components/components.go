// Package components describes what the optional components (Settings →
// Components) bring to the system, so they can be uninstalled again: their
// packages, services, the files StrataOS writes for them, their caches and
// their data. It also decides which packages may go (shared ones stay),
// what blocks an uninstall and which app folders may be deleted.
package components

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Component is an optional component that can be uninstalled.
type Component struct {
	ID   string
	Name string
	// Packages installed by its script (those not installed are skipped).
	Packages []string
	// Services of the packages, stopped and disabled before the purge.
	Services []string
	// Units of StrataOS for it: stopped, disabled and their files removed.
	Units []string
	// Files written by StrataOS (or left behind by the packages). Globs allowed.
	Files []string
	// Caches are removed with the component. Globs allowed.
	Caches []string
	// Data is removed only when asked. Globs allowed.
	Data []string
	// Repo: the APT source and its key (components from a vendor repository).
	Repo []string
}

// All lists the components that can be uninstalled, by the ids of
// GET /api/system/components.
var All = []Component{
	{ID: "zfs", Name: "ZFS",
		Packages: []string{"zfs-dkms", "zfsutils-linux", "zfs-zed"},
		Services: []string{"zfs-zed.service", "zfs-import-cache.service", "zfs-import-scan.service", "zfs-mount.service", "zfs-share.service", "zfs-volume-wait.service", "zfs.target"},
		Files:    []string{"/etc/zfs/zpool.cache"}},
	{ID: "docker", Name: "Docker",
		Packages: []string{"docker.io", "docker-compose", "docker-cli", "containerd", "runc"},
		Services: []string{"docker.service", "docker.socket", "containerd.service"},
		Files:    []string{"/etc/systemd/system/docker.service.d/strata-vpn.conf"},
		Data:     []string{"/var/lib/docker", "/var/lib/containerd"}},
	{ID: "nut", Name: "UPS (NUT)",
		Packages: []string{"nut-client", "nut-server"},
		Services: []string{"nut-monitor.service", "nut-server.service", "nut-client.service", "nut-driver.target", "nut-driver-enumerator.service", "nut-driver-enumerator.path", "nut.target"},
		Files:    []string{"/etc/nut"}},
	{ID: "vms", Name: "Virtual machines",
		Packages: []string{"qemu-system-x86", "ovmf", "qemu-system-arm", "qemu-efi-aarch64", "qemu-utils", "libvirt-daemon-system", "libvirt-clients", "virtinst", "swtpm", "swtpm-tools", "dnsmasq-base", "xorriso"},
		Services: []string{"libvirtd.service", "libvirtd.socket", "libvirtd-ro.socket", "libvirtd-admin.socket", "virtlogd.service", "virtlogd.socket", "virtlockd.service", "virtlockd.socket", "libvirt-guests.service"},
		Units:    []string{"strata-vms.service"},
		Caches:   []string{"/srv/strata/*/.strata-cloud-images"},
		Data:     []string{"/var/lib/libvirt", "/etc/libvirt"}},
	{ID: "lxc", Name: "Containers (LXC)",
		Packages: []string{"lxc", "lxcfs", "uidmap", "dnsmasq-base"},
		Services: []string{"lxc.service", "lxc-net.service", "lxc-monitord.service", "lxcfs.service"},
		Units:    []string{"strata-lxc@.service"},
		Files:    []string{"/etc/default/lxc-net"},
		Caches:   []string{"/var/lib/strata/lxc-cache", "/var/cache/lxc"},
		Data:     []string{"/var/lib/strata/lxc", "/srv/strata/*/.strata-lxc", "/srv/strata/*/*/.strata-lxc"}},
	{ID: "rclone", Name: "Cloud sync (rclone)",
		Packages: []string{"rclone", "fuse3", "openssh-client"},
		Caches:   []string{"/var/lib/strata/backup-sync"}},
	{ID: "ftp", Name: "FTP server",
		Packages: []string{"vsftpd"},
		Services: []string{"vsftpd.service"},
		Files:    []string{"/etc/vsftpd.conf", "/etc/strata/ftp-users", "/etc/pam.d/strata-ftp"}},
	{ID: "directory", Name: "Directory clients (AD, LDAP)",
		Packages: []string{"winbind", "libnss-winbind", "libpam-winbind", "krb5-user", "samba-common-bin", "sssd-ldap", "libnss-sss", "libpam-sss", "ldap-utils"},
		Services: []string{"winbind.service", "sssd.service"},
		Files:    []string{"/etc/sssd/sssd.conf"}},
	{ID: "antivirus", Name: "Antivirus (ClamAV)",
		Packages: []string{"clamav", "clamav-freshclam"},
		Services: []string{"clamav-freshclam.service"},
		Caches:   []string{"/var/lib/clamav"},
		Data:     []string{"/var/lib/strata/quarantine"}},
	{ID: "ha", Name: "High availability",
		Packages: []string{"keepalived", "rsync", "python3", "openssh-server"},
		Services: []string{"keepalived.service"},
		Files:    []string{"/etc/keepalived/keepalived.conf"}},
	{ID: "vpn", Name: "VPN (WireGuard)",
		Packages: []string{"wireguard-tools", "nftables"},
		Units:    []string{"strata-vpn-killswitch.service"},
		Files:    []string{"/etc/wireguard/strata*.conf", "/etc/sysctl.d/90-strata-vpn.conf", "/etc/systemd/system/docker.service.d/strata-vpn.conf"}},
	{ID: "photos", Name: "Media tools (ffmpeg, libvips)",
		Packages: []string{"libvips-tools", "libheif-plugin-libde265", "ffmpeg", "libimage-exiftool-perl", "intel-media-va-driver", "mesa-va-drivers"},
		Caches:   []string{"/var/lib/strata/photos/thumbs"}},
	{ID: "tailscale", Name: "Tailscale",
		Packages: []string{"tailscale"},
		Services: []string{"tailscaled.service"},
		Files:    []string{"/etc/sysctl.d/90-strata-tailscale.conf"},
		Data:     []string{"/var/lib/tailscale"},
		Repo:     []string{"/etc/apt/sources.list.d/tailscale.list", "/usr/share/keyrings/tailscale-archive-keyring.gpg"}},
	{ID: "tunnel", Name: "Cloudflare Tunnel",
		Packages: []string{"cloudflared"},
		Units:    []string{"strata-cloudflared.service"},
		Files:    []string{"/etc/strata/cloudflared.env"},
		Repo:     []string{"/etc/apt/sources.list.d/cloudflared.list", "/usr/share/keyrings/cloudflare-main.gpg"}},
	{ID: "iscsi", Name: "iSCSI target",
		Packages: []string{"targetcli-fb", "python3-rtslib-fb"},
		Services: []string{"rtslib-fb-targetctl.service"},
		Files:    []string{"/etc/rtslib-fb-target"}},
	{ID: "dlna", Name: "DLNA server",
		Packages: []string{"minidlna"},
		Services: []string{"minidlna.service"},
		Units:    []string{"strata-minidlna.service"},
		Files:    []string{"/etc/strata-minidlna.conf"},
		Caches:   []string{"/var/cache/strata-minidlna"}},
	{ID: "nvr", Name: "Surveillance recorder",
		Packages: []string{"ffmpeg"}},
	{ID: "ceph", Name: "Ceph",
		Packages: []string{"ceph-mon", "ceph-mgr", "ceph-osd", "ceph-mds", "ceph-volume", "ceph-common", "ceph-base", "ceph-mgr-modules-core"},
		Services: []string{"ceph.target", "ceph-mon.target", "ceph-mgr.target", "ceph-osd.target", "ceph-mds.target", "ceph-crash.service"},
		Files:    []string{"/etc/ceph/strata", "/etc/ceph/ceph.conf", "/etc/ceph/ceph.client.admin.keyring", "/etc/ceph/rbdmap"},
		Data:     []string{"/var/lib/ceph"}},
	{ID: "docpreview", Name: "Document previews",
		Packages: []string{"libreoffice-core-nogui", "libreoffice-writer-nogui", "libreoffice-calc-nogui", "libreoffice-impress-nogui", "libreoffice-draw-nogui", "fonts-crosextra-carlito", "fonts-crosextra-caladea"},
		Caches:   []string{"/var/cache/strata/previews"}},
}

// Find returns the component with this id.
func Find(id string) *Component {
	for i := range All {
		if All[i].ID == id {
			return &All[i]
		}
	}
	return nil
}

// Core packages are never removed: the system or StrataOS itself needs them
// even when no package depends on them (tools run directly, remote access).
// The Depends of the strataos package are added at run time, and the purge
// is simulated first to catch the rest.
var Core = []string{
	"openssh-client", "openssh-server", "rsync", "python3", "ca-certificates", "curl", "gnupg",
	"pciutils", "nftables", "fuse3", "samba-common-bin", "fonts-dejavu-core",
}

// Kept is a package of the component that stays installed, and why.
type Kept struct {
	Package string `json:"package"`
	Reason  string `json:"reason"`
}

// Packages splits the packages of component id into the ones to purge and
// the ones kept: installed reports whether a package is installed (others
// are skipped), inUse lists the other components still in use (they keep
// their packages), protected maps the packages StrataOS needs to a reason.
func Packages(id string, installed func(string) bool, inUse []string, protected map[string]string) (remove []string, kept []Kept) {
	c := Find(id)
	if c == nil {
		return nil, nil
	}
	users := map[string][]string{}
	for _, o := range All {
		if o.ID == id || !slices.Contains(inUse, o.ID) {
			continue
		}
		for _, p := range o.Packages {
			users[p] = append(users[p], o.Name)
		}
	}
	for _, p := range c.Packages {
		if installed != nil && !installed(p) {
			continue
		}
		switch {
		case protected[p] != "":
			kept = append(kept, Kept{p, protected[p]})
		case len(users[p]) > 0:
			kept = append(kept, Kept{p, fmt.Sprintf("Used by %s", strings.Join(users[p], ", "))})
		default:
			remove = append(remove, p)
		}
	}
	return remove, kept
}

// Protected builds the protected package map: the Core packages and the
// dependencies of the strataos package (its dpkg Depends field).
func Protected(depends string) map[string]string {
	out := map[string]string{}
	for _, p := range Core {
		out[p] = "Needed by StrataOS"
	}
	for _, p := range ParseDepends(depends) {
		out[p] = "Needed by StrataOS"
	}
	return out
}

// ParseDepends returns the package names of a dpkg dependency field
// ("a (>= 1), b | c, d:any"), alternatives included.
func ParseDepends(field string) []string {
	var out []string
	for _, group := range strings.Split(field, ",") {
		for _, alt := range strings.Split(group, "|") {
			name := strings.TrimSpace(alt)
			if i := strings.IndexAny(name, " ([<"); i >= 0 {
				name = name[:i]
			}
			name, _, _ = strings.Cut(name, ":")
			if name != "" && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// ParseAptSim returns the packages an "apt-get -s" run would remove (its
// "Remv" and "Purg" lines).
func ParseAptSim(out string) []string {
	var pkgs []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && (f[0] == "Remv" || f[0] == "Purg") {
			name, _, _ := strings.Cut(f[1], ":")
			if !slices.Contains(pkgs, name) {
				pkgs = append(pkgs, name)
			}
		}
	}
	return pkgs
}

// Without returns the elements of a that are not in b.
func Without(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// Blocker is a reason not to uninstall. Force: StrataOS can turn the feature
// off itself (the administrator confirms with "force").
type Blocker struct {
	Message string `json:"message"`
	Force   bool   `json:"force"`
}

// Facts is what the blockers are computed from.
type Facts struct {
	Pools       []string // imported ZFS pools
	Stacks      []string // apps and compose stacks
	Containers  []string // containers outside the stacks
	VMs         []string
	LXC         []string // containers
	Targets     []string // iSCSI targets
	HARole      string
	Directory   string // joined directory type
	VPNServer   bool
	VPNDevices  int
	VPNLinks    []string // enabled site-to-site links
	VPNClient   bool
	UPSMode     string
	DLNA        bool
	FTP         bool
	Cameras     []string // enabled cameras of the built-in recorder
	Syncs       []string // cloud syncs and two-way syncs
	AVSchedules []string // enabled scheduled scans
	Tailscale   bool     // signed in
	Tunnel      bool
	CephNode    bool     // node of a Ceph cluster
	CephUse     []string // Ceph volumes and VM storage of this NAS
}

func list(names []string) string {
	s := slices.Clone(names)
	sort.Strings(s)
	return strings.Join(s, ", ")
}

// Blockers lists what prevents uninstalling component id.
func Blockers(id string, f Facts) []Blocker {
	var b []Blocker
	hard := func(msg string, args ...any) { b = append(b, Blocker{Message: fmt.Sprintf(msg, args...)}) }
	soft := func(msg string, args ...any) { b = append(b, Blocker{Message: fmt.Sprintf(msg, args...), Force: true}) }
	switch id {
	case "zfs":
		if len(f.Pools) > 0 {
			hard("Imported ZFS pools: %s (export or destroy them first)", list(f.Pools))
		}
	case "docker":
		if len(f.Stacks) > 0 {
			hard("Installed apps and stacks: %s (uninstall them first)", list(f.Stacks))
		}
		if len(f.Containers) > 0 {
			hard("Containers: %s (delete them first)", list(f.Containers))
		}
	case "vms":
		if len(f.VMs) > 0 {
			hard("Virtual machines: %s (delete them first)", list(f.VMs))
		}
	case "lxc":
		if len(f.LXC) > 0 {
			hard("Containers (LXC): %s (delete them first)", list(f.LXC))
		}
	case "iscsi":
		if len(f.Targets) > 0 {
			hard("iSCSI targets: %s (delete them first)", list(f.Targets))
		}
	case "ceph":
		if f.CephNode {
			hard("This NAS is a node of a Ceph cluster (remove it from Ceph first)")
		}
		if len(f.CephUse) > 0 {
			hard("Ceph storage in use: %s (remove it first)", list(f.CephUse))
		}
	case "ha":
		if f.HARole != "" {
			hard("This NAS is paired for high availability (unpair it first)")
		}
	case "directory":
		if f.Directory != "" {
			hard("This NAS is joined to a directory (leave it first)")
		}
	case "vpn":
		if f.VPNServer {
			soft("The VPN server is on, with %d device(s)", f.VPNDevices)
		}
		if len(f.VPNLinks) > 0 {
			soft("Site-to-site links: %s", list(f.VPNLinks))
		}
		if f.VPNClient {
			soft("The outgoing VPN is on")
		}
	case "nut":
		if f.UPSMode != "" && f.UPSMode != "off" {
			soft("A UPS is configured (%s)", f.UPSMode)
		}
	case "dlna":
		if f.DLNA {
			soft("The DLNA server is on")
		}
	case "ftp":
		if f.FTP {
			soft("The FTP server is on")
		}
	case "nvr":
		if len(f.Cameras) > 0 {
			soft("Cameras recorded by the built-in recorder: %s (they will be disabled)", list(f.Cameras))
		}
	case "rclone":
		if len(f.Syncs) > 0 {
			soft("Cloud syncs and two-way syncs: %s (they will be disabled)", list(f.Syncs))
		}
	case "antivirus":
		if len(f.AVSchedules) > 0 {
			soft("Scheduled antivirus scans: %s (they will be disabled)", list(f.AVSchedules))
		}
	case "tailscale":
		if f.Tailscale {
			soft("The NAS is signed in to Tailscale (it will be signed out)")
		}
	case "tunnel":
		if f.Tunnel {
			soft("The Cloudflare Tunnel is on")
		}
	}
	return b
}

// Refused tells whether the blockers prevent the uninstall: always for a
// blocker that cannot be forced, otherwise unless forced.
func Refused(b []Blocker, force bool) bool {
	for _, x := range b {
		if !x.Force || !force {
			return true
		}
	}
	return false
}

// KeptPath is an app folder that is not deleted with the app, and why.
type KeptPath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// AppsRoot holds the folders of the App Center apps.
var AppsRoot = "/srv/strata/apps"

// StorageRoot is where the NAS volumes are mounted.
var StorageRoot = "/srv/strata"

func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// AppData chooses which folders mounted by an app (paths) are deleted with
// its data: absolute folders under /srv/strata/apps or an "apps" folder of a
// volume, or in a volume under a folder named after the app or its stack, never a volume itself, a shared
// folder or anything inside one (user files), never a folder another app
// mounts too (others maps the other stacks to their folders, shares the
// shared folders to their path).
func AppData(app, stack string, paths []string, others, shares map[string][]string) (del []string, kept []KeptPath) {
	seen := map[string]bool{}
	for _, raw := range paths {
		if !filepath.IsAbs(raw) {
			continue
		}
		p := filepath.Clean(raw)
		if seen[p] {
			continue
		}
		seen[p] = true
		if !within(p, StorageRoot) {
			continue // system paths (sockets, devices…) are not data
		}
		rel := strings.Split(strings.TrimPrefix(p, StorageRoot+"/"), "/")
		named := within(p, AppsRoot) && p != AppsRoot
		if !named && len(rel) >= 2 {
			for i, part := range rel[1:] {
				// A folder of the app, or inside an "apps" folder of a volume.
				if part == app || part == stack || part == "apps" && i < len(rel)-2 {
					named = true
				}
			}
		}
		if len(rel) >= 2 {
			if s := shareOf(p, shares); s != "" {
				kept = append(kept, KeptPath{p, fmt.Sprintf("In shared folder %s", s)})
				continue
			}
		}
		if !named || len(rel) < 2 {
			kept = append(kept, KeptPath{p, "Not an app folder"})
			continue
		}
		if o := userOf(p, others); o != "" {
			kept = append(kept, KeptPath{p, fmt.Sprintf("Also used by %s", o)})
			continue
		}
		del = append(del, p)
	}
	sort.Strings(del)
	// A folder inside another one goes with it.
	out := del[:0]
	for _, p := range del {
		if len(out) > 0 && within(p, out[len(out)-1]) {
			continue
		}
		out = append(out, p)
	}
	return out, kept
}

// shareOf returns the shared folder containing p or inside it.
func shareOf(p string, shares map[string][]string) string { return userOf(p, shares) }

// userOf returns the first name (sorted) whose paths contain p or are inside it.
func userOf(p string, others map[string][]string) string {
	names := make([]string, 0, len(others))
	for n := range others {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, o := range others[n] {
			if !filepath.IsAbs(o) {
				continue
			}
			o = filepath.Clean(o)
			if within(p, o) || within(o, p) {
				return n
			}
		}
	}
	return ""
}
