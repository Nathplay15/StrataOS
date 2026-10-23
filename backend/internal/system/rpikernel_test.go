package system

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// config.txt and cmdline.txt of the StrataOS 0.6.0 image, written by Debian's raspi-firmware.
const debianConfig = `# Do not modify this file!
#
# It is automatically generated upon install or update of either the
# firmware or the Linux kernel.
#
# If you need to set boot-time parameters, do so via the
# /etc/default/raspi-firmware, /etc/default/raspi-firmware-custom or
# /etc/default/raspi-extra-cmdline files.

# Switch the CPU from ARMv7 into ARMv8 (aarch64) mode
arm_64bit=1

enable_uart=1
upstream_kernel=1

kernel=vmlinuz-6.12.113+deb13-arm64
# For details on the initramfs directive, see
# https://www.raspberrypi.org/forums/viewtopic.php?f=63&t=10532
initramfs initrd.img-6.12.113+deb13-arm64
`

const oldCmdline = " console=ttyS1,115200 console=tty0 root=LABEL=RASPIROOT rw fsck.repair=yes net.ifnames=0 cma=64M rootwait\n"

func readImageFile(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "rpi", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRPiKernelNeeded(t *testing.T) {
	debian := map[string]string{"linux-image-arm64": "6.12.113-1", "linux-image-6.12.113+deb13-arm64": "6.12.113-1", "raspi-firmware": "1.20240424+ds-6"}
	both := map[string]string{"linux-image-arm64": "6.12.113-1", "linux-image-rpi-v8": "1:6.18.50-1+rpt1", "linux-image-6.18.50+rpt-rpi-v8": "1:6.18.50-1+rpt1"}
	rpi := map[string]string{"linux-image-rpi-v8": "1:6.18.50-1+rpt1", "linux-image-rpi-2712": "1:6.18.50-1+rpt1"}
	versioned := map[string]string{"linux-image-6.12.113+deb13-arm64": "6.12.113-1"}
	for _, c := range []struct {
		model string
		pkgs  map[string]string
		want  bool
	}{
		{"Raspberry Pi 4 Model B Rev 1.4", debian, true},
		{"Raspberry Pi 5 Model B Rev 1.0", versioned, true},
		{"Raspberry Pi 3 Model B Plus Rev 1.3", both, false}, // switch already made
		{"Raspberry Pi 4 Model B Rev 1.4", rpi, false},       // 0.6.1 image
		{"", debian, false},                                  // not a Raspberry Pi
		{"QEMU KVM Virtual Machine", debian, false},
		{"Raspberry Pi 4 Model B Rev 1.4", map[string]string{"linux-headers-arm64": "x"}, true},
		{"Raspberry Pi 4 Model B Rev 1.4", map[string]string{"linux-image-amd64": "x"}, false},
	} {
		if got := RPiKernelNeeded(c.model, c.pkgs); got != c.want {
			t.Errorf("RPiKernelNeeded(%q, %v) = %v, want %v", c.model, c.pkgs, got, c.want)
		}
	}
	d, r := KernelPackages(both)
	if !slices.Equal(d, []string{"linux-image-arm64"}) || !slices.Equal(r, []string{"linux-image-6.18.50+rpt-rpi-v8", "linux-image-rpi-v8"}) {
		t.Errorf("KernelPackages: %v %v", d, r)
	}
}

func TestRPiModel(t *testing.T) {
	dir := t.TempDir()
	old := DeviceTreeModel
	defer func() { DeviceTreeModel = old }()
	DeviceTreeModel = filepath.Join(dir, "model")
	if RPiModel() != "" {
		t.Error("no device tree: not a Raspberry Pi")
	}
	for in, want := range map[string]string{
		"Raspberry Pi 4 Model B Rev 1.4\x00": "Raspberry Pi 4 Model B Rev 1.4",
		"linux,dummy-virt\x00":               "",
	} {
		_ = os.WriteFile(DeviceTreeModel, []byte(in), 0o644)
		if got := RPiModel(); got != want {
			t.Errorf("RPiModel(%q) = %q, want %q", in, got, want)
		}
	}
	for m, want := range map[string]bool{"Raspberry Pi 5 Model B Rev 1.0": true, "Raspberry Pi 500 Rev 1.0": true, "Raspberry Pi Compute Module 5 Rev 1.0": true,
		"Raspberry Pi 4 Model B Rev 1.4": false, "Raspberry Pi Compute Module 4 Rev 1.0": false, "Raspberry Pi 3 Model B Rev 1.2": false} {
		if IsRPi5(m) != want {
			t.Errorf("IsRPi5(%q) != %v", m, want)
		}
	}
	installed := ParseInstalled("linux-image-arm64\t6.12.113-1\tii \nraspi-firmware\t1.20240424+ds-6\tii \nold\t1\trc \nlibc6:arm64\t2.41\tii \n")
	if installed["linux-image-arm64"] != "6.12.113-1" || installed["libc6"] != "2.41" || installed["old"] != "" {
		t.Errorf("ParseInstalled: %v", installed)
	}
}

func TestUserConfig(t *testing.T) {
	// Nothing added: nothing kept.
	keep, dropped := UserConfig(debianConfig, "")
	if len(keep) != 0 || len(dropped) != 0 {
		t.Errorf("plain Debian config: keep %v dropped %v", keep, dropped)
	}
	// Additions by hand, in sections, and through raspi-firmware-custom.
	edited := debianConfig + "dtparam=i2c_arm=on\n[pi4]\narm_boost=1\nover_voltage=2\n[all]\ndtoverlay=disable-bt\ndevice_tree=bcm2837-rpi-3-b.dtb\n" +
		"\n# Inserted by /etc/default/raspi-firmware-custom\ngpu_mem=16\n"
	keep, dropped = UserConfig(edited, "gpu_mem=16\n")
	want := []string{"dtparam=i2c_arm=on", "[pi4]", "arm_boost=1", "over_voltage=2", "[all]", "dtoverlay=disable-bt", "gpu_mem=16"}
	if !slices.Equal(keep, want) {
		t.Errorf("keep = %q, want %q", keep, want)
	}
	if !slices.Equal(dropped, []string{"device_tree=bcm2837-rpi-3-b.dtb"}) {
		t.Errorf("dropped = %q", dropped)
	}
	// A custom file written after the last kernel update, after a section.
	keep, _ = UserConfig(debianConfig+"[pi3]\nhdmi_safe=1\n", "dtoverlay=gpio-fan\n")
	if want := []string{"[pi3]", "hdmi_safe=1", "dtoverlay=gpio-fan", "[all]"}; !slices.Equal(keep, want) {
		t.Errorf("custom after a section: %q, want %q", keep, want)
	}
}

func TestMergeConfig(t *testing.T) {
	base := readImageFile(t, "config.txt")
	if got := MergeConfig(base, nil, "note"); got != base {
		t.Errorf("nothing to keep: the image's config.txt unchanged, got\n%s", got)
	}
	got := MergeConfig(base, []string{"dtparam=i2c_arm=on", "arm_boost=1", "[pi4]", "arm_boost=1", "[all]", "gpu_mem=16"}, "Kept")
	if !strings.HasPrefix(got, base) {
		t.Fatalf("the image's config.txt comes first:\n%s", got)
	}
	if tail := strings.TrimPrefix(got, base); tail != "\n# Kept\ndtparam=i2c_arm=on\n[all]\ngpu_mem=16\n" {
		t.Errorf("tail = %q", tail)
	}
	// The image's Pi 5 section stays where the firmware expects it.
	if !strings.Contains(got, "[pi5]\n# Raspberry Pi 5, 500, CM5. For software that needs 4K memory pages,\n# use kernel8.img instead.\nkernel=kernel_2712.img") {
		t.Error("missing the Pi 5 kernel")
	}
}

func TestMergeCmdline(t *testing.T) {
	base := readImageFile(t, "cmdline.txt")
	if got, added := MergeCmdline(base, oldCmdline, ""); got != base || len(added) != 0 {
		t.Errorf("plain Debian cmdline: %q %v, want the image's %q", got, added, base)
	}
	got, added := MergeCmdline(base, oldCmdline+" quiet\n", "usbcore.autosuspend=-1\n")
	if got != strings.TrimSpace(base)+" quiet usbcore.autosuspend=-1\n" || !slices.Equal(added, []string{"quiet", "usbcore.autosuspend=-1"}) {
		t.Errorf("additions: %q %v", got, added)
	}
	// A system moved to a Btrfs SSD: its root and flags, no rootfstype=ext4.
	got, _ = MergeCmdline(base, "console=tty0 root=UUID=1234 rootflags=subvol=@rootfs rw rootwait\n", "")
	if strings.Contains(got, "rootfstype") || !strings.Contains(got, " root=UUID=1234 ") || !strings.Contains(got, "rootflags=subvol=@rootfs") || strings.Contains(got, "RASPIROOT") {
		t.Errorf("moved system: %q", got)
	}
}

func TestDebianFallbackConfig(t *testing.T) {
	got := DebianFallbackConfig(debianConfig, "debian/")
	if !strings.Contains(got, "\nos_prefix=debian/\n") || !strings.HasSuffix(got, debianConfig) {
		t.Errorf("fallback config:\n%s", got)
	}
}

func TestPlanCheck(t *testing.T) {
	pin, err := PinPackages(readImageFile(t, "raspberrypi.pref"))
	if err != nil {
		t.Fatal(err)
	}
	sim := `NOTE: This is only a simulation!
Inst raspi-firmware [1.20240424+ds-6] (1:1.20260915-1 Raspberry Pi Foundation:stable [all])
Inst initramfs-tools-core [0.148.4] (0.148.4+rpt2 Raspberry Pi Foundation:stable [all]) []
Inst linux-image-6.18.50+rpt-rpi-v8 (1:6.18.50-1+rpt1 Raspberry Pi Foundation:stable [arm64])
Inst linux-image-rpi-v8 (1:6.18.50-1+rpt1 Raspberry Pi Foundation:stable [arm64])
Inst rsync (3.4.1+ds1-5 Debian:13.1/stable [arm64])
Conf raspi-firmware (1:1.20260915-1 Raspberry Pi Foundation:stable [all])
`
	up, inst, outside := PlanCheck(sim, pin)
	if len(up) != 2 || up["raspi-firmware"] != "1.20240424+ds-6" || up["initramfs-tools-core"] != "0.148.4" {
		t.Errorf("upgrades: %v", up)
	}
	if !slices.Equal(inst, []string{"linux-image-6.18.50+rpt-rpi-v8", "linux-image-rpi-v8", "rsync"}) || len(outside) != 0 {
		t.Errorf("installs %v outside %v", inst, outside)
	}
	_, _, outside = PlanCheck("Inst libc6 [2.41-12] (2.41-12+rpt1 Raspberry Pi Foundation:stable [arm64])\n", pin)
	if !slices.Equal(outside, []string{"libc6"}) {
		t.Errorf("outside the pin: %v", outside)
	}
	if !pin.MatchString("linux-headers-6.18.50+rpt-rpi-2712") || !pin.MatchString("initramfs-tools-bin") || pin.MatchString("systemd") {
		t.Error("pin regex")
	}
	if _, err := PinPackages("Package: *\n"); err == nil {
		t.Error("no list: error expected")
	}
}

func TestArchiveRestoreDir(t *testing.T) {
	src, work := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, "overlays"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "config.txt"), []byte("old\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "overlays", "a.dtbo"), []byte("dtbo"), 0o644)
	arc := filepath.Join(work, "fw.tar.gz")
	if err := ArchiveDir(src, arc); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "config.txt"), []byte("new\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "kernel8.img"), []byte("k"), 0o644)
	_ = os.RemoveAll(filepath.Join(src, "overlays"))
	if err := RestoreDir(arc, src); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "config.txt")); string(b) != "old\n" {
		t.Errorf("config.txt = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "overlays", "a.dtbo")); string(b) != "dtbo" {
		t.Errorf("overlay = %q", b)
	}
	if _, err := os.Stat(filepath.Join(src, "kernel8.img")); err == nil {
		t.Error("kernel8.img should be gone")
	}
	// A damaged archive leaves the folder alone.
	_ = os.WriteFile(arc, []byte("not a tarball"), 0o600)
	if err := RestoreDir(arc, src); err == nil {
		t.Error("damaged archive: error expected")
	}
	if _, err := os.Stat(filepath.Join(src, "config.txt")); err != nil {
		t.Error("damaged archive emptied the folder")
	}
}
