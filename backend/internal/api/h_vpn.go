package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/vpn"
)

// localNetwork guesses the LAN of the NAS (network of the default route).
func localNetwork() string {
	out, err := sys.Run("ip", "-4", "route", "show", "default")
	if err != nil {
		return ""
	}
	f := strings.Fields(out)
	dev := ""
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "dev" {
			dev = f[i+1]
		}
	}
	if dev == "" {
		return ""
	}
	a, _ := sys.Run("ip", "-4", "-o", "addr", "show", "dev", dev)
	for _, x := range strings.Fields(a) {
		if _, n, err := net.ParseCIDR(x); err == nil && strings.Contains(x, "/") {
			return n.String()
		}
	}
	return ""
}

func maskVPN(c store.VPN) store.VPN {
	c.PrivateKey = ""
	peers := make([]store.VPNPeer, len(c.Peers))
	for i, p := range c.Peers {
		p.PrivateKey, p.PresharedKey = "", ""
		peers[i] = p
	}
	c.Peers = peers
	return c
}

func maskDDNS(d store.DDNS) store.DDNS {
	if d.Token != "" {
		d.Token = masked
	}
	return d
}

// ddnsLoop keeps the dynamic name up to date (checked every 5 minutes).
func (s *Server) ddnsLoop() {
	time.Sleep(time.Minute)
	for {
		s.updateDDNS(false)
		time.Sleep(5 * time.Minute)
	}
}

func (s *Server) updateDDNS(force bool) store.DDNS {
	var d store.DDNS
	s.store.Read(func(st *store.State) { d = st.DDNS })
	if d.Provider == "" {
		return d
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ip, err := vpn.PublicIP(ctx)
	result := "OK"
	if err == nil && (force || ip != d.LastIP || time.Since(d.LastUpdate) > 24*time.Hour) {
		err = vpn.UpdateDDNS(ctx, d, ip)
	} else if err == nil {
		return d
	}
	if err != nil {
		result = err.Error()
		slog.Warn("dynamic dns", "err", err)
	}
	_ = s.store.Update(func(st *store.State) error {
		if err == nil {
			if st.DDNS.LastIP != "" && st.DDNS.LastIP != ip {
				s.store.Log("info", "ddns", "", "Public address changed: "+st.DDNS.LastIP+" → "+ip)
			}
			st.DDNS.LastIP = ip
		}
		st.DDNS.LastUpdate, st.DDNS.LastResult = time.Now(), result
		d = st.DDNS
		return nil
	})
	return d
}

func (s *Server) vpnRoutes() {
	s.admin("GET /api/vpn", func(r *http.Request) (any, error) {
		var c store.VPN
		var d store.DDNS
		s.store.Read(func(st *store.State) { c, d = st.VPN, st.DDNS })
		if c.Subnet == "" {
			_ = vpn.Validate(&c)
		}
		if c.Peers == nil {
			c.Peers = []store.VPNPeer{}
		}
		return map[string]any{"installed": vpn.Installed(), "config": maskVPN(c), "serverIp": vpn.ServerIP(c), "status": vpn.Status(), "lanGuess": localNetwork(), "ddns": maskDDNS(d), "ddnsHost": vpn.FullHostname(d)}, nil
	})

	s.admin("PUT /api/vpn", func(r *http.Request) (any, error) {
		var req store.VPN
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := vpn.Validate(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		if req.Enabled && !vpn.Installed() {
			return nil, badRequest("install WireGuard first")
		}
		var c store.VPN
		err := s.store.Update(func(st *store.State) error {
			cur := st.VPN
			if cur.PrivateKey == "" {
				priv, pub, err := vpn.GenKey()
				if err != nil {
					return err
				}
				cur.PrivateKey, cur.PublicKey = priv, pub
			}
			if cur.Subnet != "" && cur.Subnet != req.Subnet && len(cur.Peers) > 0 {
				return badRequest("delete the devices before changing the VPN network")
			}
			cur.Enabled, cur.Port, cur.Subnet, cur.Endpoint, cur.LAN = req.Enabled, req.Port, req.Subnet, req.Endpoint, req.LAN
			st.VPN = cur
			c = cur
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "VPN settings saved")
		if err := vpn.Apply(c); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return nil, nil
	})

	s.admin("POST /api/vpn/peers", func(r *http.Request) (any, error) {
		var req struct {
			Name string `json:"name"`
			Mode string `json:"mode"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var c store.VPN
		var p store.VPNPeer
		err := s.store.Update(func(st *store.State) error {
			if st.VPN.PrivateKey == "" {
				return badRequest("save the VPN settings first")
			}
			var err error
			if p, err = vpn.NewPeer(st.VPN, req.Name, req.Mode); err != nil {
				return badRequest(err.Error())
			}
			st.VPN.Peers = append(st.VPN.Peers, p)
			c = st.VPN
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Added VPN device "+p.Name)
		if err := vpn.Apply(c); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return map[string]any{"peer": maskVPN(store.VPN{Peers: []store.VPNPeer{p}}).Peers[0], "config": vpn.RenderClient(c, p)}, nil
	})

	s.admin("GET /api/vpn/peers/{id}/config", func(r *http.Request) (any, error) {
		var c store.VPN
		s.store.Read(func(st *store.State) { c = st.VPN })
		for _, p := range c.Peers {
			if p.ID == r.PathValue("id") {
				s.audit(r, "vpn", "Viewed the configuration of VPN device "+p.Name)
				return map[string]string{"config": vpn.RenderClient(c, p), "name": p.Name}, nil
			}
		}
		return nil, notFound("device not found")
	})

	s.admin("DELETE /api/vpn/peers/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var c store.VPN
		err := s.store.Update(func(st *store.State) error {
			n := len(st.VPN.Peers)
			st.VPN.Peers = slices.DeleteFunc(st.VPN.Peers, func(p store.VPNPeer) bool { return p.ID == id })
			if len(st.VPN.Peers) == n {
				return notFound("device not found")
			}
			c = st.VPN
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Removed a VPN device")
		return nil, vpn.Apply(c)
	})

	s.admin("PUT /api/ddns", func(r *http.Request) (any, error) {
		var req store.DDNS
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var cur store.DDNS
		s.store.Read(func(st *store.State) { cur = st.DDNS })
		if req.Token == masked {
			req.Token = cur.Token
		}
		if err := vpn.ValidateDDNS(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		req.LastIP, req.LastUpdate, req.LastResult = "", time.Time{}, ""
		if err := s.store.Update(func(st *store.State) error {
			st.DDNS = req
			// The VPN endpoint follows the dynamic name when it was not set by hand.
			if req.Provider != "" && (st.VPN.Endpoint == "" || st.VPN.Endpoint == vpn.FullHostname(cur)) {
				st.VPN.Endpoint = vpn.FullHostname(req)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Dynamic DNS set to "+req.Provider+" "+req.Hostname)
		d := s.updateDDNS(true)
		return maskDDNS(d), nil
	})
}
