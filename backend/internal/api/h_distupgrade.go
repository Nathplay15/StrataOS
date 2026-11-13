package api

// Debian release upgrade (13 → 14) from the interface, prepared before
// Debian 14 is released: offered only when the "stable" suite of the
// Debian archive is the next release, or, with the administrator's
// override, while it is still "testing" (not recommended). Steps:
// pre-checks (system/distupgrade.go), a restore point, the APT sources
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

	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

const distDir = "/var/lib/strata/dist-upgrade"

var distCache struct {
	sync.Mutex
	at              time.Time
	stable, testing string
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

// distSuites reads which releases "stable" and "testing" are (6 h cache).
func distSuites(ctx context.Context) (stable, testing string) {
	distCache.Lock()
	defer distCache.Unlock()
	if time.Since(distCache.at) < 6*time.Hour && distCache.stable != "" {
		return distCache.stable, distCache.testing
	}
	m := distMirror()
	if b, code, err := distFetch(ctx, m+"/dists/stable/Release"); err == nil && code == 200 {
		distCache.stable = system.ReleaseCodename(b)
	}
	if b, code, err := distFetch(ctx, m+"/dists/testing/Release"); err == nil && code == 200 {
		distCache.testing = system.ReleaseCodename(b)
	}
	distCache.at = time.Now()
	return distCache.stable, distCache.testing
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
func (s *Server) distFacts(ctx context.Context, from, to string) (system.UpgradeFacts, map[string]string, []system.SourceChange) {
	f := system.UpgradeFacts{From: from, To: to, FreeRoot: freeOf("/"), FreeVar: freeOf("/var/cache/apt/archives"), FreeBoot: -1}
	if !sameFS("/boot", "/") {
		f.FreeBoot = freeOf("/boot")
	}
	f.RestoreMode, _ = system.Mode()
	for _, j := range s.jobs.List() {
		if j.Status == jobs.Running && j.Type != "dist-upgrade" {
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
	return f, files, changes
}

type distView struct {
	Current        string                `json:"current"`
	CurrentVersion string                `json:"currentVersion"`
	Target         string                `json:"target"`
	TargetVersion  string                `json:"targetVersion"`
	Stable         string                `json:"stable"`
	Testing        string                `json:"testing"`
	Available      bool                  `json:"available"`
	Reason         string                `json:"reason,omitempty"`
	AllowTesting   bool                  `json:"allowTesting"`
	Run            *store.DistUpgradeRun `json:"run,omitempty"`
}

func (s *Server) distState(ctx context.Context) distView {
	cur := system.Codename()
	v := distView{Current: cur, CurrentVersion: system.ReleaseNumber(cur), Target: system.NextRelease(cur)}
	v.TargetVersion = system.ReleaseNumber(v.Target)
	s.store.Read(func(st *store.State) {
		v.AllowTesting = st.DistUpgrade.AllowTesting
		if r := st.DistUpgrade.Run; r != nil {
			c := *r
			v.Run = &c
		}
	})
	v.Stable, v.Testing = distSuites(ctx)
	switch {
	case v.Target == "":
		v.Reason = "no newer Debian release is known for this system"
	case v.Stable == v.Target:
		v.Available = true
	case v.AllowTesting && v.Testing == v.Target:
		v.Available = true
	case v.Stable == "":
		v.Reason = "the Debian archive could not be reached"
	default:
		v.Reason = fmt.Sprintf("Debian %s is not released yet", v.TargetVersion)
	}
	return v
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

// distKernel is the kernel /vmlinuz points to (the newest installed).
func distKernel() string {
	for _, l := range []string{"/vmlinuz", "/boot/vmlinuz"} {
		if t, err := os.Readlink(l); err == nil {
			return strings.TrimPrefix(filepath.Base(t), "vmlinuz-")
		}
	}
	// No link (cloud images): the kernel installed last.
	m, _ := filepath.Glob("/boot/vmlinuz-*")
	best, at := "", time.Time{}
	for _, f := range m {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().After(at) {
			best, at = f, fi.ModTime()
		}
	}
	return strings.TrimPrefix(filepath.Base(best), "vmlinuz-")
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
			return fail(fmt.Errorf("the package lists of Debian %s could not be read: %v", system.ReleaseNumber(run.To), err))
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
			return fail(fmt.Errorf("upgrade: %v (the system is between two releases: run the upgrade again)", err))
		}
		phase("finish")
		fallthrough
	case "upgrade", "finish":
		if run.Phase == "upgrade" { // resumed: finish what apt left
			if err := distApt(ctx, j, 30, 85, 0, append(append([]string{"apt-get", "-y", "-q"}, aptKeepConf...), "full-upgrade")...); err != nil {
				return fail(fmt.Errorf("upgrade: %v", err))
			}
		}
		j.SetProgress(86)
		if err := distApt(ctx, j, 86, 92, 0, append(append([]string{"apt-get", "-y", "-q", "--reinstall"}, aptKeepConf...), "install", "strataos")...); err != nil {
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
				return fail(fmt.Errorf("the ZFS module was not built for the new kernel %s: do not restart (the pools would not be imported); see dkms status", k))
			}
			j.Logf("ZFS is built for %s", k)
		}
		s.distSet(func(r *store.DistUpgradeRun) { r.Phase, r.BootID = "reboot", bootID() })
		j.SetProgress(100)
		s.notify("info", "Debian upgrade ready", fmt.Sprintf("Debian %s is installed: restart the NAS to finish", system.ReleaseNumber(run.To)))
		j.Logf("Upgraded to Debian %s: restart the NAS to finish", system.ReleaseNumber(run.To))
		return nil
	}
	return nil
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

// distPostChecks runs after the restart into the new release.
func (s *Server) distPostChecks(run store.DistUpgradeRun) []store.UpgradeResult {
	var out []store.UpgradeResult
	add := func(id, level, text string) { out = append(out, store.UpgradeResult{ID: id, Level: level, Text: text}) }
	if c := system.Codename(); c == run.To {
		add("release", "ok", "Debian "+system.ReleaseNumber(c)+" ("+c+") is running")
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
		s.jobs.Start("dist-upgrade", fmt.Sprintf("Upgrading Debian %s to %s (resumed)", system.ReleaseNumber(run.From), system.ReleaseNumber(run.To)), "system", s.distRun)
	case "reboot", "post-checks":
		if bootID() == run.BootID {
			return // not restarted yet
		}
		time.Sleep(time.Minute) // volumes and services come up
		s.distSet(func(r *store.DistUpgradeRun) { r.Phase = "post-checks" })
		res := s.distPostChecks(*run)
		failed := slices.ContainsFunc(res, func(c store.UpgradeResult) bool { return c.Level == "block" })
		s.distSet(func(r *store.DistUpgradeRun) {
			r.PostChecks, r.Phase, r.Finished = res, "done", time.Now()
			if failed {
				r.Error = "some checks failed after the restart"
			}
		})
		if failed {
			s.notify("error", "Debian upgrade: checks failed", "Some checks failed after the restart into the new release: see Updates")
		} else {
			s.notify("info", "Debian upgrade finished", fmt.Sprintf("The NAS runs Debian %s", system.ReleaseNumber(run.To)))
		}
	}
}

func (s *Server) distUpgradeRoutes() {
	go s.distResume()

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

	s.admin("POST /api/system/dist-upgrade/check", func(r *http.Request) (any, error) {
		v := s.distState(r.Context())
		if v.Target == "" {
			return nil, badRequest(v.Reason)
		}
		f, _, changes := s.distFacts(r.Context(), v.Current, v.Target)
		checks := system.UpgradeChecks(f)
		if changes == nil {
			changes = []system.SourceChange{}
		}
		return map[string]any{"checks": checks, "blocked": system.Blocked(checks) || !v.Available, "sources": changes, "available": v.Available, "reason": v.Reason}, nil
	})

	s.admin("POST /api/system/dist-upgrade/start", func(r *http.Request) (any, error) {
		var req struct {
			Confirm string `json:"confirm"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		v := s.distState(r.Context())
		if !v.Available {
			return nil, badRequest(v.Reason)
		}
		if req.Confirm != v.Target {
			return nil, badRequest("type " + v.Target + " to confirm")
		}
		if v.Run != nil && !slices.Contains([]string{"done", "failed"}, v.Run.Phase) {
			return nil, &Error{Status: http.StatusConflict, Msg: "an upgrade is already in progress"}
		}
		f, _, _ := s.distFacts(r.Context(), v.Current, v.Target)
		if checks := system.UpgradeChecks(f); system.Blocked(checks) {
			var why []string
			for _, c := range checks {
				if c.Level == "block" {
					why = append(why, c.Text)
				}
			}
			return nil, badRequest(strings.Join(why, "; "))
		}
		_ = s.store.Update(func(st *store.State) error {
			st.DistUpgrade.Run = &store.DistUpgradeRun{From: v.Current, To: v.Target, Started: time.Now(), Phase: "sources"}
			return nil
		})
		s.audit(r, "updates", fmt.Sprintf("Debian upgrade %s → %s started", v.CurrentVersion, v.TargetVersion))
		return s.jobs.Start("dist-upgrade", fmt.Sprintf("Upgrading Debian %s to %s", v.CurrentVersion, v.TargetVersion), userOf(r), s.distRun), nil
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
