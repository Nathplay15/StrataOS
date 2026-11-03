package network

import (
	"fmt"
	"net"
	"slices"
)

// Observed is the live IP configuration of a link, as the kernel reports it.
// It is used when StrataOS does not manage the link yet (configured by the
// Debian installer through ifupdown or networkd).
type Observed struct {
	Addresses []string // global addresses, CIDR
	Dynamic   bool     // an IPv4 address was obtained by DHCP
	Gateway   string   // default gateway reached through this link
	DNS       []string
	MTU       int
}

// PlanBridge returns cfg with a new bridge over link that takes over the
// link's IP configuration, so the NAS keeps its address: the configuration
// StrataOS already manages for link, else the observed one (DHCP stays DHCP,
// the bridge reusing the link's MAC address; static addresses are copied).
// taken lists the link names present on the machine. VLANs on link move to
// the bridge.
func PlanBridge(cfg Config, link string, obs Observed, taken []string) (Config, string, error) {
	out := Config{
		Interfaces: slices.Clone(cfg.Interfaces),
		Bonds:      slices.Clone(cfg.Bonds),
		Bridges:    slices.Clone(cfg.Bridges),
		VLANs:      slices.Clone(cfg.VLANs),
	}
	for _, br := range cfg.Bridges {
		if br.Name == link {
			return cfg, "", fmt.Errorf("%s is already a bridge", link)
		}
		if slices.Contains(br.Members, link) {
			return cfg, "", fmt.Errorf("%s is already a port of bridge %s", link, br.Name)
		}
	}
	for _, b := range cfg.Bonds {
		if slices.Contains(b.Members, link) {
			return cfg, "", fmt.Errorf("%s is a member of %s: create the bridge on %s", link, b.Name, b.Name)
		}
	}
	name := ""
	for n := 0; n < 1000 && name == ""; n++ {
		c := fmt.Sprintf("br%d", n)
		if !slices.ContainsFunc(cfg.Bridges, func(b Bridge) bool { return b.Name == c }) && !slices.Contains(taken, c) {
			name = c
		}
	}
	if name == "" {
		return cfg, "", fmt.Errorf("no free bridge name")
	}

	var ip IP
	managed := false
	if i := slices.IndexFunc(out.Interfaces, func(x Iface) bool { return x.Name == link }); i >= 0 {
		ip, managed = out.Interfaces[i].IP, true
		out.Interfaces = slices.Delete(out.Interfaces, i, i+1)
	} else if i := slices.IndexFunc(out.Bonds, func(x Bond) bool { return x.Name == link }); i >= 0 {
		ip, managed = out.Bonds[i].IP, true
		// The bond becomes a bridge port: no address of its own.
		out.Bonds[i].IP = IP{Mode: "none", MTU: ip.MTU}
	}
	if !managed {
		var err error
		if ip, err = observedIP(link, obs); err != nil {
			return cfg, "", err
		}
	}
	if ip.Mode == "none" {
		return cfg, "", fmt.Errorf("%s has no IP address: the NAS would not be reachable through the bridge", link)
	}
	out.Bridges = append(out.Bridges, Bridge{Name: name, Members: []string{link}, IP: ip})
	for i := range out.VLANs {
		if out.VLANs[i].Parent == link {
			out.VLANs[i].Parent = name
		}
	}
	return out, name, nil
}

// observedIP turns what the kernel reports into a configuration that gives
// the bridge the same address.
func observedIP(link string, obs Observed) (IP, error) {
	ip := IP{}
	if obs.MTU > 0 && obs.MTU != 1500 {
		ip.MTU = obs.MTU
	}
	if obs.Dynamic {
		ip.Mode = "dhcp"
		return ip, nil
	}
	for _, a := range obs.Addresses {
		if p, _, err := net.ParseCIDR(a); err == nil && !p.IsLinkLocalUnicast() && !p.IsLoopback() {
			ip.Addresses = append(ip.Addresses, a)
		}
	}
	if len(ip.Addresses) == 0 {
		return ip, fmt.Errorf("%s has no IP address: the NAS would not be reachable through the bridge", link)
	}
	ip.Mode = "static"
	if g := net.ParseIP(obs.Gateway); g != nil {
		ip.Gateway = obs.Gateway
	}
	for _, d := range obs.DNS {
		// 127.0.0.53 is systemd-resolved's stub, not a real server.
		if p := net.ParseIP(d); p != nil && !p.IsLoopback() {
			ip.DNS = append(ip.DNS, d)
		}
	}
	return ip, nil
}
