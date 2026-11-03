package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"strataos/internal/network"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

// One-click bridge for the VM and container wizards: a Linux bridge over the
// NAS's main link that keeps its address, applied through the same trial and
// automatic rollback as Control Panel → Network.

// netConfigAndLinks returns the network configuration StrataOS manages and
// the links a configuration may use.
func (s *Server) netConfigAndLinks() (network.Config, []string) {
	var physical []string
	for _, i := range system.GetNetwork().Interfaces {
		if i.Kind == "physical" && !strings.HasPrefix(i.Name, "bond") {
			physical = append(physical, i.Name)
		}
	}
	var prev network.Config
	s.store.Read(func(st *store.State) {
		if st.Network != nil {
			prev = *st.Network
		}
	})
	// Bond members are enslaved and may not show as physical any more: keep them valid.
	for _, b := range prev.Bonds {
		physical = append(physical, b.Members...)
	}
	return prev, physical
}

// mainLink is the link of the default route (the NAS's main interface).
func mainLink() string {
	out, err := sys.Run("ip", "-j", "route", "show", "default")
	if err != nil {
		return ""
	}
	var routes []struct {
		Dev string `json:"dev"`
	}
	if json.Unmarshal([]byte(out), &routes) != nil {
		return ""
	}
	for _, r := range routes {
		if r.Dev != "" {
			return r.Dev
		}
	}
	return ""
}

// observeLink reads the live IP configuration of a link.
func observeLink(name string) network.Observed {
	var o network.Observed
	if out, err := sys.Run("ip", "-j", "addr", "show", "dev", name); err == nil {
		var raw []struct {
			MTU      int `json:"mtu"`
			AddrInfo []struct {
				Family    string `json:"family"`
				Local     string `json:"local"`
				Prefixlen int    `json:"prefixlen"`
				Scope     string `json:"scope"`
				Dynamic   bool   `json:"dynamic"`
			} `json:"addr_info"`
		}
		if json.Unmarshal([]byte(out), &raw) == nil && len(raw) > 0 {
			o.MTU = raw[0].MTU
			for _, a := range raw[0].AddrInfo {
				if a.Scope != "global" {
					continue
				}
				if a.Dynamic {
					// IPv6 addresses from router advertisements are dynamic too:
					// only an IPv4 lease means DHCP.
					if a.Family == "inet" {
						o.Dynamic = true
					}
					continue
				}
				o.Addresses = append(o.Addresses, a.Local+"/"+strconv.Itoa(a.Prefixlen))
			}
		}
	}
	if out, err := sys.Run("ip", "-j", "route", "show", "default", "dev", name); err == nil {
		var routes []struct {
			Gateway string `json:"gateway"`
		}
		if json.Unmarshal([]byte(out), &routes) == nil && len(routes) > 0 {
			o.Gateway = routes[0].Gateway
		}
	}
	o.DNS = system.GetNetwork().DNS
	return o
}

// linkNames lists the links present on the machine.
func linkNames() []string {
	var out []string
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

type bridgePlan struct {
	Bridges []string   `json:"bridges"`          // bridges VMs can already use
	Link    string     `json:"link"`             // main link of the NAS
	Bridge  string     `json:"bridge,omitempty"` // name the new bridge would get
	IP      network.IP `json:"ip"`               // configuration the bridge would take over
	Pending bool       `json:"pending"`          // a network change is already on trial
	Error   string     `json:"error,omitempty"`  // why it cannot be done here
}

// planQuickBridge computes the configuration with a bridge over link (the
// main link when empty).
func (s *Server) planQuickBridge(link string) (bridgePlan, network.Config, []string) {
	p := bridgePlan{Bridges: hostBridges(), Link: link, Pending: network.Current() != nil}
	if p.Link == "" {
		p.Link = mainLink()
	}
	prev, physical := s.netConfigAndLinks()
	switch {
	case p.Link == "":
		p.Error = "no default route: the main network interface is unknown"
	case network.Manager() == "NetworkManager":
		p.Error = "NetworkManager manages this machine: create the bridge there"
	default:
		next, name, err := network.PlanBridge(prev, p.Link, observeLink(p.Link), linkNames())
		if err == nil {
			err = network.Validate(&next, physical)
		}
		if err != nil {
			p.Error = err.Error()
			return p, prev, physical
		}
		p.Bridge = name
		for _, br := range next.Bridges {
			if br.Name == name {
				p.IP = br.IP
			}
		}
		return p, next, physical
	}
	return p, prev, physical
}

func (s *Server) bridgeRoutes() {
	s.admin("GET /api/system/network/bridge-plan", func(r *http.Request) (any, error) {
		p, _, _ := s.planQuickBridge("")
		return p, nil
	})

	s.admin("POST /api/system/network/quick-bridge", func(r *http.Request) (any, error) {
		var req struct {
			Link string `json:"link"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Link != mainLink() {
			return nil, badRequest("the bridge can only be created on the main network interface here; use Control Panel → Network for the others")
		}
		p, next, _ := s.planQuickBridge(req.Link)
		if p.Error != "" {
			return nil, badRequest(p.Error)
		}
		var prev network.Config
		s.store.Read(func(st *store.State) {
			if st.Network != nil {
				prev = *st.Network
			}
		})
		pending, err := network.Apply(next, prev, 120*time.Second)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "network", "Bridge "+p.Bridge+" created on "+p.Link+" (on trial)")
		return map[string]any{"bridge": p.Bridge, "deadline": pending.Deadline, "links": pending.Links, "token": pending.Token}, nil
	})
}
