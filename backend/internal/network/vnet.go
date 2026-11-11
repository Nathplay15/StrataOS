package network

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"strataos/internal/firewall"
)

// VNet is a virtual network for VMs and containers: a bridge of its own
// (vn0, vn1…), with no uplink (isolated, nat) or over a VLAN of a NIC or a
// VXLAN tunnel between NAS. It is rendered with the other systemd-networkd
// files, so it is applied on trial and rolled back like the rest.
type VNet struct {
	ID     string `json:"id"` // short name: iot, lab (also the Docker network strata-<id>)
	Label  string `json:"label,omitempty"`
	Kind   string `json:"kind"`   // nat | isolated | vlan | vxlan
	Bridge string `json:"bridge"` // vn0, vn1…
	// Use is the example the network was created from (home-automation,
	// sensitive, public): only shown in the interface.
	Use    string `json:"use,omitempty"`
	Parent string `json:"parent,omitempty"` // vlan: link carrying the VLAN
	VLAN   int    `json:"vlan,omitempty"`
	VNI    int    `json:"vni,omitempty"` // vxlan
	// Peers: addresses of the other NAS of a VXLAN network (unicast, each
	// broadcast is copied to every peer).
	Peers []string `json:"peers,omitempty"`
	Local string   `json:"local,omitempty"` // vxlan: local tunnel address (optional)
	MTU   int      `json:"mtu,omitempty"`   // 0: 1500, 1450 for VXLAN
	// Address: the NAS on this network (CIDR); the gateway of a NAT network.
	Address string `json:"address,omitempty"`
	// Subnet and Gateway describe the network when the NAS has no address
	// on it (Docker addressing, firewall).
	Subnet  string   `json:"subnet,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	DHCP    bool     `json:"dhcp,omitempty"` // DHCP server of the NAS (needs Address)
	DNS     []string `json:"dns,omitempty"`  // DNS servers given by DHCP
	// Isolate: guests reach neither the LAN nor the NAS (except DHCP, the DNS
	// servers above and NASPorts); a NAT network still reaches the Internet.
	Isolate  bool   `json:"isolate,omitempty"`
	NASPorts string `json:"nasPorts,omitempty"`
	LogDrops bool   `json:"logDrops,omitempty"`
	// Docker: a Docker network (macvlan) is bound to it, through a veth pair.
	Docker bool `json:"docker,omitempty"`

	// Shared VXLAN networks (0.12). Members: the other NAS this NAS sends
	// the network to, "fleet:<id>" (a NAS of its Multi-NAS console) or
	// "cluster:<id>" (a cluster member); they get the same VNI, MTU and
	// subnet, and the tunnel peers are kept in step.
	Members []string `json:"members,omitempty"`
	// Origin: name of the NAS that declared a network received here, which
	// manages it; OriginKey identifies its link to this NAS
	// ("fleet:<console access id>" | "cluster:<member id>").
	Origin    string `json:"origin,omitempty"`
	OriginKey string `json:"originKey,omitempty"`
	// Encrypt: the VXLAN tunnel runs inside a WireGuard mesh between the NAS
	// (link vnNwg, keys exchanged over the fleet or cluster link); WG is
	// its rendered side, filled by StrataOS.
	Encrypt bool    `json:"encrypt,omitempty"`
	WG      *VNetWG `json:"wg,omitempty"`
}

// VNetWG is the WireGuard link of an encrypted VXLAN network: this NAS's
// address in the mesh (IPv6 ULA /64, one per network) and the other NAS.
type VNetWG struct {
	Port    int      `json:"port"`
	Address string   `json:"address"`
	Peers   []WGPeer `json:"peers"`
}

// WGPeer is another NAS of the mesh.
type WGPeer struct {
	PublicKey string `json:"publicKey"`
	Endpoint  string `json:"endpoint"` // address:port on the LAN
	Address   string `json:"address"`  // in the mesh
}

// WGKeyDir holds the private keys of the WireGuard links of virtual
// networks (root:systemd-network 0640: networkd reads them).
var WGKeyDir = "/etc/strata/vnet-wg"

// WGKeyFile is the private key file of a network's WireGuard link.
func WGKeyFile(id string) string { return WGKeyDir + "/" + id + ".key" }

// WGLink names the WireGuard link of a network.
func (v VNet) WGLink() string { return v.Bridge + "wg" }

// WGMTU is the MTU of the WireGuard link; the VXLAN inside it (IPv6
// between mesh addresses: 70 bytes) leaves EncryptedMTU to the guests.
const (
	WGMTU        = 1420
	EncryptedMTU = WGMTU - 70
)

var (
	vnetIDRe     = regexp.MustCompile(`^[a-z][a-z0-9]{0,11}$`)
	vnetBridgeRe = regexp.MustCompile(`^vn[0-9]{1,3}$`)
	// vnetLinkRe: the links a virtual network creates (bridge, VXLAN, veth pair).
	vnetLinkRe = regexp.MustCompile(`^vn[0-9]{1,3}(vx|dk|dkp|wg)?$`)
	wgKeyRe    = regexp.MustCompile(`^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$`)
	memberRe   = regexp.MustCompile(`^(fleet|cluster):[A-Za-z0-9-]{1,40}$`)
	vnetKinds  = []string{"nat", "isolated", "vlan", "vxlan"}
)

// VXLANPort is the UDP port of VXLAN tunnels.
const VXLANPort = 4789

// VLANLink is the name of the VLAN link of a vlan network.
func (v VNet) VLANLink() string { return fmt.Sprintf("%s.%d", v.Parent, v.VLAN) }

// VXLANLink, DockerPort and DockerParent name the links of a network.
func (v VNet) VXLANLink() string    { return v.Bridge + "vx" }
func (v VNet) DockerPort() string   { return v.Bridge + "dk" }
func (v VNet) DockerParent() string { return v.Bridge + "dkp" }

// DockerName is the name of its Docker network.
func (v VNet) DockerName() string { return "strata-" + v.ID }

// EffectiveMTU is the MTU of the bridge and its ports.
func (v VNet) EffectiveMTU() int {
	switch {
	case v.MTU > 0:
		return v.MTU
	case v.Kind == "vxlan" && v.Encrypt:
		return EncryptedMTU
	case v.Kind == "vxlan":
		return 1450
	}
	return 1500
}

// Uplinks are the ports leading out of the NAS.
func (v VNet) Uplinks() []string {
	switch v.Kind {
	case "vlan":
		return []string{v.VLANLink()}
	case "vxlan":
		return []string{v.VXLANLink()}
	}
	return nil
}

// Prefix is the network's own subnet (from Address, else Subnet).
func (v VNet) Prefix() (netip.Prefix, bool) {
	s := v.Address
	if s == "" {
		s = v.Subnet
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	return p.Masked(), true
}

// GatewayAddr is the router guests use: the NAS on a NAT network, else Gateway.
func (v VNet) GatewayAddr() string {
	if v.Kind == "nat" {
		a, _, _ := strings.Cut(v.Address, "/")
		return a
	}
	return v.Gateway
}

// Pools returns the DHCP pool (offset and size in the subnet) and the
// addresses kept for Docker: DHCP takes the middle half, Docker the last quarter.
func (v VNet) Pools() (offset, size int, docker netip.Prefix) {
	p, ok := v.Prefix()
	if !ok || p.Bits() > 28 || !p.Addr().Is4() {
		return 0, 0, netip.Prefix{}
	}
	n := 1 << (32 - p.Bits())
	base := p.Addr().As4()
	u := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	u += uint32(n * 3 / 4)
	d := netip.AddrFrom4([4]byte{byte(u >> 24), byte(u >> 16), byte(u >> 8), byte(u)})
	return n / 4, n / 2, netip.PrefixFrom(d, p.Bits()+2)
}

// FW is the isolation of the network for the firewall.
func (v VNet) FW() firewall.VNet {
	f := firewall.VNet{ID: v.ID, Bridge: v.Bridge, Uplinks: v.Uplinks(), DNS: v.DNS, NASPorts: v.NASPorts, Log: v.LogDrops}
	if p, ok := v.Prefix(); ok {
		f.Subnets = []string{p.String()}
	}
	return f
}

func validVNet(v *VNet) error {
	if !vnetIDRe.MatchString(v.ID) {
		return fmt.Errorf("network names are lower case letters and digits, 12 at most, starting with a letter")
	}
	if !vnetBridgeRe.MatchString(v.Bridge) {
		return fmt.Errorf("%s: invalid bridge name", v.ID)
	}
	if !slices.Contains(vnetKinds, v.Kind) {
		return fmt.Errorf("%s: the type is nat, isolated, vlan or vxlan", v.ID)
	}
	v.Label = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, v.Label))
	if len(v.Label) > 80 {
		return fmt.Errorf("%s: label too long", v.ID)
	}
	if v.MTU != 0 && (v.MTU < 576 || v.MTU > 9000) {
		return fmt.Errorf("%s: MTU must be between 576 and 9000", v.ID)
	}
	var addr netip.Prefix
	if v.Address != "" {
		p, err := netip.ParsePrefix(v.Address)
		if err != nil || !p.Addr().Is4() || p.Bits() < 8 || p.Bits() > 30 || p.Addr() == p.Masked().Addr() {
			return fmt.Errorf("%s: invalid address of the NAS (e.g. 10.20.0.1/24)", v.ID)
		}
		addr = p
		v.Subnet = ""
	}
	if v.Subnet != "" {
		p, err := netip.ParsePrefix(v.Subnet)
		if err != nil || !p.Addr().Is4() || p.Bits() < 8 || p.Bits() > 30 {
			return fmt.Errorf("%s: invalid subnet (e.g. 10.20.0.0/24)", v.ID)
		}
		v.Subnet = p.Masked().String()
	}
	if v.Gateway != "" {
		g, err := netip.ParseAddr(v.Gateway)
		p, ok := v.Prefix()
		if err != nil || !ok || !p.Contains(g) {
			return fmt.Errorf("%s: the gateway must be an address of the network", v.ID)
		}
	}
	switch v.Kind {
	case "nat":
		if v.Address == "" {
			return fmt.Errorf("%s: a NAT network needs the address of the NAS (its gateway)", v.ID)
		}
		v.Gateway = ""
	case "vlan":
		if v.VLAN < 1 || v.VLAN > 4094 {
			return fmt.Errorf("%s: VLAN ids go from 1 to 4094", v.ID)
		}
		if !ifNameRe.MatchString(v.Parent) || len(v.VLANLink()) > 15 {
			return fmt.Errorf("%s: choose the interface carrying the VLAN", v.ID)
		}
	case "vxlan":
		if v.VNI < 1 || v.VNI > 16777215 {
			return fmt.Errorf("%s: the VXLAN id (VNI) goes from 1 to 16777215", v.ID)
		}
		if (len(v.Peers) == 0 && len(v.Members) == 0 && v.OriginKey == "") || len(v.Peers) > 32 {
			return fmt.Errorf("%s: give the addresses of the other NAS (32 at most)", v.ID)
		}
		for i, p := range v.Peers {
			a, err := netip.ParseAddr(strings.TrimSpace(p))
			if err != nil || a.Zone() != "" || a.IsLoopback() || a.IsMulticast() || a.IsUnspecified() {
				return fmt.Errorf("%s: invalid peer address %q", v.ID, p)
			}
			v.Peers[i] = a.String()
		}
		if len(slices.Compact(slices.Sorted(slices.Values(v.Peers)))) != len(v.Peers) {
			return fmt.Errorf("%s: a peer is listed twice", v.ID)
		}
		if v.Local != "" {
			if _, err := netip.ParseAddr(v.Local); err != nil {
				return fmt.Errorf("%s: invalid local address", v.ID)
			}
		}
		if err := validShare(v); err != nil {
			return err
		}
	}
	if v.Kind != "vlan" {
		v.Parent, v.VLAN = "", 0
	}
	if v.Kind != "vxlan" {
		v.VNI, v.Peers, v.Local = 0, nil, ""
		v.Members, v.Origin, v.OriginKey, v.Encrypt, v.WG = nil, "", "", false, nil
	}
	if v.DHCP {
		if v.Address == "" || addr.Bits() > 28 {
			return fmt.Errorf("%s: the DHCP server needs an address of the NAS on a /28 network or larger", v.ID)
		}
	}
	if len(v.DNS) > 3 {
		return fmt.Errorf("%s: at most 3 DNS servers", v.ID)
	}
	for i, d := range v.DNS {
		a, err := netip.ParseAddr(strings.TrimSpace(d))
		if err != nil || !a.Is4() {
			return fmt.Errorf("%s: invalid DNS server %q", v.ID, d)
		}
		v.DNS[i] = a.String()
	}
	if err := firewall.ValidPorts(v.NASPorts); err != nil {
		return fmt.Errorf("%s: %v", v.ID, err)
	}
	if v.Docker {
		p, ok := v.Prefix()
		if !ok || p.Bits() > 28 {
			return fmt.Errorf("%s: Docker needs the network's addresses (NAS address or subnet, /28 or larger)", v.ID)
		}
		if _, _, d := v.Pools(); addr.IsValid() && d.Contains(addr.Addr()) {
			return fmt.Errorf("%s: the address of the NAS is in the last quarter of the network, kept for Docker", v.ID)
		}
	}
	return nil
}

// validShare checks the sharing fields of a VXLAN network.
func validShare(v *VNet) error {
	if len(v.Members) > 32 {
		return fmt.Errorf("%s: 32 NAS at most", v.ID)
	}
	for _, m := range v.Members {
		if !memberRe.MatchString(m) {
			return fmt.Errorf("%s: invalid NAS %q", v.ID, m)
		}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(v.Members)))) != len(v.Members) {
		return fmt.Errorf("%s: a NAS is listed twice", v.ID)
	}
	if v.OriginKey != "" && (!memberRe.MatchString(v.OriginKey) || len(v.Members) > 0) {
		return fmt.Errorf("%s: a network received from another NAS is shared by that NAS only", v.ID)
	}
	v.Origin = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, v.Origin))
	if len(v.Origin) > 64 {
		return fmt.Errorf("%s: invalid origin", v.ID)
	}
	if !v.Encrypt {
		v.WG = nil
		return nil
	}
	if v.MTU > EncryptedMTU {
		return fmt.Errorf("%s: an encrypted network has an MTU of %d at most", v.ID, EncryptedMTU)
	}
	if v.WG == nil {
		return nil // keys not exchanged yet: the tunnel waits for them
	}
	w := v.WG
	if w.Port < 1024 || w.Port > 65535 {
		return fmt.Errorf("%s: invalid WireGuard port", v.ID)
	}
	if p, err := netip.ParsePrefix(w.Address); err != nil || !p.Addr().Is6() || p.Bits() != 64 || !p.Addr().IsPrivate() {
		return fmt.Errorf("%s: invalid mesh address", v.ID)
	}
	if len(w.Peers) > 32 {
		return fmt.Errorf("%s: too many WireGuard peers", v.ID)
	}
	for _, p := range w.Peers {
		if !wgKeyRe.MatchString(p.PublicKey) {
			return fmt.Errorf("%s: invalid WireGuard key", v.ID)
		}
		if ap, err := netip.ParseAddrPort(p.Endpoint); err != nil || ap.Port() == 0 || ap.Addr().IsLoopback() || ap.Addr().IsUnspecified() {
			return fmt.Errorf("%s: invalid WireGuard endpoint %q", v.ID, p.Endpoint)
		}
		if a, err := netip.ParseAddr(p.Address); err != nil || !a.Is6() {
			return fmt.Errorf("%s: invalid mesh address %q", v.ID, p.Address)
		}
	}
	return nil
}

// validVNets checks the virtual networks of a configuration; used holds the
// link names claimed so far (see Validate).
func validVNets(c *Config, used map[string]string) error {
	ids := map[string]bool{}
	var nets []netip.Prefix
	for i := range c.Virtual {
		v := &c.Virtual[i]
		if err := validVNet(v); err != nil {
			return err
		}
		if ids[v.ID] {
			return fmt.Errorf("two virtual networks are named %s", v.ID)
		}
		ids[v.ID] = true
		names := []string{v.Bridge}
		if v.Kind == "vxlan" {
			names = append(names, v.VXLANLink())
			if v.Encrypt {
				names = append(names, v.WGLink())
			}
		}
		if v.Docker {
			names = append(names, v.DockerPort(), v.DockerParent())
		}
		if v.Kind == "vlan" {
			switch used[v.Parent] {
			case "interface", "bond", "bridge":
			case "":
				return fmt.Errorf("%s: let StrataOS manage %s first (Network → Interfaces)", v.ID, v.Parent)
			default:
				return fmt.Errorf("%s: %s is a member of %s", v.ID, v.Parent, used[v.Parent])
			}
			names = append(names, v.VLANLink())
		}
		for _, n := range names {
			if prev, ok := used[n]; ok {
				return fmt.Errorf("%s is configured twice (%s and %s)", n, prev, v.ID)
			}
			used[n] = "vnet"
		}
		if p, ok := v.Prefix(); ok {
			for _, q := range nets {
				if q.Overlaps(p) {
					return fmt.Errorf("%s: its addresses overlap another virtual network", v.ID)
				}
			}
			nets = append(nets, p)
		}
	}
	return nil
}

// NextBridge returns the first free vnN name.
func NextBridge(c Config, taken []string) string {
	for n := 0; n < 1000; n++ {
		name := fmt.Sprintf("vn%d", n)
		if !slices.ContainsFunc(c.Virtual, func(v VNet) bool { return v.Bridge == name }) && !slices.Contains(taken, name) {
			return name
		}
	}
	return ""
}

// CheckVNetAddrs refuses a network whose addresses overlap those of the NAS
// elsewhere (addrs: CIDR addresses of the other links).
func CheckVNetAddrs(v VNet, addrs []string) error {
	if v.Kind == "vxlan" {
		for _, peer := range v.Peers {
			for _, a := range addrs {
				if q, err := netip.ParsePrefix(a); err == nil && q.Addr().String() == strings.TrimSpace(peer) {
					return fmt.Errorf("%s is an address of this NAS: list the other NAS", peer)
				}
			}
		}
	}
	p, ok := v.Prefix()
	if !ok {
		return nil
	}
	for _, a := range addrs {
		q, err := netip.ParsePrefix(a)
		if err != nil || !q.Addr().Is4() {
			continue
		}
		if q.Masked().Overlaps(p) {
			return fmt.Errorf("%s overlaps a network the NAS is already on (%s)", p, a)
		}
	}
	return nil
}

func header(b *strings.Builder, name string) {
	fmt.Fprintf(b, "# Managed by StrataOS (virtual network)\n[Match]\nName=%s\n\n[Link]\nRequiredForOnline=no\n", name)
}

// portFile is the .network of a link enslaved to a virtual network's bridge.
func portFile(name, bridge string, mtu int) string {
	var b strings.Builder
	header(&b, name)
	if mtu != 1500 {
		fmt.Fprintf(&b, "MTUBytes=%d\n", mtu)
	}
	fmt.Fprintf(&b, "\n[Network]\nBridge=%s\nConfigureWithoutCarrier=yes\nLinkLocalAddressing=no\nIPv6AcceptRA=no\n", bridge)
	return b.String()
}

// renderVNet adds the files of a virtual network (the VLAN link itself is
// declared on its parent by Render).
func renderVNet(files map[string]string, v VNet) {
	mtu := v.EffectiveMTU()
	mtuLine := ""
	if mtu != 1500 {
		mtuLine = fmt.Sprintf("MTUBytes=%d\n", mtu)
	}
	files[filePrefix+v.Bridge+".netdev"] = fmt.Sprintf("# Managed by StrataOS (virtual network %s)\n[NetDev]\nName=%s\nKind=bridge\n%s\n[Bridge]\nSTP=no\n", v.ID, v.Bridge, mtuLine)

	var b strings.Builder
	header(&b, v.Bridge)
	b.WriteString(mtuLine)
	b.WriteString("\n[Network]\nConfigureWithoutCarrier=yes\nLinkLocalAddressing=no\nIPv6AcceptRA=no\n")
	if v.Address != "" {
		fmt.Fprintf(&b, "Address=%s\n", v.Address)
		if v.Kind == "nat" {
			b.WriteString("IPMasquerade=ipv4\nIPv4Forwarding=yes\n")
		}
		if v.DHCP {
			b.WriteString("DHCPServer=yes\n")
		}
	}
	if v.DHCP && v.Address != "" {
		off, size, _ := v.Pools()
		fmt.Fprintf(&b, "\n[DHCPServer]\nPoolOffset=%d\nPoolSize=%d\n", off, size)
		switch gw := v.GatewayAddr(); {
		case v.Kind == "nat":
			b.WriteString("EmitRouter=yes\n")
		case gw != "":
			fmt.Fprintf(&b, "EmitRouter=yes\nRouter=%s\n", gw)
		default:
			b.WriteString("EmitRouter=no\n")
		}
		if len(v.DNS) > 0 {
			fmt.Fprintf(&b, "EmitDNS=yes\nDNS=%s\n", strings.Join(v.DNS, " "))
		} else {
			b.WriteString("EmitDNS=no\n")
		}
	}
	files[filePrefix+v.Bridge+".network"] = b.String()

	switch v.Kind {
	case "vlan":
		l := v.VLANLink()
		files[filePrefix+l+".netdev"] = fmt.Sprintf("# Managed by StrataOS (virtual network %s)\n[NetDev]\nName=%s\nKind=vlan\n%s\n[VLAN]\nId=%d\n", v.ID, l, mtuLine, v.VLAN)
		files[filePrefix+l+".network"] = portFile(l, v.Bridge, mtu)
	case "vxlan":
		l := v.VXLANLink()
		var nd strings.Builder
		fmt.Fprintf(&nd, "# Managed by StrataOS (virtual network %s)\n[NetDev]\nName=%s\nKind=vxlan\nMTUBytes=%d\n\n[VXLAN]\nVNI=%d\nDestinationPort=%d\nIndependent=yes\nMacLearning=yes\n", v.ID, l, mtu, v.VNI, VXLANPort)
		if v.Local != "" {
			fmt.Fprintf(&nd, "Local=%s\n", v.Local)
		}
		files[filePrefix+l+".netdev"] = nd.String()
		port := portFile(l, v.Bridge, mtu)
		if !strings.Contains(port, "MTUBytes") {
			port = strings.Replace(port, "RequiredForOnline=no\n", fmt.Sprintf("RequiredForOnline=no\nMTUBytes=%d\n", mtu), 1)
		}
		// Head-end replication: broadcasts and unknown destinations go to every peer.
		for _, p := range v.Peers {
			port += fmt.Sprintf("\n[BridgeFDB]\nMACAddress=00:00:00:00:00:00\nDestination=%s\n", p)
		}
		files[filePrefix+l+".network"] = port
		if v.Encrypt && v.WG != nil {
			renderWG(files, v)
		}
	}
	if v.Docker {
		files[filePrefix+v.DockerPort()+".netdev"] = fmt.Sprintf("# Managed by StrataOS (virtual network %s, Docker)\n[NetDev]\nName=%s\nKind=veth\n%s\n[Peer]\nName=%s\n", v.ID, v.DockerPort(), mtuLine, v.DockerParent())
		files[filePrefix+v.DockerPort()+".network"] = portFile(v.DockerPort(), v.Bridge, mtu)
		var p strings.Builder
		header(&p, v.DockerParent())
		p.WriteString(mtuLine)
		p.WriteString("\n[Network]\nConfigureWithoutCarrier=yes\nLinkLocalAddressing=no\nIPv6AcceptRA=no\n")
		files[filePrefix+v.DockerParent()+".network"] = p.String()
	}
}

// renderWG writes the WireGuard link carrying an encrypted VXLAN network.
func renderWG(files map[string]string, v VNet) {
	w := v.WG
	var nd strings.Builder
	fmt.Fprintf(&nd, "# Managed by StrataOS (virtual network %s, encryption)\n[NetDev]\nName=%s\nKind=wireguard\nMTUBytes=%d\n\n[WireGuard]\nPrivateKeyFile=%s\nListenPort=%d\n",
		v.ID, v.WGLink(), WGMTU, WGKeyFile(v.ID), w.Port)
	for _, p := range w.Peers {
		fmt.Fprintf(&nd, "\n[WireGuardPeer]\nPublicKey=%s\nEndpoint=%s\nAllowedIPs=%s/128\nPersistentKeepalive=25\n", p.PublicKey, p.Endpoint, p.Address)
	}
	files[filePrefix+v.WGLink()+".netdev"] = nd.String()
	var n strings.Builder
	header(&n, v.WGLink())
	fmt.Fprintf(&n, "\n[Network]\nAddress=%s\nLinkLocalAddressing=no\nIPv6AcceptRA=no\nConfigureWithoutCarrier=yes\n", w.Address)
	files[filePrefix+v.WGLink()+".network"] = n.String()
}

// vnetLinks lists the links of a virtual network (for activation).
func vnetLinks(v VNet) []string {
	out := []string{v.Bridge}
	switch v.Kind {
	case "vlan":
		out = append(out, v.VLANLink(), v.Parent)
	case "vxlan":
		if v.Encrypt {
			out = append(out, v.WGLink())
		}
		out = append(out, v.VXLANLink())
	}
	if v.Docker {
		out = append(out, v.DockerPort(), v.DockerParent())
	}
	return out
}

// VNetByBridge returns the virtual network of a bridge.
func (c Config) VNetByBridge(bridge string) (VNet, bool) {
	for _, v := range c.Virtual {
		if v.Bridge == bridge {
			return v, true
		}
	}
	return VNet{}, false
}

// DockerForwardRules are the iptables rules (DOCKER-USER chain) that let the
// guests' traffic through Docker's FORWARD chain, whose policy is drop (with
// br_netfilter even bridged frames go through it). nat lists the bridges
// whose guests are routed by the NAS (NAT virtual networks, lxcbr0); the
// others only switch frames between their own ports. What a guest may reach
// is decided by the firewall's own tables.
func DockerForwardRules(c Config, nat []string) [][]string {
	var out [][]string
	tag := []string{"-m", "comment", "--comment", "strata-vnet", "-j", "ACCEPT"}
	add := func(br string, routed bool) {
		out = append(out, append([]string{"-i", br, "-o", br}, tag...))
		if routed {
			out = append(out, append([]string{"-i", br}, tag...))
			out = append(out, append([]string{"-o", br, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED"}, tag...))
		}
	}
	for _, v := range c.Virtual {
		add(v.Bridge, v.Kind == "nat")
	}
	for _, b := range nat {
		if ifNameRe.MatchString(b) {
			add(b, true)
		}
	}
	return out
}
