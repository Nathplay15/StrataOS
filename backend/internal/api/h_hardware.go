package api

import (
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"strataos/internal/hardware"
	"strataos/internal/store"
)

// Fan control and front-panel LEDs. With the "bios" profile (the default)
// StrataOS writes nothing; otherwise a loop drives every controllable PWM
// output from the processor and disk temperatures.

var (
	fanCtl  *hardware.Controller
	fanKick = make(chan struct{}, 1)
)

// diskTemps returns the last temperatures read by the health watcher: reading
// SMART here could wake sleeping disks.
func diskTemps() map[string]int {
	out := map[string]int{}
	smartCache.Range(func(k, v any) bool {
		if t := v.(smartEntry).info.Temperature; t > 0 && t < 100 {
			out[k.(string)] = t
		}
		return true
	})
	return out
}

func hottestDisk() float64 {
	best := math.NaN()
	for _, t := range diskTemps() {
		if math.IsNaN(best) || float64(t) > best {
			best = float64(t)
		}
	}
	return best
}

func controllable(pwms []hardware.PWM) []hardware.PWM {
	var out []hardware.PWM
	for _, p := range pwms {
		if p.Controllable {
			out = append(out, p)
		}
	}
	return out
}

// fanSignals restores the firmware fan control when the daemon is stopped
// (systemctl stop sends SIGTERM), then lets the signal terminate it as usual.
type fanSignals struct {
	ch   chan os.Signal
	stop chan struct{}
}

func (f *fanSignals) start(ctl *hardware.Controller) {
	if f.ch != nil {
		return
	}
	f.ch, f.stop = make(chan os.Signal, 1), make(chan struct{})
	signal.Notify(f.ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func(ch chan os.Signal, stop chan struct{}) {
		select {
		case sig := <-ch:
			_ = ctl.Release()
			signal.Reset(sig)
			_ = syscall.Kill(os.Getpid(), sig.(syscall.Signal))
		case <-stop:
		}
	}(f.ch, f.stop)
}

func (f *fanSignals) end() {
	if f.ch == nil {
		return
	}
	signal.Stop(f.ch)
	close(f.stop)
	f.ch, f.stop = nil, nil
}

// hardwareLoop applies the LED settings at startup, then drives the fans.
func (s *Server) hardwareLoop() {
	if fanCtl == nil {
		fanCtl = hardware.NewController(filepath.Join(s.dataDir, "fans-original.json"))
	}
	var h store.Hardware
	s.store.Read(func(st *store.State) { h = st.Hardware })
	for name, mode := range h.LEDs {
		if err := hardware.SetLED(name, mode); err != nil {
			slog.Warn("LED", "name", name, "err", err)
		}
	}
	var sigs fanSignals
	lastErr := ""
	for {
		s.store.Read(func(st *store.State) { h = st.Hardware })
		pwms, _ := hardware.Scan()
		ctl := controllable(pwms)
		cpuCurve, diskCurve := hardware.Curves(h)
		if h.FanProfile == "" || h.FanProfile == hardware.ProfileBIOS || len(ctl) == 0 || cpuCurve == nil {
			if fanCtl.Active() {
				if err := fanCtl.Release(); err != nil {
					slog.Warn("fan control release", "err", err)
				}
				s.store.Log("info", "hardware", "", "Fans given back to the BIOS")
			}
			sigs.end()
		} else {
			if !fanCtl.Active() {
				sigs.start(fanCtl)
			}
			err := fanCtl.Step(ctl, cpuCurve, diskCurve, hardware.Temps{CPU: hardware.CPUTemp(), Disk: hottestDisk()})
			if msg := errString(err); msg != lastErr {
				lastErr = msg
				if msg != "" {
					s.store.Log("warning", "hardware", "", "Fan control: "+msg)
				}
			}
		}
		select {
		case <-fanKick:
		case <-time.After(5 * time.Second):
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) hardwareRoutes() {
	fanCtl = hardware.NewController(filepath.Join(s.dataDir, "fans-original.json"))
	s.admin("GET /api/hardware", func(r *http.Request) (any, error) {
		var h store.Hardware
		s.store.Read(func(st *store.State) { h = st.Hardware })
		if h.FanProfile == "" {
			h.FanProfile = hardware.ProfileBIOS
		}
		pwms, sensors := hardware.Scan()
		if pwms == nil {
			pwms = []hardware.PWM{}
		}
		if sensors == nil {
			sensors = []hardware.FanSensor{}
		}
		dmi := hardware.ReadDMI()
		type diskTemp struct {
			Device string `json:"device"`
			Temp   int    `json:"temp"`
		}
		disks := []diskTemp{}
		for d, t := range diskTemps() {
			disks = append(disks, diskTemp{d, t})
		}
		sort.Slice(disks, func(i, j int) bool { return disks[i].Device < disks[j].Device })
		var cpu *float64
		if t := hardware.CPUTemp(); !math.IsNaN(t) {
			cpu = &t
		}
		// notice explains why fans cannot be driven: "terramaster" (the
		// out-of-tree it87 driver is needed) or "none" (no PWM output).
		notice := ""
		if len(controllable(pwms)) == 0 {
			notice = "none"
			if dmi.TerraMaster() {
				notice = "terramaster"
			}
		}
		pct, fallback, ctlErr := fanCtl.State()
		leds := hardware.LEDs()
		type ledView struct {
			hardware.LED
			Mode string `json:"mode"`
		}
		lv := []ledView{}
		for _, l := range leds {
			lv = append(lv, ledView{LED: l, Mode: h.LEDs[l.Name]})
		}
		return map[string]any{
			"dmi": dmi, "pwms": pwms, "sensors": sensors, "cpuTemp": cpu, "diskTemps": disks,
			"profile": h.FanProfile, "cpuCurve": h.CPUCurve, "diskCurve": h.DiskCurve, "presets": hardware.Presets(),
			"active": fanCtl.Active(), "percent": pct, "fallback": fallback, "error": ctlErr,
			"notice": notice, "leds": lv,
		}, nil
	})
	s.admin("PUT /api/hardware/fans", func(r *http.Request) (any, error) {
		var req struct {
			Profile   string           `json:"profile"`
			CPUCurve  []store.FanPoint `json:"cpuCurve"`
			DiskCurve []store.FanPoint `json:"diskCurve"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		switch req.Profile {
		case hardware.ProfileBIOS, hardware.ProfileQuiet, hardware.ProfileBalanced, hardware.ProfileCool:
			req.CPUCurve, req.DiskCurve = nil, nil
		case hardware.ProfileCustom:
			if err := hardware.ValidateCurve(req.CPUCurve); err != nil {
				return nil, badRequest("processor curve: " + err.Error())
			}
			if err := hardware.ValidateCurve(req.DiskCurve); err != nil {
				return nil, badRequest("disk curve: " + err.Error())
			}
		default:
			return nil, badRequest("unknown fan profile")
		}
		if req.Profile != hardware.ProfileBIOS {
			if pwms, _ := hardware.Scan(); len(controllable(pwms)) == 0 {
				return nil, badRequest("no controllable fans on this hardware")
			}
		}
		if err := s.store.Update(func(st *store.State) error {
			st.Hardware.FanProfile, st.Hardware.CPUCurve, st.Hardware.DiskCurve = req.Profile, req.CPUCurve, req.DiskCurve
			return nil
		}); err != nil {
			return nil, err
		}
		s.audit(r, "hardware", "Fan profile: "+req.Profile)
		select {
		case fanKick <- struct{}{}:
		default:
		}
		return nil, nil
	})
	s.admin("PUT /api/hardware/leds/{name}", func(r *http.Request) (any, error) {
		var req struct {
			Mode string `json:"mode"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := r.PathValue("name")
		found := false
		for _, l := range hardware.LEDs() {
			found = found || l.Name == name
		}
		if !found {
			return nil, notFound("LED not found")
		}
		if req.Mode != "" {
			if err := hardware.SetLED(name, req.Mode); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		s.audit(r, "hardware", "LED "+name+": "+req.Mode)
		return nil, s.store.Update(func(st *store.State) error {
			if req.Mode == "" {
				delete(st.Hardware.LEDs, name)
				return nil
			}
			if st.Hardware.LEDs == nil {
				st.Hardware.LEDs = map[string]string{}
			}
			st.Hardware.LEDs[name] = req.Mode
			return nil
		})
	})
}
