package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"strataos/internal/docker"
	"strataos/internal/store"
	"strataos/internal/vpn"
)

// maxUserDevices is how many devices an account may add for itself.
const maxUserDevices = 10

var vpnMu sync.Mutex

// vpnState returns a copy of the VPN configuration and which accounts are
// enabled.
func (s *Server) vpnState() (store.VPN, map[string]bool) {
	var c store.VPN
	owners := map[string]bool{}
	s.store.Read(func(st *store.State) {
		c = st.VPN
		c.Peers = slices.Clone(st.VPN.Peers)
		c.Links = slices.Clone(st.VPN.Links)
		c.Client.Apps = slices.Clone(st.VPN.Client.Apps)
		if st.VPN.Client.Detached != nil {
			c.Client.Detached = map[string][]string{}
			for k, v := range st.VPN.Client.Detached {
				c.Client.Detached[k] = slices.Clone(v)
			}
		}
		for _, u := range st.Users {
			owners[u.Username] = !u.Disabled
		}
	})
	if c.Subnet == "" {
		_ = vpn.Validate(&c)
	}
	return c, owners
}

// applyVPN puts the VPN configuration of the state in place: peers of
// disabled accounts and expired peers left out, the routed apps on the
// tunnel network.
func (s *Server) applyVPN() error {
	vpnMu.Lock()
	defer vpnMu.Unlock()
	c, owners := s.vpnState()
	eff := vpn.Effective(c, time.Now(), func(u string) bool { return owners[u] })
	var errs []error
	var paused []string
	if vpn.ClientActive(c.Client) && c.Client.Scope == "apps" && docker.Available() {
		p, err := s.ensureAppsNetwork(c.Client.AppsSubnet)
		paused = p
		errs = append(errs, err)
	}
	errs = append(errs, vpn.Apply(eff))
	errs = append(errs, s.routeApps(c.Client))
	s.unpause(paused)
	return errors.Join(errs...)
}

// vpnApplyErr is applyVPN for a handler: a failure is a 422.
func (s *Server) vpnApplyErr() error {
	if err := s.applyVPN(); err != nil {
		return &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
	}
	return nil
}

// vpnInactive tells why peers cannot connect (disabled, expired, user).
func (s *Server) vpnInactive(c store.VPN) map[string]string {
	owners := map[string]bool{}
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			owners[u.Username] = !u.Disabled
		}
	})
	out := map[string]string{}
	now := time.Now()
	for _, p := range c.Peers {
		switch {
		case p.Expired || (!p.Expires.IsZero() && !now.Before(p.Expires)):
			out[p.ID] = "expired"
		case p.Disabled:
			out[p.ID] = "disabled"
		case p.Owner != "" && !owners[p.Owner]:
			out[p.ID] = "user"
		}
	}
	return out
}

// vpnExpire disables the peers whose expiry date has passed; it reports
// whether there were any.
func (s *Server) vpnExpire() bool {
	now := time.Now()
	due := false
	s.store.Read(func(st *store.State) {
		due = slices.ContainsFunc(st.VPN.Peers, func(p store.VPNPeer) bool {
			return !p.Disabled && !p.Expires.IsZero() && !now.Before(p.Expires)
		})
	})
	if !due {
		return false
	}
	var gone []store.VPNPeer
	_ = s.store.Update(func(st *store.State) error {
		gone = vpn.Expire(&st.VPN, now)
		for _, p := range gone {
			s.store.LogLocked(st, "info", "vpn", "", fmt.Sprintf("VPN device %s expired and was disabled", p.Name))
		}
		return nil
	})
	return len(gone) > 0
}

// vpnExpiryWarn warns the administrator about the devices that expire
// soon, once per device and expiry date.
func (s *Server) vpnExpiryWarn() {
	now := time.Now()
	c, _ := s.vpnState()
	if len(vpn.ExpiringSoon(&c, now)) == 0 {
		return
	}
	var soon []store.VPNPeer
	_ = s.store.Update(func(st *store.State) error {
		soon = vpn.ExpiringSoon(&st.VPN, now)
		for _, p := range soon {
			s.store.LogLocked(st, "warning", "vpn", "", expiryMessage(p))
		}
		return nil
	})
	for _, p := range soon {
		s.notify("warning", "VPN device expiring soon", expiryMessage(p))
	}
}

func expiryMessage(p store.VPNPeer) string {
	if p.Owner != "" {
		return fmt.Sprintf("VPN device %s of %s expires on %s", p.Name, p.Owner, vpn.LastDay(p))
	}
	return fmt.Sprintf("VPN device %s expires on %s", p.Name, vpn.LastDay(p))
}

// vpnLoop puts the VPN back in place after a start (firewall and routing
// are not persistent), then expires devices and watches the outgoing
// tunnel.
func (s *Server) vpnLoop() {
	time.Sleep(15 * time.Second)
	c, _ := s.vpnState()
	if c.Enabled || c.Client.Enabled || len(c.Links) > 0 {
		if err := s.applyVPN(); err != nil {
			slog.Warn("vpn", "err", err)
		}
	}
	for {
		time.Sleep(30 * time.Second)
		s.vpnExpiryWarn()
		c, _ := s.vpnState()
		switch {
		case s.vpnExpire():
		case vpn.ClientActive(c.Client) && !vpn.RoutingOK(c.Client):
		case vpn.ClientActive(c.Client) && c.Client.Scope == "apps":
			// Containers recreated (app update) come back on their own network.
			vpnMu.Lock()
			err := s.routeApps(c.Client)
			vpnMu.Unlock()
			if err != nil {
				slog.Warn("vpn apps", "err", err)
			}
			continue
		default:
			continue
		}
		if err := s.applyVPN(); err != nil {
			slog.Warn("vpn", "err", err)
		}
	}
}

// vpnUserGone removes the devices of a deleted account.
func (s *Server) vpnUserGone(name string) {
	n := 0
	_ = s.store.Update(func(st *store.State) error {
		before := len(st.VPN.Peers)
		st.VPN.Peers = slices.DeleteFunc(st.VPN.Peers, func(p store.VPNPeer) bool { return p.Owner == name })
		n = before - len(st.VPN.Peers)
		return nil
	})
	if n > 0 {
		s.store.Log("info", "vpn", "", fmt.Sprintf("%d VPN device(s) of %s removed with the account", n, name))
		if err := s.applyVPN(); err != nil {
			slog.Warn("vpn", "err", err)
		}
	}
}

// vpnUserChanged applies the VPN when an account with devices is enabled
// or disabled.
func (s *Server) vpnUserChanged(name string) {
	has := false
	s.store.Read(func(st *store.State) {
		has = slices.ContainsFunc(st.VPN.Peers, func(p store.VPNPeer) bool { return p.Owner == name })
	})
	if has {
		if err := s.applyVPN(); err != nil {
			slog.Warn("vpn", "err", err)
		}
	}
}

func (s *Server) vpnSiteRoutes() {
	// A site code of another StrataOS: this NAS connects to it.
	s.admin("POST /api/vpn/links", func(r *http.Request) (any, error) {
		var req struct {
			Code string `json:"code"`
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if !vpn.Installed() {
			return nil, badRequest("install WireGuard first")
		}
		pr, err := vpn.ParsePairing(req.Code)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		var l store.VPNLink
		err = s.store.Update(func(st *store.State) error {
			c := st.VPN
			if c.Subnet == "" {
				_ = vpn.Validate(&c)
			}
			var err error
			if l, err = vpn.NewLink(c, pr, req.Name); err != nil {
				return badRequest(err.Error())
			}
			st.VPN.Links = append(st.VPN.Links, l)
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Imported VPN site "+l.Name)
		l.PrivateKey, l.PresharedKey = "", ""
		return l, s.vpnApplyErr()
	})

	s.admin("PUT /api/vpn/links/{id}", func(r *http.Request) (any, error) {
		var req struct {
			Disabled bool `json:"disabled"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var name string
		err := s.store.Update(func(st *store.State) error {
			i := slices.IndexFunc(st.VPN.Links, func(l store.VPNLink) bool { return l.ID == r.PathValue("id") })
			if i < 0 {
				return notFound("site not found")
			}
			st.VPN.Links[i].Disabled = req.Disabled
			name = st.VPN.Links[i].Name
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Updated VPN site "+name)
		return nil, s.vpnApplyErr()
	})

	s.admin("DELETE /api/vpn/links/{id}", func(r *http.Request) (any, error) {
		err := s.store.Update(func(st *store.State) error {
			n := len(st.VPN.Links)
			st.VPN.Links = slices.DeleteFunc(st.VPN.Links, func(l store.VPNLink) bool { return l.ID == r.PathValue("id") })
			if len(st.VPN.Links) == n {
				return notFound("site not found")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Removed a VPN site")
		return nil, s.vpnApplyErr()
	})
}

// ---- Devices of the signed-in user ----

func (s *Server) vpnUserRoutes() {
	allowed := func(c store.VPN) error {
		if !c.UserDevices || !c.Enabled || c.PrivateKey == "" {
			return &Error{Status: http.StatusForbidden, Msg: "the administrator does not let users add VPN devices"}
		}
		return nil
	}
	own := func(r *http.Request) (store.VPN, store.VPNPeer, error) {
		c, _ := s.vpnState()
		for _, p := range c.Peers {
			if p.ID == r.PathValue("id") && p.Kind == "" && p.Owner == userOf(r) {
				return c, p, nil
			}
		}
		return c, store.VPNPeer{}, notFound("device not found")
	}

	s.user("GET /api/vpn/my", func(r *http.Request) (any, error) {
		c, _ := s.vpnState()
		me := userOf(r)
		devices := []store.VPNPeer{}
		status := map[string]vpn.PeerStatus{}
		live := vpn.Status()
		for _, p := range c.Peers {
			if p.Owner == me && p.Kind == "" {
				devices = append(devices, maskPeer(p))
				if st, ok := live[p.PublicKey]; ok {
					status[p.PublicKey] = st
				}
			}
		}
		inactive := s.vpnInactive(store.VPN{Peers: devices})
		return map[string]any{"allowed": allowed(c) == nil, "devices": devices, "status": status, "inactive": inactive, "max": maxUserDevices, "mode": c.DefaultMode,
			"warnDays": vpn.WarnDays(c)}, nil
	})

	s.user("POST /api/vpn/my/devices", func(r *http.Request) (any, error) {
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		me := userOf(r)
		var c store.VPN
		var p store.VPNPeer
		err := s.store.Update(func(st *store.State) error {
			if err := allowed(st.VPN); err != nil {
				return err
			}
			mine := 0
			for _, x := range st.VPN.Peers {
				if x.Owner == me {
					mine++
				}
			}
			if mine >= maxUserDevices {
				return badRequest(fmt.Sprintf("at most %d devices per user", maxUserDevices))
			}
			var err error
			if p, err = vpn.NewPeer(st.VPN, me, req.Name, ""); err != nil {
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
		if err := s.vpnApplyErr(); err != nil {
			return nil, err
		}
		return map[string]any{"peer": maskPeer(p), "config": vpn.RenderClient(c, p)}, nil
	})

	s.user("GET /api/vpn/my/devices/{id}/config", func(r *http.Request) (any, error) {
		c, p, err := own(r)
		if err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Viewed the configuration of VPN device "+p.Name)
		return map[string]string{"config": vpn.RenderClient(c, p), "name": p.Name}, nil
	})

	s.user("DELETE /api/vpn/my/devices/{id}", func(r *http.Request) (any, error) {
		_, p, err := own(r)
		if err != nil {
			return nil, err
		}
		if err := s.store.Update(func(st *store.State) error {
			st.VPN.Peers = slices.DeleteFunc(st.VPN.Peers, func(x store.VPNPeer) bool { return x.ID == p.ID })
			return nil
		}); err != nil {
			return nil, err
		}
		s.audit(r, "vpn", "Revoked VPN device "+p.Name)
		return nil, s.vpnApplyErr()
	})
}
