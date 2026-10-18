package system

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strataos/internal/sys"
)

type Info struct {
	Hostname  string     `json:"hostname"`
	OS        string     `json:"os"`
	Kernel    string     `json:"kernel"`
	Arch      string     `json:"arch"`
	CPUModel  string     `json:"cpuModel"`
	Cores     int        `json:"cores"`
	MemTotal  int64      `json:"memTotal"`
	SwapTotal int64      `json:"swapTotal"`
	SwapFree  int64      `json:"swapFree"`
	Uptime    float64    `json:"uptime"`
	BootTime  time.Time  `json:"bootTime"`
	Load      [3]float64 `json:"load"`
	Timezone  string     `json:"timezone"`
	Temps     []Temp     `json:"temps"`
	Version   string     `json:"version"`
	Vendor    string     `json:"vendor"`
	Product   string     `json:"product"`
	RootTotal int64      `json:"rootTotal"` // system file system
	RootFree  int64      `json:"rootFree"`
}

type Temp struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

func GetInfo(version string) Info {
	i := Info{Arch: runtime.GOARCH, Cores: runtime.NumCPU(), Version: version}
	i.Hostname, _ = os.Hostname()
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				i.OS = strings.Trim(v, `"`)
			}
		}
	}
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		i.Kernel = strings.TrimSpace(string(data))
	}
	if f, err := os.Open("/proc/cpuinfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.TrimSpace(k) == "model name" {
				i.CPUModel = strings.TrimSpace(v)
				break
			}
		}
		f.Close()
	}
	mem := MemInfo()
	i.MemTotal, i.SwapTotal, i.SwapFree = mem["MemTotal"], mem["SwapTotal"], mem["SwapFree"]
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		f := strings.Fields(string(data))
		if len(f) > 0 {
			i.Uptime, _ = strconv.ParseFloat(f[0], 64)
			i.BootTime = time.Now().Add(-time.Duration(i.Uptime) * time.Second)
		}
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(data))
		for k := 0; k < 3 && k < len(f); k++ {
			i.Load[k], _ = strconv.ParseFloat(f[k], 64)
		}
	}
	if l, err := os.Readlink("/etc/localtime"); err == nil {
		i.Timezone = strings.TrimPrefix(l, "/usr/share/zoneinfo/")
	}
	i.Vendor = readTrim("/sys/class/dmi/id/sys_vendor")
	i.Product = readTrim("/sys/class/dmi/id/product_name")
	i.Temps = Temps()
	var fs syscall.Statfs_t
	if syscall.Statfs("/", &fs) == nil {
		i.RootTotal, i.RootFree = int64(fs.Blocks)*fs.Bsize, int64(fs.Bavail)*fs.Bsize
	}
	return i
}

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

// Temps reads hwmon temperature sensors.
func Temps() []Temp {
	out := []Temp{}
	dirs, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for _, d := range dirs {
		name := readTrim(filepath.Join(d, "name"))
		inputs, _ := filepath.Glob(filepath.Join(d, "temp*_input"))
		for _, in := range inputs {
			v, err := strconv.ParseFloat(readTrim(in), 64)
			if err != nil {
				continue
			}
			label := readTrim(strings.Replace(in, "_input", "_label", 1))
			if label == "" {
				label = name
			} else {
				label = name + " " + label
			}
			out = append(out, Temp{Label: label, Value: v / 1000})
		}
	}
	return out
}

// Service describes a systemd unit StrataOS cares about.
type Service struct {
	Unit        string `json:"unit"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Enabled     string `json:"enabled"`
	Loaded      bool   `json:"loaded"`
}

var KnownServices = []struct{ Unit, Name string }{
	{"smbd.service", "SMB / CIFS"},
	{"nmbd.service", "NetBIOS (nmbd)"},
	{"wsdd2.service", "Windows discovery (WSD)"},
	{"avahi-daemon.service", "Bonjour / mDNS"},
	{"nfs-server.service", "NFS server"},
	{"docker.service", "Docker Engine"},
	{"ssh.service", "SSH"},
	{"smartmontools.service", "SMART monitoring"},
	{"mdmonitor.service", "RAID monitoring"},
	{"zfs-zed.service", "ZFS event daemon"},
	{"nut-server.service", "UPS (NUT)"},
	{"vsftpd.service", "FTP"},
	{"rsync.service", "Rsync daemon"},
	{"strata.service", "StrataOS"},
}

func isKnown(unit string) bool {
	for _, s := range KnownServices {
		if s.Unit == unit {
			return true
		}
	}
	return false
}

func Services() []Service {
	out := []Service{}
	for _, k := range KnownServices {
		res, _ := sys.Run("systemctl", "show", "-p", "ActiveState,SubState,UnitFileState,LoadState,Description", k.Unit)
		s := Service{Unit: k.Unit, Name: k.Name}
		for _, l := range strings.Split(res, "\n") {
			key, v, _ := strings.Cut(l, "=")
			switch key {
			case "ActiveState":
				s.Active = v
			case "SubState":
				s.Sub = v
			case "UnitFileState":
				s.Enabled = v
			case "LoadState":
				s.Loaded = v == "loaded"
			case "Description":
				s.Description = v
			}
		}
		if s.Loaded {
			out = append(out, s)
		}
	}
	return out
}

func ServiceAction(unit, action string) error {
	if !isKnown(unit) {
		return fmt.Errorf("unknown service %s", unit)
	}
	switch action {
	case "start", "stop", "restart", "enable", "disable":
	default:
		return fmt.Errorf("invalid action %s", action)
	}
	if unit == "strata.service" && action != "restart" {
		return fmt.Errorf("StrataOS can only be restarted")
	}
	args := []string{action, unit}
	if action == "enable" || action == "disable" {
		args = []string{action, "--now", unit}
	}
	_, err := sys.Run("systemctl", args...)
	return err
}

type LogEntry struct {
	Time     int64  `json:"time"`
	Unit     string `json:"unit"`
	Priority int    `json:"priority"`
	Message  string `json:"message"`
	Ident    string `json:"ident"`
}

var unitRe = regexp.MustCompile(`^[A-Za-z0-9@._\-]+$`)

func Logs(unit string, priority, lines int, grep string) ([]LogEntry, error) {
	if lines <= 0 || lines > 5000 {
		lines = 300
	}
	args := []string{"-o", "json", "--no-pager", "-n", strconv.Itoa(lines)}
	if unit != "" {
		if !unitRe.MatchString(unit) {
			return nil, fmt.Errorf("invalid unit")
		}
		args = append(args, "-u", unit)
	}
	if priority >= 0 && priority <= 7 {
		args = append(args, "-p", strconv.Itoa(priority))
	}
	if grep != "" {
		args = append(args, "--grep", grep, "--case-sensitive=false")
	}
	out, err := sys.RunOpts(context.Background(), sys.Opts{AllowCodes: []int{1}}, "journalctl", args...)
	if err != nil {
		return nil, err
	}
	entries := []LogEntry{}
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil {
			continue
		}
		e := LogEntry{}
		if ts, ok := m["__REALTIME_TIMESTAMP"].(string); ok {
			n, _ := strconv.ParseInt(ts, 10, 64)
			e.Time = n / 1000
		}
		e.Unit, _ = m["_SYSTEMD_UNIT"].(string)
		e.Ident, _ = m["SYSLOG_IDENTIFIER"].(string)
		if p, ok := m["PRIORITY"].(string); ok {
			e.Priority, _ = strconv.Atoi(p)
		}
		switch msg := m["MESSAGE"].(type) {
		case string:
			e.Message = msg
		case []any: // binary message as byte array
			b := make([]byte, 0, len(msg))
			for _, x := range msg {
				if f, ok := x.(float64); ok {
					b = append(b, byte(f))
				}
			}
			e.Message = string(b)
		}
		entries = append(entries, e)
	}
	// Newest first.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Time > entries[j].Time })
	return entries, nil
}

type Upgradable struct {
	Package string `json:"package"`
	Current string `json:"current"`
	New     string `json:"new"`
	Source  string `json:"source"`
}

var upgRe = regexp.MustCompile(`^(\S+)/(\S+)\s+(\S+)\s+\S+\s+\[upgradable from: (\S+)\]`)

func ListUpgradable() ([]Upgradable, error) {
	out, err := sys.Run("apt", "list", "--upgradable")
	if err != nil {
		return nil, err
	}
	res := []Upgradable{}
	for _, l := range strings.Split(out, "\n") {
		if m := upgRe.FindStringSubmatch(l); m != nil {
			res = append(res, Upgradable{Package: m[1], Source: m[2], New: m[3], Current: m[4]})
		}
	}
	return res, nil
}

type Iface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	State     string   `json:"state"`
	MTU       int      `json:"mtu"`
	Addresses []string `json:"addresses"`
	Speed     int      `json:"speed"`
	Kind      string   `json:"kind"`
}

type Network struct {
	Interfaces []Iface  `json:"interfaces"`
	Gateway    string   `json:"gateway"`
	DNS        []string `json:"dns"`
	Hostname   string   `json:"hostname"`
}

func GetNetwork() Network {
	n := Network{Interfaces: []Iface{}, DNS: []string{}}
	n.Hostname, _ = os.Hostname()
	out, err := sys.Run("ip", "-j", "addr")
	if err == nil {
		var raw []struct {
			Ifname    string `json:"ifname"`
			Address   string `json:"address"`
			Operstate string `json:"operstate"`
			MTU       int    `json:"mtu"`
			LinkType  string `json:"link_type"`
			AddrInfo  []struct {
				Local     string `json:"local"`
				Prefixlen int    `json:"prefixlen"`
				Scope     string `json:"scope"`
			} `json:"addr_info"`
		}
		_ = json.Unmarshal([]byte(out), &raw)
		for _, r := range raw {
			if r.Ifname == "lo" || strings.HasPrefix(r.Ifname, "veth") {
				continue
			}
			i := Iface{Name: r.Ifname, MAC: r.Address, State: r.Operstate, MTU: r.MTU, Addresses: []string{}, Kind: "virtual"}
			if physicalIface(r.Ifname) {
				i.Kind = "physical"
			}
			if strings.HasPrefix(r.Ifname, "docker") || strings.HasPrefix(r.Ifname, "br-") {
				i.Kind = "docker"
			}
			for _, a := range r.AddrInfo {
				if a.Scope == "link" {
					continue
				}
				i.Addresses = append(i.Addresses, fmt.Sprintf("%s/%d", a.Local, a.Prefixlen))
			}
			if s, err := strconv.Atoi(readTrim("/sys/class/net/" + r.Ifname + "/speed")); err == nil && s > 0 {
				i.Speed = s
			}
			n.Interfaces = append(n.Interfaces, i)
		}
	}
	if out, err := sys.Run("ip", "-j", "route", "show", "default"); err == nil {
		var routes []struct {
			Gateway string `json:"gateway"`
		}
		if json.Unmarshal([]byte(out), &routes) == nil && len(routes) > 0 {
			n.Gateway = routes[0].Gateway
		}
	}
	if data, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "nameserver "); ok {
				n.DNS = append(n.DNS, strings.TrimSpace(v))
			}
		}
	}
	return n
}

var hostRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func SetHostname(h string) error {
	if !hostRe.MatchString(h) {
		return fmt.Errorf("invalid hostname")
	}
	if _, err := sys.Run("hostnamectl", "set-hostname", h); err != nil {
		return err
	}
	// Keep /etc/hosts resolvable.
	data, _ := os.ReadFile("/etc/hosts")
	lines := strings.Split(string(data), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, "127.0.1.1") {
			lines[i] = "127.0.1.1\t" + h
			found = true
		}
	}
	if !found {
		lines = append([]string{"127.0.1.1\t" + h}, lines...)
	}
	return sys.WriteFileAtomic("/etc/hosts", []byte(strings.Join(lines, "\n")), 0o644)
}

func Timezones() []string {
	out, err := sys.Run("timedatectl", "list-timezones")
	if err != nil {
		return []string{}
	}
	return strings.Fields(out)
}

func SetTimezone(tz string) error {
	for _, t := range Timezones() {
		if t == tz {
			if _, err := sys.Run("timedatectl", "set-timezone", tz); err != nil {
				return err
			}
			// Scheduled tasks are evaluated in local time: follow the change without a restart.
			if loc, err := time.LoadLocation(tz); err == nil {
				time.Local = loc
			}
			return nil
		}
	}
	return fmt.Errorf("unknown timezone")
}

func SetNTP(on bool) error {
	_, err := sys.Run("timedatectl", "set-ntp", strconv.FormatBool(on))
	return err
}

func Power(action string) error {
	switch action {
	case "reboot", "poweroff":
	default:
		return fmt.Errorf("invalid power action")
	}
	go func() {
		time.Sleep(time.Second)
		_, _ = sys.Run("systemctl", action)
	}()
	return nil
}
