// Package network configures interfaces, bonds and VLANs through systemd-networkd,
// with an automatic rollback when a change is not confirmed in time.
package network

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"strataos/internal/sys"
)

// IP is the layer-3 configuration of a link.
type IP struct {
	Mode      string   `json:"mode"`                // dhcp | static | none
	Addresses []string `json:"addresses,omitempty"` // CIDR, e.g. 192.168.1.10/24
	Gateway   string   `json:"gateway,omitempty"`
	DNS       []string `json:"dns,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
}

type Iface struct {
	Name string `json:"name"`
	IP
}

type Bond struct {
	Name     string   `json:"name"`     // bond0
	BondMode string   `json:"bondMode"` // active-backup | 802.3ad | balance-alb | balance-tlb | balance-rr | balance-xor | broadcast
	Members  []string `json:"members"`
	IP
}

type VLAN struct {
	Parent string `json:"parent"`
	ID     int    `json:"id"`
	IP
}

func (v VLAN) Name() string { return fmt.Sprintf("%s.%d", v.Parent, v.ID) }

// Config is the network configuration owned by StrataOS. Links not listed are left alone.
type Config struct {
	Interfaces []Iface `json:"interfaces"`
	Bonds      []Bond  `json:"bonds"`
	VLANs      []VLAN  `json:"vlans"`
}

var (
	// Paths are variables so tests can point them elsewhere.
	NetworkdDir = "/etc/systemd/network"
	InterfacesF = "/etc/network/interfaces"
	ResolvConf  = "/etc/resolv.conf"
	StateDir    = "/var/lib/strata/network"
	filePrefix  = "05-strata-"
	ifNameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-]{0,14}$`)
	bondNameRe  = regexp.MustCompile(`^bond[0-9]{1,3}$`)
	bondModes   = map[string]bool{"active-backup": true, "802.3ad": true, "balance-alb": true, "balance-tlb": true, "balance-rr": true, "balance-xor": true, "broadcast": true}
)

func validIP(ip IP, name string) error {
	switch ip.Mode {
	case "dhcp", "none":
	case "static":
		if len(ip.Addresses) == 0 {
			return fmt.Errorf("%s: a static configuration needs an address", name)
		}
		for _, a := range ip.Addresses {
			if _, _, err := net.ParseCIDR(a); err != nil {
				return fmt.Errorf("%s: invalid address %q (expected 192.168.1.10/24)", name, a)
			}
		}
		if ip.Gateway != "" && net.ParseIP(ip.Gateway) == nil {
			return fmt.Errorf("%s: invalid gateway", name)
		}
	default:
		return fmt.Errorf("%s: mode must be dhcp, static or none", name)
	}
	for _, d := range ip.DNS {
		if net.ParseIP(d) == nil {
			return fmt.Errorf("%s: invalid DNS server %q", name, d)
		}
	}
	if ip.MTU != 0 && (ip.MTU < 576 || ip.MTU > 9216) {
		return fmt.Errorf("%s: MTU must be between 576 and 9216", name)
	}
	return nil
}

// Validate checks a configuration against the links present on the machine.
func Validate(c *Config, physical []string) error {
	phys := map[string]bool{}
	for _, p := range physical {
		phys[p] = true
	}
	used := map[string]string{}
	claim := func(name, by string) error {
		if prev, ok := used[name]; ok {
			return fmt.Errorf("%s is configured twice (%s and %s)", name, prev, by)
		}
		used[name] = by
		return nil
	}
	for _, i := range c.Interfaces {
		if !ifNameRe.MatchString(i.Name) || !phys[i.Name] {
			return fmt.Errorf("unknown interface %q", i.Name)
		}
		if err := validIP(i.IP, i.Name); err != nil {
			return err
		}
		if err := claim(i.Name, "interface"); err != nil {
			return err
		}
	}
	for _, b := range c.Bonds {
		if !bondNameRe.MatchString(b.Name) {
			return fmt.Errorf("bond names are bond0, bond1…")
		}
		if !bondModes[b.BondMode] {
			return fmt.Errorf("%s: unknown bonding mode %q", b.Name, b.BondMode)
		}
		if len(b.Members) < 1 {
			return fmt.Errorf("%s: choose at least one member", b.Name)
		}
		for _, m := range b.Members {
			if !phys[m] {
				return fmt.Errorf("%s: unknown member %q", b.Name, m)
			}
			if err := claim(m, b.Name); err != nil {
				return err
			}
		}
		if err := validIP(b.IP, b.Name); err != nil {
			return err
		}
		if err := claim(b.Name, "bond"); err != nil {
			return err
		}
	}
	for _, v := range c.VLANs {
		if v.ID < 1 || v.ID > 4094 {
			return fmt.Errorf("VLAN ids go from 1 to 4094")
		}
		parentOK := phys[v.Parent]
		for _, b := range c.Bonds {
			parentOK = parentOK || b.Name == v.Parent
		}
		if !parentOK {
			return fmt.Errorf("VLAN %d: unknown parent %q", v.ID, v.Parent)
		}
		switch used[v.Parent] {
		case "interface", "bond":
		case "":
			return fmt.Errorf("VLAN %d: let StrataOS manage %s first (the VLAN is attached to its configuration)", v.ID, v.Parent)
		default:
			return fmt.Errorf("VLAN %d: %s is a bond member", v.ID, v.Parent)
		}
		if err := validIP(v.IP, v.Name()); err != nil {
			return err
		}
		if err := claim(v.Name(), "vlan"); err != nil {
			return err
		}
	}
	return nil
}

func ipSection(b *strings.Builder, ip IP) {
	switch ip.Mode {
	case "dhcp":
		b.WriteString("DHCP=yes\n")
	case "static":
		for _, a := range ip.Addresses {
			fmt.Fprintf(b, "Address=%s\n", a)
		}
		if ip.Gateway != "" {
			fmt.Fprintf(b, "Gateway=%s\n", ip.Gateway)
		}
	case "none":
		b.WriteString("LinkLocalAddressing=no\nIPv6AcceptRA=no\n")
	}
	for _, d := range ip.DNS {
		fmt.Fprintf(b, "DNS=%s\n", d)
	}
}

// dhcpSection must come last: it opens a new section after [Network].
func dhcpSection(b *strings.Builder, ip IP) {
	if ip.Mode == "dhcp" {
		// Identify by MAC address (like ifupdown/dhclient): the NAS keeps its lease and
		// DHCP reservations made on the router keep working, also for bonds.
		b.WriteString("\n[DHCPv4]\nClientIdentifier=mac\n")
	}
}

func linkSection(b *strings.Builder, ip IP) {
	if ip.MTU > 0 {
		fmt.Fprintf(b, "\n[Link]\nMTUBytes=%d\n", ip.MTU)
	}
}

// Render produces the systemd-networkd files (name -> content) for a configuration.
func Render(c Config) map[string]string {
	files := map[string]string{}
	vlansOf := map[string][]string{}
	for _, v := range c.VLANs {
		vlansOf[v.Parent] = append(vlansOf[v.Parent], v.Name())
	}
	network := func(name string, ip IP, extra string) {
		var b strings.Builder
		fmt.Fprintf(&b, "# Managed by StrataOS\n[Match]\nName=%s\n", name)
		linkSection(&b, ip)
		b.WriteString("\n[Network]\n")
		ipSection(&b, ip)
		for _, v := range vlansOf[name] {
			fmt.Fprintf(&b, "VLAN=%s\n", v)
		}
		b.WriteString(extra)
		dhcpSection(&b, ip)
		files[filePrefix+name+".network"] = b.String()
	}
	for _, i := range c.Interfaces {
		network(i.Name, i.IP, "")
	}
	for _, bd := range c.Bonds {
		var nd strings.Builder
		fmt.Fprintf(&nd, "# Managed by StrataOS\n[NetDev]\nName=%s\nKind=bond\n\n[Bond]\nMode=%s\nMIIMonitorSec=100ms\n", bd.Name, bd.BondMode)
		if bd.BondMode == "802.3ad" {
			nd.WriteString("LACPTransmitRate=fast\nTransmitHashPolicy=layer3+4\n")
		}
		if bd.BondMode == "active-backup" && len(bd.Members) > 0 {
			// The first member is primary; its MAC is kept for the bond.
			nd.WriteString("PrimaryReselectPolicy=always\n")
		}
		files[filePrefix+bd.Name+".netdev"] = nd.String()
		network(bd.Name, bd.IP, "")
		for i, m := range bd.Members {
			extra := ""
			if bd.BondMode == "active-backup" && i == 0 {
				extra = "PrimarySlave=yes\n"
			}
			files[filePrefix+m+".network"] = fmt.Sprintf("# Managed by StrataOS\n[Match]\nName=%s\n\n[Network]\nBond=%s\n%s", m, bd.Name, extra)
		}
	}
	for _, v := range c.VLANs {
		files[filePrefix+v.Name()+".netdev"] = fmt.Sprintf("# Managed by StrataOS\n[NetDev]\nName=%s\nKind=vlan\n\n[VLAN]\nId=%d\n", v.Name(), v.ID)
		network(v.Name(), v.IP, "")
	}
	return files
}

// Manager reports which network stack owns the machine.
func Manager() string {
	active := func(u string) bool {
		out, _ := sys.Run("systemctl", "is-active", u)
		return strings.TrimSpace(out) == "active"
	}
	switch {
	case active("NetworkManager.service"):
		return "NetworkManager"
	case active("networking.service") && fileHasStanzas(InterfacesF):
		return "ifupdown"
	case active("systemd-networkd.service"):
		return "systemd-networkd"
	}
	return "unknown"
}

func fileHasStanzas(p string) bool {
	b, _ := os.ReadFile(p)
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "iface" && f[1] != "lo" {
			return true
		}
	}
	return false
}

// ---- Apply with rollback ----

type Pending struct {
	Token    string    `json:"token,omitempty"`
	Deadline time.Time `json:"deadline"`
	Links    []string  `json:"links"`
	Config   *Config   `json:"config,omitempty"` // configuration being tried
	// ReadyAt is when the new configuration has been active long enough for a
	// confirmation to prove anything; earlier confirmations are refused.
	ReadyAt time.Time `json:"readyAt"`
}

// ErrNotReady is returned by Confirm while the new configuration is still settling.
var ErrNotReady = errors.New("the new configuration is still being applied, retry in a few seconds")

// settle is how long links must have been up before a confirmation is accepted.
var settle = 5 * time.Second

var (
	mu      sync.Mutex
	pending *Pending
	timer   *time.Timer
)

func backupDir() string   { return filepath.Join(StateDir, "rollback") }
func pendingPath() string { return filepath.Join(StateDir, "pending.json") }

// files StrataOS may change, so that a rollback restores them exactly.
func managedFiles() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(NetworkdDir, filePrefix+"*"))
	if err != nil {
		return nil, err
	}
	out := append(matches, InterfacesF)
	if fi, err := os.Lstat(ResolvConf); err == nil && fi.Mode().IsRegular() {
		out = append(out, ResolvConf)
	}
	return out, nil
}

func snapshot() error {
	_ = os.RemoveAll(backupDir())
	if err := os.MkdirAll(backupDir(), 0o700); err != nil {
		return err
	}
	files, err := managedFiles()
	if err != nil {
		return err
	}
	index := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		index[f] = true
		if err := os.WriteFile(filepath.Join(backupDir(), hexName(f)), data, 0o600); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(index)
	return os.WriteFile(filepath.Join(backupDir(), "index.json"), b, 0o600)
}

func hexName(p string) string { return hex.EncodeToString([]byte(p)) }

// restore puts back the files saved by snapshot and removes the ones created since.
func restore() error {
	var index map[string]bool
	b, err := os.ReadFile(filepath.Join(backupDir(), "index.json"))
	if err != nil {
		return fmt.Errorf("no rollback point: %w", err)
	}
	if err := json.Unmarshal(b, &index); err != nil {
		return err
	}
	current, _ := managedFiles()
	for _, f := range current {
		if !index[f] && strings.HasPrefix(filepath.Base(f), filePrefix) {
			_ = os.Remove(f)
		}
	}
	for f := range index {
		data, err := os.ReadFile(filepath.Join(backupDir(), hexName(f)))
		if err != nil {
			return err
		}
		if err := sys.WriteFileAtomic(f, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// links returns every link name a configuration touches.
func links(c Config) []string {
	set := map[string]bool{}
	for _, i := range c.Interfaces {
		set[i.Name] = true
	}
	for _, b := range c.Bonds {
		set[b.Name] = true
		for _, m := range b.Members {
			set[m] = true
		}
	}
	for _, v := range c.VLANs {
		set[v.Name()] = true
		set[v.Parent] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Apply writes the configuration and activates it. Unless Confirm is called with
// the returned token before the deadline, the previous configuration comes back.
func Apply(next, prev Config, timeout time.Duration) (*Pending, error) {
	mu.Lock()
	defer mu.Unlock()
	if pending != nil {
		return nil, fmt.Errorf("a network change is already waiting for confirmation")
	}
	mgr := Manager()
	if mgr == "NetworkManager" {
		return nil, fmt.Errorf("NetworkManager manages this machine: disable it (or configure the network there) before StrataOS can take over")
	}
	if err := os.MkdirAll(StateDir, 0o700); err != nil {
		return nil, err
	}
	if err := snapshot(); err != nil {
		return nil, fmt.Errorf("rollback point: %w", err)
	}
	touched := append(links(next), links(prev)...)
	if err := write(next, mgr); err != nil {
		_ = restore()
		return nil, err
	}
	tok := make([]byte, 24)
	_, _ = rand.Read(tok)
	p := &Pending{Token: hex.EncodeToString(tok), Deadline: time.Now().Add(timeout), Links: dedupe(touched), Config: &next}
	b, _ := json.Marshal(p)
	if err := os.WriteFile(pendingPath(), b, 0o600); err != nil {
		_ = restore()
		return nil, err
	}
	p.ReadyAt = time.Now().Add(24 * time.Hour) // set for real once activated
	pending = p
	// Activate just after the HTTP response has left: links may go down for a moment
	// and the browser must receive its confirmation token first.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		activate(p.Links, mgr)
		mu.Lock()
		if pending == p {
			p.ReadyAt = time.Now().Add(settle)
		}
		mu.Unlock()
	}()
	timer = time.AfterFunc(timeout, func() { _ = Rollback("timeout") })
	return p, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func write(c Config, mgr string) error {
	if err := os.MkdirAll(NetworkdDir, 0o755); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(NetworkdDir, filePrefix+"*"))
	for _, f := range old {
		_ = os.Remove(f)
	}
	for name, content := range Render(c) {
		if err := sys.WriteFileAtomic(filepath.Join(NetworkdDir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	if mgr == "ifupdown" {
		if err := releaseFromIfupdown(links(c)); err != nil {
			return err
		}
	}
	// Without systemd-resolved, DNS servers go straight to resolv.conf.
	if fi, err := os.Lstat(ResolvConf); err == nil && fi.Mode().IsRegular() {
		var dns []string
		for _, i := range c.Interfaces {
			dns = append(dns, i.DNS...)
		}
		for _, b := range c.Bonds {
			dns = append(dns, b.DNS...)
		}
		if len(dns) > 0 {
			var b strings.Builder
			b.WriteString("# Managed by StrataOS\n")
			for _, d := range dedupe(dns) {
				fmt.Fprintf(&b, "nameserver %s\n", d)
			}
			if err := sys.WriteFileAtomic(ResolvConf, []byte(b.String()), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// releaseFromIfupdown comments out the ifupdown stanzas of links StrataOS now manages.
func releaseFromIfupdown(names []string) error {
	data, err := os.ReadFile(InterfacesF)
	if err != nil {
		return nil
	}
	managed := map[string]bool{}
	for _, n := range names {
		managed[n] = true
	}
	var out []string
	inStanza := false
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && (f[0] == "auto" || f[0] == "allow-hotplug") {
			var keep []string
			for _, n := range f[1:] {
				if managed[n] {
					_, _ = sys.Run("ifdown", "--force", n)
				} else {
					keep = append(keep, n)
				}
			}
			if len(keep) < len(f)-1 {
				out = append(out, "# strata: "+l)
				if len(keep) > 0 {
					out = append(out, f[0]+" "+strings.Join(keep, " "))
				}
				continue
			}
		}
		if len(f) >= 2 && f[0] == "iface" {
			inStanza = managed[f[1]]
		} else if len(f) > 0 && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") && f[0] != "#" {
			inStanza = false
		}
		if inStanza {
			out = append(out, "# strata: "+l)
			continue
		}
		out = append(out, l)
	}
	return sys.WriteFileAtomic(InterfacesF, []byte(strings.Join(out, "\n")), 0o644)
}

// activate reloads networkd and reconfigures the links involved.
func activate(names []string, mgr string) {
	_, _ = sys.Run("systemctl", "enable", "--now", "systemd-networkd.service")
	// Links that are no longer bonds/VLANs must disappear before reconfiguration.
	for _, n := range names {
		if strings.HasPrefix(n, "bond") || strings.Contains(n, ".") {
			if _, err := os.Stat(filepath.Join(NetworkdDir, filePrefix+n+".netdev")); err != nil {
				_, _ = sys.Run("ip", "link", "delete", n)
			}
		}
	}
	_, _ = sys.Run("networkctl", "reload")
	for _, n := range names {
		if _, err := os.Stat(filepath.Join("/sys/class/net", n)); err == nil {
			_, _ = sys.Run("networkctl", "reconfigure", n)
		}
	}
	if mgr == "ifupdown" {
		_, _ = sys.Run("systemctl", "try-restart", "networking.service")
	}
}

// Confirm keeps the pending configuration and returns it.
func Confirm(token string) (*Config, error) {
	mu.Lock()
	defer mu.Unlock()
	if pending == nil {
		return nil, fmt.Errorf("no network change is waiting for confirmation")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(pending.Token)) != 1 {
		return nil, fmt.Errorf("invalid confirmation token")
	}
	if time.Now().Before(pending.ReadyAt) {
		return nil, ErrNotReady
	}
	if timer != nil {
		timer.Stop()
	}
	cfg := pending.Config
	pending = nil
	_ = os.Remove(pendingPath())
	return cfg, nil
}

// Rollback restores the previous configuration (on timeout, on request, or at startup
// when the daemon restarted while a change was pending).
func Rollback(reason string) error {
	mu.Lock()
	defer mu.Unlock()
	if pending == nil {
		if _, err := os.Stat(pendingPath()); err != nil {
			return fmt.Errorf("nothing to roll back")
		}
	}
	var names []string
	if pending != nil {
		names = pending.Links
	} else if b, err := os.ReadFile(pendingPath()); err == nil {
		var p Pending
		_ = json.Unmarshal(b, &p)
		names = p.Links
	}
	if timer != nil {
		timer.Stop()
	}
	if err := restore(); err != nil {
		return err
	}
	pending = nil
	_ = os.Remove(pendingPath())
	activate(names, Manager())
	if OnRollback != nil {
		OnRollback(reason)
	}
	return nil
}

// OnRollback is notified after an automatic or manual rollback.
var OnRollback func(reason string)

// Current returns the pending change, if any.
func Current() *Pending {
	mu.Lock()
	defer mu.Unlock()
	if pending == nil {
		return nil
	}
	c := *pending
	c.Token = ""
	c.Config = nil
	return &c
}

// Recover handles a change left pending by a previous run of the daemon.
func Recover() {
	b, err := os.ReadFile(pendingPath())
	if err != nil {
		return
	}
	var p Pending
	if json.Unmarshal(b, &p) != nil {
		_ = os.Remove(pendingPath())
		return
	}
	if time.Now().After(p.Deadline) {
		_ = Rollback("restart")
		return
	}
	mu.Lock()
	p.ReadyAt = time.Now().Add(settle)
	pending = &p
	timer = time.AfterFunc(time.Until(p.Deadline), func() { _ = Rollback("timeout") })
	mu.Unlock()
}
