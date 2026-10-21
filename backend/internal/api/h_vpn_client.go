package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"strataos/internal/docker"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/vpn"
)

func maskClient(cl store.VPNClient) store.VPNClient {
	if cl.PrivateKey != "" {
		cl.PrivateKey = masked
	}
	if cl.PresharedKey != "" {
		cl.PresharedKey = masked
	}
	return cl
}

// dockerAPIAtLeast compares the API version of the engine with v ("1.48").
func (s *Server) dockerAPIAtLeast(ctx context.Context, v string) bool {
	var ver struct {
		ApiVersion string `json:"ApiVersion"`
	}
	if s.docker.Call(ctx, "GET", "/version", nil, nil, &ver) != nil {
		return false
	}
	num := func(x string) (int, int) {
		a, b, _ := strings.Cut(x, ".")
		i, _ := strconv.Atoi(a)
		j, _ := strconv.Atoi(b)
		return i, j
	}
	m1, n1 := num(ver.ApiVersion)
	m2, n2 := num(v)
	return m1 > m2 || (m1 == m2 && n1 >= n2)
}

type dockerNet struct {
	IPAM struct {
		Config []struct {
			Subnet string `json:"Subnet"`
		} `json:"Config"`
	} `json:"IPAM"`
	Containers map[string]struct {
		Name string `json:"Name"`
	} `json:"Containers"`
}

// ensureAppsNetwork creates the Docker network of the routed apps.
func (s *Server) ensureAppsNetwork(subnet string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var n dockerNet
	if err := s.docker.Call(ctx, "GET", "/networks/"+vpn.AppsNetwork, nil, nil, &n); err == nil {
		if len(n.IPAM.Config) > 0 && n.IPAM.Config[0].Subnet == subnet {
			return nil
		}
		if len(n.Containers) > 0 {
			return fmt.Errorf("take the apps out of the tunnel before changing its network")
		}
		if err := s.docker.Call(ctx, "DELETE", "/networks/"+vpn.AppsNetwork, nil, nil, nil); err != nil {
			return err
		}
	}
	return s.docker.Call(ctx, "POST", "/networks/create", nil, map[string]any{
		"Name": vpn.AppsNetwork, "Driver": "bridge",
		"IPAM":    map[string]any{"Config": []map[string]string{{"Subnet": subnet, "Gateway": vpn.Gateway(subnet)}}},
		"Options": map[string]string{"com.docker.network.bridge.name": vpn.AppsBridge},
		"Labels":  map[string]string{"org.strataos.vpn": "1"},
	}, nil)
}

// routeApps puts the selected containers on the tunnel network and gives
// the others their networks back. Engines older than API 1.48 cannot choose
// the gateway of a container on several networks: the container is then
// taken off its other networks (remembered in Detached).
func (s *Server) routeApps(cl store.VPNClient) error {
	if !docker.Available() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var want []string
	if vpn.ClientActive(cl) && cl.Scope == "apps" {
		want = cl.Apps
	}
	var netw dockerNet
	exists := s.docker.Call(ctx, "GET", "/networks/"+vpn.AppsNetwork, nil, nil, &netw) == nil
	if !exists && len(want) == 0 && len(cl.Detached) == 0 {
		return nil
	}
	detached := map[string][]string{}
	for k, v := range cl.Detached {
		detached[k] = slices.Clone(v)
	}
	gwPrio := len(want) > 0 && s.dockerAPIAtLeast(ctx, "1.48")
	var errs []error
	for _, name := range want {
		var info struct {
			HostConfig struct {
				NetworkMode string `json:"NetworkMode"`
			} `json:"HostConfig"`
			NetworkSettings struct {
				Networks map[string]any `json:"Networks"`
			} `json:"NetworkSettings"`
		}
		if err := s.docker.Call(ctx, "GET", "/containers/"+url.PathEscape(name)+"/json", nil, nil, &info); err != nil {
			var ae *docker.APIError
			if !errors.As(err, &ae) || ae.Status != 404 {
				errs = append(errs, err)
			}
			continue
		}
		mode := info.HostConfig.NetworkMode
		if mode == "host" || strings.HasPrefix(mode, "container:") {
			errs = append(errs, fmt.Errorf("%s uses the network of the host or of another container: it cannot be routed", name))
			continue
		}
		if _, ok := info.NetworkSettings.Networks[vpn.AppsNetwork]; !ok {
			ep := map[string]any{}
			if gwPrio {
				ep["GwPriority"] = 1000
			}
			if err := s.docker.Call(ctx, "POST", "/networks/"+vpn.AppsNetwork+"/connect", nil, map[string]any{"Container": name, "EndpointConfig": ep}, nil); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if gwPrio {
			continue
		}
		for n := range info.NetworkSettings.Networks {
			if n == vpn.AppsNetwork {
				continue
			}
			if err := s.docker.Call(ctx, "POST", "/networks/"+url.PathEscape(n)+"/disconnect", nil, map[string]any{"Container": name, "Force": true}, nil); err != nil {
				errs = append(errs, err)
				continue
			}
			if !slices.Contains(detached[name], n) {
				detached[name] = append(detached[name], n)
			}
		}
	}
	// Containers no longer routed get their networks back.
	gone := map[string]bool{}
	for _, c := range netw.Containers {
		if !slices.Contains(want, c.Name) {
			gone[c.Name] = true
		}
	}
	for name := range detached {
		if !slices.Contains(want, name) {
			gone[name] = true
		}
	}
	for name := range gone {
		nets := detached[name]
		if len(nets) == 0 && !exists {
			delete(detached, name)
			continue
		}
		for _, n := range nets {
			err := s.docker.Call(ctx, "POST", "/networks/"+url.PathEscape(n)+"/connect", nil, map[string]any{"Container": name}, nil)
			var ae *docker.APIError
			if err != nil && !(errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 403)) {
				errs = append(errs, err)
			}
		}
		if exists {
			_ = s.docker.Call(ctx, "POST", "/networks/"+vpn.AppsNetwork+"/disconnect", nil, map[string]any{"Container": name, "Force": true}, nil)
		}
		delete(detached, name)
	}
	if exists && len(want) == 0 {
		_ = s.docker.Call(ctx, "DELETE", "/networks/"+vpn.AppsNetwork, nil, nil, nil)
	}
	if !mapsEqual(detached, cl.Detached) {
		_ = s.store.Update(func(st *store.State) error {
			if len(detached) == 0 {
				detached = nil
			}
			st.VPN.Client.Detached = detached
			return nil
		})
	}
	return errors.Join(errs...)
}

func mapsEqual(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

func (s *Server) vpnClientRoutes() {
	s.admin("GET /api/vpn/client", func(r *http.Request) (any, error) {
		c, _ := s.vpnState()
		cl := c.Client
		if cl.AppsSubnet == "" {
			cl.AppsSubnet = vpn.DefaultAppsSubnet
		}
		if cl.Scope == "" {
			cl.Scope = "apps"
		}
		var st *vpn.PeerStatus
		if vpn.ClientActive(cl) {
			for _, x := range vpn.StatusOf(vpn.OutIface) {
				st = &x
			}
		}
		containers := []string{}
		if docker.Available() {
			var list []struct {
				Names []string `json:"Names"`
			}
			if s.docker.Call(r.Context(), "GET", "/containers/json", url.Values{"all": {"1"}}, nil, &list) == nil {
				for _, x := range list {
					if len(x.Names) > 0 {
						containers = append(containers, strings.TrimPrefix(x.Names[0], "/"))
					}
				}
			}
			slices.Sort(containers)
		}
		return map[string]any{"installed": vpn.Installed(), "config": maskClient(cl), "status": st, "routing": vpn.ClientActive(cl) && vpn.RoutingOK(cl),
			"containers": containers, "docker": docker.Available(), "defaultCheckUrl": vpn.IPLookupURL}, nil
	})

	s.admin("PUT /api/vpn/client", func(r *http.Request) (any, error) {
		var req struct {
			store.VPNClient
			Conf string `json:"conf"` // the provider's file, when replaced
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var warn []string
		err := s.store.Update(func(st *store.State) error {
			cl := st.VPN.Client
			if strings.TrimSpace(req.Conf) != "" {
				parsed, w, err := vpn.ParseClientConf(req.Conf)
				if err != nil {
					return badRequest(err.Error())
				}
				warn = w
				cl.PrivateKey, cl.Addresses, cl.DNS, cl.MTU = parsed.PrivateKey, parsed.Addresses, parsed.DNS, parsed.MTU
				cl.PeerPublicKey, cl.PresharedKey, cl.Endpoint, cl.AllowedIPs, cl.Keepalive = parsed.PeerPublicKey, parsed.PresharedKey, parsed.Endpoint, parsed.AllowedIPs, parsed.Keepalive
			}
			cl.Enabled, cl.Name, cl.Scope, cl.Apps, cl.AppsSubnet, cl.CheckURL = req.Enabled, req.Name, req.Scope, req.Apps, req.AppsSubnet, req.CheckURL
			if err := vpn.ValidateClient(st.VPN, &cl); err != nil {
				return badRequest(err.Error())
			}
			if cl.Enabled && !vpn.Installed() {
				return badRequest("install WireGuard first")
			}
			if cl.Enabled && cl.Scope == "apps" && len(cl.Apps) > 0 && !docker.Available() {
				return badRequest("Docker is not installed")
			}
			st.VPN.Client = cl
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Outgoing VPN tunnel saved")
		if warn == nil {
			warn = []string{}
		}
		return map[string]any{"warnings": warn}, s.vpnApplyErr()
	})

	s.admin("DELETE /api/vpn/client", func(r *http.Request) (any, error) {
		_ = s.store.Update(func(st *store.State) error {
			st.VPN.Client = store.VPNClient{Detached: st.VPN.Client.Detached}
			return nil
		})
		s.audit(r, "vpn", "Outgoing VPN tunnel removed")
		return nil, s.vpnApplyErr()
	})

	s.admin("POST /api/vpn/client/check", func(r *http.Request) (any, error) {
		c, _ := s.vpnState()
		if !vpn.ClientActive(c.Client) {
			return nil, badRequest("the outgoing tunnel is off")
		}
		ip, err := vpn.TunnelPublicIP(r.Context(), c.Client)
		if err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return map[string]string{"publicIp": ip}, nil
	})

	s.admin("POST /api/vpn/client/killswitch", func(r *http.Request) (any, error) {
		c, _ := s.vpnState()
		vpnMu.Lock()
		defer vpnMu.Unlock()
		res, err := vpn.TestKillSwitch(c.Client)
		if err != nil && res.TunnelRoute == "" {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vpn", "Kill switch of the outgoing VPN tested")
		return res, nil
	})

	s.admin("POST /api/vpn/client/reconnect", func(r *http.Request) (any, error) {
		c, _ := s.vpnState()
		if !vpn.ClientActive(c.Client) {
			return nil, badRequest("the outgoing tunnel is off")
		}
		if _, err := sys.Run("systemctl", "restart", "wg-quick@"+vpn.OutIface+".service"); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return nil, s.vpnApplyErr()
	})
}
