package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/tlstrust"
)

// routineTriggers are the "if" a routine can watch.
var routineTriggers = map[string]bool{
	"cpu": true, "mem": true, "volume-full": true,
	"disk-failed": true, "disk-weak": true, "backup-failed": true,
}

// routineState is the snapshot of the NAS the triggers are evaluated against.
type routineState struct {
	cpu, mem     float64
	volumes      map[string]int  // volume name → used %
	diskFailed   map[string]bool // device path
	diskWeak     map[string]bool // device path
	backupFailed map[string]bool // backup task ID
}

// routineState collects the current usage (monitor history), volume fill,
// failing or weakening disks (cached by the health watcher) and backup tasks
// whose last run failed.
func (s *Server) routineState() routineState {
	st := routineState{
		volumes: map[string]int{}, diskFailed: map[string]bool{},
		diskWeak: map[string]bool{}, backupFailed: map[string]bool{},
	}
	if s.monitor != nil {
		if h, _ := s.monitor.History(); len(h) > 0 {
			if len(h) > 30 { // last minute at the 2 s sampling rate
				h = h[len(h)-30:]
			}
			st.cpu, st.mem = resourceAverages(h)
		}
	}
	raids, _ := storage.ListRaid()
	pools, _ := storage.ListZPools(false)
	for _, v := range s.volumeSummaries(raids, pools) {
		if v.Total > 0 {
			st.volumes[v.Name] = int(100 * float64(v.Used) / float64(v.Total))
		}
	}
	if d := s.diskAlerts.Load(); d != nil {
		for _, a := range *d {
			if a.Replace == nil || a.Replace.Disk == "" {
				continue
			}
			if a.Level == "error" {
				st.diskFailed[a.Replace.Disk] = true
			} else {
				st.diskWeak[a.Replace.Disk] = true
			}
		}
	}
	s.store.Read(func(ss *store.State) {
		for _, b := range ss.Backups {
			if n := len(b.History); n > 0 && b.History[n-1].Status == "failed" {
				st.backupFailed[b.ID] = true
			}
		}
	})
	return st
}

// routineTriggered tells whether the trigger of the rule is met, with the
// detail of what fired it for the notification texts.
func routineTriggered(rt *store.Routine, st routineState) (bool, string) {
	switch rt.Trigger {
	case "cpu":
		return rt.Value > 0 && st.cpu >= float64(rt.Value), fmt.Sprintf("CPU usage is %.0f%% (threshold %d%%)", st.cpu, rt.Value)
	case "mem":
		return rt.Value > 0 && st.mem >= float64(rt.Value), fmt.Sprintf("Memory usage is %.0f%% (threshold %d%%)", st.mem, rt.Value)
	case "volume-full":
		if rt.Value <= 0 || rt.Value >= 100 {
			return false, ""
		}
		if rt.Target != "" {
			return st.volumes[rt.Target] >= rt.Value, fmt.Sprintf("Volume %s is %d%% full (threshold %d%%)", rt.Target, st.volumes[rt.Target], rt.Value)
		}
		for name, pct := range st.volumes {
			if pct >= rt.Value {
				return true, fmt.Sprintf("Volume %s is %d%% full (threshold %d%%)", name, pct, rt.Value)
			}
		}
	case "disk-failed":
		if rt.Target != "" {
			return st.diskFailed[rt.Target], "Disk " + rt.Target + " fails its SMART health check"
		}
		for d := range st.diskFailed {
			return true, "Disk " + d + " fails its SMART health check"
		}
	case "disk-weak":
		if rt.Target != "" {
			return st.diskWeak[rt.Target], "Disk " + rt.Target + " is getting weak"
		}
		for d := range st.diskWeak {
			return true, "Disk " + d + " is getting weak"
		}
	case "backup-failed":
		if rt.Target != "" {
			return st.backupFailed[rt.Target], "Backup task failed"
		}
		for range st.backupFailed {
			return true, "Backup task failed"
		}
	}
	return false, ""
}

// routineText fills the placeholders of an action's message; empty = default.
func routineText(msg string, rt *store.Routine, detail string) string {
	if msg == "" {
		return fmt.Sprintf("%s: %s", rt.Name, detail)
	}
	return strings.NewReplacer("{name}", rt.Name, "{detail}", detail).Replace(msg)
}

// routineCopies returns deep copies of the routines, taken under the store
// lock: the evaluation loop, the webhook goroutines and the JSON answers use
// them while a PUT may rewrite the stored ones.
func (s *Server) routineCopies(id string) []store.Routine {
	var out []store.Routine
	s.store.Read(func(st *store.State) {
		for _, rt := range st.Routines {
			if id == "" || rt.ID == id {
				c := *rt
				c.Actions = slices.Clone(rt.Actions)
				out = append(out, c)
			}
		}
	})
	return out
}

// routineWebhookClient posts with a timeout and follows a redirect only to
// the same host. Like the webhook of the notifications, the address may be
// on the local network (only administrators set it).
func routineWebhookClient() *http.Client {
	cl := tlstrust.HTTPClient(store.TLSTrust{}, 10*time.Second)
	cl.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("redirect to another host (%s) refused", req.URL.Host)
		}
		return nil
	}
	return cl
}

// routineWebhook posts one JSON event of the routine to the action's URL.
func (s *Server) routineWebhook(rt *store.Routine, a store.RoutineAction, detail string) {
	host, _ := hostname()
	body, _ := json.Marshal(map[string]string{"title": rt.Name, "message": detail, "text": rt.Name + ": " + detail, "content": rt.Name + ": " + detail, "routine": rt.Name, "trigger": detail, "host": host})
	resp, err := routineWebhookClient().Post(a.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("routine webhook failed", "routine", rt.Name, "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("routine webhook failed", "routine", rt.Name, "status", resp.StatusCode)
	}
}

// fireRoutine runs the actions of a routine (eval loop or manual test) and
// records the run. rt is a copy (routineCopies): the webhook goroutines keep it.
func (s *Server) fireRoutine(rt *store.Routine, detail string) {
	for _, a := range rt.Actions {
		switch a.Kind {
		case "notify":
			s.notify("warning", rt.Name, routineText(a.Message, rt, detail))
		case "webhook":
			s.bg.Go(func() { s.routineWebhook(rt, a, detail) })
		}
	}
	s.store.Log("info", "routines", "", fmt.Sprintf("Routine %s: %s", rt.Name, detail))
}

// evalRoutinesState runs the enabled routines whose trigger is met and whose
// cooldown has elapsed, and stamps the ones that ran.
func (s *Server) evalRoutinesState(st routineState, now time.Time) {
	for _, c := range s.routineCopies("") {
		rt := &c
		if !rt.Enabled {
			continue
		}
		ok, detail := routineTriggered(rt, st)
		if !ok {
			continue
		}
		cd := rt.Cooldown
		if cd <= 0 {
			cd = 60
		}
		if !rt.LastFired.IsZero() && now.Sub(rt.LastFired) < time.Duration(cd)*time.Minute {
			continue
		}
		s.fireRoutine(rt, detail)
		s.store.Update(func(ss *store.State) error {
			for _, r := range ss.Routines {
				if r.ID == rt.ID {
					r.LastFired = now
				}
			}
			return nil
		})
	}
}

// routinesLoop evaluates the routines every minute.
func (s *Server) routinesLoop() {
	for {
		time.Sleep(time.Minute)
		s.evalRoutinesState(s.routineState(), time.Now())
	}
}

// validRoutine checks the rule the user submitted.
func (s *Server) validRoutine(rt *store.Routine) error {
	if strings.TrimSpace(rt.Name) == "" {
		return badRequest("the routine needs a name")
	}
	if !routineTriggers[rt.Trigger] {
		return badRequest("unknown trigger")
	}
	switch rt.Trigger {
	case "cpu", "mem", "volume-full":
		if rt.Value < 1 || rt.Value > 99 {
			return badRequest("the threshold must be between 1 and 99 %")
		}
	}
	if rt.Trigger == "backup-failed" && rt.Target != "" {
		var ok bool
		s.store.Read(func(st *store.State) {
			ok = slices.ContainsFunc(st.Backups, func(b *store.BackupTask) bool { return b.ID == rt.Target })
		})
		if !ok {
			return badRequest("unknown backup task")
		}
	}
	if len(rt.Actions) == 0 {
		return badRequest("the routine needs at least one action")
	}
	for _, a := range rt.Actions {
		switch a.Kind {
		case "notify":
		case "webhook":
			if u, err := url.Parse(a.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(a.URL) > 2048 {
				return badRequest("the webhook URL must be http(s)")
			}
		default:
			return badRequest("unknown action")
		}
	}
	return nil
}

func (s *Server) routineRoutes() {
	s.admin("GET /api/routines", func(r *http.Request) (any, error) {
		out := s.routineCopies("")
		if out == nil {
			out = []store.Routine{}
		}
		return out, nil
	})

	s.admin("POST /api/routines", func(r *http.Request) (any, error) {
		var rt store.Routine
		if err := decode(r, &rt); err != nil {
			return nil, err
		}
		if err := s.validRoutine(&rt); err != nil {
			return nil, err
		}
		rt.ID = token()[:24]
		rt.LastFired = time.Time{}
		err := s.store.Update(func(st *store.State) error {
			c := rt // the stored one may be rewritten by a PUT while rt is answered
			st.Routines = append(st.Routines, &c)
			return nil
		})
		return rt, err
	})

	s.admin("PUT /api/routines/{id}", func(r *http.Request) (any, error) {
		var in store.Routine
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if err := s.validRoutine(&in); err != nil {
			return nil, err
		}
		var out *store.Routine
		err := s.store.Update(func(st *store.State) error {
			for _, rt := range st.Routines {
				if rt.ID == r.PathValue("id") {
					if in.Enabled {
						in.LastFired = rt.LastFired
					}
					*rt = in
					rt.ID = r.PathValue("id")
					c := *rt // answered after the lock is released
					out = &c
				}
			}
			if out == nil {
				return notFound("unknown routine")
			}
			return nil
		})
		return out, err
	})

	s.admin("DELETE /api/routines/{id}", func(r *http.Request) (any, error) {
		found := false
		err := s.store.Update(func(st *store.State) error {
			n := len(st.Routines)
			st.Routines = slices.DeleteFunc(st.Routines, func(rt *store.Routine) bool { return rt.ID == r.PathValue("id") })
			found = len(st.Routines) < n
			return nil
		})
		if err == nil && !found {
			return nil, notFound("unknown routine")
		}
		return nil, err
	})

	// Test: run the actions now, whatever the trigger and the cooldown say.
	s.admin("POST /api/routines/{id}/fire", func(r *http.Request) (any, error) {
		list := s.routineCopies(r.PathValue("id"))
		if len(list) == 0 {
			return nil, notFound("unknown routine")
		}
		rt := &list[0]
		_, detail := routineTriggered(rt, s.routineState())
		s.fireRoutine(rt, detail)
		return nil, nil
	})
}
