package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
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
	links := make([]store.VPNLink, len(c.Links))
	for i, l := range c.Links {
		l.PrivateKey, l.PresharedKey = "", ""
		links[i] = l
	}
	c.Links = links
	c.Client = maskClient(c.Client)
	return c
}

func maskPeer(p store.VPNPeer) store.VPNPeer {
	p.PrivateKey, p.PresharedKey = "", ""
	return p
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
		users := []map[string]any{}
		s.store.Read(func(st *store.State) {
			c, d = st.VPN, st.DDNS
			for _, u := range st.Users {
				users = append(users, map[string]any{"username": u.Username, "disabled": u.Disabled})
			}
		})
		if c.Subnet == "" || c.DefaultMode == "" {
			_ = vpn.Validate(&c)
		}
		if c.Peers == nil {
			c.Peers = []store.VPNPeer{}
		}
		if c.Links == nil {
			c.Links = []store.VPNLink{}
		}
		links := map[string]vpn.PeerStatus{}
		for _, l := range c.Links {
			for _, st := range vpn.StatusOf(l.Iface) {
				links[l.ID] = st
			}
		}
		return map[string]any{"installed": vpn.Installed(), "config": maskVPN(c), "serverIp": vpn.ServerIP(c), "status": vpn.Status(), "linkStatus": links,
			"inactive": s.vpnInactive(c), "users": users, "services": vpn.Services,
			"lanGuess": localNetwork(), "ddns": maskDDNS(d), "ddnsHost": vpn.FullHostname(d)}, nil
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
			cur.DefaultMode, cur.UserDevices = req.DefaultMode, req.UserDevices
			st.VPN = cur
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "VPN settings saved")
		return nil, s.vpnApplyErr()
	})

	s.admin("POST /api/vpn/peers", func(r *http.Request) (any, error) {
		var req struct {
			Name  string `json:"name"`
			Owner string `json:"owner"`
			Kind  string `json:"kind"`
			vpn.Policy
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Kind != "" && req.Kind != "site" {
			return nil, badRequest("invalid peer kind")
		}
		var c store.VPN
		var p store.VPNPeer
		var host string
		err := s.store.Update(func(st *store.State) error {
			if st.VPN.PrivateKey == "" {
				return badRequest("save the VPN settings first")
			}
			if req.Owner != "" && st.User(req.Owner) == nil {
				return badRequest("unknown user " + req.Owner)
			}
			if req.Kind == "site" {
				req.Owner = ""
			}
			var err error
			if p, err = vpn.NewPeer(st.VPN, req.Owner, req.Name, req.Mode); err != nil {
				return badRequest(err.Error())
			}
			p.Kind = req.Kind
			if err := vpn.SetPolicy(st.VPN, &p, req.Policy); err != nil {
				return badRequest(err.Error())
			}
			st.VPN.Peers = append(st.VPN.Peers, p)
			c = st.VPN
			return nil
		})
		if err != nil {
			return nil, err
		}
		host, _ = os.Hostname()
		if p.Kind == "site" {
			s.audit(r, "vpn", "Added VPN site "+p.Name)
		} else {
			s.audit(r, "vpn", "Added VPN device "+p.Name)
		}
		if err := s.vpnApplyErr(); err != nil {
			return nil, err
		}
		if p.Kind == "site" {
			return map[string]any{"peer": maskPeer(p), "config": vpn.RenderRouter(c, p, host)}, nil
		}
		return map[string]any{"peer": maskPeer(p), "config": vpn.RenderClient(c, p)}, nil
	})

	s.admin("PUT /api/vpn/peers/{id}", func(r *http.Request) (any, error) {
		var req struct {
			Name     string `json:"name"`
			Owner    string `json:"owner"`
			Disabled bool   `json:"disabled"`
			vpn.Policy
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var name string
		err := s.store.Update(func(st *store.State) error {
			i := slices.IndexFunc(st.VPN.Peers, func(p store.VPNPeer) bool { return p.ID == r.PathValue("id") })
			if i < 0 {
				return notFound("device not found")
			}
			p := st.VPN.Peers[i]
			if n := strings.TrimSpace(req.Name); n != "" && n != p.Name {
				if err := vpn.CheckName(st.VPN, p.Owner, n, p.ID); err != nil {
					return badRequest(err.Error())
				}
				p.Name = n
			}
			if p.Kind != "site" && req.Owner != p.Owner {
				if req.Owner != "" && st.User(req.Owner) == nil {
					return badRequest("unknown user " + req.Owner)
				}
				p.Owner = req.Owner
			}
			if err := vpn.SetPolicy(st.VPN, &p, req.Policy); err != nil {
				return badRequest(err.Error())
			}
			if !req.Disabled && p.Expired {
				return badRequest("the expiry date has passed: change it to enable the device again")
			}
			p.Disabled = req.Disabled || p.Expired
			st.VPN.Peers[i] = p
			name = p.Name
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Updated VPN device "+name)
		// An expiry date already past disables the device at once.
		s.vpnExpire()
		return nil, s.vpnApplyErr()
	})

	s.admin("GET /api/vpn/peers/{id}/config", func(r *http.Request) (any, error) {
		var c store.VPN
		s.store.Read(func(st *store.State) { c = st.VPN })
		for _, p := range c.Peers {
			if p.ID != r.PathValue("id") {
				continue
			}
			s.audit(r, "vpn", "Viewed the configuration of VPN device "+p.Name)
			if p.Kind != "site" {
				return map[string]string{"config": vpn.RenderClient(c, p), "name": p.Name}, nil
			}
			host, _ := os.Hostname()
			if r.URL.Query().Get("format") == "strataos" {
				code, err := vpn.PairingCode(c, p, host)
				if err != nil {
					return nil, badRequest(err.Error())
				}
				return map[string]string{"config": code, "name": p.Name}, nil
			}
			return map[string]string{"config": vpn.RenderRouter(c, p, host), "name": p.Name}, nil
		}
		return nil, notFound("device not found")
	})

	s.admin("DELETE /api/vpn/peers/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		err := s.store.Update(func(st *store.State) error {
			n := len(st.VPN.Peers)
			st.VPN.Peers = slices.DeleteFunc(st.VPN.Peers, func(p store.VPNPeer) bool { return p.ID == id })
			if len(st.VPN.Peers) == n {
				return notFound("device not found")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Removed a VPN device")
		return nil, s.vpnApplyErr()
	})

	s.vpnSiteRoutes()
	s.vpnUserRoutes()
	s.vpnClientRoutes()

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
