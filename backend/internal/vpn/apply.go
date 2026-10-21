package vpn

import (
	"errors"
	"fmt"
	"os"
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
	setForward(NeedsForward(c))
	// The firewall first: restrictions hold as soon as a peer connects.
	errs := []error{applyFirewall(c), applyServer(c), applyLinks(c), applyClient(c.Client)}
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

func applyClient(cl store.VPNClient) error {
	path := filepath.Join(Dir, OutIface+".conf")
	if !ClientActive(cl) {
		if _, err := os.Stat(path); err == nil {
			_ = applyUnit(OutIface, "", true)
			_ = os.Remove(path)
		}
		clearRouting(true)
		removeDropIn()
		return nil
	}
	if err := applyUnit(OutIface, RenderClientTunnel(cl), true); err != nil {
		return err
	}
	// Answers from the Internet come back on strata-out while the main
	// table routes their source elsewhere: loose reverse path check.
	_, _ = sys.Run("sysctl", "-q", "-w", "net.ipv4.conf."+OutIface+".rp_filter=2")
	if cl.Scope == "apps" {
		if err := writeIfChanged(DockerDropInPath, DockerDropIn(cl.AppsSubnet), 0o644); err != nil {
			return err
		}
	} else {
		removeDropIn()
	}
	return SetupRouting(cl)
}

func removeDropIn() {
	if _, err := os.Stat(DockerDropInPath); err == nil {
		_ = os.Remove(DockerDropInPath)
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

func delRules(pref int) {
	for range 20 {
		if _, err := sys.Run("ip", "-4", "rule", "del", "pref", strconv.Itoa(pref)); err != nil {
			return
		}
	}
}

// clearRouting removes the policy routing of the tunnel (kill: also the
// kill switch rule).
func clearRouting(kill bool) {
	for _, p := range []int{prioMain, prioTunnelSrc, prioTunnel} {
		delRules(p)
	}
	if kill {
		delRules(prioKill)
	}
	_, _ = sys.Run("ip", "-4", "route", "flush", "table", strconv.Itoa(RouteTable))
}

// SetupRouting (re)creates the policy routing of the outgoing tunnel. The
// kill switch rule is never removed while the apps are routed.
func SetupRouting(cl store.VPNClient) error {
	clearRouting(false)
	if cl.Scope == "apps" {
		out, _ := sys.Run("ip", "-4", "rule", "show", "pref", strconv.Itoa(prioKill))
		if !strings.Contains(out, "from "+cl.AppsSubnet+" ") || !strings.Contains(out, "blackhole") {
			delRules(prioKill)
			if _, err := sys.Run("ip", KillRule(cl.AppsSubnet)...); err != nil {
				return err
			}
		}
	} else {
		delRules(prioKill)
	}
	var first error
	for _, cmd := range ClientRules(cl) {
		if _, err := sys.Run("ip", cmd...); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// RoutingOK reports whether the tunnel route and rules are in place (they
// go away with the interface).
func RoutingOK(cl store.VPNClient) bool {
	rt, _ := sys.Run("ip", "-4", "route", "show", "table", strconv.Itoa(RouteTable))
	rule, _ := sys.Run("ip", "-4", "rule", "show", "pref", strconv.Itoa(prioTunnel))
	return strings.Contains(rt, "dev "+OutIface) && strings.TrimSpace(rule) != ""
}

// KillSwitchResult is the outcome of TestKillSwitch.
type KillSwitchResult struct {
	TunnelRoute string `json:"tunnelRoute"` // route of the apps, tunnel up
	DownRoute   string `json:"downRoute"`   // and tunnel down
	Blocked     bool   `json:"blocked"`
}

// TestKillSwitch takes the tunnel down for a moment and checks the routed
// apps have no way out, then brings it back.
func TestKillSwitch(cl store.VPNClient) (KillSwitchResult, error) {
	var r KillSwitchResult
	if !ClientActive(cl) || cl.Scope != "apps" {
		return r, fmt.Errorf("the kill switch protects the apps routed through the tunnel")
	}
	if _, err := sys.Run("ip", "link", "show", AppsBridge); err != nil {
		return r, fmt.Errorf("the Docker network of the routed apps does not exist yet")
	}
	probe := func() (string, bool) {
		out, err := sys.Run("ip", "-4", "route", "get", "1.1.1.1", "from", Gateway(cl.AppsSubnet))
		if err != nil {
			return strings.TrimSpace(out + " " + err.Error()), false
		}
		return strings.TrimSpace(out), strings.Contains(out, " dev ") && !strings.Contains(out, "dev "+OutIface)
	}
	r.TunnelRoute, _ = probe()
	if _, err := sys.Run("ip", "link", "set", OutIface, "down"); err != nil {
		return r, err
	}
	var leak bool
	r.DownRoute, leak = probe()
	_, _ = sys.Run("ip", "link", "set", OutIface, "up")
	err := SetupRouting(cl)
	fw, _ := sys.Run("nft", "list", "chain", "inet", "strata_wg", "forward")
	r.Blocked = !leak && strings.Contains(fw, AppsBridge)
	return r, err
}

func setForward(need bool) {
	if !need {
		_ = os.Remove(SysctlPath)
		return
	}
	_ = writeIfChanged(SysctlPath, "# Managed by StrataOS: the VPN routes packets.\nnet.ipv4.ip_forward = 1\n", 0o644)
	_, _ = sys.Run("sysctl", "-q", "-w", "net.ipv4.ip_forward=1")
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
