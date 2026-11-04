package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/storagetools"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// Guided disk replacement (Storage → Disks → Replace): the plan says what uses
// the disk and refuses the dangerous cases (storage.PlanReplace), the job
// erases the new disk, hands it to mdadm / ZFS / LVM, follows the copy, takes
// the old disk out and tells which one to pull. The record is saved at each
// step, so a replacement survives a page reload and a restart of StrataOS.

// replaceLive is the progress of a running replacement (not saved).
type replaceLive struct {
	mu       sync.Mutex
	progress float64
	eta      int64
	waiting  bool
}

var replaceRuns sync.Map // replacement ID → *replaceLive

type replaceView struct {
	store.DiskReplacement
	Running  bool    `json:"running"`
	Progress float64 `json:"progress"`
	ETA      int64   `json:"eta,omitempty"`
	Waiting  bool    `json:"waiting,omitempty"`
}

func (s *Server) replaceFacts() storage.ReplaceFacts {
	disks, _ := storage.ListDisks()
	f := storage.ReplaceFacts{Disks: disks, Now: time.Now(), Smart: map[string]storage.DiskVerdict{}, Busy: map[string]string{}}
	for _, d := range disks {
		var info *storage.SmartInfo
		if e, ok := smartCache.Load(d.Path); ok {
			i := e.(smartEntry).info
			info = &i
		} else if i, err := storage.SmartIfAwake(d.Path); err == nil && !i.Standby {
			info = i
		}
		if info != nil {
			f.Smart[d.Path] = storage.DiskVerdict{Failing: info.Passed != nil && !*info.Passed, Weak: info.Warnings}
		}
		if r := storage.BusyReason(d.Path); r != "" {
			f.Busy[d.Path] = r
		}
	}
	f.Groups = storage.ReplaceGroups(disks)
	s.store.Read(func(st *store.State) {
		last := func(hist []store.ScheduleRun) {
			for _, h := range hist {
				if h.Status == "success" {
					if h.Started.After(f.LastBackup) {
						f.LastBackup = h.Started
					}
					return
				}
			}
		}
		for _, b := range st.Backups {
			if b.Enabled {
				f.Backups++
				last(b.History)
			}
		}
		for _, c := range st.Clouds {
			if c.Enabled {
				f.Backups++
				last(c.History)
			}
		}
		// Name the volumes sitting on each group.
		for i := range f.Groups {
			g := &f.Groups[i]
			for _, v := range st.Volumes {
				for _, d := range v.Devices {
					onRaid := g.Kind == "raid" && (d == g.Device || d == "/dev/md/"+g.Name || resolvePath(d) == g.Device)
					onVG := g.Kind == "lvm" && (strings.HasPrefix(d, "/dev/"+g.Name+"/") || strings.HasPrefix(d, "/dev/mapper/"+g.Name+"-"))
					if onRaid || onVG {
						g.UsedBy = append(g.UsedBy, "volume "+v.Name)
						break
					}
				}
			}
		}
	})
	return f
}

func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// replaceRunning returns the unfinished replacement of a group, if any.
func (s *Server) replaceRunning(group string) *store.DiskReplacement {
	var out *store.DiskReplacement
	s.store.Read(func(st *store.State) {
		for _, r := range st.DiskReplacements {
			if r.Finished.IsZero() && (group == "" || r.Group == group) {
				c := *r
				out = &c
			}
		}
	})
	return out
}

func (s *Server) replaceView(r store.DiskReplacement) replaceView {
	v := replaceView{DiskReplacement: r, Running: r.Finished.IsZero()}
	if r.Phase == "done" {
		v.Progress = 100
	}
	if l, ok := replaceRuns.Load(r.ID); ok {
		lv := l.(*replaceLive)
		lv.mu.Lock()
		v.Progress, v.ETA, v.Waiting = lv.progress, lv.eta, lv.waiting
		lv.mu.Unlock()
	}
	return v
}

// replacePull is the message that ends a replacement: which disk to pull.
func replacePull(r store.DiskReplacement) string {
	if r.OldSerial == "" && r.OldDisk == "" {
		return fmt.Sprintf("Disk replacement in %s finished: %s has its full redundancy again", r.Name, r.Name)
	}
	model := firstNonEmpty(r.OldModel, "disk")
	return fmt.Sprintf("Disk replacement in %s finished: you can pull the old disk %s, serial number %s (%s)", r.Name, model, firstNonEmpty(r.OldSerial, "unknown"), r.OldDisk)
}

func (s *Server) updateReplace(id string, fn func(r *store.DiskReplacement)) store.DiskReplacement {
	var out store.DiskReplacement
	_ = s.store.Update(func(st *store.State) error {
		for _, r := range st.DiskReplacements {
			if r.ID == id {
				fn(r)
				out = *r
			}
		}
		return nil
	})
	return out
}

// diskBySerial finds the current path of a disk (kernel names change between boots).
func diskBySerial(disks []storage.BlockDevice, serial, fallback string) string {
	if serial != "" {
		for _, d := range disks {
			if d.Serial == serial {
				return d.Path
			}
		}
		return ""
	}
	return fallback
}

func (s *Server) startReplaceJob(r store.DiskReplacement, resume bool) jobs.Snapshot {
	j := s.jobs.Start("replace", "Disk replacement in "+r.Name, r.User, func(ctx context.Context, j *jobs.Job) error {
		return s.runReplace(ctx, j, r.ID, resume)
	})
	s.updateReplace(r.ID, func(x *store.DiskReplacement) { x.JobID = j.ID })
	return j
}

func (s *Server) runReplace(ctx context.Context, j *jobs.Job, id string, resume bool) error {
	var rec store.DiskReplacement
	s.store.Read(func(st *store.State) {
		for _, r := range st.DiskReplacements {
			if r.ID == id {
				rec = *r
			}
		}
	})
	if rec.ID == "" {
		return errors.New("replacement not found")
	}
	op := storage.ReplaceOp{Kind: rec.Kind, Group: rec.Name, Mode: rec.Mode, Old: rec.Old, OldDisk: rec.OldDisk, New: rec.New}
	live := &replaceLive{}
	replaceRuns.Store(id, live)
	defer replaceRuns.Delete(id)
	defer storage.ClearBusy(op.New)
	phase := func(p string) {
		rec = s.updateReplace(id, func(x *store.DiskReplacement) { x.Phase = p })
	}
	fail := func(err error) error {
		rec = s.updateReplace(id, func(x *store.DiskReplacement) {
			x.Phase, x.Error, x.Finished = "failed", err.Error(), time.Now()
		})
		return err
	}

	if rec.Phase == "check" {
		if rec.SmartTest {
			j.Logf("SMART short self-test of %s", op.New)
			step := storagetools.SmartShortTest(ctx, op.New, j.Logf)
			switch step.Status {
			case "failed":
				return fail(fmt.Errorf("the new disk failed its SMART self-test: %s", step.Detail))
			case "cancelled":
				return fail(errors.New("cancelled"))
			case "skipped":
				j.Logf("SMART self-test skipped: %s", step.Detail)
			default:
				j.Logf("SMART self-test passed")
			}
		}
		if info, err := storage.Smart(op.New); err == nil && info.Passed != nil && !*info.Passed {
			return fail(errors.New("the new disk fails its SMART health check"))
		}
		phase("add")
	}
	if rec.Phase == "add" {
		// The reservation kept other tools off the new disk until now; Start
		// erases it under storage.Lock and makes it a member.
		storage.ClearBusy(op.New)
		if err := op.Start(ctx, j.Logf); err != nil {
			return fail(err)
		}
		s.store.Log("info", "storage", rec.User, fmt.Sprintf("Disk replacement in %s: rebuilding onto %s", rec.Name, op.New))
		phase("rebuild")
	}
	if rec.Phase == "rebuild" {
		if resume {
			op.Resume()
		}
		j.Logf("Following the rebuild…")
		var t0 time.Time
		var p0 float64
		errs := 0
		for {
			st, err := op.Poll()
			if err != nil {
				// A command may fail once while the kernel reshuffles devices.
				if errs++; errs >= 3 {
					return fail(err)
				}
			} else {
				errs = 0
				if st.Done {
					if st.Warning != "" {
						s.updateReplace(id, func(x *store.DiskReplacement) { x.Warning = st.Warning })
						j.Logf("Warning: %s", st.Warning)
					}
					break
				}
				eta := st.ETA
				if st.Progress > 0 && !st.Waiting {
					if t0.IsZero() {
						t0, p0 = time.Now(), st.Progress
					} else if eta == 0 && st.Progress > p0 {
						eta = int64(time.Since(t0).Seconds() * (100 - st.Progress) / (st.Progress - p0))
					}
				}
				live.mu.Lock()
				live.progress, live.eta, live.waiting = st.Progress, eta, st.Waiting
				live.mu.Unlock()
				j.SetProgress(st.Progress)
			}
			select {
			case <-ctx.Done():
				return fail(errors.New("stopped following the rebuild: it goes on in the background, check the array"))
			case <-time.After(5 * time.Second):
			}
		}
		live.mu.Lock()
		live.progress, live.eta, live.waiting = 100, 0, false
		live.mu.Unlock()
		phase("remove")
	}
	if rec.Phase == "remove" {
		if err := op.Finish(j.Logf); err != nil {
			return fail(err)
		}
	}
	rec = s.updateReplace(id, func(x *store.DiskReplacement) { x.Phase, x.Finished = "done", time.Now() })
	msg := replacePull(rec)
	j.Logf("%s", msg)
	s.store.Log("info", "storage", rec.User, msg)
	s.notify("info", "Disk replaced", msg)
	// Blink the bay of the disk to pull, when the enclosure supports it.
	if rec.OldSerial != "" && sys.Has("ledctl") {
		disks, _ := storage.ListDisks()
		if p := diskBySerial(disks, rec.OldSerial, ""); p != "" {
			if err := storage.Locate(p, true); err == nil {
				j.Logf("The locate LED of %s is blinking", p)
			}
		}
	}
	return nil
}

// replaceResume carries on the replacements a restart interrupted: RAID and
// ZFS rebuilds went on in the kernel, an LVM move is restarted.
func (s *Server) replaceResume() {
	if s.replaceRunning("") == nil {
		return
	}
	time.Sleep(45 * time.Second) // arrays assembled and pools imported (recoverStorage)
	disks, _ := storage.ListDisks()
	var todo []store.DiskReplacement
	s.store.Read(func(st *store.State) {
		for _, r := range st.DiskReplacements {
			if r.Finished.IsZero() {
				todo = append(todo, *r)
			}
		}
	})
	for _, r := range todo {
		fail := func(msg string) {
			s.updateReplace(r.ID, func(x *store.DiskReplacement) { x.Phase, x.Error, x.Finished = "failed", msg, time.Now() })
			s.store.Log("error", "storage", r.User, fmt.Sprintf("Disk replacement in %s: %s", r.Name, msg))
		}
		if r.Phase != "rebuild" && r.Phase != "remove" {
			fail("interrupted by a restart before the copy started: check the array, then start again")
			continue
		}
		nw := diskBySerial(disks, r.NewSerial, r.New)
		if nw == "" {
			fail("the new disk is no longer detected")
			continue
		}
		od := diskBySerial(disks, r.OldSerial, r.OldDisk)
		r = s.updateReplace(r.ID, func(x *store.DiskReplacement) {
			// Member names built on kernel names follow the disk (sdb1 → sdc1).
			if od != "" && x.OldDisk != "" && od != x.OldDisk && strings.HasPrefix(x.Old, x.OldDisk) {
				x.Old = od + strings.TrimPrefix(x.Old, x.OldDisk)
			}
			if od != "" {
				x.OldDisk = od
			}
			x.New = nw
		})
		if err := storage.SetBusy(r.New, "disk replacement"); err != nil {
			fail(err.Error())
			continue
		}
		s.startReplaceJob(r, true)
	}
}

func (s *Server) replaceRoutes() {
	s.admin("GET /api/storage/replace/plan", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		sel := storage.ReplaceSel{Disk: q.Get("disk"), Group: q.Get("group"), Member: q.Get("member"), Mode: q.Get("mode"), New: q.Get("new")}
		p := storage.PlanReplace(s.replaceFacts(), sel)
		var running *replaceView
		if p.Group != nil {
			if rr := s.replaceRunning(p.Group.ID); rr != nil {
				v := s.replaceView(*rr)
				running = &v
			}
		}
		return map[string]any{"plan": p, "ledctl": sys.Has("ledctl"), "running": running}, nil
	})
	s.admin("GET /api/storage/replace", func(r *http.Request) (any, error) {
		items := []replaceView{}
		var recs []store.DiskReplacement
		s.store.Read(func(st *store.State) {
			for i := len(st.DiskReplacements) - 1; i >= 0; i-- {
				recs = append(recs, *st.DiskReplacements[i])
			}
		})
		for _, x := range recs {
			items = append(items, s.replaceView(x))
		}
		return map[string]any{"ledctl": sys.Has("ledctl"), "items": items}, nil
	})
	s.admin("POST /api/storage/replace", func(r *http.Request) (any, error) {
		var req struct {
			storage.ReplaceSel
			Confirm   string `json:"confirm"` // the new disk name, typed by the user
			SmartTest bool   `json:"smartTest"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.New == "" {
			return nil, badRequest("choose the new disk")
		}
		f := s.replaceFacts()
		p := storage.PlanReplace(f, req.ReplaceSel)
		if p.Refusal != "" {
			return nil, badRequest(p.Refusal)
		}
		var nd storage.BlockDevice
		for _, d := range f.Disks {
			if d.Path == req.New {
				nd = d
			}
		}
		if req.Confirm != nd.Name || nd.Name == "" {
			return nil, badRequest("type the name of the new disk to confirm: it will be erased")
		}
		g, old := p.Group, p.Old
		if s.replaceRunning(g.ID) != nil {
			return nil, badRequest("a replacement is already running on " + g.Name)
		}
		if err := storage.SetBusy(nd.Path, "disk replacement"); err != nil {
			return nil, badRequest(err.Error())
		}
		rec := &store.DiskReplacement{ID: shortID(), Kind: g.Kind, Group: g.ID, Name: g.Name, Level: g.Level, Mode: p.Mode,
			Old: old.Name, OldDisk: old.Disk, OldSerial: old.Serial, OldModel: old.Model,
			New: nd.Path, NewSerial: nd.Serial, NewModel: nd.Model, SmartTest: req.SmartTest,
			Phase: "check", Started: time.Now(), User: userOf(r)}
		if old.Disk != "" {
			rec.OldByID = byIDName(old.Disk)
		}
		if err := s.store.Update(func(st *store.State) error {
			st.DiskReplacements = append(st.DiskReplacements, rec)
			if n := len(st.DiskReplacements); n > store.MaxDiskReplacements {
				st.DiskReplacements = st.DiskReplacements[n-store.MaxDiskReplacements:]
			}
			return nil
		}); err != nil {
			storage.ClearBusy(nd.Path)
			return nil, err
		}
		s.audit(r, "storage", fmt.Sprintf("Disk replacement in %s (%s): %s %s → %s %s (%s)", g.Name, p.Mode, firstNonEmpty(old.Disk, old.Name, "missing disk"), old.Serial, nd.Path, nd.Serial, nd.Model))
		j := s.startReplaceJob(*rec, false)
		return map[string]any{"job": j, "id": rec.ID}, nil
	})
	s.admin("POST /api/storage/replace/{id}/locate", func(r *http.Request) (any, error) {
		var req struct {
			On bool `json:"on"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var rec *store.DiskReplacement
		s.store.Read(func(st *store.State) {
			for _, x := range st.DiskReplacements {
				if x.ID == r.PathValue("id") {
					c := *x
					rec = &c
				}
			}
		})
		if rec == nil {
			return nil, notFound("replacement not found")
		}
		disks, _ := storage.ListDisks()
		p := diskBySerial(disks, rec.OldSerial, rec.OldDisk)
		if p == "" || rec.OldDisk == "" {
			return nil, badRequest("the old disk is no longer detected: it may already be unplugged")
		}
		if err := storage.Locate(p, req.On); err != nil {
			return nil, badRequest(err.Error())
		}
		return map[string]string{"device": p}, nil
	})
	s.admin("DELETE /api/storage/replace/{id}", func(r *http.Request) (any, error) {
		return nil, s.store.Update(func(st *store.State) error {
			for i, x := range st.DiskReplacements {
				if x.ID == r.PathValue("id") {
					if x.Finished.IsZero() {
						return badRequest("this replacement is still running")
					}
					st.DiskReplacements = append(st.DiskReplacements[:i], st.DiskReplacements[i+1:]...)
					return nil
				}
			}
			return notFound("replacement not found")
		})
	})
}

// byIDName returns the /dev/disk/by-id name of a disk ("" when none), which
// enclosures and labels on the trays often show.
func byIDName(dev string) string {
	real := resolvePath(dev)
	matches, _ := filepath.Glob("/dev/disk/by-id/*")
	best := ""
	for _, m := range matches {
		n := filepath.Base(m)
		if strings.Contains(n, "-part") || resolvePath(m) != real {
			continue
		}
		if best == "" || strings.HasPrefix(best, "wwn-") || strings.HasPrefix(best, "nvme-eui") {
			best = n
		}
	}
	return best
}
