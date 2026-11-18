package system

// Debian release upgrade (13 → 14) from the interface: the pure parts —
// which release comes next, the APT sources rewrite, the pre-checks and
// the progress of apt. The API (api/h_distupgrade.go) gathers the facts and
// runs the steps; docs/DEBIAN-14.md lists what was checked on forky.

import (
	"fmt"
	"net/url"
	"strings"
)

// NextRelease is the Debian release after codename ("" when unknown).
func NextRelease(codename string) string {
	return map[string]string{"bookworm": "trixie", "trixie": "forky", "forky": "duke"}[codename]
}

// ReleaseNumber is the version of a Debian release, for the interface.
func ReleaseNumber(codename string) string {
	return map[string]string{"bookworm": "12", "trixie": "13", "forky": "14", "duke": "15"}[codename]
}

// ReleaseCodename reads the Codename field of a Release file.
func ReleaseCodename(release string) string {
	for _, l := range strings.Split(release, "\n") {
		if v, ok := strings.CutPrefix(l, "Codename:"); ok {
			if v = strings.TrimSpace(v); validCodename(v) {
				return v
			}
		}
	}
	return ""
}

// MirrorListURI resolves the "mirror+file:///path" URIs of the Debian cloud
// images to the first mirror of the list (content of that file); other
// URIs are returned as they are.
func MirrorListURI(uri string, read func(path string) string) string {
	path, ok := strings.CutPrefix(strings.TrimSpace(uri), "mirror+file://")
	if !ok || read == nil {
		return uri
	}
	for _, l := range strings.Split(read(path), "\n") {
		if f := strings.Fields(l); len(f) > 0 && (strings.HasPrefix(f[0], "http://") || strings.HasPrefix(f[0], "https://")) {
			return f[0]
		}
	}
	return uri
}

// IsDebianURI tells the archives of Debian itself (debian.org: its mirrors
// and the security archive). Other mirrors are moved like a vendor
// repository, when they publish the new release.
func IsDebianURI(uri string) bool {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || u.Host == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "debian.org" || strings.HasSuffix(h, ".debian.org")
}

// SourceChange is one suite changed (or kept) by RewriteSources.
type SourceChange struct {
	URI  string `json:"uri"`
	From string `json:"from"`
	To   string `json:"to"`
	Kept bool   `json:"kept,omitempty"` // a vendor without the new release yet
}

// debianSuite moves a suite of the Debian archive: trixie, trixie-updates,
// trixie-security, trixie-backports, trixie-proposed-updates.
func debianSuite(s, from, to string) (string, bool) {
	if s == from {
		return to, true
	}
	if rest, ok := strings.CutPrefix(s, from+"-"); ok && rest != "" {
		return to + "-" + rest, true
	}
	if rest, ok := strings.CutPrefix(s, from+"/"); ok && rest == "updates" { // old security form
		return to + "-security", true
	}
	return s, false
}

// RewriteSources moves an APT sources file (deb822 .sources, or one-line
// .list) from one release to the next. Debian's archives always move;
// another repository moves when vendorHas(uri) says it publishes the new
// release, else it keeps its suite (reported as kept). Suites that do not
// name a release (stable, any, a StrataOS channel) are left alone.
func RewriteSources(content string, deb822 bool, from, to string, vendorHas func(uri string) bool) (string, []SourceChange) {
	var changes []SourceChange
	move := func(uri, suite string) string {
		if suite != from && !strings.HasPrefix(suite, from+"-") && !strings.HasPrefix(suite, from+"/") {
			return suite
		}
		if IsDebianURI(uri) {
			n, _ := debianSuite(suite, from, to)
			changes = append(changes, SourceChange{URI: uri, From: suite, To: n})
			return n
		}
		if vendorHas != nil && vendorHas(uri) {
			n, _ := debianSuite(suite, from, to)
			changes = append(changes, SourceChange{URI: uri, From: suite, To: n})
			return n
		}
		changes = append(changes, SourceChange{URI: uri, From: suite, To: suite, Kept: true})
		return suite
	}
	lines := strings.Split(content, "\n")
	if deb822 {
		// Each paragraph: its URIs (first one decides), then its Suites line.
		uris := ""
		for i := 0; i < len(lines); i++ {
			l := lines[i]
			if strings.TrimSpace(l) == "" {
				uris = ""
				continue
			}
			if strings.HasPrefix(l, "#") {
				continue
			}
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "uris":
				if f := strings.Fields(v); len(f) > 0 {
					uris = f[0]
				}
			case "suites":
				if uris == "" { // Suites before URIs: look ahead in the paragraph
					for j := i + 1; j < len(lines) && strings.TrimSpace(lines[j]) != ""; j++ {
						if kk, vv, ok := strings.Cut(lines[j], ":"); ok && strings.EqualFold(strings.TrimSpace(kk), "uris") {
							if f := strings.Fields(vv); len(f) > 0 {
								uris = f[0]
							}
						}
					}
				}
				var out []string
				for _, s := range strings.Fields(v) {
					out = append(out, move(uris, s))
				}
				lines[i] = strings.TrimSpace(k) + ": " + strings.Join(out, " ")
			}
		}
		return strings.Join(lines, "\n"), changes
	}
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 3 || (f[0] != "deb" && f[0] != "deb-src") {
			continue
		}
		// deb [options] uri suite components…
		j := 1
		if strings.HasPrefix(f[1], "[") {
			for j < len(f) && !strings.HasSuffix(f[j], "]") {
				j++
			}
			j++
		}
		if j+1 >= len(f) {
			continue
		}
		if n := move(f[j], f[j+1]); n != f[j+1] {
			f[j+1] = n
			lines[i] = strings.Join(f, " ")
		}
	}
	return strings.Join(lines, "\n"), changes
}

// UpgradeCheck is one pre-check (or post-check) of a release upgrade.
type UpgradeCheck struct {
	ID    string `json:"id"`
	Level string `json:"level"` // ok | warn | block
	Text  string `json:"text"`
}

// UpgradeFacts are what the pre-checks look at.
type UpgradeFacts struct {
	From, To        string
	Moved           int   // package sources that move to the new release
	FreeRoot        int64 // bytes free on / (packages are unpacked there)
	FreeVar         int64 // on /var (downloads), same file system or not
	FreeBoot        int64 // on /boot (-1: not a separate file system)
	RestoreMode     string
	Jobs            []string // long operations running (backups, scrubs, transfers…)
	AptBusy         bool
	DpkgAudit       string // output of dpkg --audit (broken packages)
	Held            []string
	PendingUpdates  int // updates of the current release not installed yet
	Vendors         []SourceChange
	ZFS             bool // zfs-dkms installed
	CephNodes       int  // nodes of the Ceph cluster this NAS is part of (0: none)
	LDAP            bool // joined to an LDAP directory (sssd)
	RaspberryPi     bool
	MissingInTarget []string // packages installed here that the new release does not have
}

// GiB is a gibibyte.
const GiB = 1 << 30

// UpgradeChecks evaluates the pre-checks: any "block" refuses the upgrade.
func UpgradeChecks(f UpgradeFacts) []UpgradeCheck {
	var out []UpgradeCheck
	add := func(id, level, text string) { out = append(out, UpgradeCheck{id, level, text}) }
	switch {
	case f.FreeRoot < 3*GiB:
		add("space-root", "block", fmt.Sprintf("Only %.1f GB free on the system disk: the upgrade needs about 3 GB", float64(f.FreeRoot)/GiB))
	case f.FreeRoot < 5*GiB:
		add("space-root", "warn", fmt.Sprintf("%.1f GB free on the system disk: enough, with little to spare", float64(f.FreeRoot)/GiB))
	default:
		add("space-root", "ok", fmt.Sprintf("%.1f GB free on the system disk", float64(f.FreeRoot)/GiB))
	}
	if f.FreeVar >= 0 && f.FreeVar < 2*GiB {
		add("space-var", "block", "Less than 2 GB free in /var for the downloaded packages")
	}
	if f.FreeBoot >= 0 && f.FreeBoot < 200<<20 {
		add("space-boot", "block", "Less than 200 MB free in /boot for the new kernel")
	}
	switch f.RestoreMode {
	case "":
		add("restore-point", "warn", "No restore point can be made on this system: a failed upgrade must be repaired by hand (a backup of the system disk brings it back)")
	case "btrfs", "lvm":
		add("restore-point", "ok", "A restore point is made first ("+f.RestoreMode+"): if the upgrade or the checks after the restart fail, the NAS goes back to it by itself")
	default:
		add("restore-point", "warn", "This system keeps only the versions of updated packages, which cannot bring back a whole release: a failed upgrade must be repaired by hand (systems installed from the StrataOS ISO go back automatically)")
	}
	if len(f.Jobs) > 0 {
		add("jobs", "block", "Wait for these operations to finish: "+strings.Join(f.Jobs, ", "))
	}
	if f.AptBusy {
		add("apt", "block", "Another package operation is running")
	}
	if strings.TrimSpace(f.DpkgAudit) != "" {
		add("dpkg", "block", "Some packages are half installed: run the system updates first (dpkg --audit)")
	}
	if len(f.Held) > 0 {
		add("held", "warn", "Packages held at their version (they stay so, which may block the upgrade): "+strings.Join(f.Held, ", "))
	}
	if f.PendingUpdates > 0 {
		add("updates", "block", fmt.Sprintf("Install the %d pending updates of Debian %s first", f.PendingUpdates, ReleaseNumber(f.From)))
	}
	if f.Moved == 0 {
		add("sources", "block", "No package source of Debian "+ReleaseNumber(f.From)+" was found to move to Debian "+ReleaseNumber(f.To)+" (unusual APT sources: upgrade by hand, see DEBIAN-14.md)")
	}
	for _, v := range f.Vendors {
		if v.Kept {
			add("vendor", "warn", fmt.Sprintf("%s does not publish %s yet: it stays on %s", v.URI, f.To, v.From))
		}
	}
	if f.ZFS {
		add("zfs", "ok", "ZFS is rebuilt for the new kernel by DKMS; the restart is offered only once it is built")
	}
	if f.CephNodes > 1 {
		add("ceph", "block", "This NAS is a node of a Ceph cluster: every node must be upgraded together (monitors first, noout set), which this upgrade does not do. See DEBIAN-14.md")
	} else if f.CephNodes == 1 {
		add("ceph", "warn", "Ceph moves to a new major release with the system")
	}
	if f.LDAP {
		add("ldap", "warn", "Joined to an LDAP directory: check that sssd is in the new release first (DEBIAN-14.md)")
	}
	if f.RaspberryPi {
		add("rpi", "warn", "Raspberry Pi: its kernel archive stays on the current release until it publishes kernels for the new one")
	}
	if len(f.MissingInTarget) > 0 {
		add("missing", "warn", "Not in the new release (kept at their current version, without updates): "+strings.Join(f.MissingInTarget, ", "))
	}
	return out
}

// RollbackMode tells whether a restore point of this kind brings back a
// whole release (a snapshot of the system).
func RollbackMode(mode string) bool { return mode == "btrfs" || mode == "lvm" }

// Blocked tells whether a check refuses the upgrade.
func Blocked(checks []UpgradeCheck) bool {
	for _, c := range checks {
		if c.Level == "block" {
			return true
		}
	}
	return false
}

// AptCounter follows the output of apt to give a progress: every package
// is unpacked then set up.
type AptCounter struct {
	Total, Done int
}

// PlanCount counts the packages a simulated run (apt-get -s) would change.
func PlanCount(sim string) int {
	n := 0
	for _, l := range strings.Split(sim, "\n") {
		if strings.HasPrefix(l, "Inst ") {
			n++
		}
	}
	return n
}

// Line counts a line of apt output; it returns the progress (0–100).
func (c *AptCounter) Line(l string) float64 {
	if strings.HasPrefix(l, "Unpacking ") || strings.HasPrefix(l, "Setting up ") {
		c.Done++
	}
	if c.Total <= 0 {
		return 0
	}
	return min(100, float64(c.Done)*100/float64(2*c.Total))
}

// NewestKernel picks the highest version among kernel releases
// ("6.12.48+deb13-amd64", "7.2.15+deb14-amd64"): numbers compared one by
// one, the rest as text.
func NewestKernel(versions []string) string {
	nums := func(v string) []int {
		var out []int
		n, in := 0, false
		for _, c := range v {
			if c >= '0' && c <= '9' {
				n, in = n*10+int(c-'0'), true
				continue
			}
			if in {
				out = append(out, n)
				n, in = 0, false
			}
		}
		if in {
			out = append(out, n)
		}
		return out
	}
	best := ""
	for _, v := range versions {
		if best == "" {
			best = v
			continue
		}
		a, b := nums(v), nums(best)
		newer := false
		for i := 0; ; i++ {
			if i >= len(a) || i >= len(b) {
				newer = len(a) > len(b) || (len(a) == len(b) && v > best)
				break
			}
			if a[i] != b[i] {
				newer = a[i] > b[i]
				break
			}
		}
		if newer {
			best = v
		}
	}
	return best
}
