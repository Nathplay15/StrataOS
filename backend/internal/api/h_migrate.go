package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/migrate"
	"strataos/internal/scheduler"
	"strataos/internal/storage"
	"strataos/internal/store"
)

// Migration assistant (roadmap 0.15): import data from another NAS (SMB, NFS
// or rsync daemon) into a shared folder of this NAS, on demand or on a
// schedule. The copy is a plain rsync; the password of the source is stored
// apart and never sent back to the browser.

// migrating tracks imports currently running, so a slow run never overlaps.
var migrating sync.Map

type migrationView struct {
	store.Migration
	Password string     `json:"password"` // MASK when one is saved
	Running  bool       `json:"running"`
	NextRun  *time.Time `json:"nextRun,omitempty"`
}

func maskMigration(m store.Migration) migrationView {
	v := migrationView{Migration: m}
	if m.Password != "" {
		v.Password = masked
	}
	_, v.Running = migrating.Load(m.ID)
	if m.Enabled && m.Cron != "" {
		if c, err := scheduler.ParseCron(m.Cron); err == nil {
			if n := c.Next(time.Now()); !n.IsZero() {
				v.NextRun = &n
			}
		}
	}
	return v
}

// migrationDest maps "/Share/folder" to the absolute path inside the share.
func (s *Server) migrationDest(dest string) (string, error) {
	name, sub, _ := strings.Cut(strings.TrimPrefix(dest, "/"), "/")
	var sh *store.Share
	s.store.Read(func(st *store.State) { sh = st.Share(name) })
	if sh == nil || sh.Path == "" {
		return "", badRequest("unknown shared folder")
	}
	abs := filepath.Join(sh.Path, filepath.Clean("/"+sub))
	if !strings.HasPrefix(abs, storage.MountRoot+"/") {
		return "", badRequest("the destination must be inside a shared folder")
	}
	return abs, nil
}

// dueMigrations returns the enabled migrations whose schedule matches tick.
func (s *Server) dueMigrations(tick time.Time) []string {
	var due []string
	s.store.Read(func(st *store.State) {
		for _, m := range st.Migrations {
			if !m.Enabled || m.Cron == "" {
				continue
			}
			if c, err := scheduler.ParseCron(m.Cron); err == nil && c.Match(tick) {
				due = append(due, m.ID)
			}
		}
	})
	return due
}

// runMigration starts one import as a background job and records its outcome.
func (s *Server) runMigration(id, user string, manual bool) (jobs.Snapshot, error) {
	var m store.Migration
	var found bool
	s.store.Read(func(st *store.State) {
		if x := st.Migration(id); x != nil {
			m, found = *x, true
		}
	})
	if !found {
		return jobs.Snapshot{}, notFound("migration not found")
	}
	if _, busy := migrating.LoadOrStore(id, true); busy {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "this import is already running"}
	}
	if user == "" {
		user = "scheduler"
	}
	return s.jobs.Start("migration", "Import: "+m.Name, user, func(ctx context.Context, j *jobs.Job) error {
		defer migrating.Delete(id)
		start := time.Now()
		run := store.MigrationRun{Started: start, Status: "success", Manual: manual}
		dest, err := s.migrationDest(m.Dest)
		if err == nil {
			if err = os.MkdirAll(dest, 0o755); err == nil {
				var plan migrate.Plan
				if plan, err = migrate.Build(m, dest); err == nil {
					j.Logf("Copying from %s to %s…", m.Host, m.Dest)
					var out string
					if out, err = migrate.Execute(ctx, m, plan); err == nil {
						run.Files, run.Bytes = migrate.ParseStats(out)
						run.Message = fmt.Sprintf("Import finished: %d files, %d bytes", run.Files, run.Bytes)
						j.Logf("Import finished: %d files, %d bytes", run.Files, run.Bytes)
					}
				}
			}
		}
		run.Duration = time.Since(start).Seconds()
		if err != nil {
			run.Status, run.Message = "failed", err.Error()
			s.store.Log("error", "migration", user, m.Name+": "+err.Error())
		} else if by, stopped := j.Cancelled(); stopped {
			run.Status, run.Message = "stopped", "Stopped by "+by
		}
		if uerr := s.store.Update(func(st *store.State) error {
			if x := st.Migration(id); x != nil {
				x.History = append([]store.MigrationRun{run}, x.History...)
				if len(x.History) > store.MaxScheduleHistory {
					x.History = x.History[:store.MaxScheduleHistory]
				}
			}
			return nil
		}); uerr != nil {
			slog.Error("save migration history", "err", uerr)
		}
		return err
	}), nil
}

func (s *Server) migrateRoutes() {
	s.admin("GET /api/migrations", func(r *http.Request) (any, error) {
		out := []migrationView{}
		s.store.Read(func(st *store.State) {
			for _, m := range st.Migrations {
				out = append(out, maskMigration(*m))
			}
		})
		return out, nil
	})
	s.admin("POST /api/migrations", func(r *http.Request) (any, error) {
		var m store.Migration
		if err := decode(r, &m); err != nil {
			return nil, err
		}
		if err := migrate.Validate(&m); err != nil {
			return nil, badRequest(err.Error())
		}
		if _, err := s.migrationDest(m.Dest); err != nil {
			return nil, err
		}
		if m.Cron != "" {
			if _, err := scheduler.ParseCron(m.Cron); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		m.Enabled = true
		err := s.store.Update(func(st *store.State) error {
			m.ID, m.CreatedAt, m.History = shortID(), time.Now(), []store.MigrationRun{}
			st.Migrations = append(st.Migrations, &m)
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "migration", "Created import "+m.Name+" from "+m.Host)
		return maskMigration(m), nil
	})
	s.admin("PUT /api/migrations/{id}", func(r *http.Request) (any, error) {
		var m store.Migration
		if err := decode(r, &m); err != nil {
			return nil, err
		}
		if err := migrate.Validate(&m); err != nil {
			return nil, badRequest(err.Error())
		}
		if _, err := s.migrationDest(m.Dest); err != nil {
			return nil, err
		}
		if m.Cron != "" {
			if _, err := scheduler.ParseCron(m.Cron); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		id := r.PathValue("id")
		err := s.store.Update(func(st *store.State) error {
			x := st.Migration(id)
			if x == nil {
				return notFound("migration not found")
			}
			if m.Password == "" || m.Password == masked {
				m.Password = x.Password // unchanged
			}
			m.ID, m.CreatedAt, m.History = x.ID, x.CreatedAt, x.History
			*x = m
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "migration", "Updated import "+m.Name)
		var out store.Migration
		s.store.Read(func(st *store.State) { out = *st.Migration(id) })
		return maskMigration(out), nil
	})
	s.admin("DELETE /api/migrations/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var name string
		err := s.store.Update(func(st *store.State) error {
			x := st.Migration(id)
			if x == nil {
				return notFound("migration not found")
			}
			name = x.Name
			for i, m := range st.Migrations {
				if m.ID == id {
					st.Migrations = append(st.Migrations[:i], st.Migrations[i+1:]...)
					break
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "migration", "Deleted import "+name)
		return nil, nil
	})
	s.admin("POST /api/migrations/{id}/run", func(r *http.Request) (any, error) {
		return s.runMigration(r.PathValue("id"), userOf(r), true)
	})
}
