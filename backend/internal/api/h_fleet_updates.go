package api

// Grouped updates from the Multi-NAS console. A watched NAS lets a console
// check for and install updates (and restart it) only when the access it
// gave allows it; the token reaches a handful of fixed actions, never a
// command. The console updates the selected NAS side by side or one at a
// time, waiting for each to come back healthy with the new version.

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/backup"
	"strataos/internal/jobs"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
	"strataos/internal/vms"
)

var (
	fleetLimits fleetRate
	fleetJobs   sync.Map // job ID → ID of the console that started it
	fleetPoll   = 3 * time.Second
	fleetURL    = func(m store.FleetMember) string { return "https://" + bracketHost(m.Host) + ":7443" }
	fleetInfo   struct {
		sync.Mutex
		at   time.Time
		info fleetUpdateInfo
	}
)

// Calls per minute and console: status reads (the console polls while it
// updates), and actions.
const (
	fleetReadsPerMin   = 300
	fleetActionsPerMin = 10
	fleetMaxRuns       = 20
)

// fleetUpdateInfo is what a watched NAS tells about its updates.
type fleetUpdateInfo struct {
	Version        string    `json:"version"`   // running StrataOS
	Installed      string    `json:"installed"` // installed package
	Candidate      string    `json:"candidate"` // newer StrataOS offered, "" if none
	System         int       `json:"system"`    // upgradable packages
	RebootRequired bool      `json:"rebootRequired"`
	AptBusy        bool      `json:"aptBusy"`
	BootID         string    `json:"bootId"`
	Checked        time.Time `json:"checked,omitzero"` // last refresh of the package lists
}

// fleetPeerStatus is the answer of GET /api/fleet/peer/updates.
type fleetPeerStatus struct {
	fleetUpdateInfo
	Access     fleetAccess `json:"access"`
	Errors     *int        `json:"errors,omitempty"` // error alerts, with ?health=1
	FirstError string      `json:"firstError,omitempty"`
}

type fleetAccess struct {
	Updates bool `json:"updates"`
	Reboot  bool `json:"reboot"`
}

func accessOf(m store.FleetManager) fleetAccess {
	return fleetAccess{Updates: m.AllowUpdates, Reboot: m.AllowUpdates && m.AllowReboot}
}

// strataPolicy reads the installed and candidate versions of StrataOS.
func strataPolicy() (installed, candidate string) {
	out, _ := sys.Run("apt-cache", "policy", "strataos")
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok {
			switch k {
			case "Installed":
				installed = strings.TrimSpace(v)
			case "Candidate":
				candidate = strings.TrimSpace(v)
			}
		}
	}
	return installed, candidate
}

func aptBusy() bool {
	out, _ := sys.Run("systemctl", "is-active", "strata-apt.service")
	return strings.TrimSpace(out) == "active"
}

// updateInfo reads the update state; the package lists (slow) are reused for
// a minute unless fresh.
func (s *Server) updateInfo(fresh bool) fleetUpdateInfo {
	fleetInfo.Lock()
	defer fleetInfo.Unlock()
	if fresh || time.Since(fleetInfo.at) > time.Minute {
		var i fleetUpdateInfo
		i.Installed, i.Candidate = strataPolicy()
		if i.Candidate == "(none)" || i.Candidate == i.Installed {
			i.Candidate = ""
		}
		if up, err := system.ListUpgradable(); err == nil {
			i.System = len(up)
		}
		if fi, err := os.Stat("/var/cache/apt/pkgcache.bin"); err == nil {
			i.Checked = fi.ModTime()
		}
		fleetInfo.info, fleetInfo.at = i, time.Now()
	}
	i := fleetInfo.info
	i.Version, i.AptBusy, i.RebootRequired, i.BootID = s.version, s.updateRunning(), system.RebootRequired(), system.BootID()
	return i
}

func fleetInfoReset() {
	fleetInfo.Lock()
	fleetInfo.at = time.Time{}
	fleetInfo.Unlock()
}

// updateJob is the work of an update job: check (refresh the package lists),
// strata (StrataOS alone), system (full upgrade), security (Debian security
// updates only) or others (everything but StrataOS). A dry run only
// simulates (apt-get -s): nothing is installed and no restore point is
// taken. Manual, grouped (Multi-NAS console) and automatic updates all run
// through it.
func (s *Server) updateJob(kind string, dryRun bool) func(ctx context.Context, j *jobs.Job) error {
	return func(ctx context.Context, j *jobs.Job) error {
		defer fleetInfoReset()
		if kind != "check" && !dryRun {
			s.restorePointBeforeUpdate(j)
		}
		return s.runUpdate(ctx, j, kind, dryRun)
	}
}

// runUpdate runs the apt commands of an update, without a restore point.
func (s *Server) runUpdate(ctx context.Context, j *jobs.Job, kind string, dryRun bool) error {
	keep := []string{"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
	do := []string{"apt-get", "-y"}
	if dryRun {
		do = []string{"apt-get", "-s"}
	}
	do = append(do, keep...)
	switch kind {
	case "check":
		return aptRun(ctx, j, "apt-get", "update")
	case "strata":
		if !dryRun {
			if err := aptRun(ctx, j, "apt-get", "update"); err != nil {
				return err
			}
		}
		return aptRun(ctx, j, append(do, "install", "--only-upgrade", "strataos")...)
	case "system":
		return aptRun(ctx, j, append(do, "full-upgrade")...)
	case "security":
		// apt sees the security archives alone, with their own package lists.
		opts, sources, err := system.SecurityAptOptions()
		if err != nil {
			return err
		}
		j.Logf("Security sources:\n%s", sources)
		if err := aptRun(ctx, j, append(append([]string{"apt-get"}, opts...), "update")...); err != nil {
			return err
		}
		return aptRun(ctx, j, append(append(do, opts...), "upgrade", "--with-new-pkgs")...)
	case "others":
		up, err := system.ListUpgradable()
		if err != nil {
			return err
		}
		var pkgs []string
		for _, u := range up {
			if u.Package != "strataos" && pkgNameRe.MatchString(u.Package) {
				pkgs = append(pkgs, u.Package)
			}
		}
		if len(pkgs) == 0 {
			j.Logf("No system update to install")
			return nil
		}
		return aptRun(ctx, j, append(append(do, "install", "--only-upgrade"), pkgs...)...)
	}
	return fmt.Errorf("invalid update kind")
}

var pkgNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+(:[a-z0-9-]+)?$`)

// updateRunning tells whether an update job runs or apt is busy: a grouped,
// manual or automatic update then waits (they would fight over dpkg).
func (s *Server) updateRunning() bool {
	for _, j := range s.jobs.List() {
		if j.Type == "apt" && j.Status == jobs.Running {
			return true
		}
	}
	return aptBusy()
}

// fleetManagerOf finds the console whose token the request carries.
func (s *Server) fleetManagerOf(r *http.Request) (store.FleetManager, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return store.FleetManager{}, false
	}
	h := hashToken(tok)
	var found store.FleetManager
	s.store.Read(func(st *store.State) {
		for _, m := range st.Fleet.Managers {
			if subtle.ConstantTimeCompare([]byte(m.TokenHash), []byte(h)) == 1 {
				found = m
			}
		}
	})
	return found, found.ID != ""
}

// fleetPeer registers a route a console calls with its token: perm is the
// permission it needs (see fleetAllowed), action selects the stricter rate.
func (s *Server) fleetPeer(pattern, perm string, action bool, h func(r *http.Request, m store.FleetManager) (any, error)) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		m, ok := s.fleetManagerOf(r)
		if !ok {
			s.guardFail(clientIP(r), "console multi-NAS", "")
			writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "authentication required"})
			return
		}
		key, limit := m.ID+":read", fleetReadsPerMin
		if action {
			key, limit = m.ID+":action", fleetActionsPerMin
		}
		if !fleetLimits.allow(key, limit, time.Minute, time.Now()) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many requests, try again later"})
			return
		}
		if err := fleetAllowed(m, perm); err != nil {
			writeErr(w, err)
			return
		}
		if time.Since(m.LastSeen) > time.Minute {
			_ = s.store.Update(func(st *store.State) error {
				for i := range st.Fleet.Managers {
					if st.Fleet.Managers[i].ID == m.ID {
						st.Fleet.Managers[i].LastSeen = time.Now()
					}
				}
				return nil
			})
		}
		v, err := h(r, m)
		if err != nil {
			writeErr(w, err)
			return
		}
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		writeJSON(w, http.StatusOK, v)
	})
}

// fleetHA is how a NAS describes its high-availability pairing.
func (s *Server) fleetHA() map[string]any {
	var c store.HA
	s.store.Read(func(st *store.State) { c = st.HA })
	if c.Role == "" {
		return nil
	}
	peers := append([]string{c.PeerName, c.PeerHost}, c.Peers...)
	for _, x := range c.Extra {
		peers = append(peers, x.Name, x.Host)
	}
	peers = slices.DeleteFunc(peers, func(p string) bool { return p == "" })
	return map[string]any{"role": c.Role, "peers": peers}
}

func (s *Server) fleetPeerRoutes() {
	s.fleetPeer("GET /api/fleet/peer/summary", "", false, func(r *http.Request, m store.FleetManager) (any, error) {
		sum := s.fleetSummary()
		sum["access"] = accessOf(m)
		return sum, nil
	})
	s.fleetPeer("GET /api/fleet/peer/updates", "", false, func(r *http.Request, m store.FleetManager) (any, error) {
		st := fleetPeerStatus{fleetUpdateInfo: s.updateInfo(r.URL.Query().Get("fresh") == "1"), Access: accessOf(m)}
		if r.URL.Query().Get("health") == "1" {
			n := 0
			for _, a := range s.overview()["alerts"].([]alert) {
				if a.Level == "error" {
					if n == 0 {
						st.FirstError = a.Message
					}
					n++
				}
			}
			st.Errors = &n
		}
		return st, nil
	})
	start := func(m store.FleetManager, kind string, dryRun bool) (any, error) {
		if s.updateRunning() {
			return nil, &Error{Status: http.StatusConflict, Msg: "another package operation is already running"}
		}
		title, msg := map[string]string{"check": "Checking for updates", "strata": "StrataOS update", "system": "System update"}[kind], ""
		switch {
		case kind == "check":
			msg = "Update check started by the console %s"
		case dryRun:
			title, msg = "Update simulation", "Update simulation started by the console %s"
		case kind == "strata":
			msg = "StrataOS update started by the console %s"
		default:
			msg = "System update started by the console %s"
		}
		user := m.Name + " (console)"
		s.store.Log("info", "fleet", user, fmt.Sprintf(msg, m.Name))
		j := s.jobs.Start("apt", title, user, s.updateJob(kind, dryRun))
		fleetJobs.Store(j.ID, m.ID)
		return j, nil
	}
	s.fleetPeer("POST /api/fleet/peer/updates/check", "updates", true, func(r *http.Request, m store.FleetManager) (any, error) {
		return start(m, "check", false)
	})
	s.fleetPeer("POST /api/fleet/peer/updates/start", "updates", true, func(r *http.Request, m store.FleetManager) (any, error) {
		var req struct {
			Kind   string `json:"kind"`
			DryRun bool   `json:"dryRun"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Kind != "strata" && req.Kind != "system" {
			return nil, badRequest("invalid update kind")
		}
		return start(m, req.Kind, req.DryRun)
	})
	// Only the jobs this console started, with the end of their log.
	s.fleetPeer("GET /api/fleet/peer/jobs/{id}", "updates", false, func(r *http.Request, m store.FleetManager) (any, error) {
		id := r.PathValue("id")
		if owner, ok := fleetJobs.Load(id); !ok || owner != m.ID {
			return nil, notFound("job not found")
		}
		j, ok := s.jobs.Get(id)
		if !ok {
			return nil, notFound("job not found")
		}
		if len(j.Log) > 4096 {
			j.Log = j.Log[len(j.Log)-4096:]
		}
		return j, nil
	})
	s.fleetPeer("POST /api/fleet/peer/reboot", "reboot", true, func(r *http.Request, m store.FleetManager) (any, error) {
		if s.updateRunning() {
			return nil, &Error{Status: http.StatusConflict, Msg: "another package operation is already running"}
		}
		if !system.RebootRequired() {
			return nil, &Error{Status: http.StatusConflict, Msg: "no restart is required"}
		}
		s.store.Log("warning", "fleet", m.Name+" (console)", fmt.Sprintf("Restart requested by the console %s after an update", m.Name))
		if c := s.clusterState(); c.ID != "" && len(c.Members) > 1 && vms.Installed() {
			// Member of a VM cluster: its VMs move to the other members first.
			go func() {
				s.vmcBeforeReboot(func(f string, a ...any) { slog.Info("before restart", "msg", fmt.Sprintf(f, a...)) })
				if err := system.Power("reboot"); err != nil {
					s.store.Log("error", "fleet", "", "Restart failed: "+err.Error())
				}
			}()
			return nil, nil
		}
		return nil, system.Power("reboot")
	})
}

// ---- Console side ----

// peerError is an error answer of a watched NAS.
type peerError struct {
	Status int
	Msg    string
}

func (e *peerError) Error() string { return e.Msg }

// fleetCall calls a watched NAS with the console's token, over HTTPS with the
// certificate pinned at pairing.
func fleetCall(ctx context.Context, m store.FleetMember, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, fleetURL(m)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("Content-Type", "application/json")
	tr := backup.PinnedTransport(m.Cert)
	tr.DisableKeepAlives = true // one client per call: no idle connections left behind
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return &peerError{Status: resp.StatusCode, Msg: firstNonEmpty(e.Error, "HTTP "+resp.Status)}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (s *Server) fleetMember(id string) (store.FleetMember, bool) {
	var m store.FleetMember
	s.store.Read(func(st *store.State) {
		for _, x := range st.Fleet.Members {
			if x.ID == id {
				m = x
			}
		}
	})
	return m, m.ID != ""
}

func cloneRun(r store.FleetUpdateRun) store.FleetUpdateRun {
	r.Items = slices.Clone(r.Items)
	r.Lanes = slices.Clone(r.Lanes)
	return r
}

func (s *Server) fleetRun(id string) (store.FleetUpdateRun, bool) {
	var run store.FleetUpdateRun
	s.store.Read(func(st *store.State) {
		for _, r := range st.Fleet.Updates {
			if r.ID == id {
				run = cloneRun(r)
			}
		}
	})
	return run, run.ID != ""
}

// fleetEdit changes a run under the store lock.
func (s *Server) fleetEdit(id string, fn func(r *store.FleetUpdateRun)) {
	_ = s.store.Update(func(st *store.State) error {
		for i := range st.Fleet.Updates {
			if st.Fleet.Updates[i].ID == id {
				fn(&st.Fleet.Updates[i])
			}
		}
		return nil
	})
}

// fleetSet moves an item to a new state; fn adjusts the other fields.
func (s *Server) fleetSet(id string, idx int, state string, fn func(it *store.FleetUpdateItem)) {
	s.fleetEdit(id, func(r *store.FleetUpdateRun) {
		it := &r.Items[idx]
		now := time.Now()
		if it.State != state {
			it.State, it.Since = state, now
		}
		it.Progress = fleetItemProgress(state, -1)
		if fleetTerminal(state) {
			it.Finished = &now
		}
		if fn != nil {
			fn(it)
		}
	})
}

func (s *Server) fleetFail(id string, idx int, msg string) {
	s.fleetSet(id, idx, "failed", func(it *store.FleetUpdateItem) { it.Error = msg })
}

func fleetStatus(m store.FleetMember, health bool) (fleetPeerStatus, error) {
	var st fleetPeerStatus
	path := "/api/fleet/peer/updates?fresh=1"
	if health {
		path += "&health=1"
	}
	err := fleetCall(context.Background(), m, http.MethodGet, path, nil, &st)
	if err == nil && st.Version == "" {
		err = errors.New("unexpected answer")
	}
	if st.Errors == nil {
		st.Errors = new(int)
	}
	return st, err
}

// fleetExec runs a run: its lanes side by side, each NAS of a lane once the
// previous one is back healthy. It resumes a run cut by a restart of this NAS.
func (s *Server) fleetExec(id string) {
	run, ok := s.fleetRun(id)
	if !ok {
		return
	}
	var wg sync.WaitGroup
	for li := range run.Lanes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				next := -1
				s.fleetEdit(id, func(r *store.FleetUpdateRun) {
					n, skip := laneNext(r.Items, r.Lanes[li], r.Cancel)
					for _, j := range skip {
						now := time.Now()
						r.Items[j].State, r.Items[j].Finished, r.Items[j].Progress = "skipped", &now, 100
						r.Items[j].Note = map[bool]string{true: "Cancelled", false: "Not started: an earlier update of this group failed"}[r.Cancel]
					}
					next = n
				})
				if next < 0 {
					return
				}
				s.fleetItem(id, next)
			}
		}()
	}
	wg.Wait()
	s.fleetFinish(id)
}

// fleetItem drives the update of one NAS to its end.
func (s *Server) fleetItem(id string, idx int) {
	for {
		run, ok := s.fleetRun(id)
		if !ok {
			return
		}
		it := run.Items[idx]
		if fleetTerminal(it.State) {
			return
		}
		m, ok := s.fleetMember(it.Member)
		if !ok {
			s.fleetFail(id, idx, "The NAS was removed from the console")
			return
		}
		wait := false
		switch it.State {
		case "pending":
			s.fleetStartItem(run, idx, m)
		case "running":
			wait = s.fleetPollJob(run, idx, m)
		case "restarting":
			wait = s.fleetWaitBack(run, idx, m)
		case "rebooting":
			wait = s.fleetWaitReboot(run, idx, m)
		case "verifying":
			wait = s.fleetVerify(run, idx, m)
		default:
			s.fleetFail(id, idx, "unknown state "+it.State)
		}
		if wait {
			time.Sleep(fleetPoll)
		}
	}
}

func (s *Server) fleetStartItem(run store.FleetUpdateRun, idx int, m store.FleetMember) {
	st, err := fleetStatus(m, true)
	if err != nil {
		s.fleetFail(run.ID, idx, err.Error())
		return
	}
	if !st.Access.Updates {
		s.fleetFail(run.ID, idx, "this NAS does not allow the console to install updates")
		return
	}
	if st.AptBusy {
		s.fleetFail(run.ID, idx, "another package operation is already running")
		return
	}
	now := time.Now()
	setup := func(it *store.FleetUpdateItem) {
		it.From, it.Target, it.BootID, it.Alerts, it.Started = st.Version, st.Candidate, st.BootID, *st.Errors, &now
	}
	if !run.DryRun && st.Candidate == "" && (run.Kind == "strata" || st.System == 0) {
		s.fleetSet(run.ID, idx, "verifying", func(it *store.FleetUpdateItem) { setup(it); it.Note = "Already up to date" })
		return
	}
	var j jobs.Snapshot
	if err := fleetCall(context.Background(), m, http.MethodPost, "/api/fleet/peer/updates/start", map[string]any{"kind": run.Kind, "dryRun": run.DryRun}, &j); err != nil {
		s.fleetFail(run.ID, idx, err.Error())
		return
	}
	s.fleetSet(run.ID, idx, "running", func(it *store.FleetUpdateItem) { setup(it); it.Job = j.ID })
}

func (s *Server) fleetPollJob(run store.FleetUpdateRun, idx int, m store.FleetMember) bool {
	it := run.Items[idx]
	var j jobs.Snapshot
	err := fleetCall(context.Background(), m, http.MethodGet, "/api/fleet/peer/jobs/"+it.Job, nil, &j)
	var pe *peerError
	switch {
	case errors.As(err, &pe) && pe.Status == http.StatusForbidden:
		s.fleetFail(run.ID, idx, pe.Msg)
	case err != nil:
		// Updating StrataOS restarts its service (the job is lost with it),
		// or the NAS is briefly out of reach: wait for it.
		s.fleetSet(run.ID, idx, "restarting", nil)
	case j.Status == jobs.Running:
		if time.Since(it.Since) > 3*time.Hour {
			s.fleetFail(run.ID, idx, "the update takes too long")
			return false
		}
		s.fleetEdit(run.ID, func(r *store.FleetUpdateRun) { r.Items[idx].Progress = fleetItemProgress("running", j.Progress) })
		return true
	case j.Status == jobs.Failed:
		s.fleetFail(run.ID, idx, firstNonEmpty(j.Error, "the update failed"))
	default:
		s.fleetSet(run.ID, idx, "verifying", nil)
	}
	return false
}

func (s *Server) fleetWaitBack(run store.FleetUpdateRun, idx int, m store.FleetMember) bool {
	it := run.Items[idx]
	st, err := fleetStatus(m, false)
	if err != nil || st.AptBusy {
		if time.Since(it.Since) > 45*time.Minute {
			s.fleetFail(run.ID, idx, "the NAS did not come back after the update")
			return false
		}
		return true
	}
	// A short outage: the job may still be known.
	var j jobs.Snapshot
	if it.Job != "" && fleetCall(context.Background(), m, http.MethodGet, "/api/fleet/peer/jobs/"+it.Job, nil, &j) == nil {
		switch j.Status {
		case jobs.Running:
			s.fleetSet(run.ID, idx, "running", nil)
			return false
		case jobs.Failed:
			s.fleetFail(run.ID, idx, firstNonEmpty(j.Error, "the update failed"))
			return false
		}
	}
	s.fleetSet(run.ID, idx, "verifying", nil)
	return false
}

func (s *Server) fleetWaitReboot(run store.FleetUpdateRun, idx int, m store.FleetMember) bool {
	it := run.Items[idx]
	st, err := fleetStatus(m, false)
	if err != nil || st.BootID == it.BootID {
		if time.Since(it.Since) > 20*time.Minute {
			s.fleetFail(run.ID, idx, "the NAS did not come back after the restart")
			return false
		}
		return true
	}
	s.fleetSet(run.ID, idx, "verifying", nil)
	return false
}

// fleetVerify checks a NAS after its update: new version running, restart if
// asked and needed, no new error.
func (s *Server) fleetVerify(run store.FleetUpdateRun, idx int, m store.FleetMember) bool {
	it := run.Items[idx]
	since := time.Since(it.Since)
	st, err := fleetStatus(m, true)
	if err != nil || st.AptBusy {
		if since > 15*time.Minute {
			s.fleetFail(run.ID, idx, firstNonEmpty(errString(err), "another package operation is still running"))
			return false
		}
		return true
	}
	if !run.DryRun && it.Target != "" && (st.Installed != it.Target || st.Version != it.Target) {
		if since < 5*time.Minute {
			return true // the service may still be restarting
		}
		s.fleetFail(run.ID, idx, fmt.Sprintf("Still running StrataOS %s after the update (expected %s)", st.Version, it.Target))
		return false
	}
	note := it.Note
	if st.RebootRequired && !it.Rebooted {
		switch {
		case !run.Reboot:
			note = "Restart required"
		case run.DryRun:
			note = "A restart would be required"
		case !st.Access.Reboot:
			note = "Restart required: this NAS does not allow the console to restart it"
		default:
			if err := fleetCall(context.Background(), m, http.MethodPost, "/api/fleet/peer/reboot", nil, nil); err != nil {
				s.fleetFail(run.ID, idx, err.Error())
				return false
			}
			s.fleetSet(run.ID, idx, "rebooting", func(it *store.FleetUpdateItem) { it.BootID, it.Rebooted = st.BootID, true })
			return false
		}
	}
	if *st.Errors > it.Alerts {
		if since < 3*time.Minute {
			return true // volumes may still be mounting after a restart
		}
		s.fleetFail(run.ID, idx, "New error after the update: "+st.FirstError)
		return false
	}
	s.fleetSet(run.ID, idx, "success", func(it *store.FleetUpdateItem) { it.Version, it.Note = st.Version, note })
	return false
}

// fleetFinish closes a run and sends its summary.
func (s *Server) fleetFinish(id string) {
	var run store.FleetUpdateRun
	s.fleetEdit(id, func(r *store.FleetUpdateRun) {
		now := time.Now()
		r.Status, r.Finished = fleetRunStatus(fleetAggregate(r.Items), r.Cancel), &now
		run = cloneRun(*r)
	})
	if run.ID == "" {
		return
	}
	level, subject, lines := fleetSummaryText(run)
	s.store.Log(level, "fleet", run.User, lines[0])
	s.notify(level, subject, strings.Join(lines, "\n"))
}

// fleetSummaryText is the notification of a finished run: level, subject and
// lines (the first one is also logged).
func fleetSummaryText(run store.FleetUpdateRun) (string, string, []string) {
	t := fleetAggregate(run.Items)
	level := "info"
	if t.Failed > 0 {
		level = "warning"
		if t.Success == 0 {
			level = "error"
		}
	}
	lines := []string{fmt.Sprintf("Grouped update finished: %d succeeded, %d failed, %d skipped", t.Success, t.Failed, t.Skipped)}
	for _, it := range run.Items {
		switch {
		case it.State == "failed":
			lines = append(lines, fmt.Sprintf("• %s: failed — %s", it.Name, it.Error))
		case it.State == "skipped":
			lines = append(lines, fmt.Sprintf("• %s: skipped", it.Name))
		case run.DryRun:
			lines = append(lines, fmt.Sprintf("• %s: simulation succeeded", it.Name))
		case it.Version != "" && it.Version != it.From:
			lines = append(lines, fmt.Sprintf("• %s: updated to StrataOS %s", it.Name, it.Version))
		default:
			lines = append(lines, fmt.Sprintf("• %s: updated", it.Name))
		}
	}
	subject := "Grouped update finished"
	if t.Failed > 0 {
		subject = "Grouped update failed"
	}
	return level, subject, lines
}

// fleetResume continues the runs a restart of this NAS interrupted.
func (s *Server) fleetResume() {
	var ids []string
	s.store.Read(func(st *store.State) {
		for _, r := range st.Fleet.Updates {
			if r.Status == "running" {
				ids = append(ids, r.ID)
			}
		}
	})
	for _, id := range ids {
		go s.fleetExec(id)
	}
}

type fleetRunView struct {
	store.FleetUpdateRun
	Totals fleetTotals `json:"totals"`
}

func (s *Server) fleetUpdateRoutes() {
	s.admin("PUT /api/fleet/managers/{id}", func(r *http.Request) (any, error) {
		var req struct {
			AllowUpdates bool `json:"allowUpdates"`
			AllowReboot  bool `json:"allowReboot"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.AllowReboot = req.AllowReboot && req.AllowUpdates
		var before store.FleetManager
		err := s.store.Update(func(st *store.State) error {
			for i := range st.Fleet.Managers {
				if m := &st.Fleet.Managers[i]; m.ID == r.PathValue("id") {
					before = *m
					m.AllowUpdates, m.AllowReboot = req.AllowUpdates, req.AllowReboot
					return nil
				}
			}
			return notFound("access not found")
		})
		if err != nil {
			return nil, err
		}
		if before.AllowUpdates != req.AllowUpdates {
			s.audit(r, "fleet", fmt.Sprintf(map[bool]string{true: "The console %s may now install updates", false: "The console %s may no longer install updates"}[req.AllowUpdates], before.Name))
		}
		if before.AllowReboot != req.AllowReboot {
			s.audit(r, "fleet", fmt.Sprintf(map[bool]string{true: "The console %s may now restart this NAS after an update", false: "The console %s may no longer restart this NAS"}[req.AllowReboot], before.Name))
		}
		return nil, nil
	})

	// Refreshes the package lists of the given NAS and waits for them.
	s.admin("POST /api/fleet/check", func(r *http.Request) (any, error) {
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		members, err := s.fleetSelect(req.IDs)
		if err != nil {
			return nil, err
		}
		return s.jobs.Start("fleet", "Checking for updates on the NAS of the console", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			var wg sync.WaitGroup
			var mu sync.Mutex
			failed := 0
			for _, m := range members {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := fleetCheck(ctx, m)
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						failed++
						j.Logf("%s: %v", m.Name, err)
					} else {
						j.Logf("%s: package lists refreshed", m.Name)
					}
				}()
			}
			wg.Wait()
			if failed > 0 {
				return fmt.Errorf("%d NAS could not check for updates", failed)
			}
			return nil
		}), nil
	})

	s.admin("GET /api/fleet/updates", func(r *http.Request) (any, error) {
		out := []fleetRunView{}
		s.store.Read(func(st *store.State) {
			for i := len(st.Fleet.Updates) - 1; i >= 0; i-- {
				run := cloneRun(st.Fleet.Updates[i])
				out = append(out, fleetRunView{run, fleetAggregate(run.Items)})
			}
		})
		return out, nil
	})

	s.admin("POST /api/fleet/updates", func(r *http.Request) (any, error) {
		var req struct {
			IDs      []string `json:"ids"`
			Kind     string   `json:"kind"`
			Strategy string   `json:"strategy"`
			Reboot   bool     `json:"reboot"`
			DryRun   bool     `json:"dryRun"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Kind != "strata" && req.Kind != "system" {
			return nil, badRequest("invalid update kind")
		}
		if req.Strategy != "parallel" && req.Strategy != "rolling" {
			return nil, badRequest("invalid strategy")
		}
		members, err := s.fleetSelect(req.IDs)
		if err != nil {
			return nil, err
		}
		// Their HA pairing and access, as each NAS reports them now.
		nodes := make([]fleetNode, len(members))
		errs := make([]error, len(members))
		var wg sync.WaitGroup
		for i, m := range members {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var sum struct {
					Hostname string      `json:"hostname"`
					Access   fleetAccess `json:"access"`
					HA       struct {
						Role  string   `json:"role"`
						Peers []string `json:"peers"`
					} `json:"ha"`
					Cluster string `json:"cluster"`
				}
				if err := fleetCall(r.Context(), m, http.MethodGet, "/api/fleet/peer/summary", nil, &sum); err != nil {
					errs[i] = fmt.Errorf("%s: %v", m.Name, err)
					return
				}
				if !sum.Access.Updates {
					errs[i] = fmt.Errorf("%s: this NAS does not allow the console to install updates", m.Name)
				}
				nodes[i] = fleetNode{Hostname: sum.Hostname, Host: m.Host, HARole: sum.HA.Role, HAPeers: sum.HA.Peers, Cluster: sum.Cluster}
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return nil, badRequest(err.Error())
		}
		groups := fleetGroups(nodes)
		run := store.FleetUpdateRun{ID: newLinkID()[:12], Kind: req.Kind, Strategy: req.Strategy, Reboot: req.Reboot, DryRun: req.DryRun,
			User: userOf(r), Status: "running", Started: time.Now(), Lanes: fleetLanes(nodes, req.Strategy == "rolling")}
		for i, m := range members {
			it := store.FleetUpdateItem{Member: m.ID, Name: m.Name, Host: m.Host, State: "pending"}
			if groups[i] >= 0 {
				it.HA = nodes[i].HARole
				if it.HA == "" && nodes[i].Cluster == "" {
					it.HA = "secondary"
				}
			}
			run.Items = append(run.Items, it)
		}
		err = s.store.Update(func(st *store.State) error {
			for _, x := range st.Fleet.Updates {
				if x.Status == "running" {
					return &Error{Status: http.StatusConflict, Msg: "a grouped update is already running"}
				}
			}
			st.Fleet.Updates = append(st.Fleet.Updates, run)
			if n := len(st.Fleet.Updates); n > fleetMaxRuns {
				st.Fleet.Updates = st.Fleet.Updates[n-fleetMaxRuns:]
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		msg := "Grouped update started on %d NAS"
		if req.DryRun {
			msg = "Grouped update simulation started on %d NAS"
		}
		s.audit(r, "fleet", fmt.Sprintf(msg, len(members)))
		go s.fleetExec(run.ID)
		return fleetRunView{run, fleetAggregate(run.Items)}, nil
	})

	s.admin("POST /api/fleet/updates/{id}/cancel", func(r *http.Request) (any, error) {
		found := false
		s.fleetEdit(r.PathValue("id"), func(run *store.FleetUpdateRun) {
			if run.Status == "running" {
				run.Cancel, found = true, true
			}
		})
		if !found {
			return nil, notFound("no running grouped update with this ID")
		}
		s.audit(r, "fleet", "Grouped update cancelled")
		return nil, nil
	})
}

// fleetSelect returns the console members with these IDs (in that order).
func (s *Server) fleetSelect(ids []string) ([]store.FleetMember, error) {
	if len(ids) == 0 || len(ids) > 200 {
		return nil, badRequest("select at least one NAS")
	}
	var out []store.FleetMember
	s.store.Read(func(st *store.State) {
		for _, id := range ids {
			for _, m := range st.Fleet.Members {
				if m.ID == id && !slices.ContainsFunc(out, func(x store.FleetMember) bool { return x.ID == id }) {
					out = append(out, m)
				}
			}
		}
	})
	if len(out) != len(ids) {
		return nil, badRequest("unknown NAS")
	}
	return out, nil
}

// fleetCheck refreshes the package lists of a NAS and waits for the end.
func fleetCheck(ctx context.Context, m store.FleetMember) error {
	var j jobs.Snapshot
	if err := fleetCall(ctx, m, http.MethodPost, "/api/fleet/peer/updates/check", nil, &j); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if err := fleetCall(ctx, m, http.MethodGet, "/api/fleet/peer/jobs/"+j.ID, nil, &j); err != nil {
			return err
		}
		switch j.Status {
		case jobs.Failed:
			return errors.New(firstNonEmpty(j.Error, "failed"))
		case jobs.Success:
			return nil
		}
	}
	return errors.New("timed out")
}
