package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"strataos/internal/docker"
	"strataos/internal/network"
	"strataos/internal/store"
	"strataos/internal/system"
)

// Virtual networks (network.VNet): created, changed and removed through the
// trial and automatic rollback of the network configuration.

type vnetStatus struct {
	Exists    bool     `json:"exists"`
	Up        bool     `json:"up"`
	MTU       int      `json:"mtu,omitempty"`
	Addresses []string `json:"addresses"`
	Docker    bool     `json:"docker"` // its Docker network exists
}

type vnetGuest struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type vnetView struct {
	network.VNet
	Status vnetStatus  `json:"status"`
	Guests []vnetGuest `json:"guests"`
}

func cloneNet(c network.Config) network.Config {
	var out network.Config
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, &out)
	if out.Interfaces == nil {
		out.Interfaces = []network.Iface{}
	}
	if out.Bonds == nil {
		out.Bonds = []network.Bond{}
	}
	if out.Bridges == nil {
		out.Bridges = []network.Bridge{}
	}
	if out.VLANs == nil {
		out.VLANs = []network.VLAN{}
	}
	return out
}

func readInt(p string) int {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range strings.TrimSpace(string(b)) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// dockerNetworks returns the Docker networks of virtual networks (by vnet id).
func (s *Server) dockerNetworks(ctx context.Context) map[string]string {
	out := map[string]string{}
	if !docker.Available() {
		return out
	}
	var list []struct {
		ID     string            `json:"Id"`
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
	}
	if s.docker.Call(ctx, "GET", "/networks", url.Values{"filters": {`{"label":["strata.vnet"]}`}}, nil, &list) != nil {
		return out
	}
	for _, n := range list {
		out[n.Labels["strata.vnet"]] = n.Name
	}
	return out
}

// vnetDockerSync creates the Docker networks of the virtual networks that
// want one (macvlan on the veth pair of the bridge) and removes the others.
func (s *Server) vnetDockerSync(ctx context.Context) {
	if !docker.Available() {
		return
	}
	have := s.dockerNetworks(ctx)
	want := map[string]bool{}
	for _, v := range s.netConfigNow().Virtual {
		if !v.Docker {
			continue
		}
		want[v.ID] = true
		if _, ok := have[v.ID]; ok {
			continue
		}
		if _, err := os.Stat("/sys/class/net/" + v.DockerParent()); err != nil {
			continue
		}
		p, _ := v.Prefix()
		_, _, rng := v.Pools()
		ipam := map[string]string{"Subnet": p.String(), "IPRange": rng.String()}
		if gw := v.GatewayAddr(); gw != "" {
			ipam["Gateway"] = gw
		} else if a, _, ok := strings.Cut(v.Address, "/"); ok {
			ipam["Gateway"] = a
		}
		body := map[string]any{
			"Name": v.DockerName(), "Driver": "macvlan", "CheckDuplicate": true,
			"Options": map[string]string{"parent": v.DockerParent(), "macvlan_mode": "bridge"},
			"IPAM":    map[string]any{"Config": []any{ipam}},
			"Labels":  map[string]string{"strata.vnet": v.ID},
		}
		if err := s.docker.Call(ctx, "POST", "/networks/create", nil, body, nil); err != nil {
			slog.Warn("vnet docker network", "vnet", v.ID, "err", err)
		}
	}
	for id, name := range have {
		if !want[id] {
			// Refused while containers use it: tried again next time.
			_ = s.docker.Call(ctx, "DELETE", "/networks/"+url.PathEscape(name), nil, nil, nil)
		}
	}
}

// vnetViews lists the virtual networks with their state and their guests.
func (s *Server) vnetViews(ctx context.Context, c network.Config) []vnetView {
	addrs := nasAddrsBy()
	guests := s.fwGuests(ctx, s.fwConfig())
	dn := s.dockerNetworks(ctx)
	out := []vnetView{}
	for _, v := range c.Virtual {
		vv := vnetView{VNet: v, Guests: []vnetGuest{}}
		if _, err := os.Stat("/sys/class/net/" + v.Bridge); err == nil {
			vv.Status.Exists = true
			op, _ := os.ReadFile("/sys/class/net/" + v.Bridge + "/operstate")
			vv.Status.Up = strings.TrimSpace(string(op)) == "up"
			vv.Status.MTU = readInt("/sys/class/net/" + v.Bridge + "/mtu")
		}
		vv.Status.Addresses = addrs[v.Bridge]
		if vv.Status.Addresses == nil {
			vv.Status.Addresses = []string{}
		}
		_, vv.Status.Docker = dn[v.ID]
		for _, g := range guests {
			if g.Missing {
				continue
			}
			if slices.Contains(g.Bridges, v.Bridge) || (g.Kind == "docker" && slices.Contains(g.Bridges, v.DockerName())) {
				vv.Guests = append(vv.Guests, vnetGuest{Kind: g.Kind, Name: g.Name, State: g.State})
			}
		}
		out = append(out, vv)
	}
	return out
}

type vnetPeer struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
	From string `json:"from"` // cluster | fleet
}

// applyVNets puts a network configuration with changed virtual networks on trial.
func (s *Server) applyVNets(r *http.Request, next network.Config, what string) (any, error) {
	prev, physical := s.netConfigAndLinks()
	if network.Manager() == "NetworkManager" {
		return nil, badRequest("NetworkManager manages this machine: virtual networks need systemd-networkd")
	}
	if err := network.Validate(&next, physical); err != nil {
		return nil, badRequest(err.Error())
	}
	pending, err := network.Apply(next, prev, 120*time.Second)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	s.audit(r, "network", what+" (on trial)")
	return map[string]any{"deadline": pending.Deadline, "links": pending.Links, "token": pending.Token}, nil
}

// otherAddrs lists the NAS's addresses outside a bridge.
func otherAddrs(bridge string) []string {
	var out []string
	for l, a := range nasAddrsBy() {
		if l != bridge {
			out = append(out, a...)
		}
	}
	return out
}

func (s *Server) vnetRoutes() {
	s.admin("GET /api/system/network/vnets", func(r *http.Request) (any, error) {
		cfg, _ := s.netConfigAndLinks()
		if p := network.PendingConfig(); p != nil {
			cfg = *p
		}
		var parents []string
		for _, i := range cfg.Interfaces {
			parents = append(parents, i.Name)
		}
		bridged := map[string]bool{}
		for _, b := range cfg.Bridges {
			parents = append(parents, b.Name)
			for _, m := range b.Members {
				bridged[m] = true
			}
		}
		for _, b := range cfg.Bonds {
			if !bridged[b.Name] {
				parents = append(parents, b.Name)
			}
		}
		parents = slices.DeleteFunc(parents, func(p string) bool { return bridged[p] })
		peers := []vnetPeer{}
		s.store.Read(func(st *store.State) {
			for _, m := range st.Cluster.Members {
				if m.ID == st.Cluster.Self {
					continue
				}
				a := m.Addr
				if a == "" {
					a = hostOnly(m.Host)
				}
				peers = append(peers, vnetPeer{Name: m.Name, Addr: a, From: "cluster"})
			}
			for _, m := range st.Fleet.Members {
				if a := hostOnly(m.Host); net.ParseIP(a) != nil {
					peers = append(peers, vnetPeer{Name: m.Name, Addr: a, From: "fleet"})
				}
			}
		})
		var dns []string
		for _, d := range system.GetNetwork().DNS {
			if ip := net.ParseIP(d); ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				dns = append(dns, d)
			}
		}
		if dns == nil {
			dns = []string{}
		}
		return map[string]any{
			"networks": s.vnetViews(r.Context(), cfg), "parents": parents, "peers": peers, "dns": dns,
			"pending": network.Current() != nil, "manager": network.Manager(), "docker": docker.Available(),
			"nextBridge": network.NextBridge(cfg, linkNames()), "addresses": nasAddrs(),
		}, nil
	})

	s.admin("POST /api/system/network/vnets", func(r *http.Request) (any, error) {
		var v network.VNet
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		prev, _ := s.netConfigAndLinks()
		next := cloneNet(prev)
		v.Bridge = network.NextBridge(prev, linkNames())
		next.Virtual = append(next.Virtual, v)
		if err := network.CheckVNetAddrs(v, otherAddrs(v.Bridge)); err != nil {
			return nil, badRequest(err.Error())
		}
		return s.applyVNets(r, next, "Virtual network "+v.ID+" created")
	})

	s.admin("PUT /api/system/network/vnets/{id}", func(r *http.Request) (any, error) {
		var v network.VNet
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		prev, _ := s.netConfigAndLinks()
		next := cloneNet(prev)
		i := slices.IndexFunc(next.Virtual, func(x network.VNet) bool { return x.ID == r.PathValue("id") })
		if i < 0 {
			return nil, notFound("no virtual network " + r.PathValue("id"))
		}
		v.ID, v.Bridge = next.Virtual[i].ID, next.Virtual[i].Bridge
		next.Virtual[i] = v
		if err := network.CheckVNetAddrs(v, otherAddrs(v.Bridge)); err != nil {
			return nil, badRequest(err.Error())
		}
		return s.applyVNets(r, next, "Virtual network "+v.ID+" changed")
	})

	s.admin("DELETE /api/system/network/vnets/{id}", func(r *http.Request) (any, error) {
		prev, _ := s.netConfigAndLinks()
		next := cloneNet(prev)
		i := slices.IndexFunc(next.Virtual, func(x network.VNet) bool { return x.ID == r.PathValue("id") })
		if i < 0 {
			return nil, notFound("no virtual network " + r.PathValue("id"))
		}
		for _, vv := range s.vnetViews(r.Context(), prev) {
			if vv.ID == r.PathValue("id") && len(vv.Guests) > 0 {
				var names []string
				for _, g := range vv.Guests {
					names = append(names, g.Name)
				}
				return nil, badRequest("the network is used by " + strings.Join(names, ", ") + ": move them to another network first")
			}
		}
		next.Virtual = slices.Delete(next.Virtual, i, i+1)
		return s.applyVNets(r, next, "Virtual network "+r.PathValue("id")+" removed")
	})

	// Plugs a Docker container into the Docker network of a virtual network.
	s.admin("POST /api/system/network/vnets/{id}/docker", func(r *http.Request) (any, error) {
		var req struct {
			Container string `json:"container"`
			Address   string `json:"address"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		s.vnetDockerSync(r.Context())
		name, ok := s.dockerNetworks(r.Context())[r.PathValue("id")]
		if !ok {
			return nil, badRequest("this virtual network has no Docker network (turn Docker on in its settings)")
		}
		body := map[string]any{"Container": req.Container}
		if req.Address != "" {
			if net.ParseIP(req.Address).To4() == nil {
				return nil, badRequest("invalid address")
			}
			body["EndpointConfig"] = map[string]any{"IPAMConfig": map[string]string{"IPv4Address": req.Address}}
		}
		if err := s.docker.Call(r.Context(), "POST", "/networks/"+url.PathEscape(name)+"/connect", nil, body, nil); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "network", "Container "+req.Container+" connected to the virtual network "+r.PathValue("id"))
		fwKick()
		return nil, nil
	})
}
