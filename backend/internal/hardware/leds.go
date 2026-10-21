package hardware

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LEDRoot is the kernel LED class (changed by tests).
var LEDRoot = "/sys/class/leds"

// LED is a light of /sys/class/leds.
type LED struct {
	Name    string `json:"name"`
	On      bool   `json:"on"`
	Trigger string `json:"trigger"` // current trigger ("none" when driven by hand)
	// Disk: the kernel offers the disk-activity trigger for this LED.
	Disk bool `json:"disk"`
}

var ledNameRe = regexp.MustCompile(`^[A-Za-z0-9_:.+-]{1,64}$`)

// keyboard, input and memory-card LEDs are not front-panel lights.
func panelLED(name string) bool {
	l := strings.ToLower(name)
	for _, s := range []string{"capslock", "numlock", "scrolllock", "kana", "compose", "mmc"} {
		if strings.Contains(l, s) {
			return false
		}
	}
	return !strings.HasPrefix(l, "input")
}

// parseTriggers splits "none [disk-activity] timer" into the list and the current one.
func parseTriggers(s string) (all []string, cur string) {
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, "[") && strings.HasSuffix(f, "]") {
			f = strings.Trim(f, "[]")
			cur = f
		}
		all = append(all, f)
	}
	return
}

// LEDs lists the front-panel LEDs the kernel exposes.
func LEDs() []LED {
	out := []LED{}
	entries, _ := os.ReadDir(LEDRoot)
	for _, e := range entries {
		name := e.Name()
		if !ledNameRe.MatchString(name) || !panelLED(name) {
			continue
		}
		d := filepath.Join(LEDRoot, name)
		b, err := readInt(filepath.Join(d, "brightness"))
		if err != nil {
			continue
		}
		all, cur := parseTriggers(readStr(filepath.Join(d, "trigger")))
		l := LED{Name: name, On: b > 0, Trigger: cur}
		for _, t := range all {
			if t == "disk-activity" {
				l.Disk = true
			}
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetLED applies a mode to an LED: on, off or disk (lit on disk activity).
func SetLED(name, mode string) error {
	if !ledNameRe.MatchString(name) || !panelLED(name) {
		return fmt.Errorf("invalid LED name")
	}
	d := filepath.Join(LEDRoot, name)
	if _, err := os.Stat(filepath.Join(d, "brightness")); err != nil {
		return fmt.Errorf("LED %s not found", name)
	}
	switch mode {
	case "on", "off":
		if err := os.WriteFile(filepath.Join(d, "trigger"), []byte("none"), 0o644); err != nil {
			return err
		}
		v := 0
		if mode == "on" {
			if v, _ = readInt(filepath.Join(d, "max_brightness")); v <= 0 {
				v = 1
			}
		}
		return os.WriteFile(filepath.Join(d, "brightness"), []byte(strconv.Itoa(v)), 0o644)
	case "disk":
		return os.WriteFile(filepath.Join(d, "trigger"), []byte("disk-activity"), 0o644)
	}
	return fmt.Errorf("LED mode must be on, off or disk")
}

// DMI identifies the machine.
type DMI struct {
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
	Board   string `json:"board"`
	CPU     string `json:"cpu"`
}

// ReadDMI reads the firmware identification and the processor model.
func ReadDMI() DMI {
	id := "/sys/class/dmi/id/"
	d := DMI{Vendor: readStr(id + "sys_vendor"), Product: readStr(id + "product_name"), Board: strings.TrimSpace(readStr(id+"board_vendor") + " " + readStr(id+"board_name"))}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
				d.CPU = strings.TrimSpace(v)
				break
			}
		}
	}
	return d
}

// TerraMaster reports a TerraMaster NAS. Their boards use an ITE IT8613E Super
// I/O that the mainline it87 driver does not support (forcing another chip id
// is meant for testing only): fan control needs the out-of-tree it87 driver.
func (d DMI) TerraMaster() bool {
	return strings.Contains(strings.ToLower(d.Vendor+" "+d.Product+" "+d.Board), "terramaster")
}
