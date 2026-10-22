package vpn

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"strataos/internal/store"
	"strataos/internal/sys"
)

var (
	Dir              = "/etc/wireguard"
	SysctlPath       = "/etc/sysctl.d/90-strata-vpn.conf"
	DockerDropInPath = "/etc/systemd/system/docker.service.d/strata-vpn.conf"
	KillUnit         = "strata-vpn-killswitch.service"
	KillUnitPath     = "/etc/systemd/system/" + KillUnit
)

// Apply puts c in place: forwarding, firewall, the server, the site links
// and the outgoing tunnel. c is the effective configuration (Effective).
func Apply(c store.VPN) error {
	need := c.Enabled || ClientActive(c.Client) || slices.ContainsFunc(c.Links, func(l store.VPNLink) bool { return !l.Disabled })
	if !Installed() {
		if need {
			return fmt.Errorf("WireGuard is not installed")
		}
		return nil
	}
	v6 := HostIPv6()
	if !v6 {
		c.Client = WithoutIPv6(c.Client)
	}
	setForward(NeedsForward(c), v6 && ClientActive(c.Client) && c.Client.Scope == "apps")
	// The firewall first: restrictions hold as soon as a peer connects.
	errs := []error{applyFirewall(c), applyServer(c), applyLinks(c), applyClient(c.Client, v6)}
	dockerUser(ifaces(c))
	return errors.Join(errs...)
}

// ifaceSection is the [Interface] part of a configuration: when it changes
// the interface must be restarted, peers alone change live.
func ifaceSection(conf string) string {
	if i := strings.Index(conf, "\n\n"); i >= 0 {
		return conf[:i]
	}
	return conf
}

// applyUnit writes the configuration of a wg-quick interface and starts,
// updates or (conf "") stops it.
func applyUnit(iface, conf string, live bool) error {
	path := filepath.Join(Dir, iface+".conf")
	unit := "wg-quick@" + iface + ".service"
	if conf == "" {
		_, _ = sys.Run("systemctl", "disable", "--now", unit)
		return nil
	}
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return err
	}
	old, _ := os.ReadFile(path)
	if err := sys.WriteFileAtomic(path, []byte(conf), 0o600); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "enable", unit)
	active, _ := sys.Run("systemctl", "is-active", unit)
	switch {
	case strings.TrimSpace(active) != "active":
		_, err := sys.Run("systemctl", "start", unit)
		return err
	case string(old) == conf:
		return nil
	case !live || ifaceSection(string(old)) != ifaceSection(conf):
		_, err := sys.Run("systemctl", "restart", unit)
		return err
	default:
		// Peers change without cutting the others.
		stripped, err := sys.Run("wg-quick", "strip", iface)
		if err != nil {
			return err
		}
		_, err = sys.RunInput(stripped, "wg", "syncconf", iface, "/dev/stdin")
		return err
	}
}

func applyServer(c store.VPN) error {
	if !c.Enabled {
		return applyUnit(Iface, "", true)
	}
	if err := applyUnit(Iface, RenderServer(c), true); err != nil {
		return err
	}
	// wg syncconf does not touch routes: those of the sites are ours.
	cur, err := sys.Run("ip", "-4", "route", "show", "dev", Iface)
	if err != nil {
		return err
	}
	add, del := routePlan(cur, siteRoutes(c), c.Subnet)
	for _, d := range del {
		_, _ = sys.Run("ip", "-4", "route", "del", d, "dev", Iface)
	}
	for _, a := range add {
		if _, err := sys.Run("ip", "-4", "route", "replace", a, "dev", Iface); err != nil {
			return err
		}
	}
	return nil
}

func applyLinks(c store.VPN) error {
	var errs []error
	keep := map[string]bool{}
	for _, l := range c.Links {
		if l.Disabled || !linkRe.MatchString(l.Iface) {
			continue
		}
		keep[l.Iface] = true
		errs = append(errs, applyUnit(l.Iface, RenderLink(l), false))
	}
	old, _ := filepath.Glob(filepath.Join(Dir, "strata-l*.conf"))
	for _, p := range old {
		name := strings.TrimSuffix(filepath.Base(p), ".conf")
		if linkRe.MatchString(name) && !keep[name] {
			_ = applyUnit(name, "", false)
			_ = os.Remove(p)
		}
	}
	return errors.Join(errs...)
}

func applyClient(cl store.VPNClient, v6 bool) error {
	path := filepath.Join(Dir, OutIface+".conf")
	if !ClientActive(cl) {
		if _, err := os.Stat(path); err == nil {
			_ = applyUnit(OutIface, "", true)
			_ = os.Remove(path)
		}
		clearRouting(true)
		removeDropIn()
		removeKillUnit()
		return nil
	}
	uid := AptUID()
	var errs []error
	if cl.Scope == "apps" {
		errs = append(errs, writeIfChanged(DockerDropInPath, DockerDropIn(cl, v6), 0o644))
		removeKillUnit()
	} else {
		removeDropIn()
		errs = append(errs, writeKillUnit(KillSwitchUnit(cl, v6, uid)))
	}
	// The kill switch before the tunnel: nothing leaks while it comes up,
	// nor when it cannot.
	errs = append(errs, ensureKill(KillRules(cl, v6, uid)))
	if err := applyUnit(OutIface, RenderClientTunnel(cl), true); err != nil {
		return errors.Join(append(errs, err)...)
	}
	// Answers from the Internet come back on strata-out while the main
	// table routes their source elsewhere: loose reverse path check. The
	// IPv6 address must survive the link going down (kill switch test).
	_, _ = sys.Run("sysctl", "-q", "-w", "net.ipv4.conf."+OutIface+".rp_filter=2")
	if v6 {
		_, _ = sys.Run("sysctl", "-q", "-w", "net.ipv6.conf."+OutIface+".keep_addr_on_down=1")
	}
	errs = append(errs, setupRouting(cl, v6, uid))
	return errors.Join(errs...)
}

func removeDropIn() {
	if _, err := os.Stat(DockerDropInPath); err == nil {
		_ = os.Remove(DockerDropInPath)
		_, _ = sys.Run("systemctl", "daemon-reload")
	}
}

func writeKillUnit(unit string) error {
	if old, err := os.ReadFile(KillUnitPath); err == nil && string(old) == unit {
		return nil
	}
	if err := writeIfChanged(KillUnitPath, unit, 0o644); err != nil {
		return err
	}
	// Enabled for the next boot only: the rules are in place already.
	_, err := sys.Run("systemctl", "enable", KillUnit)
	return err
}

func removeKillUnit() {
	if _, err := os.Stat(KillUnitPath); err == nil {
		_, _ = sys.Run("systemctl", "disable", KillUnit)
		_ = os.Remove(KillUnitPath)
		_, _ = sys.Run("systemctl", "daemon-reload")
	}
}

func writeIfChanged(path, content string, mode os.FileMode) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(path, []byte(content), mode); err != nil {
		return err
	}
	if strings.HasPrefix(path, "/etc/systemd/") {
		_, _ = sys.Run("systemctl", "daemon-reload")
	}
	return nil
}

var families = []string{"-4", "-6"}

func delRules(fam string, pref int) {
	for range 20 {
		if _, err := sys.Run("ip", fam, "rule", "del", "pref", strconv.Itoa(pref)); err != nil {
			return
		}
	}
}

// clearRouting removes the policy routing of the tunnel (kill: also the
// kill switch rules).
func clearRouting(kill bool) {
	prefs := []int{prioMain, prioTunnelSrc, prioTunnel}
	if kill {
		prefs = append(prefs, killPrefs...)
	}
	for _, f := range families {
		for _, p := range prefs {
			delRules(f, p)
		}
		_, _ = sys.Run("ip", f, "route", "flush", "table", strconv.Itoa(RouteTable))
	}
}

// killPrefs in the order they are set up: the exemptions first.
var killPrefs = []int{prioLocal, prioKill, prioUpdates, prioKillAll}

// ruleShown is how `ip rule show` prints a rule added by cmd.
func ruleShown(cmd []string) string {
	for i := 0; i+1 < len(cmd); i++ {
		if cmd[i] == "pref" {
			return " " + strings.Join(cmd[i+2:], " ")
		}
	}
	return ""
}

// rulesMatch tells whether `ip rule show pref N` is exactly the rules of
// cmds ("5305:	not from all fwmark 0xcb20 blackhole").
func rulesMatch(shown string, cmds [][]string) bool {
	var lines []string
	for _, l := range strings.Split(shown, "\n") {
		if _, l, ok := strings.Cut(l, ":"); ok {
			l = strings.Join(strings.Fields(l), " ")
			l = strings.Replace(strings.Replace(l, "not from all ", "not ", 1), "from all ", "", 1)
			lines = append(lines, " "+l)
		}
	}
	if len(lines) != len(cmds) {
		return false
	}
	for _, c := range cmds {
		if !slices.Contains(lines, ruleShown(c)) {
			return false
		}
	}
	return true
}

// ensureKill puts the kill switch rules in place, leaving those already
// right untouched (no gap), and removes the others. When the exemptions of
// the local networks cannot be set, the blackhole of that family goes too:
// a kill switch must never lock the administrator out.
func ensureKill(cmds [][]string) error {
	var errs []error
	failed := map[string]bool{}
	for _, f := range families {
		for _, pref := range killPrefs {
			var group [][]string
			for _, c := range cmds {
				if c[0] == f && c[4] == strconv.Itoa(pref) {
					group = append(group, c)
				}
			}
			if len(group) == 0 || (failed[f] && pref == prioKillAll) {
				delRules(f, pref)
				continue
			}
			shown, err := sys.Run("ip", f, "rule", "show", "pref", strconv.Itoa(pref))
			if err == nil && rulesMatch(shown, group) {
				continue
			}
			delRules(f, pref)
			for _, c := range group {
				if _, err := sys.Run("ip", c...); err != nil {
					errs = append(errs, err)
					if pref == prioLocal {
						failed[f] = true
					}
				}
			}
		}
	}
	return errors.Join(errs...)
}

// SetupRouting (re)creates the policy routing of the outgoing tunnel. The
// kill switch rules are never removed while the tunnel is on.
func SetupRouting(cl store.VPNClient) error {
	v6 := HostIPv6()
	if !v6 {
		cl = WithoutIPv6(cl)
	}
	return setupRouting(cl, v6, AptUID())
}

func setupRouting(cl store.VPNClient, v6 bool, aptUID int) error {
	if err := ensureKill(KillRules(cl, v6, aptUID)); err != nil {
		return err
	}
	clearRouting(false)
	var first error
	for _, cmd := range ClientRules(cl, v6) {
		if _, err := sys.Run("ip", cmd...); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// RoutingOK reports whether the tunnel routes and rules are in place (they
// go away with the interface).
func RoutingOK(cl store.VPNClient) bool {
	v6 := HostIPv6() && TunnelHasIPv6(cl)
	for _, f := range families {
		if f == "-6" && !v6 {
			continue
		}
		rt, _ := sys.Run("ip", f, "route", "show", "table", strconv.Itoa(RouteTable))
		rule, _ := sys.Run("ip", f, "rule", "show", "pref", strconv.Itoa(prioTunnel))
		if !strings.Contains(rt, "dev "+OutIface) || strings.TrimSpace(rule) == "" {
			return false
		}
	}
	return true
}

// KillSwitchResult is the outcome of TestKillSwitch.
type KillSwitchResult struct {
	TunnelRoute string `json:"tunnelRoute"` // route to the Internet, tunnel up
	DownRoute   string `json:"downRoute"`   // and tunnel down
	DownRoute6  string `json:"downRoute6,omitempty"`
	// Whole NAS, tunnel down: the local network and the WireGuard server
	// must still have their way out.
	LocalRoute  string `json:"localRoute,omitempty"`
	ServerRoute string `json:"serverRoute,omitempty"`
	Blocked     bool   `json:"blocked"`
}

// Internet test addresses of the kill switch test (only routes are looked up).
const (
	probe4 = "1.1.1.1"
	probe6 = "2606:4700:4700::1111"
)

// routeOut reads `ip route get`: the route, and whether it leaves by
// another interface than the tunnel.
func routeOut(args ...string) (string, bool) {
	out, err := sys.Run("ip", append([]string{"route", "get"}, args...)...)
	if err != nil {
		return strings.TrimSpace(out + " " + err.Error()), false
	}
	return strings.TrimSpace(out), strings.Contains(out, " dev ") && !strings.Contains(out, "dev "+OutIface)
}

// defaultGateway is the router of the main table ("" without one).
func defaultGateway() string {
	out, _ := sys.Run("ip", "-4", "route", "show", "default", "table", "main")
	f := strings.Fields(out)
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "via" {
			return f[i+1]
		}
	}
	return ""
}

// TestKillSwitch takes the tunnel down for a moment and checks nothing
// routed through it has another way out (whole NAS: while the local
// network and the WireGuard server still have theirs), then brings it back.
func TestKillSwitch(cl store.VPNClient) (KillSwitchResult, error) {
	var r KillSwitchResult
	if !ClientActive(cl) {
		return r, fmt.Errorf("the outgoing tunnel is off")
	}
	v6 := HostIPv6()
	if !v6 {
		cl = WithoutIPv6(cl)
	}
	var from4, from6 []string
	if cl.Scope == "apps" {
		if _, err := sys.Run("ip", "link", "show", AppsBridge); err != nil {
			return r, fmt.Errorf("the Docker network of the routed apps does not exist yet")
		}
		from4 = []string{"from", Gateway(cl.AppsSubnet)}
		from6 = []string{"from", Gateway6()}
		if out, _ := sys.Run("ip", "-6", "addr", "show", "dev", AppsBridge); !strings.Contains(out, Gateway6()+"/") {
			from6 = nil // the network has no IPv6: nothing to test
		}
	}
	probe := func() (string, string, bool) {
		out, leak := routeOut(append([]string{probe4}, from4...)...)
		out6, leak6 := "", false
		if v6 && (cl.Scope == "all" || from6 != nil) {
			out6, leak6 = routeOut(append([]string{probe6}, from6...)...)
		}
		return out, out6, leak || leak6
	}
	r.TunnelRoute, _, _ = probe()
	if _, err := sys.Run("ip", "link", "set", OutIface, "down"); err != nil {
		return r, err
	}
	var leak bool
	r.DownRoute, r.DownRoute6, leak = probe()
	localOK := true
	if cl.Scope == "all" {
		gw := defaultGateway()
		if gw != "" {
			out, ok := routeOut(gw)
			r.LocalRoute, localOK = out, ok
		}
		// The encrypted packets of the WireGuard server carry its mark.
		out, ok := routeOut(probe4, "mark", mark(FwMark))
		r.ServerRoute, localOK = out, localOK && ok
	}
	_, _ = sys.Run("ip", "link", "set", OutIface, "up")
	err := setupRouting(cl, v6, AptUID())
	if cl.Scope == "apps" {
		fw, _ := sys.Run("nft", "list", "chain", "inet", "strata_wg", "forward")
		r.Blocked = !leak && strings.Contains(fw, AppsBridge)
	} else {
		r.Blocked = !leak && localOK
	}
	return r, err
}

// setForward turns routing on: IPv4 for the VPN, IPv6 for the routed
// apps (v6).
func setForward(need, v6 bool) {
	if !need {
		_ = os.Remove(SysctlPath)
		return
	}
	var ra []string
	if v6 {
		ra = raIfaces()
		// Before forwarding: a router ignores the router advertisements
		// (accept_ra 1) and the NAS would lose its own IPv6 route.
		for _, i := range ra {
			_, _ = sys.Run("sysctl", "-q", "-w", "net.ipv6.conf."+i+".accept_ra=2")
		}
		_, _ = sys.Run("sysctl", "-q", "-w", "net.ipv6.conf.all.forwarding=1")
	}
	_ = writeIfChanged(SysctlPath, SysctlConf(v6, ra), 0o644)
	_, _ = sys.Run("sysctl", "-q", "-w", "net.ipv4.ip_forward=1")
}

// PrepareIPv6 keeps the router advertisements of the NAS before Docker
// creates an IPv6 network (Docker turns IPv6 forwarding on).
func PrepareIPv6() {
	for _, i := range raIfaces() {
		_, _ = sys.Run("sysctl", "-q", "-w", "net.ipv6.conf."+i+".accept_ra=2")
	}
}

// SysctlConf is the sysctl file of the VPN (ra: interfaces whose router
// advertisements must survive IPv6 forwarding).
func SysctlConf(v6 bool, ra []string) string {
	var b strings.Builder
	b.WriteString("# Managed by StrataOS: the VPN routes packets.\nnet.ipv4.ip_forward = 1\n")
	if v6 {
		for _, i := range ra {
			fmt.Fprintf(&b, "net.ipv6.conf.%s.accept_ra = 2\n", i)
		}
		b.WriteString("net.ipv6.conf.all.forwarding = 1\n")
	}
	return b.String()
}

var ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// raIfaces are the interfaces that take router advertisements, "default"
// first (interfaces that appear later); not the virtual ones of Docker,
// the VPN or the loopback.
func raIfaces() []string {
	out := []string{"default"}
	dirs, _ := os.ReadDir("/proc/sys/net/ipv6/conf")
	for _, d := range dirs {
		n := d.Name()
		if n == "all" || n == "default" || n == "lo" || !ifaceRe.MatchString(n) || strings.HasPrefix(n, "veth") || strings.HasPrefix(n, "docker") ||
			strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "strata") {
			continue
		}
		if v, err := os.ReadFile(filepath.Join("/proc/sys/net/ipv6/conf", n, "accept_ra")); err == nil && strings.TrimSpace(string(v)) != "0" {
			out = append(out, n)
		}
	}
	return out
}

// HostIPv6 reports whether the NAS has IPv6.
func HostIPv6() bool {
	v, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")
	return err == nil && strings.TrimSpace(string(v)) == "0"
}

// AptUID is the account APT downloads with (-1: none).
func AptUID() int {
	u, err := user.Lookup("_apt")
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(u.Uid)
	if err != nil {
		return -1
	}
	return n
}

func applyFirewall(c store.VPN) error {
	// Before 0.6 the NAT table came from the PostUp of the server.
	if sys.Has("nft") {
		_, _ = sys.Run("nft", "delete", "table", "ip", "strata_vpn")
	}
	fw := RenderFirewall(c)
	if fw == "" {
		if sys.Has("nft") {
			_, _ = sys.Run("nft", "delete", "table", "inet", "strata_wg")
		}
		return nil
	}
	if !sys.Has("nft") {
		return fmt.Errorf("nftables is not installed")
	}
	_, err := sys.RunInput(fw, "nft", "-f", "-")
	return err
}

var dockerUserRe = regexp.MustCompile(`^-A DOCKER-USER -([io]) (strata[a-z0-9-]*) -j ACCEPT$`)

// dockerUser lets the VPN traffic through the FORWARD chain of Docker
// (policy drop). What a peer may reach is decided before, in strata_wg.
func dockerUser(wgs []string) {
	if !sys.Has("iptables") {
		return
	}
	cur, err := sys.Run("iptables", "-S", "DOCKER-USER")
	if err != nil {
		return
	}
	for _, l := range strings.Split(cur, "\n") {
		if m := dockerUserRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil && !slices.Contains(wgs, m[2]) {
			_, _ = sys.Run("iptables", "-D", "DOCKER-USER", "-"+m[1], m[2], "-j", "ACCEPT")
		}
	}
	for _, w := range wgs {
		for _, d := range []string{"-i", "-o"} {
			if _, err := sys.Run("iptables", "-C", "DOCKER-USER", d, w, "-j", "ACCEPT"); err != nil {
				_, _ = sys.Run("iptables", "-I", "DOCKER-USER", d, w, "-j", "ACCEPT")
			}
		}
	}
}
