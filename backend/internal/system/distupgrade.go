package system

// System upgrade (the next major Debian release) from the interface: the
// pure parts — which release comes next, read from the Debian archive's
// Release files, the APT sources rewrite, the compatibility checks and the
// progress of apt. The API (api/h_distupgrade.go) gathers the facts and runs
// the steps; nothing here names a particular release: the codename and the
// number come from the archive (Codename, Version), so a new Debian release
// is offered without a new StrataOS.

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// releases maps codenames to their major numbers: the ones known when this
// was written, and those read from the archive since (RememberRelease).
var releases = struct {
	sync.RWMutex
	m map[string]string
}{m: map[string]string{"buster": "10", "bullseye": "11", "bookworm": "12", "trixie": "13", "forky": "14", "duke": "15"}}

// RememberRelease records the major number of a release read from the archive.
func RememberRelease(codename, major string) {
	if !validCodename(codename) || major == "" {
		return
	}
	if _, err := strconv.Atoi(major); err != nil {
		return
	}
	releases.Lock()
	releases.m[codename] = major
	releases.Unlock()
}

// ReleaseNumber is the major version of a Debian release ("" when unknown).
func ReleaseNumber(codename string) string {
	releases.RLock()
	defer releases.RUnlock()
	return releases.m[codename]
}

// NextRelease is the release after codename among the known ones ("" when
// unknown); the archive says which one is stable (TargetRelease).
func NextRelease(codename string) string {
	n, err := strconv.Atoi(ReleaseNumber(codename))
	if err != nil {
		return ""
	}
	releases.RLock()
	defer releases.RUnlock()
	for c, v := range releases.m {
		if v == strconv.Itoa(n+1) {
			return c
		}
	}
	return ""
}

// ArchiveRelease is a suite of the Debian archive as its Release file says.
type ArchiveRelease struct {
	Suite    string `json:"suite"`    // stable, oldstable, testing
	Codename string `json:"codename"` // trixie…
	Version  string `json:"version"`  // 13.2 ("" for testing)
}

// Major is the major number of the release ("" for testing).
func (r ArchiveRelease) Major() string {
	m, _, _ := strings.Cut(r.Version, ".")
	return m
}

// Point is the point release (13.2 → 2; 0 for 13 or 13.0).
func (r ArchiveRelease) Point() int {
	_, p, ok := strings.Cut(r.Version, ".")
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// ParseRelease reads the Suite, Codename and Version fields of a Release file.
func ParseRelease(release string) ArchiveRelease {
	var r ArchiveRelease
	for _, l := range strings.Split(release, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "Suite":
			r.Suite = v
		case "Codename":
			if validCodename(v) {
				r.Codename = v
			}
		case "Version":
			if _, err := strconv.ParseFloat(v, 64); err == nil {
				r.Version = v
			}
		}
		if k == "MD5Sum" || k == "SHA256" {
			break
		}
	}
	return r
}

// Target is the release a system on current may upgrade to.
type Target struct {
	Codename string `json:"codename"`
	Number   string `json:"number"`
	Version  string `json:"version,omitempty"` // its latest point release
	// Ready: it has had its first point release (N.1), when StrataOS
	// proposes it; Testing: Debian testing, offered only with the override.
	Ready   bool `json:"ready"`
	Testing bool `json:"testing,omitempty"`
}

// TargetRelease picks the next major release of current from the suites of
// the archive: the stable or oldstable one whose major is current's plus
// one; with allowTesting and no such release, testing (for tests: it has no
// number yet, the next one is assumed). Zero when there is none.
func TargetRelease(currentMajor string, suites []ArchiveRelease, allowTesting bool) Target {
	n, err := strconv.Atoi(currentMajor)
	if err != nil {
		return Target{}
	}
	next := strconv.Itoa(n + 1)
	for _, r := range suites {
		if (r.Suite == "stable" || r.Suite == "oldstable") && r.Major() == next && r.Codename != "" {
			return Target{Codename: r.Codename, Number: next, Version: r.Version, Ready: r.Point() >= 1}
		}
	}
	if allowTesting {
		for _, r := range suites {
			if r.Suite == "testing" && r.Codename != "" && ReleaseNumber(r.Codename) != currentMajor {
				return Target{Codename: r.Codename, Number: next, Ready: true, Testing: true}
			}
		}
	}
	return Target{}
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

// UpgradeCheck is one compatibility check (or post-check) of a release
// upgrade; Fix says what to do when it blocks or warns.
type UpgradeCheck struct {
	ID    string `json:"id"`
	Level string `json:"level"` // ok | warn | block
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"`
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
	// The simulation of the upgrade against the new release's package
	// lists (SimDone false: it could not run, SimError says why).
	SimDone        bool
	SimError       string
	Removed        []string // packages the upgrade would remove
	StrataRemoved  bool     // StrataOS itself would be removed: no build for the target
	ComponentsGone []string // StrataOS components whose packages would be removed
	Foreign        []string // installed packages no source provides (local builds…)
	KernelPending  string   // newer kernel installed, not running yet
	DKMS           []string // DKMS modules (rebuilt for the new kernel)
	DKMSBroken     []string // DKMS modules not built for the running kernel
	BootLoader     string   // "" when the boot loader is set up, else the problem
	Apps           int      // App Center apps (containers: not changed by the upgrade)
}

// GiB is a gibibyte.
const GiB = 1 << 30

// UpgradeChecks evaluates the compatibility checks: any "block" keeps the
// upgrade from being proposed (an administrator may still force it).
func UpgradeChecks(f UpgradeFacts) []UpgradeCheck {
	var out []UpgradeCheck
	add := func(id, level, text, fix string) { out = append(out, UpgradeCheck{id, level, text, fix}) }
	switch {
	case f.FreeRoot < 3*GiB:
		add("space-root", "block", fmt.Sprintf("Only %.1f GB free on the system disk: the upgrade needs about 3 GB", float64(f.FreeRoot)/GiB), "Free space on the system disk: remove old restore points (Settings → Restore points) and unused Docker images (Docker → Clean up)")
	case f.FreeRoot < 5*GiB:
		add("space-root", "warn", fmt.Sprintf("%.1f GB free on the system disk: enough, with little to spare", float64(f.FreeRoot)/GiB), "")
	default:
		add("space-root", "ok", fmt.Sprintf("%.1f GB free on the system disk", float64(f.FreeRoot)/GiB), "")
	}
	if f.FreeVar >= 0 && f.FreeVar < 2*GiB {
		add("space-var", "block", "Less than 2 GB free in /var for the downloaded packages", "Free space in /var: clean the package cache (apt-get clean) and old logs")
	}
	if f.FreeBoot >= 0 && f.FreeBoot < 200<<20 {
		add("space-boot", "block", "Less than 200 MB free in /boot for the new kernel", "Remove old kernels (apt-get autoremove) to free space in /boot")
	}
	switch f.RestoreMode {
	case "":
		add("restore-point", "warn", "No restore point can be made on this system: a failed upgrade must be repaired by hand (a backup of the system disk brings it back)", "")
	case "btrfs", "lvm":
		add("restore-point", "ok", "A restore point is made first ("+f.RestoreMode+"): if the upgrade or the checks after the restart fail, the NAS goes back to it by itself", "")
	default:
		add("restore-point", "warn", "This system keeps only the versions of updated packages, which cannot bring back a whole release: a failed upgrade must be repaired by hand (systems installed from the StrataOS ISO go back automatically)", "")
	}
	if len(f.Jobs) > 0 {
		add("jobs", "block", "Wait for these operations to finish: "+strings.Join(f.Jobs, ", "), "The checks run again by themselves once a day, or now with Check again")
	}
	if f.AptBusy {
		add("apt", "block", "Another package operation is running", "Wait for it to finish, then check again")
	}
	if strings.TrimSpace(f.DpkgAudit) != "" {
		add("dpkg", "block", "Some packages are half installed: run the system updates first (dpkg --audit)", "Install the system updates (Updates), which finish the configuration of the packages")
	}
	if len(f.Held) > 0 {
		add("held", "warn", "Packages held at their version (they stay so, which may block the upgrade): "+strings.Join(f.Held, ", "), "Release them with apt-mark unhold if they may be upgraded")
	}
	if len(f.Foreign) > 0 {
		add("foreign", "warn", "Installed packages that no package source provides (kept at their version): "+strings.Join(f.Foreign, ", "), "Remove them if they are not needed, or check that they work on the new release")
	}
	if f.PendingUpdates > 0 {
		add("updates", "block", fmt.Sprintf("Install the %d pending updates of Debian %s first", f.PendingUpdates, ReleaseNumber(f.From)), "Updates → Install the updates")
	}
	if f.KernelPending != "" {
		add("kernel", "block", "A newer kernel ("+f.KernelPending+") is installed but not running yet", "Restart the NAS first")
	} else {
		add("kernel", "ok", "The newest installed kernel is running", "")
	}
	if len(f.DKMSBroken) > 0 {
		add("dkms", "block", "Kernel modules not built for the running kernel: "+strings.Join(f.DKMSBroken, ", "), "Rebuild them (dkms autoinstall) or remove the component that installed them")
	} else if len(f.DKMS) > 0 {
		add("dkms", "ok", "Kernel modules rebuilt for the new kernel: "+strings.Join(f.DKMS, ", "), "")
	}
	if f.BootLoader != "" {
		add("bootloader", "block", "Boot loader: "+f.BootLoader, "Reinstall the boot loader (dpkg-reconfigure grub-efi-amd64 or grub-pc) and check again")
	} else {
		add("bootloader", "ok", "The boot loader is set up", "")
	}
	if f.Moved == 0 {
		add("sources", "block", "No package source of Debian "+ReleaseNumber(f.From)+" was found to move to Debian "+ReleaseNumber(f.To), "The APT sources of this system are unusual: upgrade by hand (docs/DEBIAN-14.md)")
	}
	for _, v := range f.Vendors {
		if v.Kept {
			add("vendor", "warn", fmt.Sprintf("%s does not publish %s yet: it stays on %s", v.URI, f.To, v.From), "")
		}
	}
	switch {
	case !f.SimDone && f.SimError != "":
		add("simulation", "warn", "The upgrade could not be simulated against Debian "+ReleaseNumber(f.To)+": "+f.SimError, "")
	case f.StrataRemoved:
		add("strataos", "block", "StrataOS has no build for Debian "+ReleaseNumber(f.To)+" yet: the upgrade would remove it", "Wait for a StrataOS update that supports Debian "+ReleaseNumber(f.To)+", then check again")
	case f.SimDone:
		add("strataos", "ok", "StrataOS installs on Debian "+ReleaseNumber(f.To), "")
	}
	if len(f.ComponentsGone) > 0 {
		add("components", "block", "These components would be removed by the upgrade: "+strings.Join(f.ComponentsGone, ", "), "Uninstall them first (Settings → Components) or wait until Debian "+ReleaseNumber(f.To)+" provides them")
	}
	if len(f.Removed) > 0 {
		add("removed", "warn", "Packages the upgrade removes: "+strings.Join(f.Removed, ", "), "")
	}
	if f.Apps > 0 {
		add("apps", "ok", fmt.Sprintf("%d apps run in containers: the upgrade does not change them", f.Apps), "")
	}
	if f.ZFS {
		add("zfs", "ok", "ZFS is rebuilt for the new kernel by DKMS; the restart happens only once it is built", "")
	}
	if f.CephNodes > 1 {
		add("ceph", "block", "This NAS is a node of a Ceph cluster: every node must be upgraded together (monitors first, noout set), which this upgrade does not do. See DEBIAN-14.md", "Upgrade the cluster by hand, node by node")
	} else if f.CephNodes == 1 {
		add("ceph", "warn", "Ceph moves to a new major release with the system", "")
	}
	if f.LDAP {
		add("ldap", "warn", "Joined to an LDAP directory: check that sssd is in the new release first (DEBIAN-14.md)", "")
	}
	if f.RaspberryPi {
		add("rpi", "warn", "Raspberry Pi: its kernel archive stays on the current release until it publishes kernels for the new one", "")
	}
	if len(f.MissingInTarget) > 0 {
		add("missing", "warn", "Not in the new release (kept at their current version, without updates): "+strings.Join(f.MissingInTarget, ", "), "")
	}
	return out
}

// Failed lists the checks that block.
func Failed(checks []UpgradeCheck) []UpgradeCheck {
	var out []UpgradeCheck
	for _, c := range checks {
		if c.Level == "block" {
			out = append(out, c)
		}
	}
	return out
}

// ParseSimulation reads apt-get -s full-upgrade: the packages removed.
func ParseSimulation(sim string) (removed []string) {
	for _, l := range strings.Split(sim, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "Remv" {
			removed = append(removed, f[1])
		}
	}
	return removed
}

// ParseForeign reads apt list --installed: the packages marked local (no
// source provides their installed version, nor any version).
func ParseForeign(list string) []string {
	var out []string
	for _, l := range strings.Split(list, "\n") {
		if strings.Contains(l, ",local]") || strings.HasSuffix(strings.TrimSpace(l), "[installed,local]") {
			name, _, _ := strings.Cut(l, "/")
			if name != "" && !strings.Contains(name, " ") {
				out = append(out, name)
			}
		}
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
