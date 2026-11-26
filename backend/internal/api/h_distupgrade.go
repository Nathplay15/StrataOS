package api

// System upgrade to the next major Debian release. The NAS reads the
// archive's Release files (stable, oldstable) itself: the release after
// the running one is proposed once it has had its first point release
// (N.1), whatever its number, without a new StrataOS; the administrator's
// override offers Debian testing (for tests). Compatibility checks run once
// a day (and on demand); when they pass the proposal appears in Updates
// and the administrators are told; when they fail, Updates lists what
// blocks, and an administrator may force the upgrade after a typed
// confirmation. Started now or in the maintenance window. Steps:
// a restore point, the APT sources
// moved to the new release (vendor repositories only when they publish
// it), the packages downloaded then upgraded with the managed configuration
// files kept (--force-confold), StrataOS reinstalled, the ZFS module checked
// for the new kernel; then the restart, and post-checks at the next start.
// apt runs in its own systemd unit (strata-apt): a restart of this service
// during the upgrade does not stop it, and the job resumes after it.

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"strataos/internal/components"
	"strataos/internal/docker"
	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

const distDir = "/var/lib/strata/dist-upgrade"

var distCache struct {
	sync.Mutex
	at     time.Time
	suites []system.ArchiveRelease
}

// distSourceFiles lists the APT sources files (path → deb822).
func distSourceFiles() map[string]bool {
	out := map[string]bool{}
	if _, err := os.Stat("/etc/apt/sources.list"); err == nil {
		out["/etc/apt/sources.list"] = false
	}
	for _, pat := range []string{"/etc/apt/sources.list.d/*.list", "/etc/apt/sources.list.d/*.sources"} {
		m, _ := filepath.Glob(pat)
		for _, p := range m {
			// StrataOS's own channels name no Debian release.
			if b := filepath.Base(p); b == filepath.Base(system.StrataSources) || b == filepath.Base(system.BetaSources) {
				continue
			}
			out[p] = strings.HasSuffix(p, ".sources")
		}
	}
	return out
}

// distMirror is the Debian mirror the NAS uses.
func distMirror() string {
	for p, deb822 := range distSourceFiles() {
		data, _ := os.ReadFile(p)
		for _, l := range strings.Split(string(data), "\n") {
			var uri string
			if deb822 {
				if v, ok := strings.CutPrefix(strings.TrimSpace(l), "URIs:"); ok {
					if f := strings.Fields(v); len(f) > 0 {
						uri = f[0]
					}
				}
			} else if f := strings.Fields(l); len(f) >= 3 && f[0] == "deb" && !strings.HasPrefix(f[1], "[") {
				uri = f[1]
			}
			uri = system.MirrorListURI(uri, readSmall)
			if system.IsDebianURI(uri) && !strings.Contains(uri, "security") {
				return strings.TrimRight(uri, "/")
			}
		}
	}
	return "http://deb.debian.org/debian"
}

// distFetch reads a small file of an archive.
func distFetch(ctx context.Context, url string) (string, int, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", 0, errors.New("unsupported URI")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10)) // the head of a Release file is enough
	return string(b), resp.StatusCode, nil
}

// distSuites reads the stable, oldstable and testing suites of the archive
// (6 h cache); the numbers they give are remembered.
func distSuites(ctx context.Context) []system.ArchiveRelease {
	distCache.Lock()
	defer distCache.Unlock()
	if time.Since(distCache.at) < 6*time.Hour && len(distCache.suites) > 0 {
		return distCache.suites
	}
	m := distMirror()
	var out []system.ArchiveRelease
	for _, suite := range []string{"stable", "oldstable", "testing"} {
		if b, code, err := distFetch(ctx, m+"/dists/"+suite+"/Release"); err == nil && code == 200 {
			r := system.ParseRelease(b)
			r.Suite = suite
			if r.Codename == "" {
				continue
			}
			system.RememberRelease(r.Codename, r.Major())
			out = append(out, r)
		}
	}
	distCache.suites, distCache.at = out, time.Now()
	return out
}

// currentMajor is the major Debian version running (os-release, else the
// codename's number).
func currentMajor() string {
	b, _ := os.ReadFile("/etc/os-release")
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "VERSION_ID="); ok {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if m, _, _ := strings.Cut(v, "."); m != "" {
				return m
			}
		}
	}
	return system.ReleaseNumber(system.Codename())
}

// vendorPublishes tells whether a repository has a suite.
func vendorPublishes(ctx context.Context, uri, suite string) bool {
	base := strings.TrimRight(uri, "/") + "/dists/" + suite + "/"
	for _, f := range []string{"InRelease", "Release"} {
		if _, code, err := distFetch(ctx, base+f); err == nil && code == 200 {
			return true
		}
	}
	return false
}

// distRewrite computes the new sources (path → content) and the changes.
func distRewrite(ctx context.Context, from, to string) (map[string]string, []system.SourceChange) {
	files := map[string]string{}
	var all []system.SourceChange
	known := map[string]bool{}
	has := func(uri string) bool {
		uri = system.MirrorListURI(uri, readSmall)
		if system.IsDebianURI(uri) {
			return true // a mirror list of the Debian archive (cloud images)
		}
		if v, ok := known[uri]; ok {
			return v
		}
		// The Raspberry Pi archive publishes forky without kernels yet (DEBIAN-14.md).
		v := !strings.Contains(uri, "raspberrypi.com") && vendorPublishes(ctx, uri, to)
		known[uri] = v
		return v
	}
	for p, deb822 := range distSourceFiles() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out, ch := system.RewriteSources(string(data), deb822, from, to, has)
		if out != string(data) {
			files[p] = out
		}
		all = append(all, ch...)
	}
	return files, all
}

// readSmall reads a small local file ("" when it cannot).
func readSmall(p string) string {
	if !filepath.IsAbs(p) || strings.Contains(p, "..") {
		return ""
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
		return ""
	}
	b, _ := os.ReadFile(p)
	return string(b)
}

func freeOf(p string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(p, &st) != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

func sameFS(a, b string) bool {
	var x, y syscall.Stat_t
	return syscall.Stat(a, &x) == nil && syscall.Stat(b, &y) == nil && x.Dev == y.Dev
}

// distFacts gathers what the pre-checks look at.
func (s *Server) distFacts(ctx context.Context, from, to string, deep bool) (system.UpgradeFacts, map[string]string, []system.SourceChange) {
	f := system.UpgradeFacts{From: from, To: to, FreeRoot: freeOf("/"), FreeVar: freeOf("/var/cache/apt/archives"), FreeBoot: -1}
	if !sameFS("/boot", "/") {
		f.FreeBoot = freeOf("/boot")
	}
	f.RestoreMode, _ = system.Mode()
	for _, j := range s.jobs.List() {
		if j.Status == jobs.Running && j.Type != "dist-upgrade" && j.Type != "dist-check" {
			f.Jobs = append(f.Jobs, j.Title)
		}
	}
	f.AptBusy = aptBusy()
	f.DpkgAudit, _ = sys.Run("dpkg", "--audit")
	if out, err := sys.Run("apt-mark", "showhold"); err == nil {
		f.Held = strings.Fields(out)
	}
	if out, err := sys.Run("apt-get", "-s", "-q", "upgrade"); err == nil {
		f.PendingUpdates = system.PlanCount(out)
	}
	if out, err := sys.Run("dpkg-query", "-W", "-f=${Status}", "zfs-dkms"); err == nil && strings.Contains(out, "installed") {
		f.ZFS = true
	}
	if s.cephLocal() != nil {
		f.CephNodes = max(1, len(s.clusterState().Members))
	}
	if out, _ := sys.Run("systemctl", "is-active", "sssd.service"); strings.TrimSpace(out) == "active" {
		f.LDAP = true
	}
	if b, err := os.ReadFile("/proc/device-tree/model"); err == nil && strings.Contains(string(b), "Raspberry") {
		f.RaspberryPi = true
	}
	files, changes := distRewrite(ctx, from, to)
	for _, c := range changes {
		if c.Kept {
			f.Vendors = append(f.Vendors, c)
		} else {
			f.Moved++
		}
	}
	if out, err := sys.Run("apt", "list", "--installed"); err == nil {
		f.Foreign = slices.DeleteFunc(system.ParseForeign(out), func(p string) bool { return p == "strataos" })
	}
	if k := distKernel(); k != "" {
		if u, _ := sys.Run("uname", "-r"); strings.TrimSpace(u) != "" && strings.TrimSpace(u) != k {
			f.KernelPending = k
		}
	}
	f.DKMS, f.DKMSBroken = dkmsModules()
	f.BootLoader = bootLoaderProblem()
	if stacks, err := docker.ListStacks(ctx, s.docker); err == nil {
		f.Apps = len(stacks)
	}
	if deep {
		distSimulate(ctx, &f, files)
	}
	return f, files, changes
}

// dkmsModules lists the DKMS modules, and those not built for the running
// kernel (dkms status: "zfs/2.3.2, 6.12.9-amd64, x86_64: installed").
func dkmsModules() (mods, broken []string) {
	if !sys.Has("dkms") {
		return nil, nil
	}
	out, _ := sys.Run("dkms", "status")
	u, _ := sys.Run("uname", "-r")
	u = strings.TrimSpace(u)
	built := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		name, rest, ok := strings.Cut(l, "/")
		if !ok || name == "" {
			continue
		}
		if !slices.Contains(mods, name) {
			mods = append(mods, name)
		}
		if strings.Contains(rest, ", "+u+",") && strings.Contains(rest, "installed") {
			built[name] = true
		}
	}
	for _, m := range mods {
		if !built[m] {
			broken = append(broken, m)
		}
	}
	return mods, broken
}

// bootLoaderProblem says what is wrong with the boot loader ("" when it is
// set up): its package, and in UEFI mode the EFI system partition.
func bootLoaderProblem() string {
	installed := func(p string) bool {
		out, err := sys.Run("dpkg-query", "-W", "-f=${Status}", p)
		return err == nil && strings.Contains(out, "ok installed")
	}
	if _, err := os.Stat("/sys/firmware/efi"); err == nil {
		if !installed("grub-efi-amd64") && !installed("grub-efi-arm64") && !installed("grub-efi-amd64-signed") {
			return "no GRUB package for UEFI is installed"
		}
		if out, _ := sys.Run("findmnt", "-n", "-o", "FSTYPE", "/boot/efi"); strings.TrimSpace(out) != "vfat" {
			return "the EFI system partition is not mounted on /boot/efi"
		}
		return ""
	}
	if _, err := os.Stat("/boot/firmware/config.txt"); err == nil {
		return "" // Raspberry Pi: its firmware starts the kernel
	}
	if !installed("grub-pc") {
		return "no GRUB package for BIOS (grub-pc) is installed"
	}
	return ""
}

// distSimulate runs the upgrade against the new release's package lists in
// a folder of its own (the real APT state is not touched): what it removes,
// whether StrataOS and the components stay, which packages it has not.
func distSimulate(ctx context.Context, f *system.UpgradeFacts, files map[string]string) {
	dir := "/var/cache/strata/dist-check"
	parts := filepath.Join(dir, "sources.list.d")
	_ = os.RemoveAll(parts)
	for _, d := range []string{parts, filepath.Join(dir, "lists", "partial"), filepath.Join(dir, "cache", "archives", "partial")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			f.SimError = err.Error()
			return
		}
	}
	n := 0
	for p, deb822 := range distSourceFiles() {
		content, ok := files[p]
		if !ok {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			content = string(b)
		}
		ext := ".list"
		if deb822 {
			ext = ".sources"
		}
		n++
		_ = os.WriteFile(filepath.Join(parts, fmt.Sprintf("%02d-%s%s", n, strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)), ext)), []byte(content), 0o644)
	}
	// StrataOS's own channels as they are: is there a build for the target?
	for _, p := range []string{system.StrataSources, system.BetaSources} {
		if b, err := os.ReadFile(p); err == nil {
			n++
			_ = os.WriteFile(filepath.Join(parts, fmt.Sprintf("%02d-%s", n, filepath.Base(p))), b, 0o644)
		}
	}
	opts := []string{"-o", "Dir::Etc::SourceList=/dev/null", "-o", "Dir::Etc::SourceParts=" + parts,
		"-o", "Dir::State::Lists=" + filepath.Join(dir, "lists"), "-o", "Dir::Cache=" + filepath.Join(dir, "cache")}
	run := func(timeout time.Duration, args ...string) (string, error) {
		return sys.RunOpts(ctx, sys.Opts{Timeout: timeout}, "nice", append([]string{"-n", "19", "ionice", "-c3"}, args...)...)
	}
	if _, err := run(20*time.Minute, append(append([]string{"apt-get", "-q"}, opts...), "update")...); err != nil {
		f.SimError = "the package lists could not be read: " + firstLineOf(err.Error())
		return
	}
	sim, err := run(10*time.Minute, append(append([]string{"apt-get", "-s", "-q"}, opts...), "full-upgrade")...)
	if err != nil {
		f.SimError = firstLineOf(err.Error())
		return
	}
	f.SimDone = true
	f.Removed = system.ParseSimulation(sim)
	f.StrataRemoved = slices.Contains(f.Removed, "strataos")
	for _, c := range components.All {
		for _, p := range c.Packages {
			if slices.Contains(f.Removed, p) && !slices.Contains(f.ComponentsGone, c.Name) {
				f.ComponentsGone = append(f.ComponentsGone, c.Name)
			}
		}
	}
	if out, err := run(5*time.Minute, append(append([]string{"apt"}, opts...), "list", "--installed")...); err == nil {
		for _, p := range system.ParseForeign(out) {
			if p != "strataos" && !slices.Contains(f.Foreign, p) {
				f.MissingInTarget = append(f.MissingInTarget, p)
			}
		}
	}
}

type distView struct {
	Current        string                `json:"current"`
	CurrentVersion string                `json:"currentVersion"`
	Target         string                `json:"target"`
	TargetVersion  string                `json:"targetVersion"`
	TargetPoint    string                `json:"targetPoint,omitempty"` // its latest point release (14.1)
	Testing        bool                  `json:"testing,omitempty"`
	Available      bool                  `json:"available"`
	Reason         string                `json:"reason,omitempty"`
	AllowTesting   bool                  `json:"allowTesting"`
	Run            *store.DistUpgradeRun `json:"run,omitempty"`
	Check          *store.DistCheck      `json:"check,omitempty"`
	// Proposal: shown in Updates ("Do you want to upgrade…?"); Dismissed:
	// the administrator answered No (until the next check).
	Proposal   bool                `json:"proposal"`
	Dismissed  bool                `json:"dismissed,omitempty"`
	Scheduled  *store.DistSchedule `json:"scheduled,omitempty"`
	NextWindow time.Time           `json:"nextWindow,omitzero"`
}

func (s *Server) distState(ctx context.Context) distView {
	cur := system.Codename()
	suites := distSuites(ctx)
	v := distView{Current: cur, CurrentVersion: currentMajor()}
	var st store.DistUpgrade
	s.store.Read(func(x *store.State) {
		st = x.DistUpgrade
		if r := st.Run; r != nil {
			c := *r
			v.Run = &c
		}
		if c := st.Check; c != nil {
			cc := *c
			v.Check = &cc
		}
		if sc := st.Scheduled; sc != nil {
			c := *sc
			v.Scheduled = &c
		}
	})
	v.AllowTesting = st.AllowTesting
	t := system.TargetRelease(v.CurrentVersion, suites, st.AllowTesting)
	v.Target, v.TargetVersion, v.TargetPoint, v.Testing = t.Codename, t.Number, t.Version, t.Testing
	switch {
	case len(suites) == 0:
		v.Reason = "The Debian archive could not be reached."
	case t.Codename == "":
		v.Reason = "This NAS runs the latest Debian release."
	case !t.Ready:
		v.Reason = fmt.Sprintf("Debian %s is out: it is proposed after its first point release (%s.1).", t.Number, t.Number)
	default:
		v.Available = true
	}
	if v.Available && v.Check != nil && v.Check.Target == v.Target {
		v.Dismissed = st.DismissedFor == v.Target && st.DismissedAt.After(v.Check.At)
		busy := v.Run != nil && !slices.Contains([]string{"done", "failed", "rolled-back"}, v.Run.Phase)
		v.Proposal = !v.Check.Blocked && !v.Dismissed && !busy && v.Scheduled == nil
	}
	v.NextWindow = nextDistWindow(s.autoConfig(), time.Now())
	return v
}

// nextDistWindow is the next maintenance window (the hours of the automatic
// updates, whether they are on or not).
func nextDistWindow(c store.AutoUpdates, now time.Time) time.Time {
	if ws, ok := autoWindow(c, now); ok {
		return ws
	}
	for i := 0; i < 8; i++ {
		ws := windowStart(now.AddDate(0, 0, i), c.Hour)
		if ws.After(now) && autoDays(c, ws.Weekday()) {
			return ws
		}
	}
	return time.Time{}
}

// distCheck runs the compatibility checks of the next release (the
// simulation included) and records them; a new proposal is told to the
// administrators once per release.
func (s *Server) distCheck(ctx context.Context, logf func(string, ...any)) (distView, error) {
	v := s.distState(ctx)
	if v.Target == "" {
		return v, errors.New(firstNonEmpty(v.Reason, "no newer Debian release"))
	}
	logf("Checking the upgrade from Debian %s (%s) to Debian %s (%s)", v.CurrentVersion, v.Current, v.TargetVersion, v.Target)
	f, _, _ := s.distFacts(ctx, v.Current, v.Target, true)
	f.From, f.To = v.Current, v.Target
	checks := system.UpgradeChecks(f)
	c := &store.DistCheck{At: time.Now(), Target: v.Target, Number: v.TargetVersion, Blocked: system.Blocked(checks)}
	for _, x := range checks {
		c.Checks = append(c.Checks, store.UpgradeResult{ID: x.ID, Level: x.Level, Text: x.Text, Fix: x.Fix})
		logf("%s: %s", x.Level, x.Text)
	}
	_ = s.store.Update(func(st *store.State) error {
		st.DistUpgrade.Check = c
		return nil
	})
	v = s.distState(ctx)
	if v.Proposal {
		var told string
		s.store.Read(func(st *store.State) { told = st.DistUpgrade.NotifiedFor })
		if told != v.Target {
			_ = s.store.Update(func(st *store.State) error { st.DistUpgrade.NotifiedFor = v.Target; return nil })
			s.notify("info", "System upgrade available", fmt.Sprintf("Debian %s can be installed on this NAS: see Updates", v.TargetVersion))
		}
	}
	return v, nil
}

// distCheckLoop checks the next release once a day.
func (s *Server) distCheckLoop() {
	time.Sleep(3 * time.Minute)
	for {
		v := s.distState(context.Background())
		busy := v.Run != nil && !slices.Contains([]string{"done", "failed", "rolled-back"}, v.Run.Phase)
		if v.Target != "" && !busy {
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			_, _ = s.distCheck(ctx, func(string, ...any) {})
			cancel()
		}
		time.Sleep(24 * time.Hour)
	}
}

func (s *Server) distSet(f func(r *store.DistUpgradeRun)) {
	_ = s.store.Update(func(st *store.State) error {
		if st.DistUpgrade.Run != nil {
			f(st.DistUpgrade.Run)
		}
		return nil
	})
}

// distApt runs apt in the strata-apt unit (it survives a restart of this
// service), its output parsed for the progress.
func distApt(ctx context.Context, j *jobs.Job, from, to float64, total int, args ...string) error {
	if aptBusy() {
		return errors.New("another package operation is already running")
	}
	full := append([]string{"--unit=strata-apt", "--collect", "--wait", "--pipe", "--quiet",
		"--setenv=DEBIAN_FRONTEND=noninteractive", "--setenv=NEEDRESTART_MODE=l", "--setenv=APT_LISTCHANGES_FRONTEND=none",
		"--property=TimeoutStopSec=1h"}, args...)
	j.Logf("$ %s", strings.Join(args, " "))
	c := &system.AptCounter{Total: total}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			l := sc.Text()
			fmt.Fprintln(j, l)
			if total > 0 {
				j.SetProgress(from + (to-from)*c.Line(l)/100)
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	_, err := sys.RunOpts(ctx, sys.Opts{Output: pw, Timeout: 6 * time.Hour}, "systemd-run", full...)
	pw.Close()
	<-done
	return err
}

var aptKeepConf = []string{"-o", "Dpkg::Options::=--force-confold", "-o", "Dpkg::Options::=--force-confdef"}

// distKernel is the newest kernel installed (the one the next start uses).
func distKernel() string {
	m, _ := filepath.Glob("/boot/vmlinuz-*")
	var vs []string
	for _, f := range m {
		vs = append(vs, strings.TrimPrefix(filepath.Base(f), "vmlinuz-"))
	}
	return system.NewestKernel(vs)
}

// zfsBuilt tells whether the ZFS module is built for a kernel.
func zfsBuilt(kernel string) bool {
	m, _ := filepath.Glob("/lib/modules/" + kernel + "/updates/dkms/zfs.ko*")
	return len(m) > 0
}

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// distRun runs (or resumes) the upgrade from the phase of the run.
func (s *Server) distRun(ctx context.Context, j *jobs.Job) error {
	var run store.DistUpgradeRun
	s.store.Read(func(st *store.State) { run = *st.DistUpgrade.Run })
	fail := func(err error) error {
		s.distSet(func(r *store.DistUpgradeRun) { r.Phase, r.Error, r.Finished = "failed", err.Error(), time.Now() })
		s.notify("error", "Debian upgrade failed", err.Error())
		return err
	}
	phase := func(p string) { s.distSet(func(r *store.DistUpgradeRun) { r.Phase = p }) }
	restoreSources := func() {
		if run.SourcesDir == "" {
			return
		}
		entries, _ := os.ReadDir(run.SourcesDir)
		for _, e := range entries {
			if dst, err := hexDecodePath(e.Name()); err == nil {
				if data, err := os.ReadFile(filepath.Join(run.SourcesDir, e.Name())); err == nil {
					_ = sys.WriteFileAtomic(dst, data, 0o644)
				}
			}
		}
		j.Logf("APT sources put back to %s", run.From)
		_, _ = sys.Run("apt-get", "update", "-q")
	}
	for aptBusy() { // a run of apt left by a restart of this service
		j.Logf("Waiting for the package operation in progress")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	switch run.Phase {
	case "sources":
		if p, err := system.CreateRestorePoint("before-dist-upgrade"); err == nil {
			j.Logf("Restore point: %s", p)
			s.distSet(func(r *store.DistUpgradeRun) { r.RestorePoint = p })
		} else {
			j.Logf("No restore point: %v", err)
		}
		j.SetProgress(3)
		files, changes := distRewrite(ctx, run.From, run.To)
		dir := filepath.Join(distDir, "sources-"+time.Now().Format("20060102-150405"))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail(err)
		}
		for p := range files {
			data, err := os.ReadFile(p)
			if err != nil {
				return fail(err)
			}
			if err := os.WriteFile(filepath.Join(dir, hexEncodePath(p)), data, 0o600); err != nil {
				return fail(err)
			}
		}
		run.SourcesDir = dir
		s.distSet(func(r *store.DistUpgradeRun) { r.SourcesDir = dir })
		for p, c := range files {
			if err := sys.WriteFileAtomic(p, []byte(c), 0o644); err != nil {
				restoreSources()
				return fail(err)
			}
		}
		for _, c := range changes {
			if c.Kept {
				j.Logf("%s: stays on %s (no %s yet)", c.URI, c.From, run.To)
			} else {
				j.Logf("%s: %s → %s", c.URI, c.From, c.To)
			}
		}
		phase("download")
		fallthrough
	case "download":
		j.SetProgress(5)
		if err := distApt(ctx, j, 0, 0, 0, "apt-get", "update", "-q"); err != nil {
			restoreSources()
			return fail(fmt.Errorf("the package lists of Debian %s could not be read: %v", distNum(run.ToNumber, run.To), err))
		}
		sim, _ := sys.Run("apt-get", "-s", "-q", "full-upgrade")
		n := system.PlanCount(sim)
		j.Logf("%d packages to upgrade or install", n)
		if err := distApt(ctx, j, 5, 30, 0, append(append([]string{"apt-get", "-y", "-q", "--download-only"}, aptKeepConf...), "full-upgrade")...); err != nil {
			restoreSources()
			return fail(fmt.Errorf("download: %v", err))
		}
		phase("upgrade")
		j.SetProgress(30)
		// From here the system changes: the sources stay on the new release.
		if err := distApt(ctx, j, 30, 85, n, append(append([]string{"apt-get", "-y", "-q"}, aptKeepConf...), "full-upgrade")...); err != nil {
			if s.distRollback(j.Logf, "upgrade: "+err.Error()) {
				return fmt.Errorf("upgrade: %v; the NAS goes back to the restore point and restarts", err)
			}
			return fail(fmt.Errorf("upgrade: %v (the system is between two releases: run the upgrade again)", err))
		}
		phase("finish")
		fallthrough
	case "upgrade", "finish":
		if run.Phase == "upgrade" { // resumed: finish what apt left
			if err := distApt(ctx, j, 30, 85, 0, append(append([]string{"apt-get", "-y", "-q"}, aptKeepConf...), "full-upgrade")...); err != nil {
				if s.distRollback(j.Logf, "upgrade: "+err.Error()) {
					return fmt.Errorf("upgrade: %v; the NAS goes back to the restore point and restarts", err)
				}
				return fail(fmt.Errorf("upgrade: %v", err))
			}
		}
		j.SetProgress(86)
		if err := distApt(ctx, j, 86, 92, 0, append(append([]string{"apt-get", "-y", "-q", "--reinstall"}, aptKeepConf...), "install", "strataos")...); err != nil {
			if s.distRollback(j.Logf, "StrataOS reinstall: "+err.Error()) {
				return fmt.Errorf("StrataOS reinstall: %v; the NAS goes back to the restore point and restarts", err)
			}
			return fail(fmt.Errorf("StrataOS reinstall: %v", err))
		}
		k := distKernel()
		s.distSet(func(r *store.DistUpgradeRun) { r.Kernel = k })
		if out, err := sys.Run("dpkg-query", "-W", "-f=${Status}", "zfs-dkms"); err == nil && strings.Contains(out, "installed") && k != "" {
			if !zfsBuilt(k) {
				j.Logf("Building ZFS for %s", k)
				_, _ = sys.RunOpts(ctx, sys.Opts{Output: j, Timeout: time.Hour}, "dkms", "autoinstall", "-k", k)
			}
			if !zfsBuilt(k) {
				if s.distRollback(j.Logf, "the ZFS module was not built for the new kernel "+k) {
					return fmt.Errorf("the ZFS module was not built for the new kernel %s; the NAS goes back to the restore point and restarts", k)
				}
				return fail(fmt.Errorf("the ZFS module was not built for the new kernel %s: do not restart (the pools would not be imported); see dkms status", k))
			}
			j.Logf("ZFS is built for %s", k)
		}
		s.distSet(func(r *store.DistUpgradeRun) { r.Phase, r.BootID = "reboot", bootID() })
		j.SetProgress(100)
		if run.Auto {
			j.Logf("Debian %s is installed: the NAS restarts into it now, then checks itself", distNum(run.ToNumber, run.To))
			s.notify("info", "Debian upgrade: restarting", fmt.Sprintf("Debian %s is installed: the NAS restarts into it and checks itself", distNum(run.ToNumber, run.To)))
			go func() {
				time.Sleep(10 * time.Second)
				_, _ = sys.Run("systemctl", "reboot")
			}()
			return nil
		}
		s.notify("info", "Debian upgrade ready", fmt.Sprintf("Debian %s is installed: restart the NAS to finish", distNum(run.ToNumber, run.To)))
		j.Logf("Upgraded to Debian %s: restart the NAS to finish", distNum(run.ToNumber, run.To))
		return nil
	}
	return nil
}

// distRollback goes back to the restore point made before the upgrade: the
// system subvolume (or LV) is put back, its boot loader installed again, and
// the NAS restarts into the previous release. False when it cannot.
func (s *Server) distRollback(logf func(string, ...any), why string) bool {
	var run store.DistUpgradeRun
	s.store.Read(func(st *store.State) {
		if st.DistUpgrade.Run != nil {
			run = *st.DistUpgrade.Run
		}
	})
	if !run.Rollback || run.RestorePoint == "" {
		return false
	}
	logf("Going back to the restore point %s: %s", run.RestorePoint, why)
	s.distSet(func(r *store.DistUpgradeRun) {
		r.Phase, r.RolledBack, r.Error, r.BootID = "rolling-back", why, why, bootID()
	})
	if err := system.RollbackTo(run.RestorePoint); err != nil {
		logf("The rollback failed: %v", err)
		s.distSet(func(r *store.DistUpgradeRun) {
			r.Phase, r.Finished = "failed", time.Now()
			r.Error = why + "; going back to the restore point failed too: " + err.Error()
		})
		return false
	}
	if err := system.ReinstallBootLoader(logf); err != nil {
		// The restored system has its own kernel and modules: GRUB as it
		// is now usually starts it; say so in the log and go on.
		logf("Boot loader of the restored system: %v", err)
	}
	s.notify("warning", "Debian upgrade rolled back", fmt.Sprintf("The upgrade to Debian %s did not succeed (%s): the NAS restarts into Debian %s", distNum(run.ToNumber, run.To), why, distNum(run.FromNumber, run.From)))
	s.store.Log("warning", "updates", "", fmt.Sprintf("Debian upgrade: back to the restore point %s (%s)", run.RestorePoint, why))
	if !system.RebootsItself() {
		go func() {
			time.Sleep(5 * time.Second)
			_, _ = sys.Run("systemctl", "reboot")
		}()
	}
	return true
}

// distNum is the number of a release of a run (saved when it started).
func distNum(saved, codename string) string {
	return firstNonEmpty(saved, system.ReleaseNumber(codename), codename)
}

// distFailed lists the texts of the checks that block.
func distFailed(c *store.DistCheck) []string {
	var out []string
	for _, x := range c.Checks {
		if x.Level == "block" {
			out = append(out, x.Text)
		}
	}
	return out
}

// distStart records and starts the upgrade (always with a restore point,
// the restart, the checks after it and the rollback on failure).
func (s *Server) distStart(v distView, user string, forced bool) jobs.Snapshot {
	_ = s.store.Update(func(st *store.State) error {
		st.DistUpgrade.Scheduled = nil
		st.DistUpgrade.Run = &store.DistUpgradeRun{From: v.Current, To: v.Target, FromNumber: v.CurrentVersion, ToNumber: v.TargetVersion,
			Started: time.Now(), Phase: "sources", Auto: true, Forced: forced}
		if m, _ := system.Mode(); system.RollbackMode(m) {
			st.DistUpgrade.Run.Rollback = true
		}
		return nil
	})
	return s.jobs.Start("dist-upgrade", fmt.Sprintf("Upgrading Debian %s to %s", v.CurrentVersion, v.TargetVersion), user, s.distRun)
}

// distWindowStart starts a planned upgrade in a maintenance window: the
// checks run again first; failed ones cancel a plan that was not forced.
func (s *Server) distWindowStart(ctx context.Context) {
	v := s.distState(ctx)
	sc := v.Scheduled
	if sc == nil {
		return
	}
	if v.Target != sc.Target || !v.Available {
		_ = s.store.Update(func(st *store.State) error { st.DistUpgrade.Scheduled = nil; return nil })
		return
	}
	v, err := s.distCheck(ctx, func(string, ...any) {})
	if err != nil {
		return
	}
	if failed := distFailed(v.Check); len(failed) > 0 && !sc.Force {
		_ = s.store.Update(func(st *store.State) error { st.DistUpgrade.Scheduled = nil; return nil })
		msg := fmt.Sprintf("The system upgrade to Debian %s planned for the maintenance window did not start: %s", v.TargetVersion, strings.Join(failed, "; "))
		s.store.Log("warning", "updates", sc.By, msg)
		s.notify("warning", "System upgrade not started", msg)
		return
	}
	s.store.Log("info", "updates", sc.By, fmt.Sprintf("System upgrade to Debian %s started in the maintenance window", v.TargetVersion))
	s.distStart(v, sc.By, sc.Force)
}

func hexEncodePath(p string) string { return hex.EncodeToString([]byte(p)) }

func hexDecodePath(n string) (string, error) {
	b, err := hex.DecodeString(n)
	p := string(b)
	if err != nil || !strings.HasPrefix(p, "/etc/apt/") || strings.Contains(p, "..") {
		return "", errors.New("invalid name")
	}
	return p, nil
}

// distPostCheckTestHook: when this file exists (root only, in the settings
// folder: it survives the restart and is not part of a restore point), the
// checks after the restart fail once. Used by the end-to-end test of the
// automatic rollback.
var distPostCheckTestHook = "/var/lib/strata/dist-postcheck-test"

// distPostChecks runs after the restart into the new release.
func (s *Server) distPostChecks(run store.DistUpgradeRun) []store.UpgradeResult {
	var out []store.UpgradeResult
	add := func(id, level, text string) { out = append(out, store.UpgradeResult{ID: id, Level: level, Text: text}) }
	if err := os.Remove(distPostCheckTestHook); err == nil {
		add("test", "block", "Test failure of the checks after the restart")
	}
	if c := system.Codename(); c == run.To {
		add("release", "ok", "Debian "+currentMajor()+" ("+c+") is running")
	} else {
		add("release", "block", "The system still reports "+c)
	}
	if k, _ := sys.Run("uname", "-r"); strings.TrimSpace(k) != "" && run.Kernel != "" && strings.TrimSpace(k) != run.Kernel {
		add("kernel", "warn", "Running kernel "+strings.TrimSpace(k)+", not the new "+run.Kernel)
	} else {
		add("kernel", "ok", "Kernel "+strings.TrimSpace(k))
	}
	if a, _ := sys.Run("dpkg", "--audit"); strings.TrimSpace(a) != "" {
		add("dpkg", "block", "Some packages are not fully installed: run the system updates")
	}
	if f, _ := sys.Run("systemctl", "--failed", "--plain", "--no-legend"); strings.TrimSpace(f) != "" {
		var units []string
		for _, l := range strings.Split(strings.TrimSpace(f), "\n") {
			if fs := strings.Fields(l); len(fs) > 0 {
				units = append(units, fs[0])
			}
		}
		add("units", "warn", "Services that failed: "+strings.Join(units, ", "))
	} else {
		add("units", "ok", "No service failed")
	}
	var missing []string
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			if v.MountPoint != "" && !storage.IsMountpoint(v.MountPoint) {
				missing = append(missing, v.Name)
			}
		}
	})
	if len(missing) > 0 {
		add("volumes", "block", "Volumes not mounted: "+strings.Join(missing, ", "))
	} else {
		add("volumes", "ok", "Every volume is mounted")
	}
	if sys.Has("zpool") {
		if o, _ := sys.Run("zpool", "status", "-x"); !strings.Contains(o, "all pools are healthy") && !strings.Contains(o, "no pools available") {
			add("zfs", "block", "ZFS pools need attention: "+firstLineOf(o))
		}
	}
	return out
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// distResume continues an upgrade after a restart: of this service during
// apt (the job goes on), or of the NAS into the new release (post-checks).
func (s *Server) distResume() {
	var run *store.DistUpgradeRun
	s.store.Read(func(st *store.State) {
		if st.DistUpgrade.Run != nil {
			c := *st.DistUpgrade.Run
			run = &c
		}
	})
	if run == nil {
		return
	}
	switch run.Phase {
	case "download", "upgrade", "finish":
		s.jobs.Start("dist-upgrade", fmt.Sprintf("Upgrading Debian %s to %s (resumed)", distNum(run.FromNumber, run.From), distNum(run.ToNumber, run.To)), "system", s.distRun)
	case "reboot", "post-checks":
		if bootID() == run.BootID {
			return // not restarted yet
		}
		time.Sleep(time.Minute) // volumes and services come up
		s.distSet(func(r *store.DistUpgradeRun) { r.Phase = "post-checks" })
		res := s.distPostChecks(*run)
		failed := slices.ContainsFunc(res, func(c store.UpgradeResult) bool { return c.Level == "block" })
		s.distSet(func(r *store.DistUpgradeRun) { r.PostChecks = res })
		if failed && run.Auto {
			var why []string
			for _, c := range res {
				if c.Level == "block" {
					why = append(why, c.Text)
				}
			}
			logf := func(f string, a ...any) { s.store.Log("info", "updates", "", fmt.Sprintf(f, a...)) }
			if s.distRollback(logf, "checks after the restart: "+strings.Join(why, "; ")) {
				return
			}
		}
		s.distSet(func(r *store.DistUpgradeRun) {
			r.Phase, r.Finished = "done", time.Now()
			if failed {
				r.Error = "some checks failed after the restart"
			}
		})
		if failed {
			s.notify("error", "Debian upgrade: checks failed", "Some checks failed after the restart into the new release: see Updates")
		} else {
			s.notify("info", "Debian upgrade finished", fmt.Sprintf("The NAS runs Debian %s", distNum(run.ToNumber, run.To)))
		}
	case "rolling-back":
		if bootID() == run.BootID {
			return // the restart has not happened yet
		}
		if system.Codename() == run.To {
			s.distSet(func(r *store.DistUpgradeRun) {
				r.Phase, r.Finished = "failed", time.Now()
				r.Error = run.RolledBack + "; the system still runs Debian " + distNum(run.ToNumber, run.To) + " after going back to the restore point: see Restore points"
			})
			return
		}
		s.distSet(func(r *store.DistUpgradeRun) { r.Phase, r.Finished = "rolled-back", time.Now() })
		s.notify("warning", "Debian upgrade rolled back", fmt.Sprintf("The NAS runs Debian %s again, as before the upgrade (%s)", distNum(run.FromNumber, run.From), run.RolledBack))
	}
}

func (s *Server) distUpgradeRoutes() {
	go s.distResume()
	go s.distCheckLoop()

	s.admin("GET /api/system/dist-upgrade", func(r *http.Request) (any, error) {
		return s.distState(r.Context()), nil
	})

	s.admin("PUT /api/system/dist-upgrade", func(r *http.Request) (any, error) {
		var req struct {
			AllowTesting bool `json:"allowTesting"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		_ = s.store.Update(func(st *store.State) error {
			st.DistUpgrade.AllowTesting = req.AllowTesting
			return nil
		})
		if req.AllowTesting {
			s.audit(r, "updates", "Upgrade to the Debian testing release allowed (not recommended)")
		} else {
			s.audit(r, "updates", "Upgrade to the Debian testing release no longer allowed")
		}
		return s.distState(r.Context()), nil
	})

	// Checks now (a job: the simulation reads the new release's lists).
	s.admin("POST /api/system/dist-upgrade/check", func(r *http.Request) (any, error) {
		v := s.distState(r.Context())
		if v.Target == "" {
			return nil, badRequest(v.Reason)
		}
		return s.jobs.Start("dist-check", fmt.Sprintf("Checking the upgrade to Debian %s", v.TargetVersion), userOf(r), func(ctx context.Context, j *jobs.Job) error {
			_, err := s.distCheck(ctx, j.Logf)
			return err
		}), nil
	})

	// The answer to the proposal: no hides it until the next check; show
	// brings it back.
	s.admin("POST /api/system/dist-upgrade/dismiss", func(r *http.Request) (any, error) {
		v := s.distState(r.Context())
		_ = s.store.Update(func(st *store.State) error {
			st.DistUpgrade.DismissedFor, st.DistUpgrade.DismissedAt = v.Target, time.Now()
			return nil
		})
		s.audit(r, "updates", fmt.Sprintf("System upgrade to Debian %s: not now", v.TargetVersion))
		return s.distState(r.Context()), nil
	})
	s.admin("POST /api/system/dist-upgrade/show", func(r *http.Request) (any, error) {
		_ = s.store.Update(func(st *store.State) error {
			st.DistUpgrade.DismissedFor, st.DistUpgrade.DismissedAt = "", time.Time{}
			return nil
		})
		return s.distState(r.Context()), nil
	})

	// Yes: now, or in the next maintenance window. Past failed checks only
	// with force and the codename typed (logged and told).
	s.admin("POST /api/system/dist-upgrade/start", func(r *http.Request) (any, error) {
		var req struct {
			When    string `json:"when"` // now | window
			Force   bool   `json:"force"`
			Confirm string `json:"confirm"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.When != "now" && req.When != "window" {
			return nil, badRequest("choose now or the maintenance window")
		}
		v := s.distState(r.Context())
		if !v.Available {
			return nil, badRequest(v.Reason)
		}
		if v.Run != nil && !slices.Contains([]string{"done", "failed", "rolled-back"}, v.Run.Phase) {
			return nil, &Error{Status: http.StatusConflict, Msg: "an upgrade is already in progress"}
		}
		if v.Check == nil || v.Check.Target != v.Target || time.Since(v.Check.At) > 36*time.Hour {
			return nil, badRequest("check the system first")
		}
		failed := distFailed(v.Check)
		if len(failed) > 0 {
			if !req.Force {
				return nil, badRequest("the checks failed: " + strings.Join(failed, "; "))
			}
			if req.Confirm != v.Target {
				return nil, badRequest("type " + v.Target + " to force the upgrade")
			}
			msg := fmt.Sprintf("System upgrade to Debian %s forced by %s despite failed checks: %s", v.TargetVersion, userOf(r), strings.Join(failed, "; "))
			s.audit(r, "updates", msg)
			s.store.Log("warning", "updates", userOf(r), msg)
			s.notify("warning", "System upgrade forced", msg)
		}
		if req.When == "window" {
			_ = s.store.Update(func(st *store.State) error {
				st.DistUpgrade.Scheduled = &store.DistSchedule{Target: v.Target, Force: len(failed) > 0, By: userOf(r), At: time.Now()}
				return nil
			})
			s.audit(r, "updates", fmt.Sprintf("System upgrade to Debian %s planned in the next maintenance window", v.TargetVersion))
			return s.distState(r.Context()), nil
		}
		s.audit(r, "updates", fmt.Sprintf("System upgrade %s → %s started", v.CurrentVersion, v.TargetVersion))
		return s.distStart(v, userOf(r), len(failed) > 0), nil
	})

	s.admin("DELETE /api/system/dist-upgrade/scheduled", func(r *http.Request) (any, error) {
		_ = s.store.Update(func(st *store.State) error { st.DistUpgrade.Scheduled = nil; return nil })
		s.audit(r, "updates", "Planned system upgrade cancelled")
		return s.distState(r.Context()), nil
	})

	s.admin("POST /api/system/dist-upgrade/reboot", func(r *http.Request) (any, error) {
		var run *store.DistUpgradeRun
		s.store.Read(func(st *store.State) { run = st.DistUpgrade.Run })
		if run == nil || run.Phase != "reboot" {
			return nil, badRequest("no upgrade is waiting for a restart")
		}
		s.audit(r, "updates", "Restart into the new Debian release")
		go func() {
			time.Sleep(2 * time.Second)
			_, _ = sys.Run("systemctl", "reboot")
		}()
		return nil, nil
	})
}
