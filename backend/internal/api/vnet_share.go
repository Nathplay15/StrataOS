package api

// Shared VXLAN networks (0.12): a VXLAN virtual network declared on one NAS
// with Members is sent to those NAS (Multi-NAS console members, with their
// "manage shared virtual networks" permission, or cluster members) with the
// same VNI, MTU and subnet; every NAS lists the others as tunnel peers. With
// Encrypt, the tunnel runs inside a WireGuard mesh: each NAS keeps its own
// private key and gives its public key over the fleet or cluster link.
// vnetShareLoop keeps the members in step (every 2 minutes and after a
// change) and sends removals. A network received from another NAS is
// changed there only; the receiving NAS applies it at once (only virtual
// network links change, nothing it reaches the NAS through).

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"

	"strataos/internal/network"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/vpn"
)

// vnetSpec is what the declaring NAS sends to a member.
type vnetSpec struct {
	ID       string          `json:"id"`
	Label    string          `json:"label,omitempty"`
	VNI      int             `json:"vni"`
	MTU      int             `json:"mtu,omitempty"`
	Subnet   string          `json:"subnet,omitempty"`
	Gateway  string          `json:"gateway,omitempty"`
	DNS      []string        `json:"dns,omitempty"`
	Isolate  bool            `json:"isolate,omitempty"`
	NASPorts string          `json:"nasPorts,omitempty"`
	LogDrops bool            `json:"logDrops,omitempty"`
	Encrypt  bool            `json:"encrypt,omitempty"`
	Peers    []string        `json:"peers"`
	Local    string          `json:"local,omitempty"`
	WG       *network.VNetWG `json:"wg,omitempty"`
	Origin   string          `json:"origin"`         // name of the declaring NAS
	From     string          `json:"from,omitempty"` // its cluster member id (cluster calls)
}

// vnetParticipant is one NAS of a shared network, in the declaring NAS's view.
type vnetParticipant struct {
	Member string // "" for the declaring NAS
	Addr   string // on the LAN
	PubKey string // WireGuard (encrypted)
	Port   int
}

// vnetMesh is the mesh prefix of an encrypted network: an IPv6 ULA /64
// derived from the declaring NAS and the network, so that it never changes.
func vnetMesh(seed, id string) netip.Prefix {
	h := sha256.Sum256([]byte("strata-vnet|" + seed + "|" + id))
	var a [16]byte
	a[0] = 0xfd
	copy(a[1:8], h[:7])
	return netip.PrefixFrom(netip.AddrFrom16(a), 64)
}

func meshAddr(p netip.Prefix, i int) netip.Addr {
	a := p.Addr().As16()
	a[14], a[15] = byte(i>>8), byte(i)
	return netip.AddrFrom16(a)
}

// vnetPlan computes the tunnel side of every participant of a shared
// network (index 0 is the declaring NAS): the peers (LAN addresses, or
// mesh addresses when encrypted), the local mesh address and WireGuard
// peers. Encrypted participants without a key yet are left out.
func vnetPlan(v network.VNet, mesh netip.Prefix, parts []vnetParticipant) map[int]vnetSpec {
	in := []int{}
	for i, p := range parts {
		if !v.Encrypt || (p.PubKey != "" && p.Port > 0) {
			in = append(in, i)
		}
	}
	out := map[int]vnetSpec{}
	for _, i := range in {
		sp := vnetSpec{Peers: []string{}}
		if v.Encrypt {
			self := meshAddr(mesh, i+1)
			sp.Local = self.String()
			sp.WG = &network.VNetWG{Port: parts[i].Port, Address: netip.PrefixFrom(self, 64).String(), Peers: []network.WGPeer{}}
		}
		for _, j := range in {
			if j == i {
				continue
			}
			if v.Encrypt {
				m := meshAddr(mesh, j+1).String()
				sp.Peers = append(sp.Peers, m)
				sp.WG.Peers = append(sp.WG.Peers, network.WGPeer{PublicKey: parts[j].PubKey, Endpoint: netip.AddrPortFrom(netip.MustParseAddr(parts[j].Addr), uint16(parts[j].Port)).String(), Address: m})
			} else {
				sp.Peers = append(sp.Peers, parts[j].Addr)
			}
		}
		out[i] = sp
	}
	return out
}

// receivedVNet builds the network a member applies from a spec; cur is the
// member's current copy, if any (it keeps its bridge).
func receivedVNet(sp vnetSpec, originKey string, cur *network.VNet, bridge string) network.VNet {
	v := network.VNet{ID: sp.ID, Label: sp.Label, Kind: "vxlan", Bridge: bridge, VNI: sp.VNI, MTU: sp.MTU, Subnet: sp.Subnet, Gateway: sp.Gateway,
		DNS: sp.DNS, Isolate: sp.Isolate, NASPorts: sp.NASPorts, LogDrops: sp.LogDrops, Encrypt: sp.Encrypt, Peers: sp.Peers, Local: sp.Local, WG: sp.WG,
		Origin: sp.Origin, OriginKey: originKey}
	if cur != nil {
		v.Bridge = cur.Bridge
	}
	return v
}

// sameButVirtual tells whether two configurations differ only in their
// virtual networks (what a remote NAS may change without a trial).
func sameButVirtual(a, b network.Config) bool {
	a.Virtual, b.Virtual = nil, nil
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

var vnetApplyMu sync.Mutex

// applyVNetAuto applies a change of virtual networks made by StrataOS
// itself (shared networks): through the usual trial, confirmed once the
// links settled.
func (s *Server) applyVNetAuto(next network.Config, what string) error {
	vnetApplyMu.Lock()
	defer vnetApplyMu.Unlock()
	prev, physical := s.netConfigAndLinks()
	if !sameButVirtual(prev, next) {
		return errors.New("only virtual networks may change")
	}
	if network.Manager() == "NetworkManager" {
		return errors.New("NetworkManager manages this NAS: virtual networks need systemd-networkd")
	}
	if err := network.Validate(&next, physical); err != nil {
		return err
	}
	p, err := network.Apply(next, prev, 2*time.Minute)
	if err != nil {
		return err
	}
	for i := 0; ; i++ {
		cfg, err := network.Confirm(p.Token)
		if errors.Is(err, network.ErrNotReady) && i < 120 {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err != nil {
			return err
		}
		_ = s.store.Update(func(st *store.State) error {
			st.Network = cfg
			return nil
		})
		break
	}
	s.store.Log("info", "network", "", what)
	fwKick()
	return nil
}

// wgKey reads (or creates) the private key of a network's WireGuard link
// and returns its public key.
func wgKey(id string) (string, error) {
	f := network.WGKeyFile(id)
	data, err := os.ReadFile(f)
	if err != nil {
		priv, pub, err := vpn.GenKey()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(network.WGKeyDir, 0o750); err != nil {
			return "", err
		}
		if err := sys.WriteFileAtomic(f, []byte(priv+"\n"), 0o640); err != nil {
			return "", err
		}
		// systemd-networkd reads PrivateKeyFile as its own user.
		_, _ = sys.Run("chgrp", "systemd-network", network.WGKeyDir, f)
		return pub, nil
	}
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(k) != 32 {
		return "", errors.New("invalid WireGuard key file " + f)
	}
	p, err := curve25519.X25519(k, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(p), nil
}

// wgPort picks the WireGuard port of a network: the one it has, else the
// first free one of 51900–51999.
func wgPort(c network.Config, id string) int {
	used := map[int]bool{}
	for _, v := range c.Virtual {
		if v.WG != nil {
			if v.ID == id {
				return v.WG.Port
			}
			used[v.WG.Port] = true
		}
	}
	for p := 51900; p < 52000; p++ {
		if !used[p] {
			return p
		}
	}
	return 0
}

// ---- Sending side ----

type vnetMemberStatus struct {
	Member string    `json:"member"`
	Name   string    `json:"name"`
	OK     bool      `json:"ok"`
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
}

var vnetShare = struct {
	sync.Mutex
	status map[string]map[string]vnetMemberStatus // vnet → member → status
	kick   chan struct{}
}{kick: make(chan struct{}, 1)}

func vnetShareKick() {
	select {
	case vnetShare.kick <- struct{}{}:
	default:
	}
}

func vnetShareStatus(id string) []vnetMemberStatus {
	vnetShare.Lock()
	defer vnetShare.Unlock()
	out := []vnetMemberStatus{}
	for _, st := range vnetShare.status[id] {
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b vnetMemberStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func setVNetStatus(id, member, name string, err error) {
	vnetShare.Lock()
	defer vnetShare.Unlock()
	if vnetShare.status == nil {
		vnetShare.status = map[string]map[string]vnetMemberStatus{}
	}
	if vnetShare.status[id] == nil {
		vnetShare.status[id] = map[string]vnetMemberStatus{}
	}
	st := vnetMemberStatus{Member: member, Name: name, OK: err == nil, At: time.Now()}
	if err != nil {
		st.Error = err.Error()
	}
	vnetShare.status[id][member] = st
}

// vnetTarget is a member as the sending NAS reaches it.
type vnetTarget struct {
	Key, Name, Addr string
	call            func(ctx context.Context, op, id string, in, out any) error
}

func (s *Server) vnetTarget(key string) (vnetTarget, error) {
	kind, id, _ := strings.Cut(key, ":")
	t := vnetTarget{Key: key}
	switch kind {
	case "fleet":
		m, ok := s.fleetMember(id)
		if !ok {
			return t, errors.New("this NAS is no longer in the Multi-NAS console")
		}
		t.Name, t.Addr = m.Name, hostOnly(m.Host)
		t.call = func(ctx context.Context, op, vid string, in, out any) error {
			return fleetCall(ctx, m, http.MethodPost, "/api/fleet/peer/vnets/"+vid+"/"+op, in, out)
		}
	case "cluster":
		c := s.clusterState()
		i := slices.IndexFunc(c.Members, func(m store.ClusterMember) bool { return m.ID == id })
		if i < 0 || id == c.Self {
			return t, errors.New("this NAS is no longer a member of the cluster")
		}
		m := c.Members[i]
		t.Name, t.Addr = m.Name, firstNonEmpty(m.Addr, hostOnly(m.Host))
		t.call = func(ctx context.Context, op, vid string, in, out any) error {
			return s.clusterCall(ctx, m, 60*time.Second, "vnets/"+vid+"/"+op, in, out)
		}
	default:
		return t, errors.New("unknown NAS")
	}
	if ip, err := netip.ParseAddr(t.Addr); err == nil {
		t.Addr = ip.Unmap().String()
	} else if ips, err := net.LookupIP(t.Addr); err == nil && len(ips) > 0 {
		t.Addr = ips[0].String()
	} else {
		return t, fmt.Errorf("the address of %s is unknown", t.Name)
	}
	return t, nil
}

// localAddrTo is the address of this NAS that reaches addr.
func localAddrTo(addr string) string {
	c, err := net.Dial("udp", net.JoinHostPort(addr, "4789"))
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

func (s *Server) vnetShareLoop() {
	time.Sleep(30 * time.Second)
	for {
		s.vnetShareRound(context.Background())
		select {
		case <-vnetShare.kick:
			time.Sleep(2 * time.Second)
		case <-time.After(2 * time.Minute):
		}
	}
}

// vnetShareRound brings every member of the shared networks of this NAS in
// step, and sends the removals.
func (s *Server) vnetShareRound(ctx context.Context) {
	if network.Current() != nil {
		return // a change is on trial here
	}
	cfg := s.netConfigNow()
	self, _ := hostname()
	var sent []store.VNetSent
	s.store.Read(func(st *store.State) { sent = slices.Clone(st.Fleet.VNetSent) })
	want := map[store.VNetSent]bool{}
	selfID := s.clusterState().Self
	for _, v := range cfg.Virtual {
		if v.Kind != "vxlan" || v.OriginKey != "" || len(v.Members) == 0 {
			continue
		}
		parts := []vnetParticipant{{}}
		var targets []vnetTarget
		for _, key := range v.Members {
			want[store.VNetSent{VNet: v.ID, Member: key}] = true
			t, err := s.vnetTarget(key)
			if err != nil {
				setVNetStatus(v.ID, key, key, err)
				continue
			}
			p := vnetParticipant{Member: key, Addr: t.Addr}
			if v.Encrypt {
				var k struct {
					PublicKey string `json:"publicKey"`
					Port      int    `json:"port"`
				}
				cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
				err := t.call(cctx, "key", v.ID, map[string]string{"from": selfID}, &k)
				cancel()
				if err != nil {
					// The key it gave before keeps the tunnel up meanwhile.
					p.PubKey, p.Port = oldWGPeer(v, t.Addr)
					if p.PubKey == "" {
						setVNetStatus(v.ID, key, t.Name, err)
						continue
					}
				} else {
					p.PubKey, p.Port = k.PublicKey, k.Port
				}
			}
			parts = append(parts, p)
			targets = append(targets, t)
		}
		if len(targets) == 0 {
			continue
		}
		parts[0].Addr = localAddrTo(targets[0].Addr)
		if parts[0].Addr == "" {
			continue
		}
		if v.Encrypt {
			pub, err := wgKey(v.ID)
			if err != nil {
				slog.Warn("vnet share", "vnet", v.ID, "err", err)
				continue
			}
			parts[0].PubKey, parts[0].Port = pub, wgPort(cfg, v.ID)
		}
		plan := vnetPlan(v, vnetMesh(s.vnetSeed(), v.ID), parts)
		// This NAS's own side first.
		mine := plan[0]
		next := cloneNet(cfg)
		for i := range next.Virtual {
			if nv := &next.Virtual[i]; nv.ID == v.ID {
				nv.Peers, nv.Local, nv.WG = mine.Peers, mine.Local, mine.WG
			}
		}
		if !slices.Equal(v.Peers, mine.Peers) || v.Local != mine.Local || !sameWG(v.WG, mine.WG) {
			if err := s.applyVNetAuto(next, "Virtual network "+v.ID+": tunnel to the other NAS updated"); err != nil {
				slog.Warn("vnet share", "vnet", v.ID, "err", err)
				continue
			}
			cfg = s.netConfigNow()
		}
		gw := v.Gateway
		if a, _, ok := strings.Cut(v.Address, "/"); ok && gw == "" {
			gw = a
		}
		subnet := ""
		if p, ok := v.Prefix(); ok {
			subnet = p.String()
		}
		for i, t := range targets {
			sp, ok := plan[i+1]
			if !ok {
				continue
			}
			sp.ID, sp.Label, sp.VNI, sp.MTU, sp.Subnet, sp.Gateway, sp.DNS = v.ID, v.Label, v.VNI, v.MTU, subnet, gw, v.DNS
			sp.Isolate, sp.NASPorts, sp.LogDrops, sp.Encrypt, sp.Origin, sp.From = v.Isolate, v.NASPorts, v.LogDrops, v.Encrypt, self, selfID
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			err := t.call(cctx, "apply", v.ID, sp, nil)
			cancel()
			setVNetStatus(v.ID, t.Key, t.Name, err)
			if err == nil && !slices.Contains(sent, store.VNetSent{VNet: v.ID, Member: t.Key}) {
				s.vnetSentUpdate(store.VNetSent{VNet: v.ID, Member: t.Key}, true)
			}
		}
	}
	for _, e := range sent {
		if want[e] {
			continue
		}
		t, err := s.vnetTarget(e.Member)
		if err == nil {
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			err = t.call(cctx, "remove", e.VNet, map[string]string{"from": selfID}, nil)
			cancel()
		}
		var pe *peerError
		if err == nil || errors.As(err, &pe) && pe.Status == http.StatusNotFound || strings.Contains(fmt.Sprint(err), "no longer") {
			s.vnetSentUpdate(e, false)
			vnetShare.Lock()
			delete(vnetShare.status[e.VNet], e.Member)
			vnetShare.Unlock()
		} else {
			setVNetStatus(e.VNet, e.Member, t.Name, fmt.Errorf("removal pending: %v", err))
		}
	}
}

func (s *Server) vnetSentUpdate(e store.VNetSent, add bool) {
	_ = s.store.Update(func(st *store.State) error {
		st.Fleet.VNetSent = slices.DeleteFunc(st.Fleet.VNetSent, func(x store.VNetSent) bool { return x == e })
		if add {
			st.Fleet.VNetSent = append(st.Fleet.VNetSent, e)
		}
		return nil
	})
}

// vnetSeed identifies this NAS for the mesh prefixes (its machine id).
func (s *Server) vnetSeed() string {
	b, _ := os.ReadFile("/etc/machine-id")
	return strings.TrimSpace(string(b))
}

func oldWGPeer(v network.VNet, addr string) (string, int) {
	if v.WG == nil {
		return "", 0
	}
	for _, p := range v.WG.Peers {
		if ap, err := netip.ParseAddrPort(p.Endpoint); err == nil && ap.Addr().String() == addr {
			return p.PublicKey, int(ap.Port())
		}
	}
	return "", 0
}

func sameWG(a, b *network.VNetWG) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// ---- Receiving side ----

// vnetReceive applies a network sent by another NAS (originKey names it).
func (s *Server) vnetReceive(r *http.Request, originKey string, sp vnetSpec) error {
	if sp.ID != r.PathValue("id") {
		return badRequest("network id mismatch")
	}
	prev, _ := s.netConfigAndLinks()
	next := cloneNet(prev)
	i := slices.IndexFunc(next.Virtual, func(x network.VNet) bool { return x.ID == sp.ID })
	var cur *network.VNet
	if i >= 0 {
		cur = &next.Virtual[i]
		if cur.OriginKey != originKey {
			return &Error{Status: http.StatusConflict, Msg: "this NAS already has a virtual network named " + sp.ID}
		}
	}
	for _, x := range next.Virtual {
		if x.ID != sp.ID && x.Kind == "vxlan" && x.VNI == sp.VNI {
			return &Error{Status: http.StatusConflict, Msg: fmt.Sprintf("the VXLAN id %d is used by the network %s of this NAS", sp.VNI, x.ID)}
		}
	}
	bridge := network.NextBridge(prev, linkNames())
	v := receivedVNet(sp, originKey, cur, bridge)
	if v.Encrypt {
		if _, err := wgKey(v.ID); err != nil {
			return err
		}
	}
	if err := network.CheckVNetAddrs(v, otherAddrs(v.Bridge)); err != nil {
		return badRequest(err.Error())
	}
	if cur != nil {
		x, _ := json.Marshal(*cur)
		y, _ := json.Marshal(v)
		if string(x) == string(y) {
			return nil // already in step
		}
		*cur = v
	} else {
		next.Virtual = append(next.Virtual, v)
	}
	what := "Virtual network " + v.ID + " received from " + v.Origin
	if cur != nil {
		what = "Virtual network " + v.ID + " updated by " + v.Origin
	}
	if err := s.applyVNetAuto(next, what); err != nil {
		return badRequest(err.Error())
	}
	return nil
}

func (s *Server) vnetRemoveReceived(r *http.Request, originKey string) error {
	prev, _ := s.netConfigAndLinks()
	next := cloneNet(prev)
	id := r.PathValue("id")
	i := slices.IndexFunc(next.Virtual, func(x network.VNet) bool { return x.ID == id && x.OriginKey == originKey })
	if i < 0 {
		return notFound("no such shared network here")
	}
	for _, vv := range s.vnetViews(r.Context(), prev) {
		if vv.ID == id && len(vv.Guests) > 0 {
			return badRequest("the network is still used by guests of " + func() string { n, _ := hostname(); return n }())
		}
	}
	next.Virtual = slices.Delete(next.Virtual, i, i+1)
	_ = os.Remove(network.WGKeyFile(id))
	return s.applyVNetAuto(next, "Virtual network "+id+" removed by its NAS")
}

// vnetKeyAnswer gives this NAS's WireGuard key and port for a network.
func (s *Server) vnetKeyAnswer(r *http.Request, originKey string) (any, error) {
	id := r.PathValue("id")
	cfg := s.netConfigNow()
	for _, x := range cfg.Virtual {
		if x.ID == id && x.OriginKey != originKey {
			return nil, &Error{Status: http.StatusConflict, Msg: "this NAS already has a virtual network named " + id}
		}
	}
	pub, err := wgKey(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"publicKey": pub, "port": wgPort(cfg, id)}, nil
}

func validVNetID(id string) bool {
	if len(id) == 0 || len(id) > 12 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func (s *Server) vnetShareRoutes() {
	go s.vnetShareLoop()
	fleetOrigin := func(m store.FleetManager) string { return "fleet:" + m.ID }
	s.fleetPeer("POST /api/fleet/peer/vnets/{id}/key", "networks", false, func(r *http.Request, m store.FleetManager) (any, error) {
		if !validVNetID(r.PathValue("id")) {
			return nil, badRequest("invalid network id")
		}
		return s.vnetKeyAnswer(r, fleetOrigin(m))
	})
	s.fleetPeer("POST /api/fleet/peer/vnets/{id}/apply", "networks", true, func(r *http.Request, m store.FleetManager) (any, error) {
		var sp vnetSpec
		if err := decode(r, &sp); err != nil {
			return nil, err
		}
		sp.Origin = firstNonEmpty(strings.TrimSpace(m.Name), "console")
		return nil, s.vnetReceive(r, fleetOrigin(m), sp)
	})
	s.fleetPeer("POST /api/fleet/peer/vnets/{id}/remove", "networks", true, func(r *http.Request, m store.FleetManager) (any, error) {
		return nil, s.vnetRemoveReceived(r, fleetOrigin(m))
	})
	clusterOrigin := func(r *http.Request, from string) (string, string, error) {
		c := s.clusterState()
		i := slices.IndexFunc(c.Members, func(m store.ClusterMember) bool { return m.ID == from })
		if i < 0 || from == c.Self {
			return "", "", badRequest("unknown cluster member")
		}
		return "cluster:" + from, c.Members[i].Name, nil
	}
	s.clusterPeer("POST /api/cluster/peer/vnets/{id}/key", true, func(r *http.Request) (any, error) {
		var req struct {
			From string `json:"from"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		key, _, err := clusterOrigin(r, req.From)
		if err != nil || !validVNetID(r.PathValue("id")) {
			return nil, badRequest("invalid request")
		}
		return s.vnetKeyAnswer(r, key)
	})
	s.clusterPeer("POST /api/cluster/peer/vnets/{id}/apply", true, func(r *http.Request) (any, error) {
		var sp vnetSpec
		if err := decode(r, &sp); err != nil {
			return nil, err
		}
		key, name, err := clusterOrigin(r, sp.From)
		if err != nil {
			return nil, err
		}
		sp.Origin = name
		return nil, s.vnetReceive(r, key, sp)
	})
	s.clusterPeer("POST /api/cluster/peer/vnets/{id}/remove", true, func(r *http.Request) (any, error) {
		var req struct {
			From string `json:"from"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		key, _, err := clusterOrigin(r, req.From)
		if err != nil {
			return nil, err
		}
		return nil, s.vnetRemoveReceived(r, key)
	})
}

// vnetOriginActive tells whether the NAS that sent a network still manages
// it (its console access or cluster membership is still there).
func (s *Server) vnetOriginActive(key string) bool {
	kind, id, _ := strings.Cut(key, ":")
	active := false
	s.store.Read(func(st *store.State) {
		switch kind {
		case "fleet":
			active = slices.ContainsFunc(st.Fleet.Managers, func(m store.FleetManager) bool { return m.ID == id && m.AllowNetworks })
		case "cluster":
			active = slices.ContainsFunc(st.Cluster.Members, func(m store.ClusterMember) bool { return m.ID == id })
		}
	})
	return active
}
