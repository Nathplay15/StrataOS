package api

// Update channel (stable or beta) and scheduled automatic updates.
//
// The beta channel adds the repository's "testing" suite (pre-releases and
// final releases) next to the package's own sources (see
// system.BetaSources). Automatic updates run in a maintenance window: an
// update check, then, as chosen, the Debian security updates, the other
// system updates and StrataOS, after a restore point. They use the update
// job of manual and grouped updates and wait for any of them (and for
// backups, scrubs and other tasks), like the scheduled shutdown.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/release"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

const (
	autoWindowLen = 3 * time.Hour
	autoRetry     = 10 * time.Minute
)

// autoTestHook: when this file exists (root only), a window opens now and
// the automatic update only simulates. Used by the end-to-end test.
var autoTestHook = "/run/strata/auto-update-test"

func autoDays(c store.AutoUpdates, d time.Weekday) bool {
	return len(c.Days) == 0 || slices.Contains(c.Days, int(d))
}

func windowStart(day time.Time, hour int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, day.Location())
}

// autoWindow returns the start of the maintenance window now is in.
func autoWindow(c store.AutoUpdates, now time.Time) (time.Time, bool) {
	for _, back := range []int{0, -1} { // a late window may run past midnight
		ws := windowStart(now.AddDate(0, 0, back), c.Hour)
		if autoDays(c, ws.Weekday()) && !now.Before(ws) && now.Before(ws.Add(autoWindowLen)) {
			return ws, true
		}
	}
	return time.Time{}, false
}

// nextAutoWindow returns the next window that will run: the current one
// when it has not run yet, else the next start.
func nextAutoWindow(c store.AutoUpdates, now time.Time) time.Time {
	if !c.Enabled() {
		return time.Time{}
	}
	if ws, ok := autoWindow(c, now); ok && (c.Last == nil || c.Last.Window.Before(ws)) {
		return ws
	}
	for i := 0; i < 8; i++ {
		ws := windowStart(now.AddDate(0, 0, i), c.Hour)
		if ws.After(now) && autoDays(c, ws.Weekday()) {
			return ws
		}
	}
	return time.Time{}
}

// autoDecide tells what the scheduler does now: "" (nothing), run,
// postpone (a task is running: try again in 10 minutes) or skip (still busy
// at the end of the window). busy is only asked inside a window.
func autoDecide(c store.AutoUpdates, now time.Time, busy func() string, retryAt time.Time) (action string, window time.Time, reason string) {
	if !c.Enabled() {
		return "", time.Time{}, ""
	}
	ws, ok := autoWindow(c, now)
	if !ok || (c.Last != nil && !c.Last.Window.Before(ws)) || now.Before(retryAt) {
		return "", time.Time{}, ""
	}
	if b := busy(); b != "" {
		if now.Add(autoRetry).Before(ws.Add(autoWindowLen)) {
			return "postpone", ws, b
		}
		return "skip", ws, b
	}
	return "run", ws, ""
}

// maintenanceBusy names a running task an automatic update must not
// disturb: a job (backup, update, installation…), a RAID sync, a ZFS scrub
// or resilver, a Btrfs scrub.
func (s *Server) maintenanceBusy() string {
	for _, j := range s.jobs.List() {
		if j.Status == jobs.Running {
			return j.Title
		}
	}
	if aptBusy() {
		return "apt"
	}
	if raids, err := storage.ListRaid(); err == nil {
		for _, a := range raids {
			if act := raidAction(a); act != "" {
				return "RAID " + firstNonEmpty(a.Label, a.Name) + ": " + actionWord(act)
			}
		}
	}
	if pools, err := storage.ListZPools(false); err == nil {
		for _, p := range pools {
			if z, err := storage.ZPoolScan(p.Name); err == nil && z.Running {
				return "ZFS " + p.Name + ": " + z.Kind
			}
		}
	}
	var vols []store.Volume
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			if v.FSType == "btrfs" && v.MountPoint != "" {
				vols = append(vols, *v)
			}
		}
	})
	for _, v := range vols {
		if out, err := sys.Run("btrfs", "scrub", "status", v.MountPoint); err == nil && btrfsScrubRunning(out) {
			return "Btrfs " + v.Name + ": scrub"
		}
	}
	return ""
}

func btrfsScrubRunning(status string) bool {
	for _, l := range strings.Split(status, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && strings.TrimSpace(k) == "Status" {
			return strings.TrimSpace(v) == "running"
		}
	}
	return false
}

func (s *Server) autoConfig() store.AutoUpdates {
	var c store.AutoUpdates
	s.store.Read(func(st *store.State) { c = st.Updates })
	return c
}

func (s *Server) saveAutoRun(r store.AutoUpdateRun) {
	_ = s.store.Update(func(st *store.State) error {
		st.Updates.Last = &r
		return nil
	})
}

// autoUpdateLoop finishes a run the StrataOS update interrupted, then opens
// the maintenance windows.
func (s *Server) autoUpdateLoop() {
	time.Sleep(40 * time.Second)
	s.autoUpdateResume()
	var retryAt time.Time
	for {
		c := s.autoConfig()
		now := time.Now()
		if _, err := os.Stat(autoTestHook); err == nil {
			_ = os.Remove(autoTestHook)
			s.autoUpdateStart(c, now.Truncate(time.Minute), true)
		} else {
			action, ws, reason := autoDecide(c, now, s.maintenanceBusy, retryAt)
			switch action {
			case "postpone":
				retryAt = now.Add(autoRetry)
				s.store.Log("warning", "system", "", fmt.Sprintf("Automatic updates postponed by 10 minutes: task running (%s)", reason))
			case "skip":
				retryAt = time.Time{}
				t := time.Now()
				s.saveAutoRun(store.AutoUpdateRun{Window: ws, Started: t, Finished: &t, Status: "skipped", Message: reason})
				msg := fmt.Sprintf("Automatic updates skipped: a task was running until the end of the maintenance window (%s)", reason)
				s.store.Log("warning", "system", "", msg)
				s.notify("warning", "Automatic updates", msg)
			case "run":
				retryAt = time.Time{}
				s.autoUpdateStart(c, ws, false)
			}
		}
		time.Sleep(20 * time.Second)
	}
}

func (s *Server) autoUpdateStart(c store.AutoUpdates, ws time.Time, dryRun bool) {
	title := "Automatic update"
	if dryRun {
		title = "Automatic update (simulation)"
	}
	// Recorded before the job starts, so the window never runs twice.
	s.saveAutoRun(store.AutoUpdateRun{Window: ws, Started: time.Now(), Status: "running", DryRun: dryRun, From: s.version})
	s.jobs.Start("apt", title, "", func(ctx context.Context, j *jobs.Job) error {
		defer fleetInfoReset()
		run := store.AutoUpdateRun{Window: ws, Started: time.Now(), Status: "running", DryRun: dryRun, Job: j.ID, From: s.version}
		s.saveAutoRun(run)
		err := s.autoUpdateSteps(ctx, j, c, &run)
		s.finishAutoRun(c, run, err)
		return err
	})
}

func isSecuritySource(src string) bool {
	for _, x := range strings.Split(src, ",") {
		if strings.HasSuffix(x, "-security") {
			return true
		}
	}
	return false
}

// autoUpdateSteps checks for updates and installs what the settings ask,
// StrataOS last (its service restarts: the new one finishes the run).
func (s *Server) autoUpdateSteps(ctx context.Context, j *jobs.Job, c store.AutoUpdates, run *store.AutoUpdateRun) error {
	if run.DryRun {
		j.Logf("Simulation: nothing is installed and no restore point is taken")
	}
	if err := s.runUpdate(ctx, j, "check", false); err != nil {
		return err
	}
	up, err := system.ListUpgradable()
	if err != nil {
		return err
	}
	sec, others := 0, 0
	for _, u := range up {
		switch {
		case u.Package == "strataos":
		case isSecuritySource(u.Source):
			sec++
		default:
			others++
		}
	}
	installed, cand := strataPolicy()
	if cand == "(none)" || cand == installed {
		cand = ""
	}
	j.Logf("Updates found: %d security, %d other, StrataOS %s", sec, others, firstNonEmpty(cand, "up to date"))
	taken := false
	restore := func() {
		if !run.DryRun && !taken {
			s.restorePointBeforeUpdate(j)
			taken = true
		}
	}
	switch {
	case c.System == store.UpdateInstall:
		if sec+others > 0 {
			restore()
			if err := s.runUpdate(ctx, j, "others", run.DryRun); err != nil {
				return err
			}
			run.Security, run.Others = sec, others
		}
	case c.Security == store.UpdateInstall:
		// Even with nothing seen in the system lists: the security archive
		// is read on its own.
		restore()
		if err := s.runUpdate(ctx, j, "security", run.DryRun); err != nil {
			return err
		}
		run.Security = sec
		if c.System == store.UpdateNotify {
			run.Available = others
		}
	case c.System == store.UpdateNotify:
		run.Available = sec + others
	}
	if cand != "" {
		switch c.Strata {
		case store.UpdateNotify:
			run.Offered = cand
		case store.UpdateInstall:
			restore()
			run.StrataTo = cand
			if !run.DryRun {
				run.Phase = "strata"
				s.saveAutoRun(*run)
			}
			if err := s.runUpdate(ctx, j, "strata", run.DryRun); err != nil {
				run.Phase = ""
				return err
			}
			run.Phase = ""
		}
	}
	return nil
}

// autoUpdateResume finishes a run that was installing StrataOS when the
// service restarted (or marks an interrupted one as failed).
func (s *Server) autoUpdateResume() {
	c := s.autoConfig()
	if c.Last == nil || c.Last.Status != "running" {
		return
	}
	run := *c.Last
	if run.Phase != "strata" {
		s.finishAutoRun(c, run, errors.New("interrupted (the NAS or its service restarted)"))
		return
	}
	// The package may still be configuring while this new service starts.
	for i := 0; i < 360 && aptBusy(); i++ {
		time.Sleep(10 * time.Second)
	}
	var err error
	if installed, _ := strataPolicy(); installed != run.StrataTo || s.version != run.StrataTo {
		err = fmt.Errorf("StrataOS %s is running (expected %s)", s.version, run.StrataTo)
	}
	run.Phase = ""
	s.finishAutoRun(c, run, err)
}

// finishAutoRun records the result, logs and notifies it, and restarts the
// NAS when allowed and needed.
func (s *Server) finishAutoRun(c store.AutoUpdates, run store.AutoUpdateRun, err error) {
	now := time.Now()
	run.Finished, run.Phase = &now, ""
	run.Status = "success"
	if err != nil {
		run.Status, run.Message = "failed", err.Error()
	}
	if system.RebootRequired() {
		run.Restart = "required"
		if c.Reboot && !run.DryRun && err == nil {
			run.Restart = "done"
		}
	}
	n := run.Security + run.Others
	if run.StrataTo != "" {
		n++
	}
	type event struct{ level, subject, msg string }
	var evs []event
	switch {
	case err != nil:
		evs = append(evs, event{"error", "Automatic updates", fmt.Sprintf("Automatic updates failed: %v", err)})
	case run.DryRun:
		evs = append(evs, event{"info", "Automatic updates", fmt.Sprintf("Automatic updates simulated: %d package(s) would be updated", n)})
	case n > 0:
		evs = append(evs, event{"info", "Automatic updates", fmt.Sprintf("Automatic updates finished: %d package(s) updated", n)})
		if run.StrataTo != "" {
			evs = append(evs, event{"info", "Automatic updates", fmt.Sprintf("StrataOS updated automatically to %s", run.StrataTo)})
		}
	default:
		s.store.Log("info", "system", "", "Automatic updates: nothing to install")
	}
	// "Notify only": once per new set of updates.
	offer := ""
	if run.Offered != "" || run.Available > 0 {
		offer = fmt.Sprintf("%s/%d", run.Offered, run.Available)
	}
	if offer != "" && (run.DryRun || offer != c.Notified) {
		if run.Offered != "" {
			evs = append(evs, event{"info", "Updates available", fmt.Sprintf("StrataOS %s is available: install it from Updates", run.Offered)})
		}
		if run.Available > 0 {
			evs = append(evs, event{"info", "Updates available", fmt.Sprintf("%d system update(s) available: install them from Updates", run.Available)})
		}
	}
	switch run.Restart {
	case "required":
		evs = append(evs, event{"warning", "Automatic updates", "A restart is required to finish the updates"})
	case "done":
		evs = append(evs, event{"warning", "Automatic updates", "Restarting the NAS after the automatic updates"})
	}
	_ = s.store.Update(func(st *store.State) error {
		st.Updates.Last = &run
		if !run.DryRun {
			st.Updates.Notified = offer
		}
		return nil
	})
	for _, e := range evs {
		s.store.Log(e.level, "system", "", e.msg)
		s.notify(e.level, e.subject, e.msg)
	}
	if run.Restart == "done" {
		go func() {
			time.Sleep(30 * time.Second) // let the notifications leave
			_ = system.Power("reboot")
		}()
	}
}

// latestStable is the newest StrataOS of the stable suite ("" if unknown).
func latestStable() string {
	out, _ := sys.Run("apt-cache", "madison", "strataos")
	return system.Newest(system.MadisonVersions(out, release.Stable))
}

func validAutoUpdates(c store.AutoUpdates) error {
	if !slices.Contains([]string{store.UpdateOff, store.UpdateNotify, store.UpdateInstall}, c.Strata) ||
		!slices.Contains([]string{store.UpdateOff, store.UpdateInstall}, c.Security) ||
		!slices.Contains([]string{store.UpdateOff, store.UpdateNotify, store.UpdateInstall}, c.System) {
		return badRequest("invalid automatic update mode")
	}
	if c.Hour < 0 || c.Hour > 23 || len(c.Days) > 7 {
		return badRequest("invalid maintenance window")
	}
	for _, d := range c.Days {
		if d < 0 || d > 6 {
			return badRequest("invalid maintenance window")
		}
	}
	return nil
}

func (s *Server) updateRoutes() {
	s.admin("GET /api/system/updates/auto", func(r *http.Request) (any, error) {
		c := s.autoConfig()
		installed, _ := strataPolicy()
		if installed == "" || installed == "(none)" {
			installed = s.version
		}
		_, err := os.Stat(system.StrataSources)
		out := map[string]any{
			"strata": c.Strata, "security": c.Security, "system": c.System,
			"days": c.Days, "hour": c.Hour, "reboot": c.Reboot,
			"channel": system.Channel(), "repository": err == nil,
			"installed": installed, "beta": release.Prerelease(installed),
		}
		if c.Days == nil {
			out["days"] = []int{}
		}
		if !c.Enabled() && c.Hour == 0 && len(c.Days) == 0 {
			out["hour"] = 3 // suggested window: 03:00
		}
		if c.Last != nil {
			out["last"] = c.Last
		}
		if n := nextAutoWindow(c, time.Now()); !n.IsZero() {
			out["nextWindow"] = n
		}
		// Back on stable with a pre-release installed: apt moves on at the
		// next stable version newer than it; going down needs a downgrade.
		if out["channel"] == "stable" && release.Prerelease(installed) {
			if v := latestStable(); v != "" {
				out["latestStable"] = v
				if release.Compare(v, installed) < 0 {
					out["downgrade"] = v
				}
			}
		}
		return out, nil
	})

	s.admin("PUT /api/system/updates/auto", func(r *http.Request) (any, error) {
		var req store.AutoUpdates
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := validAutoUpdates(req); err != nil {
			return nil, err
		}
		slices.Sort(req.Days)
		req.Days = slices.Compact(req.Days)
		if len(req.Days) == 7 {
			req.Days = nil
		}
		_ = s.store.Update(func(st *store.State) error {
			req.Channel, req.Notified, req.Last = st.Updates.Channel, st.Updates.Notified, st.Updates.Last
			st.Updates = req
			return nil
		})
		s.audit(r, "system", "Automatic update settings saved")
		return nil, nil
	})

	// Switches the channel, then refreshes the package lists (the job).
	s.admin("PUT /api/system/updates/channel", func(r *http.Request) (any, error) {
		var req struct {
			Channel string `json:"channel"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Channel != "stable" && req.Channel != "beta" {
			return nil, badRequest("invalid update channel")
		}
		if err := system.SetChannel(req.Channel); err != nil {
			return nil, badRequest(err.Error())
		}
		_ = s.store.Update(func(st *store.State) error {
			st.Updates.Channel = req.Channel
			if req.Channel == "stable" {
				st.Updates.Channel = ""
			}
			return nil
		})
		if req.Channel == "beta" {
			s.audit(r, "system", "Update channel set to Beta")
		} else {
			s.audit(r, "system", "Update channel set to Stable")
		}
		if s.updateRunning() {
			return nil, nil // the running update refreshes the lists anyway
		}
		return s.jobs.Start("apt", "Checking for updates", userOf(r), s.updateJob("check", false)), nil
	})

	// Back from a pre-release to the latest stable version (explicitly
	// confirmed in the interface), after a restore point.
	s.admin("POST /api/system/updates/downgrade", func(r *http.Request) (any, error) {
		var req struct {
			Version string `json:"version"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if release.Valid(req.Version) != nil || release.Prerelease(req.Version) {
			return nil, badRequest("invalid version")
		}
		if system.Channel() != "stable" {
			return nil, badRequest("choose the stable channel first")
		}
		installed, _ := strataPolicy()
		if !release.Prerelease(installed) || release.Compare(req.Version, installed) >= 0 || latestStable() != req.Version {
			return nil, badRequest("this version is not the latest stable version older than the installed one")
		}
		if s.updateRunning() {
			return nil, &Error{Status: http.StatusConflict, Msg: "another package operation is already running"}
		}
		s.audit(r, "system", fmt.Sprintf("Downgrade to StrataOS %s started", req.Version))
		v := req.Version
		return s.jobs.Start("apt", fmt.Sprintf("Downgrade to StrataOS %s", v), userOf(r), func(ctx context.Context, j *jobs.Job) error {
			defer fleetInfoReset()
			s.restorePointBeforeUpdate(j)
			if err := aptRun(ctx, j, "apt-get", "update"); err != nil {
				return err
			}
			return aptRun(ctx, j, "apt-get", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
				"--allow-downgrades", "install", "strataos="+v)
		}), nil
	})
}
