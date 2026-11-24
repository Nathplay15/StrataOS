package network

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

// WiFi is the WPA authentication of one wireless link. The IP configuration of
// the link itself stays in Interfaces (a wlan is managed there like any other
// link); this entry only carries the credentials. The password is a secret: it
// is masked in API answers, like the other secrets of the settings.
type WiFi struct {
	Name    string `json:"name"`              // wlan0
	SSID    string `json:"ssid,omitempty"`    // network name
	PSK     string `json:"psk,omitempty"`     // WPA passphrase (64 hex chars = raw key)
	Hidden  bool   `json:"hidden,omitempty"`  // the network does not broadcast its name
	Enabled bool   `json:"enabled,omitempty"` // connect to it
}

var (
	// WPAConfDir holds the wpa_supplicant configurations written by StrataOS
	// (variable so tests can point it elsewhere).
	WPAConfDir = "/etc/wpa_supplicant"
	wpaPrefix  = "strata-"
	// SysNetDir is where the kernel lists the links (variable for tests).
	SysNetDir = "/sys/class/net"
)

// Wireless lists the wireless links of the machine: a /sys/class/net entry
// with a wireless (or phy80211) directory.
func Wireless() []string {
	ents, err := os.ReadDir(SysNetDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		dir := filepath.Join(SysNetDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "wireless")); err == nil {
			out = append(out, e.Name())
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "phy80211")); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// validWiFi checks one wireless entry.
func validWiFi(w WiFi) error {
	if !ifNameRe.MatchString(w.Name) {
		return fmt.Errorf("invalid interface name %q", w.Name)
	}
	if !w.Enabled {
		return nil
	}
	if w.SSID == "" || len(w.SSID) > 32 {
		return fmt.Errorf("%s: the network name (SSID) is 1 to 32 bytes", w.Name)
	}
	for i := 0; i < len(w.SSID); i++ {
		if w.SSID[i] < 0x20 || w.SSID[i] == 0x7f {
			return fmt.Errorf("%s: the network name contains control characters", w.Name)
		}
	}
	if w.PSK == "" {
		return nil // open network
	}
	if len(w.PSK) == 64 {
		if _, err := hex.DecodeString(w.PSK); err == nil {
			return nil // raw key (PMK), written unquoted
		}
	}
	if len(w.PSK) < 8 || len(w.PSK) > 63 {
		return fmt.Errorf("%s: the WiFi password is 8 to 63 characters (or a 64-digit hexadecimal key)", w.Name)
	}
	for i := 0; i < len(w.PSK); i++ {
		if w.PSK[i] < 0x20 || w.PSK[i] > 0x7e {
			return fmt.Errorf("%s: the WiFi password must be printable ASCII", w.Name)
		}
	}
	return nil
}

// ValidateWiFi checks the wireless entries against the wireless links present.
func ValidateWiFi(c *Config, wireless []string) error {
	wl := map[string]bool{}
	for _, w := range wireless {
		wl[w] = true
	}
	seen := map[string]bool{}
	for _, w := range c.WiFi {
		if err := validWiFi(w); err != nil {
			return err
		}
		if !wl[w.Name] {
			return fmt.Errorf("%s: no wireless interface with this name on this machine", w.Name)
		}
		if seen[w.Name] {
			return fmt.Errorf("%s: configured twice in WiFi", w.Name)
		}
		seen[w.Name] = true
	}
	return nil
}

// wpaValue renders a wpa_supplicant string value: quoted when safe, hexadecimal
// otherwise, so a name or password can never inject configuration lines.
func wpaValue(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			return hex.EncodeToString([]byte(s))
		}
	}
	return `"` + s + `"`
}

// wpaConf renders the wpa_supplicant configuration of one wireless link.
func wpaConf(w WiFi) string {
	var b strings.Builder
	b.WriteString("ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\nupdate_config=1\n\nnetwork={\n")
	fmt.Fprintf(&b, "\tssid=%s\n", wpaValue(w.SSID))
	if w.PSK == "" {
		b.WriteString("\tkey_mgmt=NONE\n")
	} else if len(w.PSK) == 64 {
		if _, err := hex.DecodeString(w.PSK); err == nil {
			fmt.Fprintf(&b, "\tpsk=%s\n", w.PSK) // raw key: unquoted hex
		} else {
			fmt.Fprintf(&b, "\tpsk=%s\n", wpaValue(w.PSK))
		}
	} else {
		fmt.Fprintf(&b, "\tpsk=%s\n", wpaValue(w.PSK))
	}
	if w.Hidden {
		b.WriteString("\tscan_ssid=1\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// writeWPA (re)writes the wpa_supplicant configurations of the enabled wireless
// links and removes the ones of the disabled ones. With no WiFi configured and
// no leftover configuration, it does not even create the directory.
func writeWPA(c Config) error {
	enabled := map[string]bool{}
	for _, w := range c.WiFi {
		if w.Enabled {
			enabled[w.Name] = true
		}
	}
	old, _ := filepath.Glob(filepath.Join(WPAConfDir, wpaPrefix+"*.conf"))
	if len(enabled) == 0 && len(old) == 0 {
		return nil
	}
	if err := os.MkdirAll(WPAConfDir, 0o700); err != nil {
		return err
	}
	for _, f := range old {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), wpaPrefix), ".conf")
		if !enabled[name] {
			_ = os.Remove(f)
			_, _ = sys.Run("systemctl", "disable", "--now", "wpa_supplicant@"+name+".service")
		}
	}
	for _, w := range c.WiFi {
		if !w.Enabled {
			continue
		}
		if err := sys.WriteFileAtomic(filepath.Join(WPAConfDir, wpaPrefix+w.Name+".conf"), []byte(wpaConf(w)), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// wifiLinks returns the enabled wireless links of a configuration.
func wifiLinks(c Config) []string {
	var out []string
	for _, w := range c.WiFi {
		if w.Enabled {
			out = append(out, w.Name)
		}
	}
	sort.Strings(out)
	return out
}

// iwRun runs the iw tool (variable for tests).
var iwRun = func(ctx context.Context, args ...string) (string, error) {
	return sys.RunCtx(ctx, "iw", args...)
}

// WiFiScan is one network seen around a wireless link.
type WiFiScan struct {
	SSID     string `json:"ssid"`
	BSSID    string `json:"bssid"`
	Signal   int    `json:"signalDbm"`
	Freq     int    `json:"freqMhz,omitempty"`
	Security string `json:"security"` // open | WEP | WPA | WPA2 | WPA/WPA2
}

// Scan lists the networks visible from one wireless link, strongest first.
func Scan(ctx context.Context, iface string) ([]WiFiScan, error) {
	out, err := iwRun(ctx, "dev", iface, "scan")
	if err != nil {
		return nil, fmt.Errorf("the scan failed: %v", err)
	}
	bySSID := map[string]WiFiScan{}
	var cur *WiFiScan
	var privacy, rsn, wpa bool
	flush := func() {
		if cur == nil || cur.SSID == "" {
			return
		}
		switch {
		case !privacy:
			cur.Security = "open"
		case rsn && wpa:
			cur.Security = "WPA/WPA2"
		case rsn:
			cur.Security = "WPA2"
		case wpa:
			cur.Security = "WPA"
		default:
			cur.Security = "WEP"
		}
		if prev, ok := bySSID[cur.SSID]; !ok || cur.Signal > prev.Signal {
			bySSID[cur.SSID] = *cur
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "BSS ") {
			flush()
			cur = &WiFiScan{BSSID: strings.TrimSuffix(strings.Fields(line)[1], ":")}
			privacy, rsn, wpa = false, false, false
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "\tSSID: "):
			cur.SSID = strings.TrimPrefix(line, "\tSSID: ")
		case strings.HasPrefix(line, "\tsignal: "):
			if f, err := strconv.ParseFloat(strings.Fields(strings.TrimPrefix(line, "\tsignal: "))[0], 64); err == nil {
				cur.Signal = int(f)
			}
		case strings.HasPrefix(line, "\tfreq: "):
			cur.Freq, _ = strconv.Atoi(strings.Fields(strings.TrimPrefix(line, "\tfreq: "))[0])
		case strings.HasPrefix(line, "\tcapability:") && strings.Contains(line, "Privacy"):
			privacy = true
		case strings.HasPrefix(line, "\tRSN:"):
			rsn = true
		case strings.HasPrefix(line, "\tWPA:"):
			wpa = true
		}
	}
	flush()
	res := make([]WiFiScan, 0, len(bySSID))
	for _, s := range bySSID {
		res = append(res, s)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Signal > res[j].Signal })
	if len(res) > 50 {
		res = res[:50]
	}
	return res, nil
}

// WiFiLinkStatus is the live state of one wireless link.
type WiFiLinkStatus struct {
	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`
	Signal    int    `json:"signalDbm,omitempty"`
	Freq      int    `json:"freqMhz,omitempty"`
	State     string `json:"state"`
}

// LinkStatus reads the live state of a wireless link (iw dev <iface> link, plus
// the operational state of the kernel).
func LinkStatus(ctx context.Context, iface string) WiFiLinkStatus {
	st := WiFiLinkStatus{State: "unknown"}
	if b, err := os.ReadFile(filepath.Join(SysNetDir, iface, "operstate")); err == nil {
		st.State = strings.ToLower(strings.TrimSpace(string(b)))
	}
	out, err := iwRun(ctx, "dev", iface, "link")
	if err != nil || strings.Contains(out, "Not connected") {
		return st
	}
	st.Connected = true
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "\tSSID: "):
			st.SSID = strings.TrimPrefix(line, "\tSSID: ")
		case strings.HasPrefix(line, "\tsignal: "):
			if f, err := strconv.ParseFloat(strings.Fields(strings.TrimPrefix(line, "\tsignal: "))[0], 64); err == nil {
				st.Signal = int(f)
			}
		case strings.HasPrefix(line, "\tfreq: "):
			st.Freq, _ = strconv.Atoi(strings.Fields(strings.TrimPrefix(line, "\tfreq: "))[0])
		}
	}
	return st
}
