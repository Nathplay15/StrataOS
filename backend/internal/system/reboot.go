package system

import (
	"os"
	"strings"
	"unicode"
)

// RebootRequired tells whether an update needs a restart: Debian tools
// leave /run/reboot-required behind, and a kernel newer than the running
// one is installed (its modules and image are on disk).
func RebootRequired() bool {
	if _, err := os.Stat("/run/reboot-required"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	entries, _ := os.ReadDir("/lib/modules")
	var installed []string
	for _, e := range entries {
		if _, err := os.Stat("/boot/vmlinuz-" + e.Name()); err == nil {
			installed = append(installed, e.Name())
		}
	}
	return NewerKernel(strings.TrimSpace(string(b)), installed)
}

// BootID changes at every boot: the console uses it to see a restart happened.
func BootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// NewerKernel reports whether one of the installed kernel releases is newer
// than the running one.
func NewerKernel(running string, installed []string) bool {
	if running == "" {
		return false
	}
	for _, k := range installed {
		if compareRelease(k, running) > 0 {
			return true
		}
	}
	return false
}

// compareRelease compares kernel releases such as 6.12.43+deb13-amd64:
// runs of digits numerically, everything else as text.
func compareRelease(a, b string) int {
	for a != "" && b != "" {
		ca, ra := chunk(a)
		cb, rb := chunk(b)
		if c := compareChunk(ca, cb); c != 0 {
			return c
		}
		a, b = ra, rb
	}
	switch {
	case a == b:
		return 0
	case a == "":
		return -1
	}
	return 1
}

func chunk(s string) (string, string) {
	digit := unicode.IsDigit(rune(s[0]))
	i := 1
	for i < len(s) && unicode.IsDigit(rune(s[i])) == digit {
		i++
	}
	return s[:i], s[i:]
}

func compareChunk(a, b string) int {
	if unicode.IsDigit(rune(a[0])) && unicode.IsDigit(rune(b[0])) {
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a, b)
}
