package api

// Switch of a Raspberry Pi installed from the 0.4.0–0.6.0 images (Debian's
// arm64 kernel, boot files written by Debian's raspi-firmware) to the
// Raspberry Pi kernels of the 0.6.1 image, without reinstalling. Offered on
// the Updates page, never automatic:
//
//  1. backup of the firmware partition (tarball under /var/lib/strata-restore)
//     and a restore point;
//  2. the Raspberry Pi archive, its pin and key (the image's own files, in
//     /usr/lib/strata/rpi), then its kernels v8 and 2712, raspi-firmware,
//     initramfs-tools and Wi-Fi firmware;
//  3. the Debian kernel, its initramfs, device trees and config.txt are kept
//     in the folder debian/ of the firmware partition (config-debian.txt
//     starts them: os_prefix), the image's config.txt (plus what the
//     administrator had added) and cmdline.txt are written, and the NAS
//     restarts;
//  4. at the next start on a Raspberry Pi kernel, StrataOS confirms: the
//     Debian kernel and the fallback files are removed.
//
// The firmware's tryboot (a one-time trial of tryboot.txt that falls back to
// config.txt by itself) cannot be used for this first start: the flag is set
// by the Raspberry Pi kernel's firmware driver on `reboot "0 tryboot"`, and
// Debian's kernel has no such support. Until StrataOS confirms, the fallback
// is manual: config-debian.txt in place of config.txt (any computer can do it,
// the partition is FAT), then "Undo" on the Updates page.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

const rpiJobType = "rpi-kernel"

var (
	rpiBackupDir = filepath.Join(system.PkgPointsDir, "rpi-kernel")
	rpiKeyring   = "/etc/apt/keyrings/raspberrypi-archive-keyring.pgp"
	rpiSources   = "/etc/apt/sources.list.d/raspberrypi.sources"
	rpiPref      = "/etc/apt/preferences.d/raspberrypi"
	// The packages of the Raspberry Pi image's boot (packaging/rpi/build-rpi-image.sh).
	rpiPackages = []string{"linux-image-rpi-v8", "linux-image-rpi-2712", "raspi-firmware", "initramfs-tools", "firmware-brcm80211"}
	// What the firmware partition must hold afterwards (both kernel flavours,
	// device trees of the Pi 3, 4 and 5).
	rpiBootFiles = []string{"bootcode.bin", "start.elf", "start4.elf", "kernel8.img", "initramfs8", "kernel_2712.img", "initramfs_2712",
		"bcm2710-rpi-3-b.dtb", "bcm2710-rpi-3-b-plus.dtb", "bcm2711-rpi-4-b.dtb", "bcm2712-rpi-5-b.dtb", "overlays/vc4-kms-v3d.dtbo"}
)

// Files of the fallback on the firmware partition.
const (
	rpiFallbackDir    = "debian"
	rpiFallbackConfig = "config-debian.txt"
	rpiFallbackReadme = "README-StrataOS.txt"
)

type rpiKernelStatus struct {
	store.RPiKernel
	Model         string   `json:"model"`
	Pi5           bool     `json:"pi5"`
	Kernel        string   `json:"kernel"`
	Applicable    bool     `json:"applicable"`
	Reason        string   `json:"reason,omitempty"`
	DebianKernels []string `json:"debianKernels,omitempty"`
	RPiKernels    []string `json:"rpiKernels,omitempty"`
	CanUndo       bool     `json:"canUndo"`
}

func installedPackages() map[string]string {
	out, _ := sys.Run("dpkg-query", "-W", "-f", "${Package}\t${Version}\t${db:Status-Abbrev}\n")
	return system.ParseInstalled(out)
}

func runningKernel() string {
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(b))
}

func (s *Server) rpiKernelStatus() rpiKernelStatus {
	st := rpiKernelStatus{Model: system.RPiModel(), Kernel: runningKernel()}
	s.store.Read(func(x *store.State) { st.RPiKernel = x.RPiKernel })
	if st.Model == "" {
		return st
	}
	st.Pi5 = system.IsRPi5(st.Model)
	inst := installedPackages()
	st.DebianKernels, st.RPiKernels = system.KernelPackages(inst)
	st.Applicable = system.RPiKernelNeeded(st.Model, inst) && st.Phase != "running"
	if st.Applicable {
		if _, err := os.Stat(filepath.Join(system.RPiFilesDir, "config.txt")); err != nil {
			st.Applicable, st.Reason = false, "this StrataOS package has no Raspberry Pi boot files"
		}
	}
	st.CanUndo = slices.Contains([]string{"switched", "fell-back", "failed"}, st.Phase) && !system.IsRPiKernel(st.Kernel)
	return st
}

func (s *Server) rpiSet(fn func(k *store.RPiKernel)) {
	_ = s.store.Update(func(st *store.State) error {
		fn(&st.RPiKernel)
		st.RPiKernel.Updated = time.Now()
		return nil
	})
}

func (s *Server) rpiJobRunning() bool {
	for _, j := range s.jobs.List() {
		if j.Type == rpiJobType && j.Status == jobs.Running {
			return true
		}
	}
	return false
}

func (s *Server) rpiKernelRoutes() {
	s.admin("GET /api/system/rpi-kernel", func(r *http.Request) (any, error) {
		return s.rpiKernelStatus(), nil
	})
	s.admin("POST /api/system/rpi-kernel/switch", func(r *http.Request) (any, error) {
		if st := s.rpiKernelStatus(); !st.Applicable {
			if st.Reason != "" {
				return nil, badRequest(st.Reason)
			}
			return nil, badRequest("this system is not a Raspberry Pi running Debian's kernel")
		}
		if s.rpiJobRunning() {
			return nil, badRequest("the switch is already running")
		}
		s.audit(r, "system", "Switch to the Raspberry Pi kernel started")
		return s.jobs.Start(rpiJobType, "Switching to the Raspberry Pi kernel", userOf(r), s.rpiSwitchJob(userOf(r))), nil
	})
	s.admin("POST /api/system/rpi-kernel/undo", func(r *http.Request) (any, error) {
		if !s.rpiKernelStatus().CanUndo {
			return nil, badRequest("nothing to undo while the system runs a Raspberry Pi kernel, or after the switch was confirmed")
		}
		if s.rpiJobRunning() {
			return nil, badRequest("the switch is already running")
		}
		s.audit(r, "system", "Undo of the switch to the Raspberry Pi kernel started")
		return s.jobs.Start(rpiJobType, "Undoing the switch to the Raspberry Pi kernel", userOf(r), s.rpiUndoJob(userOf(r))), nil
	})
}

// rpiInputs reads what the switch writes, before changing anything.
type rpiInputs struct {
	key, sources, pref []byte
	config, cmdline    string
	pin                *regexp.Regexp
}

func readRPiInputs(j *jobs.Job) (rpiInputs, error) {
	var in rpiInputs
	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(system.RPiFilesDir, name))
		if err != nil {
			return nil, fmt.Errorf("missing file of the Raspberry Pi image: %w", err)
		}
		return b, nil
	}
	var err error
	if in.key, err = read("raspberrypi-archive-keyring.pgp"); err != nil {
		return in, err
	}
	if in.sources, err = read("raspberrypi.sources"); err != nil {
		return in, err
	}
	if in.pref, err = read("raspberrypi.pref"); err != nil {
		return in, err
	}
	pin, err := system.PinPackages(string(in.pref))
	if err != nil {
		return in, fmt.Errorf("raspberrypi.pref: no package list")
	}
	in.pin = pin
	base, err := read("config.txt")
	if err != nil {
		return in, err
	}
	baseCmd, err := read("cmdline.txt")
	if err != nil {
		return in, err
	}
	fw := system.FirmwareDir
	old, err := os.ReadFile(filepath.Join(fw, "config.txt"))
	if err != nil {
		return in, fmt.Errorf("cannot read the current config.txt: %w", err)
	}
	custom, _ := os.ReadFile("/etc/default/raspi-firmware-custom")
	keep, dropped := system.UserConfig(string(old), string(custom))
	in.config = system.MergeConfig(string(base), keep, "Kept from the previous configuration (Debian kernel), "+time.Now().Format("2006-01-02"))
	for _, l := range keep {
		j.Logf("config.txt: keeping %s", l)
	}
	for _, l := range dropped {
		j.Logf("config.txt: not kept (it chooses the Debian kernel or its device trees): %s", l)
	}
	oldCmd, _ := os.ReadFile(filepath.Join(fw, "cmdline.txt"))
	extra, _ := os.ReadFile("/etc/default/raspi-extra-cmdline")
	var added []string
	in.cmdline, added = system.MergeCmdline(string(baseCmd), string(oldCmd), string(extra))
	for _, p := range added {
		j.Logf("cmdline.txt: keeping %s", p)
	}
	return in, nil
}

func (s *Server) rpiSwitchJob(user string) func(ctx context.Context, j *jobs.Job) error {
	return func(ctx context.Context, j *jobs.Job) (err error) {
		fw := system.FirmwareDir
		if out, _ := sys.Run("findmnt", "-n", "-o", "FSTYPE", fw); strings.TrimSpace(out) != "vfat" {
			return fmt.Errorf("the firmware partition is not mounted on %s", fw)
		}
		in, err := readRPiInputs(j)
		if err != nil {
			return err
		}
		// Room for a copy of the Debian kernel and initramfs (the fallback)
		// and for both Raspberry Pi kernels with their initramfs.
		var debianSet int64
		for _, pat := range []string{"vmlinuz-*", "initrd.img-*"} {
			m, _ := filepath.Glob(filepath.Join(fw, pat))
			for _, f := range m {
				if fi, err := os.Stat(f); err == nil {
					debianSet += fi.Size()
				}
			}
		}
		if need := debianSet + 200<<20; system.FreeBytes(fw) < need {
			return fmt.Errorf("not enough room on the firmware partition: %d MiB free, %d MiB needed", system.FreeBytes(fw)>>20, need>>20)
		}
		if system.FreeBytes("/var") < 1<<30 {
			return fmt.Errorf("not enough room on the system partition (1 GiB needed)")
		}
		model := system.RPiModel()
		backup := filepath.Join(rpiBackupDir, "firmware-"+time.Now().Format("2006-01-02_15-04-05")+".tar.gz")
		s.rpiSet(func(k *store.RPiKernel) {
			*k = store.RPiKernel{Phase: "running", Model: model, BootID: system.BootID(), Backup: backup}
		})
		// installing: the firmware partition may have changed, put it back on failure.
		installing := false
		defer func() {
			if err == nil {
				return
			}
			if installing {
				j.Logf("Putting the previous firmware partition back")
				if rerr := system.RestoreDir(backup, fw); rerr != nil {
					j.Logf("Cannot restore the firmware partition: %v (backup: %s)", rerr, backup)
				}
				s.rpiSet(func(k *store.RPiKernel) {
					k.Phase, k.Message = "failed", "Switch to the Raspberry Pi kernel failed: "+err.Error()
				})
			} else {
				for _, f := range []string{rpiSources, rpiPref, rpiKeyring} {
					_ = os.Remove(f)
				}
				s.rpiSet(func(k *store.RPiKernel) {
					k.Phase, k.Message = "", "Switch to the Raspberry Pi kernel failed: "+err.Error()
				})
			}
			s.store.Log("error", "system", user, "Switch to the Raspberry Pi kernel failed: "+err.Error())
		}()

		j.Logf("Backing up the firmware partition to %s", backup)
		j.SetProgress(5)
		if err := system.ArchiveDir(fw, backup); err != nil {
			return fmt.Errorf("backup of the firmware partition: %w", err)
		}

		j.Logf("Adding the Raspberry Pi archive (kernels and boot firmware only)")
		if err := os.MkdirAll(filepath.Dir(rpiKeyring), 0o755); err != nil {
			return err
		}
		for f, b := range map[string][]byte{rpiKeyring: in.key, rpiSources: in.sources, rpiPref: in.pref} {
			if err := sys.WriteFileAtomic(f, b, 0o644); err != nil {
				return err
			}
		}
		if err := aptRun(ctx, j, "apt-get", "update"); err != nil {
			return err
		}
		j.SetProgress(15)
		sim, err := sys.RunOpts(ctx, sys.Opts{Timeout: 5 * time.Minute}, "apt-get", append([]string{"-s", "-o", "Debug::NoLocking=1", "install", "--no-install-recommends"}, rpiPackages...)...)
		if err != nil {
			return fmt.Errorf("the Raspberry Pi packages cannot be installed: %w", err)
		}
		upgrades, installs, outside := system.PlanCheck(sim, in.pin)
		if len(outside) > 0 {
			return fmt.Errorf("packages outside of the pin would come from the Raspberry Pi archive: %s", strings.Join(outside, ", "))
		}
		if !slices.Contains(installs, "linux-image-rpi-v8") || !slices.Contains(installs, "linux-image-rpi-2712") {
			return fmt.Errorf("the Raspberry Pi kernels are not available from the Raspberry Pi archive")
		}
		replaced := []string{}
		for p, v := range upgrades {
			j.Logf("Replacing %s %s", p, v)
			replaced = append(replaced, p)
		}
		slices.Sort(replaced)

		// The restore point keeps the replaced packages (packages mode); the
		// Debian kernel is not part of it: it stays installed until the
		// Raspberry Pi kernel is confirmed.
		point, perr := system.CreateRestorePointFor("before-rpi-kernel", replaced)
		if perr != nil {
			j.Logf("Cannot create a restore point: %v", perr)
		} else {
			j.Logf("Restore point created: %s", point)
		}
		s.rpiSet(func(k *store.RPiKernel) { k.RestorePoint, k.Previous = point, upgrades })
		j.SetProgress(25)

		installing = true
		j.Logf("Keeping the Debian kernel in %s/%s (fallback)", fw, rpiFallbackDir)
		if err := rpiKeepFallback(fw); err != nil {
			return fmt.Errorf("copy of the Debian kernel: %w", err)
		}
		j.SetProgress(35)
		if err := aptRun(ctx, j, append([]string{"apt-get", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
			"install", "--no-install-recommends"}, rpiPackages...)...); err != nil {
			return err
		}
		j.SetProgress(85)
		if missing := rpiMissingBootFiles(fw); len(missing) > 0 {
			// The kernels' initramfs reach the partition through the hook of
			// update-initramfs.
			j.Logf("Missing on the firmware partition: %s; regenerating the initramfs", strings.Join(missing, ", "))
			_ = aptRun(ctx, j, "update-initramfs", "-u", "-k", "all")
			if missing := rpiMissingBootFiles(fw); len(missing) > 0 {
				return fmt.Errorf("the firmware partition has no %s", strings.Join(missing, ", "))
			}
		}

		j.Logf("Writing config.txt and cmdline.txt")
		// The Debian kernel's files are in debian/ now (its device trees
		// bcm2711/bcm2712 were replaced by the Raspberry Pi ones).
		for _, pat := range []string{"vmlinuz-*", "initrd.img-*", "bcm2837-*.dtb"} {
			m, _ := filepath.Glob(filepath.Join(fw, pat))
			for _, f := range m {
				_ = os.Remove(f)
			}
		}
		if err := sys.WriteFileAtomic(filepath.Join(fw, "cmdline.txt"), []byte(in.cmdline), 0o644); err != nil {
			return err
		}
		if err := sys.WriteFileAtomic(filepath.Join(fw, "config.txt"), []byte(in.config), 0o644); err != nil {
			return err
		}
		_, _ = sys.Run("sync", "-f", fw+"/config.txt")
		s.rpiSet(func(k *store.RPiKernel) {
			k.Phase, k.Message, k.BootID = "switched", "Switched to the Raspberry Pi kernel: restarting", system.BootID()
		})
		s.store.Log("warning", "system", user, "Switched to the Raspberry Pi kernel: restarting")
		j.Logf("Restarting on the Raspberry Pi kernel. If the NAS does not answer again within 15 minutes, see %s on the firmware partition.", rpiFallbackReadme)
		go func() {
			time.Sleep(5 * time.Second)
			_, _ = sys.Run("systemctl", "reboot")
		}()
		return nil
	}
}

const rpiReadme = `StrataOS: switch to the Raspberry Pi kernel
===========================================

config.txt now starts the Raspberry Pi kernel (kernel8.img, or kernel_2712.img
on a Raspberry Pi 5). The previous Debian kernel, its initramfs, device trees
and cmdline.txt are kept in the folder debian/, started by config-debian.txt.

If the NAS does not start any more, put this card (or disk) in any computer:
  1. rename config.txt to config-rpi.txt,
  2. rename config-debian.txt to config.txt,
then start the Raspberry Pi again: it runs the Debian kernel as before. On the
Updates page of StrataOS, "Undo" then removes the Raspberry Pi kernels.

Once the NAS has started on the Raspberry Pi kernel, StrataOS removes the
Debian kernel and these files. A copy of the whole partition as it was before
is kept in /var/lib/strata-restore/rpi-kernel/ on the system partition.
`

// rpiKeepFallback copies the files that start the Debian kernel into the
// folder debian/ of the firmware partition, with the config.txt that loads
// them from there.
func rpiKeepFallback(fw string) error {
	dst := filepath.Join(fw, rpiFallbackDir)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	var files []string
	for _, pat := range []string{"vmlinuz-*", "initrd.img-*", "*.dtb", "cmdline.txt", "overlays/*"} {
		m, _ := filepath.Glob(filepath.Join(fw, pat))
		files = append(files, m...)
	}
	for _, f := range files {
		if fi, err := os.Stat(f); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		rel, _ := filepath.Rel(fw, f)
		if err := system.CopyFile(f, filepath.Join(dst, rel)); err != nil {
			return err
		}
	}
	// With os_prefix, overlays come from debian/overlays/ only when it has a README.
	if err := os.MkdirAll(filepath.Join(dst, "overlays"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dst, "overlays", "README"), []byte("Overlays of the Debian kernel (StrataOS fallback).\n"), 0o644); err != nil {
		return err
	}
	old, err := os.ReadFile(filepath.Join(fw, "config.txt"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(fw, rpiFallbackConfig), []byte(system.DebianFallbackConfig(string(old), rpiFallbackDir+"/")), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(fw, rpiFallbackReadme), []byte(strings.ReplaceAll(rpiReadme, "\n", "\r\n")), 0o644); err != nil {
		return err
	}
	_, _ = sys.Run("sync", "-f", dst)
	return nil
}

func rpiMissingBootFiles(fw string) []string {
	var missing []string
	for _, f := range rpiBootFiles {
		if fi, err := os.Stat(filepath.Join(fw, f)); err != nil || fi.Size() == 0 {
			missing = append(missing, f)
		}
	}
	return missing
}

func (s *Server) rpiUndoJob(user string) func(ctx context.Context, j *jobs.Job) error {
	return func(ctx context.Context, j *jobs.Job) error {
		var k store.RPiKernel
		s.store.Read(func(st *store.State) { k = st.RPiKernel })
		if system.IsRPiKernel(runningKernel()) {
			return fmt.Errorf("the system runs a Raspberry Pi kernel")
		}
		for _, f := range []string{rpiSources, rpiPref, rpiKeyring} {
			_ = os.Remove(f)
		}
		j.Logf("Raspberry Pi archive removed")
		_ = aptRun(ctx, j, "apt-get", "update")
		inst := installedPackages()
		var purge []string
		for p := range inst {
			if strings.HasPrefix(p, "linux-") && strings.Contains(p, "rpi") {
				purge = append(purge, p)
			}
		}
		if len(purge) > 0 {
			slices.Sort(purge)
			if err := aptRun(ctx, j, append([]string{"apt-get", "-y", "purge"}, purge...)...); err != nil {
				return err
			}
		}
		j.SetProgress(40)
		// The replaced packages: from the restore point when it has them
		// (no download), from the Debian archive otherwise.
		var debs, wanted []string
		for p, v := range k.Previous {
			if installedPackages()[p] == v {
				continue
			}
			wanted = append(wanted, p+"="+v)
			for _, d := range system.PkgPointDebs(k.RestorePoint) {
				if strings.HasPrefix(filepath.Base(d), p+"_") {
					debs = append(debs, d)
				}
			}
		}
		slices.Sort(wanted)
		switch {
		case len(wanted) == 0:
		case len(debs) == len(wanted):
			if err := aptRun(ctx, j, append([]string{"dpkg", "-i", "--force-downgrade", "--force-confold"}, debs...)...); err != nil {
				return err
			}
		default:
			if err := aptRun(ctx, j, append([]string{"apt-get", "-y", "--allow-downgrades", "-o", "Dpkg::Options::=--force-confold", "install"}, wanted...)...); err != nil {
				return err
			}
		}
		j.SetProgress(80)
		if k.Backup != "" {
			j.Logf("Putting the previous firmware partition back (%s)", k.Backup)
			if err := system.RestoreDir(k.Backup, system.FirmwareDir); err != nil {
				return fmt.Errorf("cannot restore the firmware partition: %w", err)
			}
		}
		s.rpiSet(func(k *store.RPiKernel) { *k = store.RPiKernel{Message: "Switch to the Raspberry Pi kernel undone"} })
		s.store.Log("info", "system", user, "Switch to the Raspberry Pi kernel undone")
		return nil
	}
}

// rpiKernelResume follows up a switch after a restart: on a Raspberry Pi
// kernel, it is made permanent; on Debian's again, the administrator is told.
func (s *Server) rpiKernelResume() {
	var k store.RPiKernel
	s.store.Read(func(st *store.State) { k = st.RPiKernel })
	switch k.Phase {
	case "running":
		// Interrupted (power cut, restart of StrataOS): "Undo" puts back
		// whatever got changed.
		s.rpiSet(func(k *store.RPiKernel) {
			k.Phase, k.Message = "failed", "Switch to the Raspberry Pi kernel failed: interrupted"
		})
		return
	case "switched", "fell-back":
		if k.BootID == system.BootID() {
			return // not restarted yet
		}
	default:
		return
	}
	if !system.IsRPiKernel(runningKernel()) {
		if k.Phase == "switched" {
			s.rpiSet(func(k *store.RPiKernel) {
				k.Phase, k.Message = "fell-back", "The Raspberry Pi started on the Debian kernel again"
			})
			s.store.Log("warning", "system", "", "The Raspberry Pi started on the Debian kernel again")
			s.notify("warning", "Raspberry Pi kernel", "The Raspberry Pi started on the Debian kernel again")
		}
		return
	}
	// Running a Raspberry Pi kernel: the switch worked. Let the services
	// settle first, and wait for any package operation.
	time.Sleep(time.Minute)
	for range 12 {
		snap := s.jobs.Start(rpiJobType, "Finishing the switch to the Raspberry Pi kernel", "", s.rpiFinishJob())
		for {
			time.Sleep(5 * time.Second)
			if j, ok := s.jobs.Get(snap.ID); !ok || j.Status != jobs.Running {
				break
			}
		}
		var phase string
		s.store.Read(func(st *store.State) { phase = st.RPiKernel.Phase })
		if phase == "done" {
			return
		}
		time.Sleep(10 * time.Minute)
	}
}

func (s *Server) rpiFinishJob() func(ctx context.Context, j *jobs.Job) error {
	return func(ctx context.Context, j *jobs.Job) error {
		if !system.IsRPiKernel(runningKernel()) {
			return fmt.Errorf("the system does not run a Raspberry Pi kernel")
		}
		debian, _ := system.KernelPackages(installedPackages())
		if len(debian) > 0 {
			j.Logf("Removing the Debian kernel: %s", strings.Join(debian, " "))
			if err := aptRun(ctx, j, append([]string{"apt-get", "-y", "purge"}, debian...)...); err != nil {
				return err
			}
		}
		fw := system.FirmwareDir
		for _, f := range []string{rpiFallbackDir, rpiFallbackConfig, rpiFallbackReadme} {
			if err := os.RemoveAll(filepath.Join(fw, f)); err != nil {
				return err
			}
		}
		_, _ = sys.Run("sync", "-f", fw)
		s.rpiSet(func(k *store.RPiKernel) {
			k.Phase, k.Message = "done", "The Raspberry Pi runs its own kernel: the Debian kernel was removed"
		})
		s.store.Log("info", "system", "", "The Raspberry Pi runs its own kernel: the Debian kernel was removed")
		s.notify("info", "Raspberry Pi kernel", "The Raspberry Pi runs its own kernel: the Debian kernel was removed")
		return nil
	}
}
