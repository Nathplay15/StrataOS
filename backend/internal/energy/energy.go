// Package energy saves power: disk spin-down, CPU frequency policy, scheduled
// shutdown with wake-up by the real-time clock, and Wake-on-LAN.
package energy

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// SysRoot is "/" (tests point it elsewhere).
var SysRoot = "/"

// ---- Disks ----

// SpindownValue converts minutes to the hdparm -S encoding (0 = never):
// 1–240 count 5 s steps (up to 20 min), 241–251 count 30 min steps.
func SpindownValue(min int) int {
	switch {
	case min <= 0:
		return 0
	case min <= 20:
		return min * 12
	case min <= 330:
		return 240 + (min+29)/30
	default:
		return 251
	}
}

// SetSpindown sets the standby timeout of rotating disks.
func SetSpindown(devs []string, min int) error {
	if !sys.Has("hdparm") {
		return fmt.Errorf("hdparm is not installed")
	}
	var errs []string
	for _, d := range devs {
		if _, err := sys.Run("hdparm", "-q", "-S", strconv.Itoa(SpindownValue(min)), d); err != nil {
			errs = append(errs, d+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Standby puts a disk to sleep now (it wakes up on the next access).
func Standby(dev string) error {
	_, err := sys.Run("hdparm", "-q", "-y", dev)
	return err
}

// DiskState is "active", "standby" or "unknown" (never wakes the disk).
func DiskState(dev string) string {
	if !sys.Has("hdparm") {
		return "unknown"
	}
	out, err := sys.Run("hdparm", "-C", dev)
	if err != nil {
		return "unknown"
	}
	switch {
	case strings.Contains(out, "standby") || strings.Contains(out, "sleeping"):
		return "standby"
	case strings.Contains(out, "active") || strings.Contains(out, "idle"):
		return "active"
	}
	return "unknown"
}

// ---- Processor ----

func cpufreq(name string) string {
	b, _ := os.ReadFile(filepath.Join(SysRoot, "sys/devices/system/cpu/cpu0/cpufreq", name))
	return strings.TrimSpace(string(b))
}

// Governors returns the current CPU frequency policy and the available ones
// (empty when the machine does not expose frequency scaling, e.g. many VMs).
func Governors() (string, []string) {
	av := strings.Fields(cpufreq("scaling_available_governors"))
	if av == nil {
		av = []string{}
	}
	return cpufreq("scaling_governor"), av
}

// SetGovernor applies a policy to every CPU.
func SetGovernor(g string) error {
	_, av := Governors()
	if !slices.Contains(av, g) {
		return fmt.Errorf("unsupported CPU policy %q", g)
	}
	files, _ := filepath.Glob(filepath.Join(SysRoot, "sys/devices/system/cpu/cpu*/cpufreq/scaling_governor"))
	for _, f := range files {
		if err := os.WriteFile(f, []byte(g), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ---- Schedule ----

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// Schedule shuts the NAS down on some days and wakes it up with the RTC.
type Schedule struct {
	Enabled  bool   `json:"enabled"`
	Days     []int  `json:"days"`     // 0 = Sunday … 6 = Saturday (shutdown days)
	Shutdown string `json:"shutdown"` // HH:MM
	Wake     string `json:"wake"`     // HH:MM, "" = stay off
}

func (s Schedule) Validate() error {
	if !s.Enabled {
		return nil
	}
	if !hhmm.MatchString(s.Shutdown) || (s.Wake != "" && !hhmm.MatchString(s.Wake)) {
		return fmt.Errorf("times must be HH:MM")
	}
	if len(s.Days) == 0 {
		return fmt.Errorf("choose at least one day")
	}
	for _, d := range s.Days {
		if d < 0 || d > 6 {
			return fmt.Errorf("invalid day")
		}
	}
	if s.Wake == s.Shutdown {
		return fmt.Errorf("the wake-up time must differ from the shutdown time")
	}
	return nil
}

func at(day time.Time, t string) time.Time {
	h, _ := strconv.Atoi(t[:2])
	m, _ := strconv.Atoi(t[3:])
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
}

// NextShutdown returns the next planned shutdown after now.
func (s Schedule) NextShutdown(now time.Time) time.Time {
	if !s.Enabled || !hhmm.MatchString(s.Shutdown) {
		return time.Time{}
	}
	for i := 0; i < 8; i++ {
		d := now.AddDate(0, 0, i)
		t := at(d, s.Shutdown)
		if t.After(now) && slices.Contains(s.Days, int(t.Weekday())) {
			return t
		}
	}
	return time.Time{}
}

// WakeAfter returns when to wake up after a shutdown at t.
func (s Schedule) WakeAfter(t time.Time) time.Time {
	if s.Wake == "" {
		return time.Time{}
	}
	w := at(t, s.Wake)
	if !w.After(t) {
		w = w.AddDate(0, 0, 1)
	}
	return w
}

// RTCWakeSupported tells whether the clock can power the machine on.
func RTCWakeSupported() bool {
	_, err := os.Stat(filepath.Join(SysRoot, "sys/class/rtc/rtc0/wakealarm"))
	return err == nil && sys.Has("rtcwake")
}

// PowerOff shuts down now, programming the RTC wake-up when wake is set.
func PowerOff(wake time.Time) error {
	if !wake.IsZero() && RTCWakeSupported() {
		_, err := sys.Run("rtcwake", "-m", "off", "-t", strconv.FormatInt(wake.Unix(), 10))
		return err
	}
	_, err := sys.Run("systemctl", "poweroff")
	return err
}

// ---- Wake-on-LAN ----

// NIC is a network interface and its Wake-on-LAN state.
type NIC struct {
	Name      string `json:"name"`
	MAC       string `json:"mac"`
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
}

// NICs lists the physical interfaces.
func NICs() []NIC {
	var out []NIC
	ents, _ := os.ReadDir(filepath.Join(SysRoot, "sys/class/net"))
	for _, e := range ents {
		if _, err := os.Stat(filepath.Join(SysRoot, "sys/class/net", e.Name(), "device")); err != nil {
			continue // virtual (bridge, bond, docker, wg…)
		}
		mac, _ := os.ReadFile(filepath.Join(SysRoot, "sys/class/net", e.Name(), "address"))
		n := NIC{Name: e.Name(), MAC: strings.TrimSpace(string(mac))}
		if out2, err := sys.Run("ethtool", e.Name()); err == nil {
			for _, l := range strings.Split(out2, "\n") {
				l = strings.TrimSpace(l)
				if v, ok := strings.CutPrefix(l, "Supports Wake-on:"); ok {
					n.Supported = strings.Contains(v, "g")
				} else if v, ok := strings.CutPrefix(l, "Wake-on:"); ok {
					n.Enabled = strings.Contains(v, "g")
				}
			}
		}
		out = append(out, n)
	}
	return out
}

var ifaceRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,15}$`)

// SetWoL turns Wake-on-LAN (magic packet) on or off.
func SetWoL(iface string, on bool) error {
	if !ifaceRe.MatchString(iface) {
		return fmt.Errorf("invalid interface")
	}
	mode := "d"
	if on {
		mode = "g"
	}
	_, err := sys.Run("ethtool", "-s", iface, "wol", mode)
	return err
}

// ParseMAC normalises a MAC address (aa:bb:cc:dd:ee:ff).
func ParseMAC(s string) (net.HardwareAddr, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("invalid MAC address")
	}
	return hw, nil
}

// MagicPacket builds a Wake-on-LAN packet: 6 × 0xFF then 16 × the MAC.
func MagicPacket(hw net.HardwareAddr) []byte {
	p := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		p = append(p, 0xFF)
	}
	for i := 0; i < 16; i++ {
		p = append(p, hw...)
	}
	return p
}

// Wake sends a magic packet to the broadcast address of the local network.
func Wake(mac string) error {
	hw, err := ParseMAC(mac)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4bcast, Port: 9})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(MagicPacket(hw))
	return err
}
