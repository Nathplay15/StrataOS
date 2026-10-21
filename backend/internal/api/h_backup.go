package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"strataos/internal/backup"
	"strataos/internal/jobs"
	"strataos/internal/scheduler"
	"strataos/internal/storage"
	"strataos/internal/store"
)

const masked = "********"

type backupView struct {
	store.BackupTask
	NextRun *time.Time `json:"nextRun"`
	Running bool       `json:"running"`
}

// maskBackup hides secrets before a task leaves the server.
func maskBackup(b store.BackupTask) store.BackupTask {
	if b.Password != "" {
		b.Password = masked
	}
	if b.SecretKey != "" {
		b.SecretKey = masked
	}
	if b.VaultToken != "" {
		b.VaultToken = masked
	}
	if b.RemotePassword != "" {
		b.RemotePassword = masked
	}
	return b
}

// unmaskBackup restores secrets the UI sent back masked.
func (s *Server) unmaskBackup(b *store.BackupTask, id string) {
	s.store.Read(func(st *store.State) {
		old := st.Backup(id)
		if old == nil {
			return
		}
		if b.Password == masked {
			b.Password = old.Password
		}
		if b.SecretKey == masked {
			b.SecretKey = old.SecretKey
		}
		if b.VaultToken == masked {
			b.VaultToken = old.VaultToken
		}
		if b.RemotePassword == masked {
			b.RemotePassword = old.RemotePassword
		}
	})
}

func (s *Server) runBackup(id, user string, manual bool) (jobs.Snapshot, error) {
	var t store.BackupTask
	var found bool
	s.store.Read(func(st *store.State) {
		if x := st.Backup(id); x != nil {
			t, found = *x, true
		}
	})
	if !found {
		return jobs.Snapshot{}, notFound("backup not found")
	}
	key := "backup-" + id
	if _, busy := running.LoadOrStore(key, true); busy {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "this backup is already running"}
	}
	if user == "" {
		user = "scheduler"
	}
	return s.jobs.Start("backup", "Backup: "+t.Name, user, func(ctx context.Context, j *jobs.Job) error {
		defer running.Delete(key)
		start := time.Now()
		var msg string
		var err error
		local := t.Sources
		if backup.IsPull(&t) {
			local = []string{t.DestPath} // the remote paths mean nothing here
		}
		if name := s.lockedShareIn(local); name != "" {
			// An empty mount point would be copied (and, with deletion, wipe the backup).
			err = fmt.Errorf("the encrypted share %s is locked: unlock it before the backup", name)
		} else {
			msg, err = backup.Run(ctx, t, backup.Progress{Set: j.SetProgress, Log: j.Logf})
		}
		run := store.ScheduleRun{Started: start, Duration: time.Since(start).Seconds(), Status: "success", Message: msg, Manual: manual}
		if err != nil {
			run.Status, run.Message = "failed", err.Error()
		} else {
			j.Logf("%s", msg)
		}
		if uerr := s.store.Update(func(st *store.State) error {
			if x := st.Backup(id); x != nil {
				if err == nil && x.Kind == "sync" {
					x.Synced = true // the next runs propagate changes and deletions
				}
				x.History = append([]store.ScheduleRun{run}, x.History...)
				if len(x.History) > store.MaxScheduleHistory {
					x.History = x.History[:store.MaxScheduleHistory]
				}
			}
			return nil
		}); uerr != nil {
			slog.Error("save backup history", "err", uerr)
		}
		return err
	}), nil
}

// dueBackups returns the enabled backups whose schedule matches tick.
func (s *Server) dueBackups(tick time.Time) []string {
	var due []string
	s.store.Read(func(st *store.State) {
		for _, b := range st.Backups {
			if !b.Enabled || b.Cron == "" {
				continue
			}
			if c, err := scheduler.ParseCron(b.Cron); err == nil && c.Match(tick) {
				due = append(due, b.ID)
			}
		}
	})
	return due
}

func (s *Server) backupRoutes() {
	s.admin("GET /api/backups", func(r *http.Request) (any, error) {
		out := []backupView{}
		s.store.Read(func(st *store.State) {
			for _, b := range st.Backups {
				v := backupView{BackupTask: maskBackup(*b)}
				if c, err := scheduler.ParseCron(b.Cron); err == nil && b.Enabled && b.Cron != "" {
					if n := c.Next(time.Now()); !n.IsZero() {
						v.NextRun = &n
					}
				}
				_, v.Running = running.Load("backup-" + b.ID)
				if v.History == nil {
					v.History = []store.ScheduleRun{}
				}
				out = append(out, v)
			}
		})
		return out, nil
	})

	s.admin("GET /api/backups/ssh-key", func(r *http.Request) (any, error) {
		k, err := backup.PublicKey()
		return map[string]string{"publicKey": k}, err
	})

	// What can be backed up: share and volume folders, ZFS datasets.
	s.admin("GET /api/backups/sources", func(r *http.Request) (any, error) {
		type src struct {
			Path  string `json:"path"`
			Label string `json:"label"`
		}
		folders, datasets := []src{}, []src{}
		seen := map[string]bool{}
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				if !seen[sh.Path] {
					seen[sh.Path] = true
					folders = append(folders, src{sh.Path, "Share " + sh.Name})
				}
			}
			for _, v := range st.Volumes {
				if !seen[v.MountPoint] {
					seen[v.MountPoint] = true
					folders = append(folders, src{v.MountPoint, "Volume " + v.Name})
				}
			}
		})
		if storage.ZFSAvailable() {
			ds, _ := storage.ListZDatasets("", "filesystem")
			for _, d := range ds {
				datasets = append(datasets, src{d.Name, d.Name})
				if strings.HasPrefix(d.Mountpoint, storage.MountRoot+"/") && !seen[d.Mountpoint] {
					seen[d.Mountpoint] = true
					folders = append(folders, src{d.Mountpoint, "ZFS " + d.Name})
				}
			}
		}
		return map[string]any{"folders": folders, "datasets": datasets}, nil
	})

	save := func(r *http.Request, id string) (any, error) {
		var req store.BackupTask
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		s.unmaskBackup(&req, id)
		if err := backup.Validate(&req); err != nil {
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
				st.Backups = append(st.Backups, &c)
				return nil
			}
			x := st.Backup(id)
			if x == nil {
				return notFound("backup not found")
			}
			req.ID, req.CreatedAt, req.History = x.ID, x.CreatedAt, x.History
			// A two-way sync starts again with a merge when its folders change.
			req.Synced = x.Synced && req.Kind == x.Kind && slices.Equal(req.Sources, x.Sources) && req.Host == x.Host && req.DestPath == x.DestPath
			*x = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "backup", "Saved backup task "+req.Name)
		return maskBackup(req), nil
	}
	s.admin("POST /api/backups", func(r *http.Request) (any, error) { return save(r, "") })
	s.admin("PUT /api/backups/{id}", func(r *http.Request) (any, error) { return save(r, r.PathValue("id")) })

	s.admin("DELETE /api/backups/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var name string
		err := s.store.Update(func(st *store.State) error {
			x := st.Backup(id)
			if x == nil {
				return notFound("backup not found")
			}
			name = x.Name
			if x.Kind == "sync" {
				backup.RemoveSync(id)
			}
			st.Backups = slices.DeleteFunc(st.Backups, func(b *store.BackupTask) bool { return b.ID == id })
			return nil
		})
		if err == nil {
			s.audit(r, "backup", "Deleted backup task "+name+" (backed-up data kept)")
		}
		return nil, err
	})

	s.admin("POST /api/backups/{id}/run", func(r *http.Request) (any, error) {
		return s.runBackup(r.PathValue("id"), userOf(r), true)
	})

	// Two-way sync: start again with a merge of both sides (nothing deleted).
	s.admin("POST /api/backups/{id}/resync", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		if _, busy := running.Load("backup-" + id); busy {
			return nil, &Error{Status: http.StatusConflict, Msg: "this backup is already running"}
		}
		err := s.store.Update(func(st *store.State) error {
			x := st.Backup(id)
			if x == nil || x.Kind != "sync" {
				return notFound("two-way sync not found")
			}
			x.Synced = false
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "backup", "Two-way sync "+id+" initialised again")
		return s.runBackup(id, userOf(r), true)
	})

	// Checks a destination before saving (the task may be new or edited).
	s.admin("POST /api/backups/test", func(r *http.Request) (any, error) {
		var req store.BackupTask
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		s.unmaskBackup(&req, req.ID)
		if err := backup.Validate(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		msg, err := backup.Test(ctx, req)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return map[string]string{"message": msg}, nil
	})

	s.admin("GET /api/backups/{id}/snapshots", func(r *http.Request) (any, error) {
		var t store.BackupTask
		var found bool
		s.store.Read(func(st *store.State) {
			if x := st.Backup(r.PathValue("id")); x != nil {
				t, found = *x, true
			}
		})
		if !found {
			return nil, notFound("backup not found")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		snaps, err := backup.Snapshots(ctx, t)
		if snaps == nil && err == nil {
			snaps = []backup.ResticSnapshot{}
		}
		return snaps, err
	})

	s.admin("POST /api/backups/{id}/restore", func(r *http.Request) (any, error) {
		var req struct {
			Snapshot string `json:"snapshot"`
			Include  string `json:"include"`
			Target   string `json:"target"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var t store.BackupTask
		var found bool
		s.store.Read(func(st *store.State) {
			if x := st.Backup(r.PathValue("id")); x != nil {
				t, found = *x, true
			}
		})
		if !found {
			return nil, notFound("backup not found")
		}
		s.audit(r, "backup", "Restoring "+t.Name+" snapshot "+req.Snapshot+" to "+req.Target)
		return s.jobs.Start("restore", "Restore: "+t.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			msg, err := backup.Restore(ctx, t, req.Snapshot, req.Include, req.Target, backup.Progress{Set: j.SetProgress, Log: j.Logf})
			if err == nil {
				j.Logf("%s", msg)
			}
			return err
		}), nil
	})
}
