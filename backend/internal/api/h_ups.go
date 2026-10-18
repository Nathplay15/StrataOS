package api

import (
	"net/http"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/ups"
)

func (s *Server) upsConfig() ups.Config {
	c := ups.Config{Mode: "off"}
	s.store.Read(func(st *store.State) {
		if st.UPS != nil {
			c = *st.UPS
		}
	})
	return c
}

// upsWatcher polls the UPS and turns status changes into events and notifications.
func (s *Server) upsWatcher() {
	last := ""
	for {
		time.Sleep(10 * time.Second)
		c := s.upsConfig()
		if c.Mode == "off" || !ups.Installed() {
			last = ""
			continue
		}
		vars, err := ups.Status(c)
		status := "COMMBAD"
		if err == nil {
			status = vars["ups.status"]
		}
		if status == last {
			continue
		}
		prev := last
		last = status
		if prev == "" && strings.HasPrefix(status, "OL") {
			continue // first reading after start
		}
		var msg, level string
		switch {
		case strings.Contains(status, "LB"):
			level, msg = "error", "UPS battery low: the NAS is shutting down"
		case strings.HasPrefix(status, "OB"):
			level, msg = "warning", "Power outage: the NAS runs on battery"
			if c.ShutdownAfter > 0 {
				msg += " (automatic shutdown in " + (time.Duration(c.ShutdownAfter) * time.Second).String() + " if power does not return)"
			}
		case strings.HasPrefix(status, "OL") && strings.HasPrefix(prev, "OB"):
			level, msg = "info", "Power restored: the UPS is back on mains"
		case status == "COMMBAD":
			level, msg = "warning", "Communication with the UPS lost"
		default:
			continue
		}
		s.store.Log(level, "ups", "", msg)
		s.notify("UPS", msg)
	}
}

func (s *Server) upsRoutes() {
	go s.upsWatcher()

	s.admin("GET /api/ups", func(r *http.Request) (any, error) {
		c := s.upsConfig()
		out := map[string]any{"installed": ups.Installed()}
		if c.RemotePassword != "" {
			c.RemotePassword = masked
		}
		if c.SharePassword != "" {
			c.SharePassword = masked
		}
		c.LocalPassword = ""
		out["config"] = c
		if c.Mode != "off" && ups.Installed() {
			if vars, err := ups.Status(s.upsConfig()); err == nil {
				out["status"] = ups.Summary(vars)
			} else {
				out["error"] = err.Error()
			}
		}
		return out, nil
	})

	s.admin("PUT /api/ups", func(r *http.Request) (any, error) {
		var req ups.Config
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		old := s.upsConfig()
		if req.RemotePassword == masked {
			req.RemotePassword = old.RemotePassword
		}
		if req.SharePassword == masked {
			req.SharePassword = old.SharePassword
		}
		req.LocalPassword = old.LocalPassword
		if err := ups.Validate(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := ups.Apply(req); err != nil {
			return nil, badRequest(err.Error())
		}
		_ = s.store.Update(func(st *store.State) error {
			c := req
			st.UPS = &c
			return nil
		})
		s.audit(r, "ups", "UPS configuration: "+req.Mode)
		return nil, nil
	})

	s.admin("GET /api/ups/detect", func(r *http.Request) (any, error) {
		list, err := ups.Detect()
		if list == nil && err == nil {
			list = []ups.Detected{}
		}
		return list, err
	})

	// Simulation mode only: change the simulated power state to test the policy.
	s.admin("POST /api/ups/simulate", func(r *http.Request) (any, error) {
		if s.upsConfig().Mode != "simulation" {
			return nil, badRequest("the UPS is not in simulation mode")
		}
		var req struct {
			Status string `json:"status"`
			Charge int    `json:"charge"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Charge < 0 || req.Charge > 100 {
			return nil, badRequest("invalid charge")
		}
		s.audit(r, "ups", "Simulated UPS state: "+req.Status)
		return nil, ups.SetSimulation(req.Status, req.Charge)
	})
}
