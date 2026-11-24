package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"strataos/internal/network"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// maskWiFi masks the WiFi passwords of a configuration copy, the way the other
// secrets of the settings are masked in answers.
func maskWiFi(c *network.Config) {
	for i := range c.WiFi {
		if c.WiFi[i].PSK != "" {
			c.WiFi[i].PSK = "********"
		}
	}
}

// restoreMaskedWiFiPSK replaces the masked WiFi passwords of a submitted
// configuration with the saved ones (the UI sends "********" back unchanged).
func restoreMaskedWiFiPSK(req *network.Config, prev network.Config) {
	for i := range req.WiFi {
		if req.WiFi[i].PSK == "********" {
			for _, w := range prev.WiFi {
				if w.Name == req.WiFi[i].Name {
					req.WiFi[i].PSK = w.PSK
				}
			}
		}
	}
}

// wifiRoutes: live state of the wireless links and scan. Changing the WiFi
// credentials goes through the usual PUT /api/system/network/config (applied on
// trial like every network change).
func (s *Server) wifiRoutes() {
	// Live state of every wireless link, with its saved configuration (masked).
	s.admin("GET /api/system/network/wifi", func(r *http.Request) (any, error) {
		var cfg *network.Config
		s.store.Read(func(st *store.State) { cfg = st.Network })
		out := []map[string]any{}
		for _, name := range network.Wireless() {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			st := network.LinkStatus(ctx, name)
			cancel()
			conf := map[string]any{"configured": false, "enabled": false}
			if cfg != nil {
				for _, w := range cfg.WiFi {
					if w.Name == name {
						conf = map[string]any{"configured": true, "ssid": w.SSID, "hidden": w.Hidden, "enabled": w.Enabled, "pskSet": w.PSK != ""}
					}
				}
			}
			out = append(out, map[string]any{"name": name, "status": st, "config": conf})
		}
		return map[string]any{
			"ifaces":       out,
			"wpaInstalled": sys.Has("wpa_supplicant"),
			"iwInstalled":  sys.Has("iw"),
		}, nil
	})

	// Networks visible from one wireless link.
	s.admin("POST /api/system/network/wifi/scan", func(r *http.Request) (any, error) {
		var req struct {
			Iface string `json:"iface"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Iface = strings.TrimSpace(req.Iface)
		wl := map[string]bool{}
		for _, w := range network.Wireless() {
			wl[w] = true
		}
		if !wl[req.Iface] {
			return nil, badRequest("not a wireless interface of this machine")
		}
		if !sys.Has("iw") {
			return nil, badRequest("iw is not installed on the NAS (apt install iw)")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		res, err := network.Scan(ctx, req.Iface)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "network", "WiFi scan on "+req.Iface)
		return map[string]any{"networks": res}, nil
	})
}
