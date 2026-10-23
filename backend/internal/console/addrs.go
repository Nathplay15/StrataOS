package console

import (
	"net"
	"os"
	"strings"
)

// Interfaces that never carry the LAN address of the NAS: containers, VM NAT
// bridges, VPN tunnels.
var notLAN = []string{"lo", "docker", "br-", "veth", "virbr", "lxcbr", "vnet", "tap", "tun", "wg", "tailscale", "zt", "cni", "flannel", "kube"}

func lanIface(name string) bool {
	for _, p := range notLAN {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

type ifaceAddrs struct {
	Name string
	IPv4 []string
}

// orderAddrs lists the IPv4 addresses of the LAN interfaces, those of the
// interface holding the default route first (its first one is the address of
// the QR code).
func orderAddrs(ifs []ifaceAddrs, def string) []string {
	var first, rest []string
	for _, i := range ifs {
		if !lanIface(i.Name) {
			continue
		}
		if i.Name == def {
			first = append(first, i.IPv4...)
		} else {
			rest = append(rest, i.IPv4...)
		}
	}
	return append(first, rest...)
}

// defaultIface reads the interface of the IPv4 default route from the
// contents of /proc/net/route.
func defaultIface(route string) string {
	for _, l := range strings.Split(route, "\n") {
		f := strings.Fields(l)
		if len(f) >= 8 && f[1] == "00000000" && f[7] == "00000000" {
			return f[0]
		}
	}
	return ""
}

// LANAddresses reads the addresses from the kernel: the screen stays useful
// while the daemon is down or starting.
func LANAddresses() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var list []ifaceAddrs
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		ia := ifaceAddrs{Name: i.Name}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip := n.IP.To4(); ip != nil && !ip.IsLinkLocalUnicast() {
					ia.IPv4 = append(ia.IPv4, ip.String())
				}
			}
		}
		list = append(list, ia)
	}
	route, _ := os.ReadFile("/proc/net/route")
	return orderAddrs(list, defaultIface(string(route)))
}
