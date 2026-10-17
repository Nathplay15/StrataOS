// Package ups configures Network UPS Tools (NUT): local USB UPS, network client of
// another NUT server, sharing the local UPS, and the shutdown policy.
package ups

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

type Config struct {
	Mode   string `json:"mode"`   // off | usb | netclient | simulation
	Driver string `json:"driver"` // local driver, default usbhid-ups
	Port   string `json:"port"`   // driver port, default auto
	Desc   string `json:"desc,omitempty"`
	// Network client.
	RemoteHost     string `json:"remoteHost,omitempty"`
	RemotePort     int    `json:"remotePort,omitempty"`
	RemoteName     string `json:"remoteName,omitempty"`
	RemoteUser     string `json:"remoteUser,omitempty"`
	RemotePassword string `json:"remotePassword,omitempty"`
	// Share the local UPS with other machines (NUT server on the network).
	Share         bool   `json:"share"`
	ShareUser     string `json:"shareUser,omitempty"`
	SharePassword string `json:"sharePassword,omitempty"`
	// Shutdown policy: after N seconds on battery (0 = only when the battery is low).
	ShutdownAfter int `json:"shutdownAfter"`
	// Internal credentials for the local monitor (generated).
	LocalPassword string `json:"localPassword,omitempty"`
}

var (
	ConfDir     = "/etc/nut"
	ScriptPath  = "/usr/lib/strata/scripts/upssched-cmd"
	SimDevPath  = "/etc/nut/strata-simulation.dev"
	nameRe      = regexp.MustCompile(`^[A-Za-z0-9_\-.]{1,32}$`)
	safeValueRe = regexp.MustCompile(`^[A-Za-z0-9_\-.:/@+=,]{0,128}$`)
	drivers     = map[string]bool{"usbhid-ups": true, "blazer_usb": true, "nutdrv_qx": true, "blazer_ser": true, "apcsmart": true, "bcmxcp_usb": true, "riello_usb": true, "tripplite_usb": true, "snmp-ups": true}
)

// Installed reports whether NUT is present.
func Installed() bool { return sys.Has("upsmon") && sys.Has("upsc") }

func randomSecret() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Validate checks a configuration and fills defaults.
func Validate(c *Config) error {
	switch c.Mode {
	case "off":
		return nil
	case "usb", "simulation":
		if c.Mode == "simulation" {
			c.Driver, c.Port = "dummy-ups", SimDevPath
		} else {
			if c.Driver == "" {
				c.Driver = "usbhid-ups"
			}
			if !drivers[c.Driver] {
				return fmt.Errorf("unsupported driver %q", c.Driver)
			}
			if c.Port == "" {
				c.Port = "auto"
			}
			if !safeValueRe.MatchString(c.Port) {
				return fmt.Errorf("invalid port")
			}
		}
		if c.Share {
			if !nameRe.MatchString(c.ShareUser) || len(c.SharePassword) < 6 || !safeValueRe.MatchString(c.SharePassword) {
				return fmt.Errorf("sharing needs a user name and a password of 6+ characters (letters, digits, - _ . : / @ + = ,)")
			}
		}
	case "netclient":
		if net.ParseIP(c.RemoteHost) == nil && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]{0,252}$`).MatchString(c.RemoteHost) {
			return fmt.Errorf("invalid server address")
		}
		if c.RemoteName == "" {
			c.RemoteName = "ups"
		}
		if c.RemotePort == 0 {
			c.RemotePort = 3493
		}
		if !nameRe.MatchString(c.RemoteName) || !nameRe.MatchString(c.RemoteUser) || !safeValueRe.MatchString(c.RemotePassword) || c.RemotePassword == "" {
			return fmt.Errorf("enter the UPS name, user and password of the NUT server (Synology: ups / monuser / secret)")
		}
	default:
		return fmt.Errorf("unknown mode %q", c.Mode)
	}
	if c.ShutdownAfter < 0 || c.ShutdownAfter > 24*3600 {
		return fmt.Errorf("invalid shutdown delay")
	}
	if strings.ContainsAny(c.Desc, "\"\n\r") {
		return fmt.Errorf("invalid description")
	}
	if c.LocalPassword == "" {
		c.LocalPassword = randomSecret()
	}
	return nil
}

// Render produces the NUT configuration files (name -> content).
func Render(c Config) map[string]string {
	head := "# Managed by StrataOS - manual changes will be overwritten.\n"
	f := map[string]string{}
	mode := "none"
	switch c.Mode {
	case "usb", "simulation":
		mode = "standalone"
		if c.Share {
			mode = "netserver"
		}
	case "netclient":
		mode = "netclient"
	}
	f["nut.conf"] = head + "MODE=" + mode + "\n"
	if mode == "standalone" || mode == "netserver" {
		desc := c.Desc
		if desc == "" {
			desc = "Onduleur"
		}
		f["ups.conf"] = head + fmt.Sprintf("maxretry = 3\n\n[ups]\n\tdriver = %s\n\tport = %s\n\tdesc = \"%s\"\n", c.Driver, c.Port, desc)
		listen := "LISTEN 127.0.0.1 3493\nLISTEN ::1 3493\n"
		if c.Share {
			listen = "LISTEN 0.0.0.0 3493\nLISTEN :: 3493\n"
		}
		f["upsd.conf"] = head + listen
		users := fmt.Sprintf("[strata]\n\tpassword = %s\n\tupsmon primary\n", c.LocalPassword)
		if c.Share {
			users += fmt.Sprintf("\n[%s]\n\tpassword = %s\n\tupsmon secondary\n", c.ShareUser, c.SharePassword)
		}
		f["upsd.users"] = head + users
	} else {
		f["ups.conf"] = head
		f["upsd.conf"] = head
		f["upsd.users"] = head
	}
	monitor := ""
	switch mode {
	case "standalone", "netserver":
		monitor = fmt.Sprintf("MONITOR ups@localhost 1 strata %s primary\n", c.LocalPassword)
	case "netclient":
		monitor = fmt.Sprintf("MONITOR %s@%s:%d 1 %s %s secondary\n", c.RemoteName, c.RemoteHost, c.RemotePort, c.RemoteUser, c.RemotePassword)
	}
	f["upsmon.conf"] = head + monitor + `MINSUPPLIES 1
SHUTDOWNCMD "/sbin/shutdown -h +0"
NOTIFYCMD /usr/sbin/upssched
POLLFREQ 5
POLLFREQALERT 5
HOSTSYNC 15
DEADTIME 15
POWERDOWNFLAG /etc/killpower
NOTIFYFLAG ONLINE SYSLOG+EXEC
NOTIFYFLAG ONBATT SYSLOG+EXEC
NOTIFYFLAG LOWBATT SYSLOG+EXEC
NOTIFYFLAG FSD SYSLOG+EXEC
NOTIFYFLAG COMMBAD SYSLOG+EXEC
NOTIFYFLAG COMMOK SYSLOG+EXEC
NOTIFYFLAG SHUTDOWN SYSLOG+EXEC
RBWARNTIME 43200
NOCOMMWARNTIME 300
FINALDELAY 5
`
	sched := head + fmt.Sprintf("CMDSCRIPT %s\nPIPEFN /run/nut/upssched.pipe\nLOCKFN /run/nut/upssched.lock\n", ScriptPath)
	if c.ShutdownAfter > 0 {
		sched += fmt.Sprintf("AT ONBATT * START-TIMER strata-shutdown %d\nAT ONLINE * CANCEL-TIMER strata-shutdown\n", c.ShutdownAfter)
	}
	f["upssched.conf"] = sched
	return f
}

// Apply writes the configuration and (re)starts the NUT services.
func Apply(c Config) error {
	if !Installed() {
		return fmt.Errorf("NUT is not installed")
	}
	files := Render(c)
	for name, content := range files {
		perm := os.FileMode(0o640)
		if name == "nut.conf" {
			perm = 0o644
		}
		p := ConfDir + "/" + name
		if err := sys.WriteFileAtomic(p, []byte(content), perm); err != nil {
			return err
		}
		_, _ = sys.Run("chown", "root:nut", p)
	}
	if c.Mode == "simulation" {
		if _, err := os.Stat(SimDevPath); err != nil {
			if err := SetSimulation("OL", 100); err != nil {
				return err
			}
		}
	}
	units := []string{"nut-monitor.service", "nut-server.service", "nut-driver-enumerator.service", "nut-driver.target"}
	if c.Mode == "off" {
		for _, u := range units {
			_, _ = sys.Run("systemctl", "stop", u)
		}
		return nil
	}
	if c.Mode == "netclient" {
		_, _ = sys.Run("systemctl", "stop", "nut-server.service", "nut-driver.target")
		_, err := sys.Run("systemctl", "restart", "nut-monitor.service")
		return err
	}
	_, _ = sys.Run("systemctl", "enable", "nut-server.service", "nut-monitor.service", "nut-driver-enumerator.path")
	_, _ = sys.Run("systemctl", "restart", "nut-driver-enumerator.service")
	_, _ = sys.Run("systemctl", "restart", "nut-driver.target")
	if _, err := sys.Run("systemctl", "restart", "nut-server.service"); err != nil {
		return fmt.Errorf("NUT server: %w (is the UPS connected?)", err)
	}
	_, err := sys.Run("systemctl", "restart", "nut-monitor.service")
	return err
}

// Status reads the UPS variables with upsc.
func Status(c Config) (map[string]string, error) {
	target := "ups@localhost"
	if c.Mode == "netclient" {
		target = fmt.Sprintf("%s@%s:%d", c.RemoteName, c.RemoteHost, c.RemotePort)
	}
	out, err := sys.Run("upsc", target)
	if err != nil {
		return nil, err
	}
	vars := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, ": "); ok {
			vars[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return vars, nil
}

type Detected struct {
	Driver  string `json:"driver"`
	Port    string `json:"port"`
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
}

// Detect lists USB UPS found by nut-scanner.
func Detect() ([]Detected, error) {
	if !sys.Has("nut-scanner") {
		return nil, fmt.Errorf("nut-scanner is not installed")
	}
	out, _ := sys.Run("nut-scanner", "-U", "-q")
	var list []Detected
	var cur *Detected
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			if cur != nil {
				list = append(list, *cur)
			}
			cur = &Detected{}
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || cur == nil {
			continue
		}
		v = strings.Trim(v, `"`)
		switch strings.TrimSpace(k) {
		case "driver":
			cur.Driver = v
		case "port":
			cur.Port = v
		case "vendor":
			cur.Vendor = v
		case "product":
			cur.Product = v
		}
	}
	if cur != nil {
		list = append(list, *cur)
	}
	return list, nil
}

// SetSimulation writes the state read by the simulation driver (dummy-ups).
func SetSimulation(status string, charge int) error {
	if status != "OL" && status != "OB" && status != "OB LB" {
		return fmt.Errorf("invalid simulated status")
	}
	runtime := charge * 18
	content := fmt.Sprintf("ups.mfr: StrataOS\nups.model: Onduleur de test\nups.status: %s\nbattery.charge: %d\nbattery.runtime: %d\nups.load: 23\ninput.voltage: %s\noutput.voltage: 230\n",
		status, charge, runtime, map[bool]string{true: "230", false: "0"}[status == "OL"])
	if err := sys.WriteFileAtomic(SimDevPath, []byte(content), 0o640); err != nil {
		return err
	}
	_, _ = sys.Run("chown", "root:nut", SimDevPath)
	return nil
}

// Summary extracts the useful values from upsc variables.
func Summary(v map[string]string) map[string]any {
	num := func(k string) any {
		if f, err := strconv.ParseFloat(v[k], 64); err == nil {
			return f
		}
		return nil
	}
	return map[string]any{
		"status":  v["ups.status"],
		"charge":  num("battery.charge"),
		"runtime": num("battery.runtime"),
		"load":    num("ups.load"),
		"input":   num("input.voltage"),
		"model":   strings.TrimSpace(v["ups.mfr"] + " " + v["ups.model"]),
	}
}
