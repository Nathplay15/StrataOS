package api

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"strataos/internal/cloudsync"
	"strataos/internal/jobs"
	"strataos/internal/scheduler"
	"strataos/internal/store"
)

type cloudView struct {
	store.CloudSync
	NextRun *time.Time `json:"nextRun"`
	Running bool       `json:"running"`
}

// maskCloud replaces credentials by a placeholder before a task leaves the server.
func maskCloud(c store.CloudSync) store.CloudSync {
	m := map[string]string{}
	for k, v := range c.Secrets {
		if v != "" {
			m[k] = masked
		}
	}
	c.Secrets = m
	return c
}

func (s *Server) unmaskCloud(c *store.CloudSync, id string) {
	s.store.Read(func(st *store.State) {
		old := st.Cloud(id)
		if old == nil {
			return
		}
		for k, v := range c.Secrets {
			if v == masked {
				c.Secrets[k] = old.Secrets[k]
			}
		}
	})
}

func (s *Server) runCloud(id, user string, manual bool) (jobs.Snapshot, error) {
	var t store.CloudSync
	var found bool
	s.store.Read(func(st *store.State) {
		if x := st.Cloud(id); x != nil {
			t, found = *x, true
			t.Secrets = maps(x.Secrets)
		}
	})
	if !found {
		return jobs.Snapshot{}, notFound("cloud sync not found")
	}
	key := "cloud-" + id
	if _, busy := running.LoadOrStore(key, true); busy {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "this sync is already running"}
	}
	if user == "" {
		user = "scheduler"
	}
	return s.jobs.Start("cloudsync", "Synchronisation cloud : "+t.Name, user, func(ctx context.Context, j *jobs.Job) error {
		defer running.Delete(key)
		start := time.Now()
		msg, err := cloudsync.Run(ctx, t, cloudsync.Progress{Set: j.SetProgress, Log: j.Logf})
		run := store.ScheduleRun{Started: start, Duration: time.Since(start).Seconds(), Status: "success", Message: msg, Manual: manual}
		if err != nil {
			run.Status, run.Message = "failed", err.Error()
		} else {
			j.Logf("%s", msg)
		}
		token := cloudsync.RefreshedToken(&t)
		if uerr := s.store.Update(func(st *store.State) error {
			if x := st.Cloud(id); x != nil {
				x.History = append([]store.ScheduleRun{run}, x.History...)
				if len(x.History) > store.MaxScheduleHistory {
					x.History = x.History[:store.MaxScheduleHistory]
				}
				if err == nil && t.Direction == "bisync" {
					x.Synced = true
				}
				// rclone refreshed the OAuth token: keep the new one.
				if token != "" && x.Secrets != nil {
					x.Secrets["token"] = token
				}
			}
			return nil
		}); uerr != nil {
			slog.Error("save cloud sync history", "err", uerr)
		}
		if err != nil {
			s.notify("Synchronisation cloud échouée", t.Name+" : "+err.Error())
		}
		return err
	}), nil
}

func maps(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (s *Server) dueClouds(tick time.Time) []string {
	var due []string
	s.store.Read(func(st *store.State) {
		for _, c := range st.Clouds {
			if !c.Enabled || c.Cron == "" {
				continue
			}
			if cr, err := scheduler.ParseCron(c.Cron); err == nil && cr.Match(tick) {
				due = append(due, c.ID)
			}
		}
	})
	return due
}

func (s *Server) cloudRoutes() {
	s.admin("GET /api/cloudsync", func(r *http.Request) (any, error) {
		out := []cloudView{}
		s.store.Read(func(st *store.State) {
			for _, c := range st.Clouds {
				v := cloudView{CloudSync: maskCloud(*c)}
				if cr, err := scheduler.ParseCron(c.Cron); err == nil && c.Enabled && c.Cron != "" {
					if n := cr.Next(time.Now()); !n.IsZero() {
						v.NextRun = &n
					}
				}
				_, v.Running = running.Load("cloud-" + c.ID)
				if v.History == nil {
					v.History = []store.ScheduleRun{}
				}
				out = append(out, v)
			}
		})
		return map[string]any{"installed": cloudsync.Installed(), "tasks": out, "providers": cloudsync.Providers}, nil
	})

	save := func(r *http.Request, id string) (any, error) {
		var req store.CloudSync
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		s.unmaskCloud(&req, id)
		if err := cloudsync.Validate(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		if req.Cron != "" {
			if _, err := scheduler.ParseCron(req.Cron); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		err := s.store.Update(func(st *store.State) error {
			if id == "" {
				req.ID, req.CreatedAt, req.History, req.Synced = shortID(), time.Now(), []store.ScheduleRun{}, false
				c := req
				st.Clouds = append(st.Clouds, &c)
				return nil
			}
			x := st.Cloud(id)
			if x == nil {
				return notFound("cloud sync not found")
			}
			// Another folder or direction means a new two-way baseline.
			synced := x.Synced && x.LocalPath == req.LocalPath && x.RemotePath == req.RemotePath && x.Provider == req.Provider && x.Direction == req.Direction
			req.ID, req.CreatedAt, req.History, req.Synced = x.ID, x.CreatedAt, x.History, synced
			*x = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "cloudsync", "Saved cloud sync "+req.Name)
		return maskCloud(req), nil
	}
	s.admin("POST /api/cloudsync", func(r *http.Request) (any, error) { return save(r, "") })
	s.admin("PUT /api/cloudsync/{id}", func(r *http.Request) (any, error) { return save(r, r.PathValue("id")) })

	s.admin("DELETE /api/cloudsync/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var name string
		err := s.store.Update(func(st *store.State) error {
			x := st.Cloud(id)
			if x == nil {
				return notFound("cloud sync not found")
			}
			name = x.Name
			st.Clouds = slices.DeleteFunc(st.Clouds, func(c *store.CloudSync) bool { return c.ID == id })
			return nil
		})
		if err == nil {
			cloudsync.Remove(id)
			s.audit(r, "cloudsync", "Deleted cloud sync "+name+" (files kept on both sides)")
		}
		return nil, err
	})

	s.admin("POST /api/cloudsync/{id}/run", func(r *http.Request) (any, error) {
		return s.runCloud(r.PathValue("id"), userOf(r), true)
	})

	// Starts a two-way sync over: both sides are merged again, nothing is deleted.
	s.admin("POST /api/cloudsync/{id}/resync", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		if err := s.store.Update(func(st *store.State) error {
			x := st.Cloud(id)
			if x == nil {
				return notFound("cloud sync not found")
			}
			x.Synced = false
			return nil
		}); err != nil {
			return nil, err
		}
		return s.runCloud(id, userOf(r), true)
	})

	withTask := func(r *http.Request) (store.CloudSync, error) {
		var req store.CloudSync
		if err := decode(r, &req); err != nil {
			return req, err
		}
		s.unmaskCloud(&req, req.ID)
		req.ID = ""
		if req.LocalPath == "" {
			req.LocalPath = "/srv/strata/x"
		}
		if req.Direction == "" {
			req.Direction = "upload"
		}
		if req.Name == "" {
			req.Name = "test"
		}
		rp := req.RemotePath
		if err := cloudsync.Validate(&req); err != nil && !(rp == "" && err.Error() == "the remote folder must start with the bucket name (bucket/folder)") {
			return req, badRequest(err.Error())
		}
		return req, nil
	}

	s.admin("POST /api/cloudsync/test", func(r *http.Request) (any, error) {
		t, err := withTask(r)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		msg, err := cloudsync.Test(ctx, t)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return map[string]string{"message": msg}, nil
	})

	s.admin("POST /api/cloudsync/browse", func(r *http.Request) (any, error) {
		path := r.URL.Query().Get("path")
		t, err := withTask(r)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		dirs, err := cloudsync.Browse(ctx, t, path)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return dirs, nil
	})
}
