package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strataos/internal/antivirus"
	"sync"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/scheduler"
	"strataos/internal/shares"
	"strataos/internal/storage"
	"strataos/internal/store"
)

type scheduleView struct {
	store.Schedule
	NextRun *time.Time `json:"nextRun"`
	Running bool       `json:"running"`
}

// running tracks schedules currently executing so a slow run never overlaps the next one.
var running sync.Map

func (s *Server) resolveVolume(id string) (string, bool) {
	var mp string
	s.store.Read(func(st *store.State) {
		if v := st.Volume(id); v != nil {
			mp = v.MountPoint
		}
	})
	return mp, mp != "" && storage.IsMountpoint(mp)
}

// schedulerLoop fires due schedules at the start of every minute.
func (s *Server) schedulerLoop() {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		time.Sleep(time.Until(next))
		tick := next
		var due []string
		s.store.Read(func(st *store.State) {
			for _, sc := range st.Schedules {
				if !sc.Enabled {
					continue
				}
				c, err := scheduler.ParseCron(sc.Cron)
				if err == nil && c.Match(tick) {
					due = append(due, sc.ID)
				}
			}
		})
		for _, id := range due {
			s.runSchedule(id, "", false)
		}
		for _, id := range s.dueBackups(tick) {
			s.runBackup(id, "", false)
		}
		for _, id := range s.dueClouds(tick) {
			s.runCloud(id, "", false)
		}
		if tick.Hour() == 3 && tick.Minute() == 30 {
			go s.purgeRecycleBins()
		}
	}
}

// runSchedule starts a schedule as a background job and records the outcome in its history.
func (s *Server) runSchedule(id, user string, manual bool) (jobs.Snapshot, error) {
	var sc store.Schedule
	var found bool
	s.store.Read(func(st *store.State) {
		if x := st.Schedule(id); x != nil {
			sc, found = *x, true
		}
	})
	if !found {
		return jobs.Snapshot{}, notFound("task not found")
	}
	if _, busy := running.LoadOrStore(id, true); busy {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "this task is already running"}
	}
	if user == "" {
		user = "scheduler"
	}
	return s.jobs.Start("schedule", "Tâche planifiée : "+sc.Name, user, func(ctx context.Context, j *jobs.Job) error {
		defer running.Delete(id)
		start := time.Now()
		msg, err := scheduler.Execute(ctx, sc, s.resolveVolume, j.Logf)
		run := store.ScheduleRun{Started: start, Duration: time.Since(start).Seconds(), Status: "success", Message: msg, Manual: manual}
		if err != nil {
			run.Status, run.Message = "failed", err.Error()
			if errors.Is(err, antivirus.ErrInfected) {
				s.store.Log("error", "antivirus", "", err.Error())
				s.notify("Virus détecté", err.Error())
			}
		} else {
			j.Logf("%s", msg)
		}
		if uerr := s.store.Update(func(st *store.State) error {
			if x := st.Schedule(id); x != nil {
				x.History = append([]store.ScheduleRun{run}, x.History...)
				if len(x.History) > store.MaxScheduleHistory {
					x.History = x.History[:store.MaxScheduleHistory]
				}
			}
			return nil
		}); uerr != nil {
			slog.Error("save schedule history", "err", uerr)
		}
		if errors.Is(err, antivirus.ErrInfected) {
			// Already reported as "virus found": no second "task failed" alert.
			j.Logf("%s", err.Error())
			return nil
		}
		return err
	}), nil
}

func (s *Server) scheduleRoutes() {
	s.admin("GET /api/schedules", func(r *http.Request) (any, error) {
		out := []scheduleView{}
		s.store.Read(func(st *store.State) {
			for _, sc := range st.Schedules {
				v := scheduleView{Schedule: *sc}
				if c, err := scheduler.ParseCron(sc.Cron); err == nil && sc.Enabled {
					n := c.Next(time.Now())
					if !n.IsZero() {
						v.NextRun = &n
					}
				}
				_, v.Running = running.Load(sc.ID)
				if v.History == nil {
					v.History = []store.ScheduleRun{}
				}
				out = append(out, v)
			}
		})
		return out, nil
	})

	// Preview of the next occurrences of a cron expression (used by the editor).
	s.admin("GET /api/schedules/preview", func(r *http.Request) (any, error) {
		c, err := scheduler.ParseCron(r.URL.Query().Get("cron"))
		if err != nil {
			return nil, badRequest(err.Error())
		}
		var next []time.Time
		t := time.Now()
		for range 5 {
			t = c.Next(t)
			if t.IsZero() {
				break
			}
			next = append(next, t)
		}
		return next, nil
	})

	// Everything a task can target.
	s.admin("GET /api/schedules/targets", func(r *http.Request) (any, error) {
		type opt struct {
			Kind       string   `json:"kind"`
			Value      string   `json:"value"`
			Label      string   `json:"label"`
			Subvolumes []string `json:"subvolumes,omitempty"`
		}
		snap, scrub, disks := []opt{}, []opt{}, []opt{}
		if arrays, err := storage.ListRaid(); err == nil {
			for _, a := range arrays {
				name := firstNonEmpty(a.Label, a.Name)
				scrub = append(scrub, opt{Kind: "mdadm", Value: name, Label: fmt.Sprintf("RAID %s (%s)", name, a.Level)})
			}
		}
		if storage.ZFSAvailable() {
			pools, _ := storage.ListZPools(false)
			for _, p := range pools {
				scrub = append(scrub, opt{Kind: "zfs", Value: p.Name, Label: "Pool ZFS " + p.Name})
			}
			ds, _ := storage.ListZDatasets("", "filesystem,volume")
			for _, d := range ds {
				snap = append(snap, opt{Kind: "zfs", Value: d.Name, Label: "ZFS " + d.Name})
			}
		}
		var vols []store.Volume
		s.store.Read(func(st *store.State) {
			for _, v := range st.Volumes {
				if v.FSType == "btrfs" {
					vols = append(vols, *v)
				}
			}
		})
		for _, v := range vols {
			o := opt{Kind: "btrfs", Value: v.ID, Label: "Btrfs " + v.Name}
			if storage.IsMountpoint(v.MountPoint) {
				if info, err := storage.BtrfsDetail(v.MountPoint); err == nil {
					for _, sv := range info.Subvolumes {
						if !sv.Snapshot {
							o.Subvolumes = append(o.Subvolumes, sv.Path)
						}
					}
				}
			}
			snap = append(snap, o)
			scrub = append(scrub, opt{Kind: "btrfs", Value: v.ID, Label: "Btrfs " + v.Name})
		}
		if list, err := storage.ListDisks(); err == nil {
			for _, d := range list {
				disks = append(disks, opt{Kind: "disks", Value: d.Path, Label: fmt.Sprintf("%s (%s)", d.Path, firstNonEmpty(d.Model, d.Name))})
			}
		}
		return map[string]any{"snapshot": snap, "scrub": scrub, "disks": disks}, nil
	})

	save := func(r *http.Request, id string) (any, error) {
		var req store.Schedule
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := scheduler.Validate(&req, s.resolveVolume); err != nil {
			return nil, badRequest(err.Error())
		}
		err := s.store.Update(func(st *store.State) error {
			if id == "" {
				req.ID = shortID()
				req.CreatedAt = time.Now()
				req.History = []store.ScheduleRun{}
				c := req
				st.Schedules = append(st.Schedules, &c)
				return nil
			}
			x := st.Schedule(id)
			if x == nil {
				return notFound("task not found")
			}
			req.ID, req.CreatedAt, req.History = x.ID, x.CreatedAt, x.History
			*x = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "schedules", "Saved scheduled task "+req.Name)
		return req, nil
	}
	s.admin("POST /api/schedules", func(r *http.Request) (any, error) { return save(r, "") })
	s.admin("PUT /api/schedules/{id}", func(r *http.Request) (any, error) { return save(r, r.PathValue("id")) })

	s.admin("DELETE /api/schedules/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var name string
		err := s.store.Update(func(st *store.State) error {
			x := st.Schedule(id)
			if x == nil {
				return notFound("task not found")
			}
			name = x.Name
			st.Schedules = slices.DeleteFunc(st.Schedules, func(v *store.Schedule) bool { return v.ID == id })
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "schedules", "Deleted scheduled task "+name+" (existing snapshots kept)")
		return nil, nil
	})

	s.admin("POST /api/schedules/{id}/run", func(r *http.Request) (any, error) {
		return s.runSchedule(r.PathValue("id"), userOf(r), true)
	})

	// Creates a sensible maintenance plan for every array, pool and disk not covered yet.
	s.admin("POST /api/schedules/defaults", func(r *http.Request) (any, error) {
		var plan []store.Schedule
		add := func(sc store.Schedule) { plan = append(plan, sc) }
		if arrays, err := storage.ListRaid(); err == nil {
			for _, a := range arrays {
				name := firstNonEmpty(a.Label, a.Name)
				add(store.Schedule{Name: "Vérification RAID " + name, Type: "scrub", TargetKind: "mdadm", Target: name, Cron: "0 1 1 * *"})
			}
		}
		if storage.ZFSAvailable() {
			pools, _ := storage.ListZPools(false)
			for _, p := range pools {
				add(store.Schedule{Name: "Scrub ZFS " + p.Name, Type: "scrub", TargetKind: "zfs", Target: p.Name, Cron: "0 2 8 * *"})
				add(store.Schedule{Name: "Snapshots quotidiens " + p.Name, Type: "snapshot", TargetKind: "zfs", Target: p.Name, Recursive: true, Cron: "0 0 * * *", KeepCount: 14, KeepDays: 0})
			}
		}
		var vols []store.Volume
		s.store.Read(func(st *store.State) {
			for _, v := range st.Volumes {
				if v.FSType == "btrfs" {
					vols = append(vols, *v)
				}
			}
		})
		for _, v := range vols {
			add(store.Schedule{Name: "Scrub Btrfs " + v.Name, Type: "scrub", TargetKind: "btrfs", Target: v.ID, Cron: "0 3 15 * *"})
			add(store.Schedule{Name: "Snapshots quotidiens " + v.Name, Type: "snapshot", TargetKind: "btrfs", Target: v.ID, Cron: "0 0 * * *", KeepCount: 14})
		}
		add(store.Schedule{Name: "Test SMART court", Type: "smart", TargetKind: "disks", Target: "all", SmartTest: "short", Cron: "0 4 * * 0"})
		add(store.Schedule{Name: "Test SMART long", Type: "smart", TargetKind: "disks", Target: "all", SmartTest: "long", Cron: "0 5 22 * *"})

		created := 0
		err := s.store.Update(func(st *store.State) error {
			for _, sc := range plan {
				dup := slices.ContainsFunc(st.Schedules, func(x *store.Schedule) bool {
					return x.Type == sc.Type && x.TargetKind == sc.TargetKind && x.Target == sc.Target && x.SmartTest == sc.SmartTest
				})
				if dup {
					continue
				}
				c := sc
				c.ID, c.Enabled, c.CreatedAt, c.History = shortID(), true, time.Now(), []store.ScheduleRun{}
				st.Schedules = append(st.Schedules, &c)
				created++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "schedules", fmt.Sprintf("Created %d recommended maintenance tasks", created))
		return map[string]int{"created": created}, nil
	})
}

// purgeRecycleBins empties network recycle bins older than Settings.TrashDays (daily).
func (s *Server) purgeRecycleBins() {
	var days int
	var paths []string
	s.store.Read(func(st *store.State) {
		days = st.Settings.TrashDays
		for _, sh := range st.Shares {
			if sh.RecycleBin {
				paths = append(paths, sh.Path)
			}
		}
	})
	if days <= 0 {
		return
	}
	var files int
	var freed int64
	for _, p := range paths {
		n, b := shares.PurgeRecycle(p, days)
		files += n
		freed += b
	}
	if files > 0 {
		s.store.Log("info", "shares", "", fmt.Sprintf("Recycle bins: %d file(s) older than %d days removed (%d MiB freed)", files, days, freed>>20))
	}
}
