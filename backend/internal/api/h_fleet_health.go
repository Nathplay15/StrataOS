package api

// Fleet health dashboard. Each NAS describes its health (fleetHealthHere,
// kept 30 s so that several consoles cost one reading); the console keeps the
// last report of every NAS of its fleet and refreshes them in the
// background: every 2 minutes while someone looks at the dashboard, every 10
// minutes otherwise.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sync"
	"time"

	"strataos/internal/hardware"
	"strataos/internal/storage"
	"strataos/internal/store"
)

var fleetHere struct {
	sync.Mutex
	at time.Time
	h  fleetHealth
}

// fleetHealthHere is this NAS's health report.
func (s *Server) fleetHealthHere() fleetHealth {
	fleetHere.Lock()
	defer fleetHere.Unlock()
	if time.Since(fleetHere.at) < 30*time.Second {
		return fleetHere.h
	}
	h := fleetHealth{Version: s.version, UptimeDays: uptimeDays(), At: time.Now()}
	h.Hostname, _ = hostname()
	u := s.updateInfo(false)
	h.Updates = &u

	s.store.Read(func(st *store.State) {
		if a := st.Advisor.Last; a != nil {
			adv := fleetAdvisor{Score: a.Score, Time: a.Time}
			for _, it := range a.Items {
				if it.Ignored || slices.Contains(st.Advisor.Ignored, it.ID) {
					continue
				}
				switch it.Severity {
				case "high":
					adv.High++
				case "medium":
					adv.Medium++
				}
			}
			h.Advisor = &adv
		}
		for _, b := range st.Backups {
			if !b.Enabled {
				continue
			}
			h.Backups.Total++
			var success *time.Time
			for i, run := range b.History {
				if run.Status == "success" {
					t := run.Started
					success = &t
					break
				}
				if i == 0 {
					h.Backups.Failed++
					if h.Backups.LastFailure == nil || run.Started.After(*h.Backups.LastFailure) {
						t := run.Started
						h.Backups.LastFailure, h.Backups.LastFailureName = &t, b.Name
					}
				}
			}
			if success != nil && (h.Backups.LastSuccess == nil || success.After(*h.Backups.LastSuccess)) {
				h.Backups.LastSuccess = success
			}
			since := b.CreatedAt
			if success != nil {
				since = *success
			}
			if !since.IsZero() && time.Since(since) > fleetBackupStale {
				h.Backups.Stale = append(h.Backups.Stale, b.Name)
			}
		}
	})

	disks, _ := storage.ListDisks()
	h.Disks.Total = len(disks)
	for _, d := range disks {
		e, ok := smartCache.Load(d.Path)
		if !ok {
			continue
		}
		info := e.(smartEntry).info
		switch {
		case info.Passed != nil && !*info.Passed:
			h.Disks.Failing++
		case len(info.Warnings) > 0:
			h.Disks.Warning++
		}
		smp := info.Sample()
		if t := smp.Temperature; t != nil && *t > 0 && (h.Temps.Disk == nil || *t > *h.Temps.Disk) {
			h.Temps.Disk, h.Temps.DiskName = t, d.Path
		}
		if w := smp.PercentUsed; w != nil && (h.Disks.MaxWear == nil || *w > *h.Disks.MaxWear) {
			h.Disks.MaxWear = w
		}
	}
	if d := s.diskAlerts.Load(); d != nil {
		for _, a := range *d {
			if len(h.Disks.Issues) < 5 {
				h.Disks.Issues = append(h.Disks.Issues, a.Message)
			}
		}
	}
	raids, _ := storage.ListRaid()
	pools, _ := storage.ListZPools(false)
	h.Arrays.Total = len(raids) + len(pools)
	for _, r := range raids {
		switch r.Health {
		case "degraded", "failed", "inactive":
			h.Arrays.Degraded++
			h.Arrays.Issues = append(h.Arrays.Issues, fmt.Sprintf("RAID array %s (%s) is %s", r.Label, r.Name, r.Health))
		case "rebuilding":
			h.Arrays.Rebuilding++
		}
	}
	for _, p := range pools {
		if p.Health != "ONLINE" {
			h.Arrays.Degraded++
			h.Arrays.Issues = append(h.Arrays.Issues, fmt.Sprintf("ZFS pool %s is %s", p.Name, p.Health))
		}
	}
	vols := s.volumeSummaries(raids, pools)
	fv := make([]fleetVol, 0, len(vols))
	for _, v := range vols {
		fv = append(fv, fleetVol{Name: v.Name, Total: int64(v.Total), Used: int64(v.Used), Health: v.Health})
	}
	h.Space = fleetSpaceOf(fv)
	for _, a := range s.alerts(raids, pools, vols) {
		switch a.Level {
		case "error":
			h.Alerts.Errors++
		case "warning":
			h.Alerts.Warnings++
		default:
			continue
		}
		if len(h.Alerts.Messages) < 6 {
			h.Alerts.Messages = append(h.Alerts.Messages, fleetAlertMsg{a.Level, a.Message})
		}
	}
	if t := hardware.CPUTemp(); !math.IsNaN(t) && t > 0 {
		t = math.Round(t)
		h.Temps.CPU = &t
	}
	fleetHere.h, fleetHere.at = h, time.Now()
	return h
}

// fleetHealthRow is one NAS of the dashboard.
type fleetHealthRow struct {
	ID      string        `json:"id"` // FleetMember.ID or "local"
	Name    string        `json:"name"`
	Host    string        `json:"host"`
	Online  bool          `json:"online"`
	Error   string        `json:"error,omitempty"`
	Health  *fleetHealth  `json:"health,omitempty"` // last report (kept while offline)
	Level   string        `json:"level"`
	Reasons []fleetReason `json:"reasons"`
	Checked time.Time     `json:"checked"`           // last attempt
	Seen    time.Time     `json:"seen,omitzero"`     // last answer
	Pending bool          `json:"pending,omitempty"` // not asked yet
}

var fleetDash struct {
	sync.Mutex
	rows       map[string]fleetHealthRow // member ID → row
	at         time.Time                 // end of the last refresh
	viewed     time.Time
	refreshing bool
	done       chan struct{} // closed at the end of the running refresh
}

// fleetHealthOf asks a NAS for its health (or its summary when it is older).
func fleetHealthOf(ctx context.Context, m store.FleetMember) (*fleetHealth, error) {
	var h fleetHealth
	err := fleetCall(ctx, m, http.MethodGet, "/api/fleet/peer/health", nil, &h)
	var pe *peerError
	if errors.As(err, &pe) && (pe.Status == http.StatusNotFound || pe.Status == http.StatusMethodNotAllowed) {
		var sum map[string]any
		if err := fleetCall(ctx, m, http.MethodGet, "/api/fleet/peer/summary", nil, &sum); err != nil {
			return nil, err
		}
		return fleetFromSummary(sum), nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// fleetHealthRefresh asks every NAS for its health; a refresh already
// running is waited for instead of starting another.
func (s *Server) fleetHealthRefresh(ctx context.Context) {
	fleetDash.Lock()
	if fleetDash.refreshing {
		done := fleetDash.done
		fleetDash.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		return
	}
	fleetDash.refreshing, fleetDash.done = true, make(chan struct{})
	prev := fleetDash.rows
	fleetDash.Unlock()

	var members []store.FleetMember
	s.store.Read(func(st *store.State) { members = slices.Clone(st.Fleet.Members) })
	rows := map[string]fleetHealthRow{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			row := fleetHealthRow{ID: m.ID, Name: m.Name, Host: m.Host, Checked: time.Now()}
			h, err := fleetHealthOf(c, m)
			if err != nil {
				var pe *peerError
				if errors.As(err, &pe) {
					row.Error = fmt.Sprintf("HTTP %d: %s", pe.Status, pe.Msg)
				} else {
					row.Error = err.Error()
				}
				// The last report stays visible, marked offline.
				if p, ok := prev[m.ID]; ok {
					row.Health, row.Seen = p.Health, p.Seen
				}
			} else {
				row.Online, row.Health, row.Seen = true, h, time.Now()
				if h.Hostname != "" {
					row.Name = h.Hostname
				}
			}
			mu.Lock()
			rows[m.ID] = row
			mu.Unlock()
		}()
	}
	wg.Wait()

	fleetDash.Lock()
	fleetDash.rows, fleetDash.at, fleetDash.refreshing = rows, time.Now(), false
	close(fleetDash.done)
	fleetDash.Unlock()
}

// fleetHealthLoop keeps the reports fresh in the background.
func (s *Server) fleetHealthLoop() {
	time.Sleep(45 * time.Second)
	for {
		n := 0
		s.store.Read(func(st *store.State) { n = len(st.Fleet.Members) })
		fleetDash.Lock()
		every := 10 * time.Minute
		if time.Since(fleetDash.viewed) < 15*time.Minute {
			every = 2 * time.Minute
		}
		due := time.Since(fleetDash.at) >= every
		fleetDash.Unlock()
		if n > 0 && due {
			s.fleetHealthRefresh(context.Background())
		}
		time.Sleep(30 * time.Second)
	}
}

type fleetDashboard struct {
	Rows       []fleetHealthRow  `json:"rows"`
	Totals     fleetHealthTotals `json:"totals"`
	Refreshed  time.Time         `json:"refreshed,omitzero"`
	Refreshing bool              `json:"refreshing"`
}

// fleetDashboardView assembles the dashboard: this NAS first, then the
// members in the console's order.
func (s *Server) fleetDashboardView() fleetDashboard {
	local := s.fleetHealthHere()
	rows := []fleetHealthRow{{ID: "local", Name: local.Hostname, Online: true, Health: &local, Checked: local.At, Seen: local.At}}
	var members []store.FleetMember
	s.store.Read(func(st *store.State) { members = slices.Clone(st.Fleet.Members) })
	fleetDash.Lock()
	for _, m := range members {
		if r, ok := fleetDash.rows[m.ID]; ok {
			r.Host = m.Host
			rows = append(rows, r)
		} else {
			rows = append(rows, fleetHealthRow{ID: m.ID, Name: m.Name, Host: m.Host, Pending: true})
		}
	}
	out := fleetDashboard{Refreshed: fleetDash.at, Refreshing: fleetDash.refreshing}
	fleetDash.Unlock()
	items := make([]fleetHealthItem, len(rows))
	for i := range rows {
		r := &rows[i]
		if r.Pending {
			r.Reasons, r.Level = []fleetReason{}, levelOK
		} else {
			r.Reasons = fleetReasons(r.Online, r.Health)
			if r.Reasons == nil {
				r.Reasons = []fleetReason{}
			}
			r.Level = fleetLevel(r.Reasons)
		}
		items[i] = fleetHealthItem{Online: r.Online, Level: r.Level, Health: r.Health}
	}
	out.Rows, out.Totals = rows, fleetHealthSum(items)
	return out
}

func (s *Server) fleetHealthRoutes() {
	s.fleetPeer("GET /api/fleet/peer/health", "", false, func(r *http.Request, m store.FleetManager) (any, error) {
		h := s.fleetHealthHere()
		acc := accessOf(m)
		h.Access = &acc
		return h, nil
	})
	s.admin("GET /api/fleet/health", func(r *http.Request) (any, error) {
		fleetDash.Lock()
		fleetDash.viewed = time.Now()
		never, stale := fleetDash.at.IsZero(), time.Since(fleetDash.at) > time.Minute
		fleetDash.Unlock()
		switch {
		case never:
			// First look: wait a little for the first reports.
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			s.fleetHealthRefresh(ctx)
			cancel()
		case stale:
			go s.fleetHealthRefresh(context.Background())
		}
		return s.fleetDashboardView(), nil
	})
	s.admin("POST /api/fleet/health/refresh", func(r *http.Request) (any, error) {
		fleetHere.Lock()
		fleetHere.at = time.Time{}
		fleetHere.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		s.fleetHealthRefresh(ctx)
		return s.fleetDashboardView(), nil
	})
}
