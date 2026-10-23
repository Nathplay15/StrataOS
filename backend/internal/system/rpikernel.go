package system

// Raspberry Pi installations made from the 0.4.0 to 0.6.0 images run
// Debian's generic arm64 kernel, booted by Debian's raspi-firmware (which
// writes config.txt and cmdline.txt itself on every kernel update). The
// 0.6.1 image uses the Raspberry Pi kernels instead (the only ones with the
// Raspberry Pi 5 drivers). This file holds the pure part of the switch from
// one to the other: detection and the boot configuration carried over.

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

var (
	// DeviceTreeModel names the board ("Raspberry Pi 4 Model B Rev 1.4").
	DeviceTreeModel = "/proc/device-tree/model"
	// FirmwareDir is the FAT partition the Raspberry Pi firmware boots from.
	FirmwareDir = "/boot/firmware"
	// RPiFilesDir holds the files of the Raspberry Pi image (packaging/rpi):
	// APT source, pin and key, config.txt and cmdline.txt.
	RPiFilesDir = "/usr/lib/strata/rpi"
)

// RPiModel returns the board name, or "" when this is not a Raspberry Pi.
func RPiModel() string {
	b, err := os.ReadFile(DeviceTreeModel)
	if err != nil {
		return ""
	}
	m := strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
	if !strings.HasPrefix(m, "Raspberry Pi") {
		return ""
	}
	return m
}

// IsRPi5 tells the boards Debian's kernel cannot drive (RP1 chip: no
// Ethernet, no USB): Raspberry Pi 5, 500 and Compute Module 5.
func IsRPi5(model string) bool {
	return regexp.MustCompile(`^Raspberry Pi (5|500|Compute Module 5)\b`).MatchString(model)
}

var (
	debianKernelRe = regexp.MustCompile(`^linux-(image|headers)-(arm64|[0-9][^ ]*-arm64(-unsigned)?)$`)
	rpiKernelRe    = regexp.MustCompile(`^linux-image-.*rpi`)
)

// KernelPackages splits the installed kernel packages (name → version)
// into Debian's arm64 ones and the Raspberry Pi ones, sorted.
func KernelPackages(installed map[string]string) (debian, rpi []string) {
	for p := range installed {
		switch {
		case debianKernelRe.MatchString(p):
			debian = append(debian, p)
		case rpiKernelRe.MatchString(p):
			rpi = append(rpi, p)
		}
	}
	slices.Sort(debian)
	slices.Sort(rpi)
	return debian, rpi
}

// ParseInstalled reads `dpkg-query -W -f '${Package}\t${Version}\t${db:Status-Abbrev}\n'`:
// the installed packages and their versions.
func ParseInstalled(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		if len(f) >= 3 && strings.HasPrefix(f[2], "ii") {
			m[strings.SplitN(f[0], ":", 2)[0]] = f[1]
		}
	}
	return m
}

// RPiKernelNeeded tells whether the switch applies: a Raspberry Pi with
// Debian's kernel and none of the Raspberry Pi ones.
func RPiKernelNeeded(model string, installed map[string]string) bool {
	if !strings.HasPrefix(model, "Raspberry Pi") {
		return false
	}
	debian, rpi := KernelPackages(installed)
	return len(debian) > 0 && len(rpi) == 0
}

// IsRPiKernel tells a Raspberry Pi kernel release (6.18.50+rpt-rpi-v8).
func IsRPiKernel(release string) bool { return strings.Contains(release, "-rpi-") }

// Settings of config.txt that belong to the kernel being booted: those of
// the previous configuration would start the wrong kernel or device trees.
var bootOwnKeys = []string{"kernel", "initramfs", "auto_initramfs", "arm_64bit", "os_prefix", "overlay_prefix", "upstream_kernel", "device_tree", "cmdline", "kernel_address"}

// Lines Debian's raspi-firmware writes to every config.txt.
var debianGenerated = []string{"arm_64bit=1", "enable_uart=1", "upstream_kernel=1"}

func configKey(l string) string {
	k, _, _ := strings.Cut(l, "=")
	if f := strings.Fields(k); len(f) > 0 {
		k = f[0] // "initramfs initrd.img-… followkernel"
	}
	return strings.TrimSpace(k)
}

// UserConfig returns what the administrator added to a config.txt written by
// Debian's raspi-firmware (by hand, or through custom, the content of
// /etc/default/raspi-firmware-custom), with the conditional sections ([pi4]…)
// it is in. dropped lists the additions that cannot be kept (they choose a
// kernel or device tree).
func UserConfig(old, custom string) (keep, dropped []string) {
	section, emitted := "[all]", "[all]"
	add := func(l string) {
		if section != emitted {
			keep = append(keep, section)
			emitted = section
		}
		keep = append(keep, l)
	}
	walk := func(text string) {
		for _, raw := range strings.Split(text, "\n") {
			l := strings.TrimSpace(raw)
			switch {
			case l == "" || strings.HasPrefix(l, "#"):
			case strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]"):
				section = l
			case section == "[all]" && slices.Contains(debianGenerated, l):
			case slices.Contains(bootOwnKeys, configKey(l)):
				if !(section == "[all]" && (strings.HasPrefix(l, "kernel=vmlinuz-") || strings.HasPrefix(l, "initramfs initrd.img-"))) {
					dropped = append(dropped, l)
				}
			default:
				add(l)
			}
		}
	}
	walk(old)
	// The hook appends the custom file at the end of config.txt; it may have
	// been written since the last kernel update.
	if strings.TrimSpace(custom) != "" && !strings.Contains(old, "/etc/default/raspi-firmware-custom\n"+strings.SplitN(strings.TrimSpace(custom), "\n", 2)[0]) {
		walk(custom) // in the section config.txt ends with
	}
	if emitted != "[all]" {
		keep = append(keep, "[all]")
	}
	return keep, dropped
}

// MergeConfig appends the kept lines to the image's config.txt (which ends
// in its [all] section), under a comment, leaving out those it already has.
func MergeConfig(base string, keep []string, note string) string {
	have := map[string]bool{}
	for _, l := range strings.Split(base, "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, l := range keep {
		if strings.HasPrefix(l, "[") || !have[l] {
			add = append(add, l)
		}
	}
	// Sections left empty by the filter above.
	var out []string
	for i, l := range add {
		if strings.HasPrefix(l, "[") && (i+1 == len(add) || strings.HasPrefix(add[i+1], "[")) && l != "[all]" {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 1 && out[0] == "[all]" {
		out = nil
	}
	s := strings.TrimRight(base, "\n") + "\n"
	if len(out) > 0 {
		s += "\n# " + note + "\n" + strings.Join(out, "\n") + "\n"
	}
	return s
}

// Kernel parameters Debian's raspi-firmware always writes (its consoles and
// memory reservation do not apply to the Raspberry Pi kernels).
var debianCmdline = []string{"console", "rw", "fsck.repair", "net.ifnames", "cma", "rootwait"}

func paramKey(p string) string { k, _, _ := strings.Cut(p, "="); return k }

// MergeCmdline returns the image's cmdline.txt with the system partition of
// the previous one (when it was moved) and the parameters the administrator
// added (extra: /etc/default/raspi-extra-cmdline), listed in added.
func MergeCmdline(base, old, extra string) (string, []string) {
	params := strings.Fields(base)
	get := func(list []string, key string) string {
		for _, p := range list {
			if paramKey(p) == key {
				return p
			}
		}
		return ""
	}
	prev := strings.Fields(old)
	var added []string
	if root := get(prev, "root"); root != "" && root != get(params, "root") {
		// Another system partition (a moved system, Btrfs…): its own
		// file system type and mount flags, not the image's.
		params = slices.DeleteFunc(params, func(p string) bool { return paramKey(p) == "rootfstype" })
		for i, p := range params {
			if paramKey(p) == "root" {
				params[i] = root
			}
		}
		added = append(added, root)
	}
	for _, p := range append(prev, strings.Fields(extra)...) {
		k := paramKey(p)
		if k == "root" || slices.Contains(debianCmdline, k) || slices.Contains(params, p) {
			continue
		}
		if k == "rootfstype" && get(params, "rootfstype") != "" {
			continue
		}
		params = append(params, p)
		added = append(added, p)
	}
	return strings.Join(params, " ") + "\n", added
}

// DebianFallbackConfig is the previous config.txt made to load the Debian
// kernel, its initramfs, device trees and cmdline.txt from prefix (a folder
// of the firmware partition): putting it in place of config.txt starts the
// previous system again.
func DebianFallbackConfig(old, prefix string) string {
	return "# StrataOS: the previous boot configuration (Debian kernel), its files in " + prefix + ".\n" +
		"# To start the Debian kernel again, rename config.txt to config-rpi.txt and this\n" +
		"# file to config.txt (from any computer: this partition is FAT).\n" +
		"os_prefix=" + prefix + "\n\n" + old
}

// PlanCheck reads the "Inst" lines of apt-get -s: the packages that would be
// upgraded (with their installed version), and those that would come from
// the Raspberry Pi archive but are not in its pin (allowed).
func PlanCheck(out string, allowed *regexp.Regexp) (upgrades map[string]string, installs, outside []string) {
	upgrades = map[string]string{}
	re := regexp.MustCompile(`^Inst (\S+) (?:\[([^\]]+)\] )?\((.*)\)`)
	for _, l := range strings.Split(out, "\n") {
		m := re.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		name := strings.SplitN(m[1], ":", 2)[0]
		if m[2] != "" {
			upgrades[name] = m[2]
		} else {
			installs = append(installs, name)
		}
		if strings.Contains(m[3], "Raspberry Pi") && !allowed.MatchString(name) {
			outside = append(outside, name)
		}
	}
	return upgrades, installs, outside
}

// PinPackages reads the package list of the Raspberry Pi archive's pin
// (its first "Package: /regex/" line).
func PinPackages(pref string) (*regexp.Regexp, error) {
	for _, l := range strings.Split(pref, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Package: /"); ok && strings.HasSuffix(v, "/") {
			return regexp.Compile(strings.TrimSuffix(v, "/"))
		}
	}
	return nil, os.ErrNotExist
}
