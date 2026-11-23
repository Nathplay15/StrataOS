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
	"strataos/internal/mediaplay"
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

// energyConfig reads the settings, the 0.11 schedule turned into rules.
func (s *Server) energyConfig() store.Energy {
	var c store.Energy
	s.store.Read(func(st *store.State) { c = st.Energy })
	if len(c.Rules) == 0 && c.Schedule.Enabled {
		c.Rules = energy.MigrateSchedule(c.Schedule)
	}
	return c
}

// applyEnergy applies the saved settings (at startup and after a change).
func (s *Server) applyEnergy() []string {
	c := s.energyConfig()
	var problems []string
	if disks := rotatingDisks(); len(disks) > 0 && sys.Has("hdparm") {
		if err := energy.SetSpindown(diskPaths(disks), c.SpindownMin); err != nil {
			problems = append(problems, err.Error())
		} else {
			appliedSpindown = c.SpindownMin
		}
	}
	if c.Governor != "" {
		if err := energy.SetGovernor(c.Governor); err != nil {
			problems = append(problems, err.Error())
		}
	}
	persist := map[string]string{}
	for _, n := range energy.NICs() {
		on := slices.Contains(c.WoL, n.Name)
		if n.Supported && on != n.Enabled {
			if err := energy.SetWoL(n.Name, on); err != nil {
				problems = append(problems, n.Name+": "+err.Error())
			}
		}
		if on && n.Supported {
			persist[n.Name] = energy.PermanentMAC(n.Name)
		}
	}
	// The .link files keep it on from boot, before the daemon starts (and
	// for drivers that turn it off when the card goes down).
	if err := energy.PersistWoL(persist); err != nil {
		problems = append(problems, "Wake-on-LAN: "+err.Error())
	}
	s.armWake(c)
	return problems
}

var wakeArmed time.Time

// appliedSpindown is the spin-down delay currently set on the data disks.
var appliedSpindown = -1

// applyNightSpindown applies the night plan's spin-down during its window,
// the global spindownMin outside (roadmap 0.15, "Night energy plan"). Only
// awake disks are touched: hdparm -S would wake a sleeping one.
func (s *Server) applyNightSpindown(c store.Energy, now time.Time) {
	want := c.SpindownMin
	if c.Night != nil && c.Night.Active(now) && c.Night.SpindownMin > 0 {
		want = c.Night.SpindownMin
	}
	if want == appliedSpindown || !sys.Has("hdparm") {
		return
	}
	var devs []string
	for _, d := range rotatingDisks() {
		if energy.DiskState(d.Path) != "standby" {
			devs = append(devs, d.Path)
		}
	}
	if len(devs) > 0 {
		if err := energy.SetSpindown(devs, want); err != nil {
			return
		}
	}
	appliedSpindown = want
}

// armWake keeps the clock's wake alarm on the next scheduled power-on, so
// that the NAS also comes back after a shutdown by hand.
func (s *Server) armWake(c store.Energy) {
	if !energy.RTCWakeSupported() {
		return
	}
	next := energy.NextWake(c.Rules, time.Now().Add(time.Minute))
	if next.Equal(wakeArmed) {
		return
	}
	if next.IsZero() && wakeArmed.IsZero() {
		return
	}
	if err := energy.SetRTCAlarm(next); err != nil {
		s.store.Log("error", "energy", "", "Could not program the power-on of the NAS: "+err.Error())
		s.notify("error", "Scheduled power-on", "Could not program the power-on of the NAS: "+err.Error())
		return
	}
	wakeArmed = next
}

// usersActive names what shows someone is using the NAS ("" when nobody):
// files open over SMB, a shell session, a video being played. Nothing here
// wakes a sleeping disk.
func usersActive() string {
	if n := len(smbOpenFiles()); n > 0 {
		return fmt.Sprintf("%d files open over SMB", n)
	}
	if out, err := sys.Run("who"); err == nil {
		if n := len(strings.Fields(strings.TrimSpace(out))); n > 0 {
			return "a shell session is open"
		}
	}
	if mediaplay.Default.Active() > 0 {
		return "a video is being played"
	}
	return ""
}

// smbOpenFiles lists the files open over SMB (smbstatus reads Samba's
// lock database: no disk of the volumes is touched).
func smbOpenFiles() []string {
	if !sys.Has("smbstatus") {
		return nil
	}
	out, err := sys.Run("smbstatus", "-L", "-n")
	if err != nil {
		return nil
	}
	var files []string
	data := false
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "-----") {
			data = true
			continue
		}
		if data && strings.TrimSpace(l) != "" {
			files = append(files, l)
		}
	}
	return files
}

// powerTexts are the messages of a scheduled shutdown or restart, whole
// sentences so that they translate (frontend/src/i18n/server.ts).
type powerTexts struct {
	title, warn, postponedTitle, postponed, skippedTitle, skipped, failedTitle, failed string
}

func powerTextsOf(action string) powerTexts {
	if action == energy.ActRestart {
		return powerTexts{"Scheduled restart", "Scheduled restart of the NAS at %s",
			"Scheduled restart postponed", "Scheduled restart postponed: %s",
			"Scheduled restart skipped", "Scheduled restart skipped: %s",
			"Scheduled restart failed", "Scheduled restart failed: %s"}
	}
	return powerTexts{"Scheduled shutdown", "Scheduled shutdown of the NAS at %s",
		"Scheduled shutdown postponed", "Scheduled shutdown postponed: %s",
		"Scheduled shutdown skipped", "Scheduled shutdown skipped: %s",
		"Scheduled shutdown failed", "Scheduled shutdown failed: %s"}
}

// energyLoop applies the settings at startup, then runs the power rules.
func (s *Server) energyLoop() {
	time.Sleep(20 * time.Second)
	if p := s.applyEnergy(); len(p) > 0 {
		slog.Warn("energy settings", "problems", strings.Join(p, "; "))
	}
	last := time.Now()
	var warned time.Time  // occurrence already announced
	var pending time.Time // occurrence postponed (its first planned time)
	var pendingRule energy.PowerRule
	var retryAt time.Time
	for {
		time.Sleep(30 * time.Second)
		c := s.energyConfig()
		s.armWake(c)
		now := time.Now()
		s.applyNightSpindown(c, now)
		due, rule := energy.NextAction(c.Rules, last)
		if !pending.IsZero() {
			due, rule = pending, pendingRule
		}
		if due.IsZero() {
			last = now
			continue
		}
		t := powerTextsOf(rule.Action)
		if w := c.Guard.Warn(); w > 0 && pending.IsZero() && due.Sub(now) > 0 && due.Sub(now) <= w && !warned.Equal(due) {
			warned = due
			msg := fmt.Sprintf(t.warn, due.Format("15:04"))
			s.store.Log("info", "energy", "", msg)
			s.notify("info", t.title, msg)
		}
		if now.Before(due) || (!retryAt.IsZero() && now.Before(retryAt)) {
			if pending.IsZero() {
				last = now
			}
			continue
		}
		user := ""
		if c.Guard.Users {
			user = usersActive()
		}
		verdict, why := energy.Decide(c.Guard, due, now, s.maintenanceBusy(), user)
		switch verdict {
		case energy.Postpone:
			if pending.IsZero() {
				msg := fmt.Sprintf(t.postponed, why)
				s.store.Log("warning", "energy", "", msg)
				s.notify("warning", t.postponedTitle, msg)
			}
			pending, pendingRule, retryAt = due, rule, now.Add(c.Guard.RetryDelay())
			continue
		case energy.Skip:
			msg := fmt.Sprintf(t.skipped, why)
			s.store.Log("warning", "energy", "", msg)
			s.notify("warning", t.skippedTitle, msg)
			pending, retryAt, last = time.Time{}, time.Time{}, now
			continue
		}
		pending, retryAt, last = time.Time{}, time.Time{}, now
		var err error
		if rule.Action == energy.ActRestart {
			s.store.Log("info", "energy", "", "Scheduled restart of the NAS")
			err = energy.Restart()
		} else {
			wake := energy.WakeAfterShutdown(c.Rules, now)
			msg := "Scheduled shutdown of the NAS"
			if !wake.IsZero() && energy.RTCWakeSupported() {
				msg = fmt.Sprintf("Scheduled shutdown of the NAS, power-on planned on %s", wake.Format("2006-01-02 15:04"))
			}
			s.store.Log("info", "energy", "", msg)
			err = energy.PowerOff(wake)
		}
		if err != nil {
			msg := fmt.Sprintf(t.failed, err.Error())
			s.store.Log("error", "energy", "", msg)
			s.notify("error", t.failedTitle, msg)
		}
		time.Sleep(2 * time.Minute)
	}
}

func (s *Server) energyRoutes() {
	// Consumption history (hwmon), when the hardware reports it (roadmap 0.15).
	s.admin("GET /api/energy/power", func(r *http.Request) (any, error) {
		now := energy.PowerNow()
		if now <= 0 { // fallback: the UPS
			if uc := s.upsConfig(); uc.Mode != "off" && ups.Installed() {
				if v, err := ups.Status(uc); err == nil {
					if f, err := strconv.ParseFloat(v["ups.realpower"], 64); err == nil && f > 0 {
						now = f
					}
				}
			}
		}
		return map[string]any{"now": now, "history": s.hist.Power()}, nil
	})

	s.admin("GET /api/energy", func(r *http.Request) (any, error) {
		c := s.energyConfig()
		if c.Rules == nil {
			c.Rules = []energy.PowerRule{}
		}
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
		now := time.Now()
		if n, r := energy.NextAction(c.Rules, now); !n.IsZero() {
			out["nextAction"] = map[string]any{"time": n, "action": r.Action, "rule": r.ID}
			if r.Action == energy.ActShutdown {
				out["nextShutdown"] = n
				if w := energy.WakeAfterShutdown(c.Rules, n); !w.IsZero() {
					out["nextWake"] = w
				}
			}
		}
		if w := energy.NextWake(c.Rules, now); !w.IsZero() {
			out["nextPowerOn"] = w
		}
		if a := energy.RTCAlarm(); !a.IsZero() {
			out["rtcAlarm"] = a
		}
		persisted := energy.PersistedWoL()
		if persisted == nil {
			persisted = []string{}
		}
		out["wolPersisted"] = persisted
		out["timezone"] = now.Location().String()
		out["nightActive"] = c.Night != nil && c.Night.Active(now)
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
		if req.Night != nil {
			if err := req.Night.Validate(); err != nil {
				return nil, badRequest("night plan: " + err.Error())
			}
		}
		slices.Sort(req.Schedule.Days)
		req.Schedule.Days = slices.Compact(req.Schedule.Days)
		// A file of 0.11 (configuration import) only has the schedule.
		if len(req.Rules) == 0 && req.Schedule.Enabled {
			req.Rules = energy.MigrateSchedule(req.Schedule)
		}
		req.Schedule = energy.Schedule{}
		if err := energy.ValidateRules(req.Rules); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := req.Guard.Validate(); err != nil {
			return nil, badRequest(err.Error())
		}
		for i := range req.Rules {
			slices.Sort(req.Rules[i].Days)
			req.Rules[i].Days = slices.Compact(req.Rules[i].Days)
		}
		if !energy.RTCWakeSupported() && slices.ContainsFunc(req.Rules, func(r energy.PowerRule) bool { return r.Enabled && r.Action == energy.ActWake }) {
			return nil, badRequest("this machine's clock cannot power it on: schedule the power-on in its firmware (BIOS/UEFI) or wake it over the network")
		}
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
		s.audit(r, "energy", fmt.Sprintf("Energy settings: spin-down %d min, CPU %q, %d power rules", req.SpindownMin, req.Governor, len(req.Rules)))
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
