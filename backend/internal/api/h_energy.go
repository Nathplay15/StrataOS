package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"strataos/internal/energy"
	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/ups"
)

func rotatingDisks() []storage.BlockDevice {
	disks, _ := storage.ListDisks()
	var out []storage.BlockDevice
	for _, d := range disks {
		// Real SATA/SAS hard disks only (not USB, NVMe or virtual disks).
		if d.Rotational && strings.HasPrefix(d.Path, "/dev/sd") && d.Transport != "usb" {
			out = append(out, d)
		}
	}
	return out
}

func diskPaths(l []storage.BlockDevice) []string {
	var p []string
	for _, d := range l {
		p = append(p, d.Path)
	}
	return p
}

// applyEnergy applies the saved settings (at startup and after a change).
func (s *Server) applyEnergy() []string {
	var c store.Energy
	s.store.Read(func(st *store.State) { c = st.Energy })
	var problems []string
	if disks := rotatingDisks(); len(disks) > 0 && sys.Has("hdparm") {
		if err := energy.SetSpindown(diskPaths(disks), c.SpindownMin); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if c.Governor != "" {
		if err := energy.SetGovernor(c.Governor); err != nil {
			problems = append(problems, err.Error())
		}
	}
	for _, n := range energy.NICs() {
		on := slices.Contains(c.WoL, n.Name)
		if n.Supported && on != n.Enabled {
			if err := energy.SetWoL(n.Name, on); err != nil {
				problems = append(problems, n.Name+": "+err.Error())
			}
		}
	}
	return problems
}

// energyLoop applies the settings at startup, then runs the shutdown schedule.
func (s *Server) energyLoop() {
	time.Sleep(20 * time.Second)
	if p := s.applyEnergy(); len(p) > 0 {
		slog.Warn("energy settings", "problems", strings.Join(p, "; "))
	}
	var warned, postponed time.Time
	for {
		time.Sleep(30 * time.Second)
		var c store.Energy
		s.store.Read(func(st *store.State) { c = st.Energy })
		now := time.Now()
		next := c.Schedule.NextShutdown(now.Add(-time.Minute))
		if !postponed.IsZero() {
			next = postponed
		}
		if next.IsZero() {
			continue
		}
		if d := next.Sub(now); d > 0 && d <= 10*time.Minute && !warned.Equal(next) {
			warned = next
			s.store.Log("info", "energy", "", fmt.Sprintf("Arrêt programmé à %s", next.Format("15:04")))
		}
		if now.Before(next) {
			continue
		}
		// Never cut a running backup, update or installation: wait for it.
		busy := ""
		for _, j := range s.jobs.List() {
			if j.Status == jobs.Running {
				busy = j.Title
			}
		}
		if busy != "" {
			postponed = now.Add(10 * time.Minute)
			s.store.Log("warning", "energy", "", fmt.Sprintf("Arrêt programmé reporté de 10 minutes : tâche en cours (%s)", busy))
			continue
		}
		postponed = time.Time{}
		wake := c.Schedule.WakeAfter(next)
		msg := "Arrêt programmé du NAS"
		if !wake.IsZero() {
			msg = fmt.Sprintf("Arrêt programmé du NAS, redémarrage prévu le %s", wake.Format("02/01 15:04"))
		}
		s.store.Log("info", "energy", "", msg)
		if err := energy.PowerOff(wake); err != nil {
			s.store.Log("error", "energy", "", "Arrêt programmé impossible : "+err.Error())
		}
		time.Sleep(2 * time.Minute)
	}
}

func (s *Server) energyRoutes() {
	s.admin("GET /api/energy", func(r *http.Request) (any, error) {
		var c store.Energy
		s.store.Read(func(st *store.State) { c = st.Energy })
		if c.Devices == nil {
			c.Devices = []store.WakeDevice{}
		}
		if c.Schedule.Days == nil {
			c.Schedule.Days = []int{}
		}
		type disk struct {
			Path  string `json:"path"`
			Model string `json:"model"`
			Size  int64  `json:"size"`
			State string `json:"state"`
		}
		disks := []disk{}
		for _, d := range rotatingDisks() {
			disks = append(disks, disk{d.Path, d.Model, d.Size, energy.DiskState(d.Path)})
		}
		gov, govs := energy.Governors()
		nics := energy.NICs()
		if nics == nil {
			nics = []energy.NIC{}
		}
		out := map[string]any{
			"config": c, "disks": disks, "hdparm": sys.Has("hdparm"),
			"governor": gov, "governors": govs, "rtcWake": energy.RTCWakeSupported(),
			"nics": nics,
		}
		if n := c.Schedule.NextShutdown(time.Now()); !n.IsZero() {
			out["nextShutdown"] = n
			if w := c.Schedule.WakeAfter(n); !w.IsZero() {
				out["nextWake"] = w
			}
		}
		// Power drawn, when an UPS reports it.
		if uc := s.upsConfig(); uc.Mode != "off" && ups.Installed() {
			if v, err := ups.Status(uc); err == nil {
				p := map[string]float64{}
				for _, k := range []string{"ups.realpower", "ups.power", "ups.load", "ups.realpower.nominal", "ups.power.nominal"} {
					if f, err := strconv.ParseFloat(v[k], 64); err == nil {
						p[k] = f
					}
				}
				watts := p["ups.realpower"]
				if watts == 0 && p["ups.load"] > 0 && p["ups.realpower.nominal"] > 0 {
					watts = p["ups.load"] / 100 * p["ups.realpower.nominal"]
				}
				if watts > 0 {
					out["watts"] = watts
				}
				if p["ups.load"] > 0 {
					out["upsLoad"] = p["ups.load"]
				}
			}
		}
		return out, nil
	})

	s.admin("PUT /api/energy", func(r *http.Request) (any, error) {
		var req store.Energy
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.SpindownMin < 0 || req.SpindownMin > 330 {
			return nil, badRequest("invalid spin-down delay")
		}
		if req.Governor != "" {
			if _, av := energy.Governors(); !slices.Contains(av, req.Governor) {
				return nil, badRequest("unsupported CPU policy")
			}
		}
		if err := req.Schedule.Validate(); err != nil {
			return nil, badRequest(err.Error())
		}
		slices.Sort(req.Schedule.Days)
		req.Schedule.Days = slices.Compact(req.Schedule.Days)
		nics := energy.NICs()
		for _, n := range req.WoL {
			if !slices.ContainsFunc(nics, func(x energy.NIC) bool { return x.Name == n && x.Supported }) {
				return nil, badRequest(n + " does not support Wake-on-LAN")
			}
		}
		if len(req.Devices) > 100 {
			return nil, badRequest("too many devices")
		}
		for i, d := range req.Devices {
			hw, err := energy.ParseMAC(d.MAC)
			if err != nil {
				return nil, badRequest(err.Error())
			}
			name := strings.TrimSpace(d.Name)
			if name == "" || len(name) > 60 || strings.ContainsAny(name, "\n\r") {
				return nil, badRequest("invalid device name")
			}
			req.Devices[i] = store.WakeDevice{Name: name, MAC: hw.String()}
		}
		if err := s.store.Update(func(st *store.State) error { st.Energy = req; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "energy", fmt.Sprintf("Energy settings: spin-down %d min, CPU %q, schedule %v", req.SpindownMin, req.Governor, req.Schedule.Enabled))
		if p := s.applyEnergy(); len(p) > 0 {
			return map[string]string{"warning": strings.Join(p, "; ")}, nil
		}
		return nil, nil
	})

	// Puts the rotating disks to sleep now (test of the spin-down).
	s.admin("POST /api/energy/standby", func(r *http.Request) (any, error) {
		if !sys.Has("hdparm") {
			return nil, badRequest("hdparm is not installed")
		}
		var errs []string
		for _, d := range rotatingDisks() {
			if err := energy.Standby(d.Path); err != nil {
				errs = append(errs, d.Path+": "+err.Error())
			}
		}
		if len(errs) > 0 {
			return map[string]string{"warning": strings.Join(errs, "; ")}, nil
		}
		return nil, nil
	})

	s.admin("POST /api/energy/wake", func(r *http.Request) (any, error) {
		var req struct {
			MAC string `json:"mac"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := energy.Wake(req.MAC); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "energy", "Wake-on-LAN packet sent to "+req.MAC)
		return nil, nil
	})
}
