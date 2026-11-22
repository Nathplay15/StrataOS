package energy

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"strataos/internal/sys"
)

func readSys(p string) (string, error) {
	b, err := os.ReadFile(filepath.Join(SysRoot, p))
	return strings.TrimSpace(string(b)), err
}

// LinkDir holds the systemd .link files (udev applies them to network cards
// when they appear, whatever manages the network).
var LinkDir = "/etc/systemd/network"

const linkPrefix = "50-strata-wol-"

// LinkFile is the .link file that keeps Wake-on-LAN on for a card across
// restarts. A .link file replaces 99-default.link for the cards it matches,
// so it repeats Debian's naming policies: the card keeps its name.
func LinkFile(iface, mac string) string {
	return fmt.Sprintf(`# Written by StrataOS (Power page): Wake-on-LAN of %s. Do not edit.
[Match]
PermanentMACAddress=%s

[Link]
NamePolicy=keep kernel database onboard slot path
AlternativeNamesPolicy=database onboard slot path
MACAddressPolicy=persistent
Name=%s
WakeOnLan=magic
`, iface, mac, iface)
}

// PersistWoL writes a .link file for each card of on (name → permanent MAC
// address) and removes those of the other cards.
func PersistWoL(on map[string]string) error {
	if err := os.MkdirAll(LinkDir, 0o755); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(LinkDir, linkPrefix+"*.link"))
	keep := map[string]bool{}
	for iface, mac := range on {
		if !ifaceRe.MatchString(iface) {
			return fmt.Errorf("invalid interface")
		}
		hw, err := ParseMAC(mac)
		if err != nil {
			return err
		}
		p := filepath.Join(LinkDir, linkPrefix+iface+".link")
		keep[p] = true
		if err := os.WriteFile(p, []byte(LinkFile(iface, hw.String())), 0o644); err != nil {
			return err
		}
	}
	for _, p := range old {
		if !keep[p] {
			_ = os.Remove(p)
		}
	}
	return nil
}

// PersistedWoL lists the cards with a .link file.
func PersistedWoL() []string {
	files, _ := filepath.Glob(filepath.Join(LinkDir, linkPrefix+"*.link"))
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), linkPrefix), ".link"))
	}
	return out
}

// PermanentMAC is the factory address of a card (its current one when the
// driver does not report it): Wake-on-LAN listens for that one.
func PermanentMAC(iface string) string {
	if !ifaceRe.MatchString(iface) {
		return ""
	}
	if out, err := runEthtool("-P", iface); err == nil {
		if _, v, ok := strings.Cut(out, "address:"); ok {
			if hw, err := ParseMAC(v); err == nil && hw.String() != "00:00:00:00:00:00" {
				return hw.String()
			}
		}
	}
	b, _ := readSys("sys/class/net/" + iface + "/address")
	return b
}

// Broadcasts lists the broadcast addresses of the IPv4 networks given, each
// once: a magic packet is sent on every local network, not only through the
// interface of the default route.
func Broadcasts(nets []*net.IPNet) []net.IP {
	var out []net.IP
	seen := map[string]bool{}
	for _, n := range nets {
		ip4 := n.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || len(n.Mask) != net.IPv4len {
			continue
		}
		ones, _ := n.Mask.Size()
		if ones >= 31 {
			continue
		}
		b := make(net.IP, 4)
		for i := range ip4 {
			b[i] = ip4[i] | ^n.Mask[i]
		}
		if !seen[b.String()] {
			seen[b.String()] = true
			out = append(out, b)
		}
	}
	return out
}

func localNets() []*net.IPNet {
	var out []*net.IPNet
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

// WakeTargets are where Wake sends its packets.
func WakeTargets() []net.IP {
	return append([]net.IP{net.IPv4bcast}, Broadcasts(localNets())...)
}

func runEthtool(args ...string) (string, error) { return sys.Run("ethtool", args...) }
