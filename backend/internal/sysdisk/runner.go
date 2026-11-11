package sysdisk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// The runner: each step re-checks what it is about to touch and refuses when
// unsure. Every command goes to the job log.

const (
	work      = "/run/strata/sysdisk"
	newRoot   = work + "/new"
	oldRoot   = work + "/old"
	testCfg   = "/etc/default/grub.d/strataos-sysdisk-test.cfg"
	grubEntry = "strataos-sysdisk"
	espSync   = "/usr/lib/strata/scripts/esp-sync"
	// Names of the arrays of a mirror made by StrataOS.
	RootArrayName = "strata-root"
	SwapArrayName = "strata-swap"
)

// Run carries out the steps of an operation.
type Run struct {
	Ctx      context.Context
	Logf     func(string, ...any)
	Progress func(float64)
	Op       *store.SysDiskOp
	// Save persists the operation (phase, UUIDs…).
	Save func()
}

func (r *Run) cmd(timeout time.Duration, name string, args ...string) (string, error) {
	r.Logf("$ %s %s", name, strings.Join(args, " "))
	out, err := sys.RunOpts(r.Ctx, sys.Opts{Timeout: timeout}, name, args...)
	if err != nil {
		r.Logf("  failed: %v", err)
	}
	return out, err
}

func (r *Run) run(name string, args ...string) (string, error) {
	return r.cmd(10*time.Minute, name, args...)
}

// PartPath names partition n of a disk (sda3, nvme0n1p3).
func PartPath(disk string, n int) string {
	if last := disk[len(disk)-1]; last >= '0' && last <= '9' {
		return fmt.Sprintf("%sp%d", disk, n)
	}
	return fmt.Sprintf("%s%d", disk, n)
}

// DiskBySerial finds the current path of a disk.
func DiskBySerial(serial, fallback string) string {
	if serial == "" {
		return fallback
	}
	disks, _ := storage.ListDisks()
	for _, d := range disks {
		if d.Serial == serial {
			return d.Path
		}
	}
	return ""
}

func blkUUID(dev string) string {
	out, _ := sys.Run("blkid", "-c", "/dev/null", "-s", "UUID", "-o", "value", dev)
	return strings.TrimSpace(out)
}

func partUUID(dev string) string {
	out, _ := sys.Run("lsblk", "-n", "-d", "-o", "PARTUUID", dev)
	return strings.TrimSpace(out)
}

// mountSource returns the device mounted on a directory ("" when none).
func mountSource(dir string) string {
	out, err := sys.Run("findmnt", "-n", "-o", "SOURCE", "--mountpoint", dir)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(out)
	if i := strings.Index(s, "["); i > 0 {
		s = s[:i]
	}
	return s
}

func sameDevice(a, b string) bool {
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	return a != "" && b != "" && (a == b || (ra != "" && ra == rb))
}

func (r *Run) part(role string) *store.SysDiskPart {
	for i := range r.Op.Parts {
		if r.Op.Parts[i].Role == role {
			return &r.Op.Parts[i]
		}
	}
	return nil
}

// rootDev and swapDev are the devices of the copy.
func (r *Run) rootDev() string {
	if r.Op.RootArray != "" {
		return r.Op.RootArray
	}
	return PartPath(r.Op.New, r.part("root").Num)
}

func (r *Run) swapDev() string {
	if r.part("swap") == nil {
		return ""
	}
	if r.Op.SwapArray != "" {
		return r.Op.SwapArray
	}
	return PartPath(r.Op.New, r.part("swap").Num)
}

// checkNew refuses a new disk that became part of the system or that is
// not the disk chosen (serial number).
func (r *Run) checkNew(l Layout) error {
	if r.Op.New == "" || l.IsSystemDisk(r.Op.New) {
		return fmt.Errorf("%s is a disk of the running system: stopping", r.Op.New)
	}
	if r.Op.NewSerial != "" {
		if p := DiskBySerial(r.Op.NewSerial, ""); p != r.Op.New {
			return fmt.Errorf("the new disk (serial %s) is no longer %s: stopping", r.Op.NewSerial, r.Op.New)
		}
	}
	return nil
}

// partition writes the layout on a whole disk (erased first).
func (r *Run) partition(disk string) error {
	if err := storage.ValidateDevice(disk); err != nil {
		return err
	}
	r.Logf("Erasing %s", disk)
	if err := storage.Wipe(disk); err != nil {
		return err
	}
	if _, err := r.run("sgdisk", "--zap-all", disk); err != nil {
		return err
	}
	args := []string{"-o"}
	for _, p := range r.Op.Parts {
		args = append(args, fmt.Sprintf("-n%d:0:+%dM", p.Num, p.MiB), fmt.Sprintf("-t%d:%s", p.Num, p.Code), fmt.Sprintf("-c%d:%s", p.Num, p.Name))
	}
	args = append(args, disk)
	if _, err := r.run("sgdisk", args...); err != nil {
		return err
	}
	_, _ = r.run("partprobe", disk)
	_, _ = r.run("udevadm", "settle")
	for _, p := range r.Op.Parts {
		dev := PartPath(disk, p.Num)
		for i := 0; ; i++ {
			if _, err := os.Stat(dev); err == nil {
				break
			}
			if i > 20 {
				return fmt.Errorf("%s did not appear", dev)
			}
			time.Sleep(500 * time.Millisecond)
		}
		// Leftovers of an older layout at the same place.
		_, _ = r.run("wipefs", "-a", dev)
	}
	return nil
}

func mdExists(name string) bool {
	_, err := os.Stat("/dev/md/" + name)
	return err == nil
}

// Prepare erases and partitions the new disk, creates the arrays (mirror)
// and the file systems.
func (r *Run) Prepare(l Layout) error {
	if err := r.checkNew(l); err != nil {
		return err
	}
	storage.Lock.Lock()
	defer storage.Lock.Unlock()
	storage.ClearBusy(r.Op.New)
	defer func() { _ = storage.SetBusy(r.Op.New, "system disk change") }()
	if err := r.partition(r.Op.New); err != nil {
		return err
	}
	if r.Op.Kind == "mirror" {
		if mdExists(RootArrayName) || mdExists(SwapArrayName) {
			return fmt.Errorf("an array named %s or %s already exists", RootArrayName, SwapArrayName)
		}
		create := func(name, part string) (string, error) {
			dev := "/dev/md/" + name
			_, err := r.cmd(time.Minute, "mdadm", "--create", dev, "--level=1", "--raid-devices=2", "--metadata=1.2", "--bitmap=internal",
				"--homehost=any", "--name="+name, "--run", part, "missing")
			if err != nil {
				return "", err
			}
			_, _ = r.run("udevadm", "settle")
			return dev, nil
		}
		var err error
		if r.Op.RootArray, err = create(RootArrayName, PartPath(r.Op.New, r.part("root").Num)); err != nil {
			return err
		}
		if sp := r.part("swap"); sp != nil {
			if r.Op.SwapArray, err = create(SwapArrayName, PartPath(r.Op.New, sp.Num)); err != nil {
				return err
			}
		}
		r.Save()
	}
	if ep := r.part("esp"); ep != nil {
		if _, err := r.run("mkfs.vfat", "-F", "32", "-n", "EFI", PartPath(r.Op.New, ep.Num)); err != nil {
			return err
		}
		r.Op.ESPUUID = blkUUID(PartPath(r.Op.New, ep.Num))
	}
	root := r.rootDev()
	var err error
	switch r.Op.FSType {
	case "btrfs":
		_, err = r.run("mkfs.btrfs", "-f", root)
	case "xfs":
		_, err = r.run("mkfs.xfs", "-f", root)
	default:
		_, err = r.cmd(30*time.Minute, "mkfs."+r.Op.FSType, "-F", "-q", root)
	}
	if err != nil {
		return err
	}
	if sw := r.swapDev(); sw != "" {
		if _, err := r.run("mkswap", sw); err != nil {
			return err
		}
		r.Op.SwapUUID = blkUUID(sw)
	}
	_, _ = r.run("udevadm", "settle")
	r.Op.RootUUID = blkUUID(root)
	if r.Op.RootUUID == "" || (r.part("esp") != nil && r.Op.ESPUUID == "") {
		return errors.New("the new file systems have no UUID")
	}
	if r.Op.FSType == "btrfs" {
		// The same subvolumes as the running system.
		top := work + "/top"
		_ = os.MkdirAll(top, 0o700)
		if _, err := r.run("mount", "-t", "btrfs", "-o", "subvolid=5", root, top); err != nil {
			return err
		}
		defer func() { _, _ = r.run("umount", top) }()
		subs := []string{r.Op.Subvol}
		for _, m := range l.Mounts {
			if m.Role == "subvol" {
				subs = append(subs, m.Subvol)
			}
		}
		for _, s := range subs {
			if _, err := r.run("btrfs", "subvolume", "create", filepath.Join(top, s)); err != nil {
				return err
			}
		}
	}
	r.Save()
	return nil
}

// mounts of the copy, outermost first.
func (r *Run) mountNew(l Layout) error {
	if err := r.umountNew(); err != nil {
		return err
	}
	_ = os.MkdirAll(newRoot, 0o700)
	root := r.rootDev()
	if r.Op.FSType == "btrfs" {
		if _, err := r.run("mount", "-t", "btrfs", "-o", "subvol="+r.Op.Subvol, root, newRoot); err != nil {
			return err
		}
	} else if _, err := r.run("mount", root, newRoot); err != nil {
		return err
	}
	// Paranoid: the copy goes into the new file system and nowhere else.
	if u := blkUUID(mountSource(newRoot)); u != r.Op.RootUUID {
		_ = r.umountNew()
		return fmt.Errorf("%s is not the new file system (UUID %q, expected %q): stopping", newRoot, u, r.Op.RootUUID)
	}
	for _, m := range l.Mounts {
		if m.Role == "subvol" {
			dir := filepath.Join(newRoot, m.Target)
			_ = os.MkdirAll(dir, 0o755)
			if _, err := r.run("mount", "-t", "btrfs", "-o", "subvol="+m.Subvol, root, dir); err != nil {
				return err
			}
		}
	}
	if ep := r.part("esp"); ep != nil {
		dir := filepath.Join(newRoot, "boot/efi")
		_ = os.MkdirAll(dir, 0o755)
		if _, err := r.run("mount", "-t", "vfat", PartPath(r.Op.New, ep.Num), dir); err != nil {
			return err
		}
	}
	return nil
}

func (r *Run) umountNew() error {
	if mountSource(newRoot) == "" {
		return nil
	}
	_, err := r.run("umount", "-R", newRoot)
	return err
}

// progressWriter reads rsync --info=progress2 lines.
type progressWriter struct {
	mu         sync.Mutex
	buf        []byte
	set        func(float64)
	logf       func(string, ...any)
	unreadable []string
	count      int
	logged     int
}

var (
	pctRe     = regexp.MustCompile(`\s(\d{1,3})%\s`)
	ioErrRe   = regexp.MustCompile(`"(/[^"]*)"[^"]*(Input/output error|No data available|Bad message|Structure needs cleaning)`)
	readErrRe = regexp.MustCompile(`read errors mapping "(/[^"]*)"`)
)

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexAny(string(w.buf), "\r\n")
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if m := pctRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			w.set(float64(n))
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var path string
		if m := readErrRe.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if m := ioErrRe.FindStringSubmatch(line); m != nil {
			path = m[1]
		}
		if path != "" {
			if !slices.Contains(w.unreadable, path) {
				w.count++
				if len(w.unreadable) < 200 {
					w.unreadable = append(w.unreadable, path)
				}
			}
		}
		if strings.HasPrefix(line, "rsync") || strings.Contains(line, "error") {
			if w.logged < 100 {
				w.logf("  %s", line)
			}
			w.logged++
		}
	}
	return len(p), nil
}

// rsync copies src into dst; exit codes 23 (some files unreadable) and 24
// (files vanished during the copy) are reported, not fatal.
func (r *Run) rsync(w *progressWriter, extra []string, src, dst string) error {
	args := append([]string{"-aHAXx", "--numeric-ids", "--info=progress2", "--no-inc-recursive"}, extra...)
	args = append(args, src, dst)
	r.Logf("$ rsync %s", strings.Join(args, " "))
	_, err := sys.RunOpts(r.Ctx, sys.Opts{Timeout: -1, Output: w}, "rsync", args...)
	var ce *sys.CmdError
	if errors.As(err, &ce) && (ce.Code == 23 || ce.Code == 24) {
		r.Logf("  rsync finished with code %d (some files could not be read or vanished)", ce.Code)
		return nil
	}
	if err != nil {
		r.Logf("  failed: %v", err)
	}
	return err
}

// Copy copies the running system onto the new disk; final also deletes on
// the copy what is gone from the system (second pass, just before the restart).
func (r *Run) Copy(l Layout, final bool) error {
	if err := r.checkNew(l); err != nil {
		return err
	}
	if err := r.mountNew(l); err != nil {
		return err
	}
	defer func() { _ = r.umountNew() }()
	var excl []string
	if final {
		excl = append(excl, "--delete")
	}
	// The other file systems of the system are copied on their own: on the
	// copy, their mount points must not be emptied by --delete.
	others := []SysMount{}
	for _, m := range l.Mounts {
		if m.Role == "subvol" || m.Role == "merge" || m.Role == "esp" {
			others = append(others, m)
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].Target < others[j].Target })
	rootExcl := slices.Clone(excl)
	for _, m := range others {
		rootExcl = append(rootExcl, "--exclude="+m.Target)
	}
	for _, f := range l.SwapFiles {
		rootExcl = append(rootExcl, "--exclude="+f)
	}
	w := &progressWriter{logf: r.Logf, set: func(p float64) {
		if r.Progress != nil {
			r.Progress(p)
		}
	}}
	r.Logf("Copying / onto %s", r.rootDev())
	if err := r.rsync(w, rootExcl, "/", newRoot+"/"); err != nil {
		return err
	}
	for _, m := range others {
		dst := filepath.Join(newRoot, m.Target) + "/"
		var sub []string
		for _, o := range others {
			if o.Target != m.Target && strings.HasPrefix(o.Target, m.Target+"/") {
				sub = append(sub, "--exclude=/"+strings.TrimPrefix(o.Target, m.Target+"/"))
			}
		}
		if m.Role == "esp" {
			if r.part("esp") == nil {
				continue // BIOS: no EFI system partition on the new disk
			}
			// FAT: no owners, permissions or links.
			args := append([]string{"-rt", "--modify-window=2"}, excl...)
			if _, err := r.run("rsync", append(args, m.Target+"/", dst)...); err != nil {
				return err
			}
			continue
		}
		_ = os.MkdirAll(dst, 0o755)
		r.Logf("Copying %s", m.Target)
		if err := r.rsync(w, append(slices.Clone(excl), sub...), m.Target+"/", dst); err != nil {
			return err
		}
	}
	r.Op.Unreadable, r.Op.UnreadableCount = w.unreadable, w.count
	if w.count > 0 {
		r.Logf("%d file(s) could not be read on the system disk: they are missing from the copy", w.count)
	}
	_, _ = r.run("sync")
	return nil
}

// resolveSpec finds the device an fstab source names.
func resolveSpec(spec string) string {
	for _, k := range []string{"UUID", "PARTUUID", "LABEL", "PARTLABEL"} {
		if v, ok := strings.CutPrefix(spec, k+"="); ok {
			p, err := filepath.EvalSymlinks("/dev/disk/by-" + strings.ToLower(k) + "/" + strings.Trim(v, `"`))
			if err != nil {
				return ""
			}
			return p
		}
	}
	if strings.HasPrefix(spec, "/dev/") {
		p, _ := filepath.EvalSymlinks(spec)
		return p
	}
	return ""
}

// chroot runs a command in the copy.
func (r *Run) chroot(timeout time.Duration, args ...string) (string, error) {
	return r.cmd(timeout, "chroot", append([]string{newRoot}, args...)...)
}

func (r *Run) bindAll(root string) error {
	for _, d := range []string{"dev", "dev/pts", "proc", "sys", "run"} {
		dir := filepath.Join(root, d)
		_ = os.MkdirAll(dir, 0o755)
		if _, err := r.run("mount", "--bind", "/"+d, dir); err != nil {
			return err
		}
	}
	return nil
}

func grubTarget(boot, arch string) string {
	switch {
	case boot == "bios":
		return "i386-pc"
	case arch == "arm64":
		return "arm64-efi"
	}
	return "x86_64-efi"
}

// loader is the file the UEFI firmware starts on an ESP mounted on dir.
func loader(dir string) string {
	for _, f := range []string{"shimx64.efi", "shimaa64.efi", "grubx64.efi", "grubaa64.efi"} {
		if _, err := os.Stat(filepath.Join(dir, "EFI/debian", f)); err == nil {
			return `\EFI\debian\` + f
		}
	}
	return ""
}

func byIDPath(disk string) string {
	real, _ := filepath.EvalSymlinks(disk)
	matches, _ := filepath.Glob("/dev/disk/by-id/*")
	sort.Strings(matches)
	for _, m := range matches {
		n := filepath.Base(m)
		if strings.Contains(n, "-part") || strings.HasPrefix(n, "wwn-") || strings.HasPrefix(n, "nvme-eui") {
			continue
		}
		if p, _ := filepath.EvalSymlinks(m); p == real {
			return m
		}
	}
	return disk
}

// Switch makes the final copy, points the copy to its new devices, installs
// the boot loader and arranges a single start from the new disk.
func (r *Run) Switch(l Layout, bootID string) error {
	if err := r.checkNew(l); err != nil {
		return err
	}
	// The copy must say "rebooting": it is how the started system knows.
	r.Op.Phase, r.Op.BootID = "rebooting", bootID
	r.Save()
	if err := r.Copy(l, true); err != nil {
		return err
	}
	if err := r.mountNew(l); err != nil {
		return err
	}
	defer func() { _ = r.umountNew() }()

	// fstab.
	fsPath := filepath.Join(newRoot, "etc/fstab")
	fstab, err := os.ReadFile(fsPath)
	if err != nil {
		return err
	}
	e := FstabEdit{Root: "UUID=" + r.Op.RootUUID}
	if r.Op.ESPUUID != "" {
		e.ESP = "UUID=" + r.Op.ESPUUID
	}
	if r.Op.SwapUUID != "" {
		e.Swap = "UUID=" + r.Op.SwapUUID
	}
	for _, m := range l.Mounts {
		switch m.Role {
		case "subvol":
			e.SameFS = append(e.SameFS, m.Target)
		case "merge":
			e.Merged = append(e.Merged, m.Target)
		}
	}
	for _, fe := range ParseFstab(string(fstab)) {
		if fe.Type != "swap" {
			continue
		}
		dev := resolveSpec(fe.Spec)
		for _, s := range l.Swap {
			if dev != "" && sameDevice(dev, s) {
				e.OldSwap = append(e.OldSwap, fe.Spec)
			}
		}
	}
	nf := RewriteFstab(string(fstab), e)
	r.Logf("New /etc/fstab of the copy:\n%s", nf)
	if err := os.WriteFile(fsPath, []byte(nf), 0o644); err != nil {
		return err
	}
	// Hibernation resume device.
	resume := filepath.Join(newRoot, "etc/initramfs-tools/conf.d/resume")
	if _, err := os.Stat(resume); err == nil {
		v := "RESUME=none\n"
		if r.Op.SwapUUID != "" {
			v = "RESUME=UUID=" + r.Op.SwapUUID + "\n"
		}
		_ = os.WriteFile(resume, []byte(v), 0o644)
	}
	// Swap files are created again (a copied Btrfs swap file is not usable).
	for _, f := range l.SwapFiles {
		var size int64
		if fi, err := os.Stat(f); err == nil {
			size = fi.Size()
		}
		dst := filepath.Join(newRoot, f)
		if size <= 0 || !strings.HasPrefix(filepath.Clean(dst), newRoot+"/") {
			continue
		}
		if r.Op.FSType == "btrfs" {
			_, _ = r.run("btrfs", "filesystem", "mkswapfile", "--size", fmt.Sprintf("%dm", size>>20), dst)
		} else {
			_, _ = r.run("dd", "if=/dev/zero", "of="+dst, "bs=1M", fmt.Sprintf("count=%d", size>>20), "status=none")
			_ = os.Chmod(dst, 0o600)
			_, _ = r.run("mkswap", dst)
		}
	}
	// mdadm.conf of a mirror: the arrays as they run now (data arrays included).
	if r.Op.Kind == "mirror" {
		if err := writeMdadmConf(filepath.Join(newRoot, "etc/mdadm/mdadm.conf"), r.Logf); err != nil {
			return err
		}
	}
	// The test start restarts by itself after 30 s when it cannot find its
	// system, instead of waiting at a rescue shell: the next start then uses
	// the old disk.
	_ = os.MkdirAll(filepath.Join(newRoot, "etc/default/grub.d"), 0o755)
	if err := os.WriteFile(filepath.Join(newRoot, testCfg), []byte("# StrataOS: test start from a new system disk (removed once it started)\nGRUB_CMDLINE_LINUX=\"$GRUB_CMDLINE_LINUX panic=30\"\n"), 0o644); err != nil {
		return err
	}
	if err := r.bindAll(newRoot); err != nil {
		return err
	}
	if _, err := r.chroot(20*time.Minute, "update-initramfs", "-u", "-k", "all"); err != nil {
		return err
	}
	target := grubTarget(r.Op.Boot, l.Arch)
	if r.Op.Boot == "bios" {
		if _, err := r.chroot(5*time.Minute, "grub-install", "--target="+target, r.Op.New); err != nil {
			return err
		}
		_, _ = r.cmd(time.Minute, "chroot", newRoot, "sh", "-c", "echo 'grub-pc grub-pc/install_devices multiselect "+byIDPath(r.Op.New)+"' | debconf-set-selections")
	} else {
		if _, err := r.chroot(5*time.Minute, "grub-install", "--target="+target, "--efi-directory=/boot/efi", "--bootloader-id=debian", "--no-nvram", "--force-extra-removable"); err != nil {
			return err
		}
	}
	if _, err := r.chroot(5*time.Minute, "update-grub"); err != nil {
		return err
	}
	if err := r.umountNew(); err != nil {
		return err
	}
	return r.oneShot(l)
}

// writeMdadmConf writes an mdadm.conf listing the running arrays.
func writeMdadmConf(path string, logf func(string, ...any)) error {
	scan, err := sys.Run("mdadm", "--detail", "--scan")
	if err != nil {
		return err
	}
	old, _ := os.ReadFile(path)
	var keep []string
	for _, l := range strings.Split(string(old), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "ARRAY") || strings.HasPrefix(t, "HOMEHOST") || t == "# Arrays managed by StrataOS" {
			continue
		}
		keep = append(keep, l)
	}
	var arrays []string
	for _, l := range strings.Split(scan, "\n") {
		var f []string
		for _, w := range strings.Fields(l) {
			if !strings.HasPrefix(w, "spares=") {
				f = append(f, w)
			}
		}
		if len(f) > 0 {
			arrays = append(arrays, strings.Join(f, " "))
		}
	}
	content := "HOMEHOST <ignore>\n" + strings.Trim(strings.Join(keep, "\n"), "\n") + "\n\n# Arrays managed by StrataOS\n" + strings.Join(arrays, "\n") + "\n"
	logf("mdadm.conf of the copy:\n%s", content)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, []byte(content), 0o644)
}

var bootLineRe = regexp.MustCompile(`^Boot([0-9A-Fa-f]{4})\*?\s+(.*)$`)

// EFIEntries reads efibootmgr -v: number → line.
func EFIEntries() (map[string]string, string, string) {
	out, _ := sys.Run("efibootmgr", "-v")
	m := map[string]string{}
	var cur, order string
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, "BootCurrent: "); ok {
			cur = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(l, "BootOrder: "); ok {
			order = strings.TrimSpace(v)
		}
		if g := bootLineRe.FindStringSubmatch(l); g != nil {
			m[strings.ToUpper(g[1])] = g[2]
		}
	}
	return m, cur, order
}

// oneShot arranges the next start (only) from the new disk.
func (r *Run) oneShot(l Layout) error {
	if r.Op.Boot == "uefi" {
		dir := work + "/esp"
		_ = os.MkdirAll(dir, 0o700)
		esp := PartPath(r.Op.New, r.part("esp").Num)
		if _, err := r.run("mount", "-t", "vfat", "-o", "ro", esp, dir); err != nil {
			return err
		}
		ld := loader(dir)
		_, _ = r.run("umount", dir)
		if ld == "" {
			return errors.New("no boot loader found on the new EFI system partition")
		}
		before, cur, order := EFIEntries()
		r.Op.OldBootNum, r.Op.OldBootOrd = cur, order
		label := "StrataOS (" + filepath.Base(r.Op.New) + ")"
		if r.Op.NewSerial != "" {
			label = "StrataOS (" + r.Op.NewSerial + ")"
		}
		if _, err := r.run("efibootmgr", "-c", "-d", r.Op.New, "-p", strconv.Itoa(r.part("esp").Num), "-L", label, "-l", ld); err != nil {
			return err
		}
		after, _, _ := EFIEntries()
		for n := range after {
			if _, ok := before[n]; !ok {
				r.Op.BootEntry = n
			}
		}
		if r.Op.BootEntry == "" {
			return errors.New("the UEFI boot entry of the new disk was not created")
		}
		r.Save()
		// efibootmgr -c puts the entry first: keep the old order, and start
		// from the new disk once (BootNext).
		if order != "" {
			_, _ = r.run("efibootmgr", "-o", order)
		}
		if _, err := r.run("efibootmgr", "-n", r.Op.BootEntry); err != nil {
			return err
		}
		return nil
	}
	// BIOS: the firmware keeps starting the old disk; its GRUB gets an entry
	// that loads the menu of the copy.
	oneShot := false
	if src := mountSource("/boot"); src == "" {
		// /boot on the root file system: GRUB can write its environment on ext2/3/4 only.
		oneShot = (l.Root.FSType == "ext4" || l.Root.FSType == "ext3" || l.Root.FSType == "ext2") && l.RootOn == "part"
	} else {
		fs, _, _ := storage.Probe(src)
		oneShot = strings.HasPrefix(fs, "ext") && !strings.HasPrefix(filepath.Base(src), "md")
	}
	file := "/etc/grub.d/06_strataos_sysdisk"
	if oneShot {
		file = "/etc/grub.d/42_strataos_sysdisk"
	}
	cfg := "/boot/grub/grub.cfg"
	if r.Op.FSType == "btrfs" {
		cfg = "/" + r.Op.Subvol + "/boot/grub/grub.cfg"
	}
	script := fmt.Sprintf(`#!/bin/sh
# Added by StrataOS: starts the system copied onto %s (removed once done).
cat <<'EOF'
menuentry 'StrataOS on the new system disk' --id %s {
	insmod part_gpt
	insmod part_msdos
	insmod mdraid1x
	insmod ext2
	insmod btrfs
	insmod xfs
	search --no-floppy --fs-uuid --set=root %s
	configfile %s
}
EOF
`, r.Op.New, grubEntry, r.Op.RootUUID, cfg)
	r.Logf("Writing %s", file)
	if err := os.WriteFile(file, []byte(script), 0o755); err != nil {
		return err
	}
	r.Op.GrubEntry, r.Op.GrubOneShot = file, oneShot
	r.Save()
	if _, err := r.run("update-grub"); err != nil {
		return err
	}
	if oneShot {
		if _, err := r.run("grub-reboot", grubEntry); err != nil {
			return err
		}
	}
	return nil
}

// UndoOneShot removes what oneShot changed on the running (old) system.
func (r *Run) UndoOneShot() {
	if r.Op.BootEntry != "" && r.Op.Boot == "uefi" {
		_, _ = r.run("efibootmgr", "-N")
		_, _ = r.run("efibootmgr", "-b", r.Op.BootEntry, "-B")
		if r.Op.OldBootOrd != "" {
			_, _ = r.run("efibootmgr", "-o", r.Op.OldBootOrd)
		}
		r.Op.BootEntry = ""
	}
	if r.Op.GrubEntry != "" && strings.HasPrefix(r.Op.GrubEntry, "/etc/grub.d/") && strings.HasSuffix(r.Op.GrubEntry, "_strataos_sysdisk") {
		if _, err := os.Stat(r.Op.GrubEntry); err == nil {
			r.Logf("Removing %s", r.Op.GrubEntry)
			_ = os.Remove(r.Op.GrubEntry)
			_, _ = r.run("update-grub")
		}
		if r.Op.GrubOneShot {
			_, _ = r.run("grub-editenv", "-", "unset", "next_entry")
		}
		r.Op.GrubEntry = ""
	}
}

// Booted finishes the start from the new disk: the test option goes, and the
// new disk becomes the first UEFI boot entry.
func (r *Run) Booted() {
	if _, err := os.Stat(testCfg); err == nil {
		_ = os.Remove(testCfg)
		_, _ = r.run("update-grub")
	}
	if r.Op.Boot == "uefi" {
		_, cur, order := EFIEntries()
		if cur != "" && order != "" {
			rest := []string{cur}
			for _, n := range strings.Split(order, ",") {
				if n != cur {
					rest = append(rest, n)
				}
			}
			_, _ = r.run("efibootmgr", "-o", strings.Join(rest, ","))
		}
	}
}

// Cleanup releases a copy that will not be used: unmounts it, stops its
// arrays and erases the new disk.
func (r *Run) Cleanup(l Layout) {
	_ = r.umountNew()
	r.UndoOneShot()
	for _, a := range []string{r.Op.SwapArray, r.Op.RootArray} {
		if a == "" {
			continue
		}
		if l.RootArray() != nil && sameDevice(l.RootArray().Device, a) {
			r.Logf("%s holds the running system: left alone", a)
			return
		}
		_, _ = r.run("mdadm", "--stop", a)
	}
	if r.Op.New != "" && !l.IsSystemDisk(r.Op.New) && DiskBySerial(r.Op.NewSerial, r.Op.New) == r.Op.New {
		storage.Lock.Lock()
		storage.ClearBusy(r.Op.New)
		if err := storage.Wipe(r.Op.New); err != nil {
			r.Logf("Could not erase %s: %v", r.Op.New, err)
		}
		storage.Lock.Unlock()
	}
	_ = storage.SaveMdadmConf()
}

// CompleteMirror adds the old disk to the arrays of the running mirror.
func (r *Run) CompleteMirror(l Layout) error {
	ra := l.RootArray()
	if ra == nil || !sameDevice(ra.Device, r.Op.RootArray) {
		return errors.New("the system does not run from the mirror: stopping")
	}
	old := DiskBySerial(r.Op.OldSerial, r.Op.Old)
	if old == "" {
		return errors.New("the old system disk is no longer detected")
	}
	if l.IsSystemDisk(old) {
		return fmt.Errorf("%s is in use by the running system: stopping", old)
	}
	disks, _ := storage.ListDisks()
	for _, d := range disks {
		if d.Path == old {
			if d.ActiveUse != "" {
				return fmt.Errorf("%s is in use (%s %s): stopping", old, d.ActiveUse, d.ActiveBy)
			}
			if d.Size < r.Op.OldSize {
				return fmt.Errorf("%s is not the old system disk (size changed): stopping", old)
			}
		}
	}
	r.Op.Old = old
	storage.Lock.Lock()
	err := r.partition(old)
	if err == nil {
		_, err = r.cmd(time.Minute, "mdadm", "--manage", r.Op.RootArray, "--add", PartPath(old, r.part("root").Num))
	}
	if err == nil && r.Op.SwapArray != "" {
		_, err = r.cmd(time.Minute, "mdadm", "--manage", r.Op.SwapArray, "--add", PartPath(old, r.part("swap").Num))
	}
	storage.Lock.Unlock()
	if err != nil {
		return err
	}
	if err := writeMdadmConf("/etc/mdadm/mdadm.conf", r.Logf); err != nil {
		return err
	}
	if _, err := r.cmd(20*time.Minute, "update-initramfs", "-u", "-k", "all"); err != nil {
		return err
	}
	if r.Op.Boot == "uefi" {
		if _, err := r.run("mkfs.vfat", "-F", "32", "-n", "EFI", PartPath(old, r.part("esp").Num)); err != nil {
			return err
		}
		_, _ = r.run("udevadm", "settle")
		if _, err := r.run(espSync, "--setup"); err != nil {
			return err
		}
		// Entries of the old ESP (erased) point nowhere now.
		r.removeEntriesOf(r.Op.OldESP)
	} else {
		if _, err := r.cmd(5*time.Minute, "grub-install", "--target=i386-pc", old); err != nil {
			return err
		}
		sel := fmt.Sprintf("grub-pc grub-pc/install_devices multiselect %s, %s", byIDPath(r.Op.New), byIDPath(old))
		_, _ = r.cmd(time.Minute, "sh", "-c", "echo '"+sel+"' | debconf-set-selections")
	}
	return nil
}

func (r *Run) removeEntriesOf(partuuid string) {
	if partuuid == "" {
		return
	}
	entries, cur, _ := EFIEntries()
	for n, l := range entries {
		if n != cur && strings.Contains(strings.ToLower(l), strings.ToLower(partuuid)) {
			_, _ = r.run("efibootmgr", "-b", n, "-B")
		}
	}
}

// MirrorSynced polls the rebuild of the arrays: progress and whether done.
func (r *Run) MirrorSynced() (float64, bool, error) {
	raids, _ := storage.ListRaid()
	total, n := 0.0, 0
	done := true
	for _, a := range []string{r.Op.RootArray, r.Op.SwapArray} {
		if a == "" {
			continue
		}
		var arr *storage.RaidArray
		for i := range raids {
			if sameDevice(raids[i].Device, a) {
				arr = &raids[i]
			}
		}
		if arr == nil {
			return 0, false, fmt.Errorf("array %s not found", a)
		}
		if arr.Health == "failed" {
			return 0, false, fmt.Errorf("array %s failed", a)
		}
		n++
		if arr.ActiveDevices >= arr.RaidDevices && arr.SyncAction == "" {
			total += 100
		} else {
			done = false
			total += arr.SyncProgress
		}
	}
	if n == 0 {
		return 100, true, nil
	}
	return total / float64(n), done, nil
}

// EraseOld erases the old system disk after a move.
func (r *Run) EraseOld(l Layout) error {
	old := DiskBySerial(r.Op.OldSerial, r.Op.Old)
	if old == "" {
		return errors.New("the old system disk is no longer detected")
	}
	if l.IsSystemDisk(old) {
		return fmt.Errorf("%s holds the running system: stopping", old)
	}
	storage.Lock.Lock()
	defer storage.Lock.Unlock()
	r.Logf("Erasing %s", old)
	if err := storage.Wipe(old); err != nil {
		return err
	}
	r.removeEntriesOf(r.Op.OldESP)
	return nil
}

// GoBack arranges the next start from the old disk (before a mirror is
// completed or after a move, while the old disk is unchanged).
func (r *Run) GoBack(l Layout) error {
	if r.Op.Boot == "uefi" {
		if r.Op.OldBootNum == "" {
			return errors.New("the boot entry of the old disk is unknown")
		}
		_, err := r.run("efibootmgr", "-n", r.Op.OldBootNum)
		return err
	}
	if r.Op.GrubOneShot {
		return nil // the old disk's GRUB starts the old system by default again
	}
	// The old GRUB starts the copy by default: take the entry out of the old system.
	old := DiskBySerial(r.Op.OldSerial, r.Op.Old)
	if old == "" || l.IsSystemDisk(old) {
		return errors.New("the old system disk is not found")
	}
	var root string
	tree, _ := Tree()
	for _, d := range tree {
		if d.Path != old {
			continue
		}
		for _, p := range d.Children {
			if p.UUID == r.Op.OldRootUUID {
				root = p.Path
			}
		}
	}
	if root == "" {
		return errors.New("the old system partition is not found")
	}
	_ = os.MkdirAll(oldRoot, 0o700)
	opts := []string{root, oldRoot}
	if r.Op.FSType == "btrfs" {
		opts = []string{"-o", "subvol=" + r.Op.Subvol, root, oldRoot}
	}
	if _, err := r.run("mount", opts...); err != nil {
		return err
	}
	defer func() { _, _ = r.run("umount", "-R", oldRoot) }()
	f := filepath.Join(oldRoot, r.Op.GrubEntry)
	if !strings.HasSuffix(f, "_strataos_sysdisk") {
		return errors.New("unexpected GRUB entry name")
	}
	_ = os.Remove(f)
	if err := r.bindAll(oldRoot); err != nil {
		return err
	}
	_, err := r.cmd(5*time.Minute, "chroot", oldRoot, "update-grub")
	return err
}

// RootUUID is the file system UUID of the running "/".
func RootUUID() string {
	out, _ := sys.Run("findmnt", "-n", "-o", "UUID", "/")
	return strings.TrimSpace(out)
}

var _ io.Writer = (*progressWriter)(nil)
