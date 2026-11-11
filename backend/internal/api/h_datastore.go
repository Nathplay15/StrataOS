package api

// Backup datastores (0.11): restic repositories shared by backup tasks and
// NAS, with deduplicated VM and container images, sizes, scheduled
// integrity checks, pruning, whole-guest restores, and the disk backups
// opened read-only for the guided restore.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"strataos/internal/backup"
	"strataos/internal/jobs"
	"strataos/internal/protect"
	"strataos/internal/restore"
	"strataos/internal/scheduler"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/vms"
)

// resolveTask returns a copy of a task with the repository of its datastore.
func resolveTask(st *store.State, b *store.BackupTask) store.BackupTask {
	t := *b
	if t.DestType == "datastore" {
		if ds := st.Datastore(t.Datastore); ds != nil {
			backup.ApplyDatastore(&t, ds)
		}
	}
	return t
}

func maskDatastore(d store.Datastore) store.Datastore {
	if d.Password != "" {
		d.Password = masked
	}
	if d.SecretKey != "" {
		d.SecretKey = masked
	}
	if d.VaultToken != "" {
		d.VaultToken = masked
	}
	d.Verifies = slices.Clone(d.Verifies)
	return d
}

func verifyCron(d *store.Datastore) string {
	if d.Verify.Cron != "" {
		return d.Verify.Cron
	}
	return store.DefaultVerifyCron
}

func pruneCron(d *store.Datastore) string {
	if d.PruneCron != "" {
		return d.PruneCron
	}
	return store.DefaultPruneCron
}

func verifySubsets(d *store.Datastore) int {
	if d.Verify.Subsets > 0 {
		return d.Verify.Subsets
	}
	return store.DefaultVerifySubsets
}

// datastoreMedia is the medium of a datastore for the 3-2-1 rule.
func datastoreMedia(d *store.Datastore) (string, bool) {
	return protect.MediaOf(d.DestType, d.DestType == "local" && storage.OnUSB(d.DestPath))
}

type datastoreView struct {
	store.Datastore
	Tasks      []map[string]string `json:"tasks"`
	Running    string              `json:"running,omitempty"`
	JobID      string              `json:"jobId,omitempty"` // job of the running check or cleanup
	NextVerify *time.Time          `json:"nextVerify,omitempty"`
	NextPrune  *time.Time          `json:"nextPrune,omitempty"`
	LastOK     time.Time           `json:"lastVerified,omitzero"`
	Subsets    int                 `json:"subsets"`
	Media      string              `json:"media"`
	IsOffsite  bool                `json:"isOffsite"`
}

func nextOf(cron string, now time.Time) *time.Time {
	if c, err := scheduler.ParseCron(cron); err == nil {
		if n := c.Next(now); !n.IsZero() {
			return &n
		}
	}
	return nil
}

// lastVerified is the time of the newest successful check.
func lastVerified(d *store.Datastore) time.Time {
	for _, v := range d.Verifies {
		if v.Status == "success" {
			return v.Started
		}
	}
	return time.Time{}
}

func (s *Server) datastoreView(d *store.Datastore, st *store.State, now time.Time) datastoreView {
	v := datastoreView{Datastore: maskDatastore(*d), Tasks: []map[string]string{}, LastOK: lastVerified(d), Subsets: verifySubsets(d)}
	if v.Verifies == nil {
		v.Verifies = []store.VerifyRun{}
	}
	for _, b := range st.Backups {
		if b.DestType == "datastore" && b.Datastore == d.ID {
			v.Tasks = append(v.Tasks, map[string]string{"id": b.ID, "name": b.Name})
		}
	}
	if x, ok := running.Load("datastore-" + d.ID); ok {
		v.Running, _ = x.(string)
		v.JobID = s.runningJob("datastore-" + d.ID)
	}
	if !d.Verify.Off {
		v.NextVerify = nextOf(verifyCron(d), now)
	}
	if !d.AppendOnly {
		v.NextPrune = nextOf(pruneCron(d), now)
	}
	v.Media, v.IsOffsite = datastoreMedia(d)
	v.IsOffsite = v.IsOffsite || d.Offsite
	return v
}

// datastoreOf returns a copy of a datastore.
func (s *Server) datastoreOf(id string) (store.Datastore, error) {
	var d store.Datastore
	var ok bool
	s.store.Read(func(st *store.State) {
		if x := st.Datastore(id); x != nil {
			d, ok = *x, true
		}
	})
	if !ok {
		return d, notFound("backup repository not found")
	}
	return d, nil
}

// lockDatastore reserves a datastore for a long operation (one at a time).
func lockDatastore(id, what string) (func(), error) {
	if _, busy := running.LoadOrStore("datastore-"+id, what); busy {
		return nil, &Error{Status: http.StatusConflict, Msg: "the backup repository is busy (check or cleanup running)"}
	}
	return func() { running.Delete("datastore-" + id) }, nil
}

// verifyDatastore checks a datastore as a job.
func (s *Server) verifyDatastore(id, user string, manual, full bool) (jobs.Snapshot, error) {
	d, err := s.datastoreOf(id)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	unlock, err := lockDatastore(id, "verify")
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if user == "" {
		user = "scheduler"
	}
	subset, next := backup.VerifySubset(d.VerifyNext, verifySubsets(&d))
	if full {
		subset, next = "all", d.VerifyNext
	}
	return trackJob("datastore-"+id, s.jobs.Start("datastore-verify", "Integrity check: "+d.Name, user, func(ctx context.Context, j *jobs.Job) error {
		defer unlock()
		run := backup.Verify(ctx, &d, subset, backup.Progress{Set: j.SetProgress, Log: j.Logf})
		run.Manual = manual
		if ctx.Err() != nil {
			// Stopped: no result is recorded, the same subset is read next time.
			backup.Unlock(backup.RepoTask(&d), j.Logf)
			return ctx.Err()
		}
		reached := run.Status == "success" || len(run.Errors) > 0 || len(run.Broken) > 0
		var before string
		_ = s.store.Update(func(st *store.State) error {
			x := st.Datastore(id)
			if x == nil {
				return nil
			}
			if len(x.Verifies) > 0 {
				before = x.Verifies[0].Status
			}
			x.Verifies = append([]store.VerifyRun{run}, x.Verifies...)
			if len(x.Verifies) > store.MaxVerifies {
				x.Verifies = x.Verifies[:store.MaxVerifies]
			}
			if reached {
				x.VerifyNext = next
			}
			return nil
		})
		if run.Status != "success" {
			msg := run.Message
			if len(run.Affected) > 0 {
				short := run.Affected[:min(len(run.Affected), 10)]
				for i := range short {
					short[i] = short[i][:min(8, len(short[i]))]
				}
				msg += "\n" + fmt.Sprintf("Snapshots affected: %s", strings.Join(short, ", "))
			}
			s.store.Log("error", "backup", user, fmt.Sprintf("Integrity check of the backup repository %s failed: %s", d.Name, run.Message))
			s.notify("error", "Backup repository check failed", d.Name+": "+msg)
			go s.runAdvisor(false)
			return fmt.Errorf("%s", run.Message)
		}
		s.store.Log("info", "backup", user, fmt.Sprintf("Integrity check of the backup repository %s passed (%s)", d.Name, firstNonEmpty(subset, "structure")))
		if before == "failed" {
			go s.runAdvisor(false)
		}
		return nil
	})), nil
}

// pruneDatastore removes unused data, as a job.
func (s *Server) pruneDatastore(id, user string) (jobs.Snapshot, error) {
	d, err := s.datastoreOf(id)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if d.AppendOnly {
		return jobs.Snapshot{}, badRequest("an append-only vault cannot be pruned")
	}
	unlock, err := lockDatastore(id, "prune")
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if user == "" {
		user = "scheduler"
	}
	return trackJob("datastore-"+id, s.jobs.Start("datastore-prune", "Repository cleanup: "+d.Name, user, func(ctx context.Context, j *jobs.Job) error {
		defer unlock()
		msg, err := backup.Prune(ctx, &d, backup.Progress{Set: j.SetProgress, Log: j.Logf})
		if err != nil && ctx.Err() != nil {
			// An interrupted prune is safe: the next one removes what it left.
			backup.Unlock(backup.RepoTask(&d), j.Logf)
			return err
		}
		if err != nil {
			s.store.Log("error", "backup", user, fmt.Sprintf("Cleanup of the backup repository %s failed: %v", d.Name, err))
			return err
		}
		j.Logf("%s", msg)
		_ = s.store.Update(func(st *store.State) error {
			if x := st.Datastore(id); x != nil {
				x.LastPrune = time.Now()
			}
			return nil
		})
		s.refreshDatastoreStats(ctx, id, j.Logf)
		return nil
	})), nil
}

// refreshDatastoreStats measures a datastore and saves its sizes.
func (s *Server) refreshDatastoreStats(ctx context.Context, id string, logf func(string, ...any)) {
	if _, busy := running.LoadOrStore("datastore-stats-"+id, true); busy {
		return
	}
	defer running.Delete("datastore-stats-" + id)
	d, err := s.datastoreOf(id)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	st, err := backup.Stats(ctx, &d)
	if err != nil {
		logf("Sizes of %s not measured: %v", d.Name, err)
		return
	}
	logf("%s: %d snapshot(s), %s backed up, %s stored (x%.2f)", d.Name, st.Snapshots, humanSize(st.Logical), humanSize(st.Stored), st.Ratio)
	_ = s.store.Update(func(state *store.State) error {
		if x := state.Datastore(id); x != nil {
			x.Stats = &st
		}
		return nil
	})
}

func humanSize(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// datastoreTick runs the checks and cleanups that are due, and closes the
// disk backups left open.
func (s *Server) datastoreTick(tick time.Time) {
	backup.ExpireDisks(tick)
	var verify, prune []string
	s.store.Read(func(st *store.State) {
		for _, d := range st.Datastores {
			if c, err := scheduler.ParseCron(verifyCron(d)); err == nil && !d.Verify.Off && c.Match(tick) {
				verify = append(verify, d.ID)
			}
			if c, err := scheduler.ParseCron(pruneCron(d)); err == nil && !d.AppendOnly && c.Match(tick) {
				prune = append(prune, d.ID)
			}
		}
	})
	for _, id := range prune {
		if _, err := s.pruneDatastore(id, ""); err != nil {
			slog.Warn("datastore prune", "id", id, "err", err)
		}
	}
	for _, id := range verify {
		if _, err := s.verifyDatastore(id, "", false, false); err != nil {
			slog.Warn("datastore verify", "id", id, "err", err)
		}
	}
}

// unmaskDatastore restores the secrets the UI sent back masked.
func (s *Server) unmaskDatastore(d *store.Datastore, id string) {
	s.store.Read(func(st *store.State) {
		old := st.Datastore(id)
		if old == nil {
			return
		}
		if d.Password == masked {
			d.Password = old.Password
		}
		if d.SecretKey == masked {
			d.SecretKey = old.SecretKey
		}
		if d.VaultToken == masked {
			d.VaultToken = old.VaultToken
		}
	})
}

// imageSourceOf reads "image:<task>/<vm>" sources.
func (s *Server) imageSourceOf(rest string) (restore.Source, error) {
	tid, vm, _ := strings.Cut(rest, "/")
	if vms.ValidName(vm) != nil {
		return nil, notFound("restore source not found")
	}
	var t *store.BackupTask
	s.store.Read(func(st *store.State) {
		if x := st.Backup(tid); x != nil && x.DestType == "datastore" {
			c := resolveTask(st, x)
			t = &c
		}
	})
	if t == nil || t.DestType == "datastore" {
		return nil, notFound("restore source not found")
	}
	return restore.ImageSource(*t, vm), nil
}

// imageSources lists the VM disk backups the guided restore can open.
func (s *Server) imageSources() []restore.Source {
	var out []restore.Source
	s.store.Read(func(st *store.State) {
		for _, b := range st.Backups {
			if b.DestType != "datastore" || st.Datastore(b.Datastore) == nil {
				continue
			}
			t := resolveTask(st, b)
			for _, vm := range b.VMs {
				out = append(out, restore.ImageSource(t, vm))
			}
		}
	})
	return out
}

func (s *Server) datastoreRoutes() {
	if os.Geteuid() == 0 {
		go backup.CleanupDisks() // left by an earlier run of the daemon
	}

	s.admin("GET /api/datastores", func(r *http.Request) (any, error) {
		out := []datastoreView{}
		now := time.Now()
		s.store.Read(func(st *store.State) {
			for _, d := range st.Datastores {
				out = append(out, s.datastoreView(d, st, now))
			}
		})
		return out, nil
	})

	save := func(r *http.Request, id string) (any, error) {
		var req store.Datastore
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		s.unmaskDatastore(&req, id)
		if err := backup.ValidateDatastore(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		for _, c := range []string{req.Verify.Cron, req.PruneCron} {
			if c != "" {
				if _, err := scheduler.ParseCron(c); err != nil {
					return nil, badRequest(err.Error())
				}
			}
		}
		// Reach the repository first: created when new, joined when it
		// exists (another NAS of the fleet may have made it).
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		err := backup.TestDatastore(ctx, &req)
		cancel()
		if err != nil {
			return nil, badRequest(err.Error())
		}
		err = s.store.Update(func(st *store.State) error {
			if id == "" {
				req.ID, req.Created, req.Verifies, req.Stats, req.VerifyNext = shortID(), time.Now(), nil, nil, 0
				c := req
				st.Datastores = append(st.Datastores, &c)
				return nil
			}
			x := st.Datastore(id)
			if x == nil {
				return notFound("backup repository not found")
			}
			req.ID, req.Created, req.Verifies, req.Stats, req.VerifyNext, req.LastPrune = x.ID, x.Created, x.Verifies, x.Stats, x.VerifyNext, x.LastPrune
			*x = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "backup", "Saved backup repository "+req.Name)
		go s.refreshDatastoreStats(context.Background(), req.ID, func(string, ...any) {})
		return maskDatastore(req), nil
	}
	s.admin("POST /api/datastores", func(r *http.Request) (any, error) { return save(r, "") })
	s.admin("PUT /api/datastores/{id}", func(r *http.Request) (any, error) { return save(r, r.PathValue("id")) })

	s.admin("POST /api/datastores/test", func(r *http.Request) (any, error) {
		var req store.Datastore
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		s.unmaskDatastore(&req, req.ID)
		if err := backup.ValidateDatastore(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if err := backup.TestDatastore(ctx, &req); err != nil {
			return nil, badRequest(err.Error())
		}
		return map[string]string{"message": "restic repository reachable"}, nil
	})

	s.admin("DELETE /api/datastores/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var name string
		err := s.store.Update(func(st *store.State) error {
			x := st.Datastore(id)
			if x == nil {
				return notFound("backup repository not found")
			}
			for _, b := range st.Backups {
				if b.DestType == "datastore" && b.Datastore == id {
					return badRequest("the backup task " + b.Name + " uses this repository: change or delete it first")
				}
			}
			name = x.Name
			st.Datastores = slices.DeleteFunc(st.Datastores, func(d *store.Datastore) bool { return d.ID == id })
			return nil
		})
		if err == nil {
			s.audit(r, "backup", "Removed backup repository "+name+" (data kept)")
		}
		return nil, err
	})

	s.admin("POST /api/datastores/{id}/verify", func(r *http.Request) (any, error) {
		var req struct {
			Full bool `json:"full"`
		}
		_ = decode(r, &req)
		return s.verifyDatastore(r.PathValue("id"), userOf(r), true, req.Full)
	})
	s.admin("POST /api/datastores/{id}/prune", func(r *http.Request) (any, error) {
		return s.pruneDatastore(r.PathValue("id"), userOf(r))
	})
	s.admin("POST /api/datastores/{id}/stats", func(r *http.Request) (any, error) {
		d, err := s.datastoreOf(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		return s.jobs.Start("datastore-stats", "Repository sizes: "+d.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			s.refreshDatastoreStats(ctx, d.ID, j.Logf)
			return ctx.Err() // stats run with --no-lock: nothing to unlock
		}), nil
	})

	s.admin("GET /api/datastores/{id}/contents", func(r *http.Request) (any, error) {
		d, err := s.datastoreOf(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		items, err := backup.Contents(ctx, &d)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		names := map[string]string{}
		s.store.Read(func(st *store.State) {
			for _, b := range st.Backups {
				names[b.ID] = b.Name
			}
		})
		me, _ := os.Hostname()
		return map[string]any{"items": items, "taskNames": names, "host": me}, nil
	})

	s.admin("POST /api/datastores/{id}/restore", func(r *http.Request) (any, error) {
		var req struct {
			Snapshot string `json:"snapshot"`
			backup.GuestRestore
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		d, err := s.datastoreOf(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		t := backup.RepoTask(&d)
		what := firstNonEmpty(req.Name, req.Snapshot)
		s.audit(r, "backup", fmt.Sprintf("Restoring %s from the backup repository %s (snapshot %s, replace=%v)", what, d.Name, req.Snapshot, req.Replace))
		return s.jobs.Start("restore", "Restoring "+what, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			msg, err := backup.RestoreGuest(ctx, t, req.Snapshot, req.GuestRestore, backup.Progress{Set: j.SetProgress, Log: j.Logf})
			if err != nil {
				if ctx.Err() != nil {
					backup.Unlock(t, j.Logf)
				}
				return err
			}
			j.Logf("%s", msg)
			s.store.Log("info", "backup", userOf(r), msg)
			return nil
		}), nil
	})

	// ---- Disk backups opened for the guided restore ----
	diskSource := func(source, point string) (string, error) {
		kind, rest, _ := strings.Cut(source, ":")
		if kind != "image" {
			return "", badRequest("not a disk backup")
		}
		if _, err := s.imageSourceOf(rest); err != nil {
			return "", err
		}
		tid, _, _ := strings.Cut(rest, "/")
		return tid, nil
	}
	s.admin("GET /api/restore/disk", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		tid, err := diskSource(q.Get("source"), q.Get("point"))
		if err != nil {
			return nil, err
		}
		if sess := backup.DiskSessionFor(tid, q.Get("point")); sess != nil {
			return map[string]any{"state": sess.State, "session": sess, "idleMinutes": int(backup.DiskIdle.Minutes())}, nil
		}
		return map[string]any{"state": "closed", "open": len(backup.DiskSessions()), "max": backup.MaxDiskSessions}, nil
	})
	s.admin("POST /api/restore/disk/open", func(r *http.Request) (any, error) {
		var req struct{ Source, Point string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		_, rest, _ := strings.Cut(req.Source, ":")
		src, err := s.imageSourceOf(rest)
		if err != nil || !strings.HasPrefix(req.Source, "image:") {
			return nil, badRequest("not a disk backup")
		}
		var t store.BackupTask
		tid, _, _ := strings.Cut(rest, "/")
		s.store.Read(func(st *store.State) {
			if x := st.Backup(tid); x != nil {
				t = resolveTask(st, x)
			}
		})
		s.audit(r, "backup", "Opened a disk backup of "+src.Info().Name+" read-only ("+req.Point+")")
		return s.jobs.Start("disk-open", "Opening a disk backup of "+src.Info().Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			ctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
			defer cancel()
			sess, err := backup.OpenDisk(ctx, t, req.Point, backup.Progress{Set: j.SetProgress, Log: j.Logf})
			if err != nil {
				return err
			}
			for _, sk := range sess.Skipped {
				j.Logf("Not opened: %s", sk)
			}
			j.Logf("%d file system(s) open read-only", len(sess.Roots))
			return nil
		}), nil
	})
	s.admin("POST /api/restore/disk/close", func(r *http.Request) (any, error) {
		var req struct{ Source, Point string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		tid, err := diskSource(req.Source, req.Point)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"closed": backup.CloseDisk(tid, req.Point)}, nil
	})
}
