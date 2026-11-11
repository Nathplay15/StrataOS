package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/sysdisk"
	"strataos/internal/system"
)

// System disk (Storage → Disks → System disk): mirror it onto a second disk,
// move the system to another disk, export a system backup. The planning is
// in internal/sysdisk (pure, tested); here the jobs, the saved record and
// the steps around the restart.

func (s *Server) sysDiskFacts(l sysdisk.Layout) sysdisk.PlanFacts {
	rf := s.replaceFacts()
	pf := sysdisk.PlanFacts{Layout: l, Disks: rf.Disks, Smart: rf.Smart, Busy: rf.Busy, Now: time.Now()}
	pf.RestorePoints, _ = system.RestoreSupported()
	s.store.Read(func(st *store.State) {
		pf.LastConfigBackup = st.ConfigBackup.LastRun
		for _, o := range st.SysDiskOps {
			if o.Finished.IsZero() {
				pf.Running = "An operation on the system disk is not finished: finish or cancel it first."
			}
		}
	})
	// The disks taken by the running operation are reserved anyway.
	for _, d := range rf.Disks {
		if r := storage.BusyReason(d.Path); r != "" {
			pf.Busy[d.Path] = r
		}
	}
	return pf
}

func (s *Server) sysDiskOp(id string) *store.SysDiskOp {
	var out *store.SysDiskOp
	s.store.Read(func(st *store.State) {
		for _, o := range st.SysDiskOps {
			if o.ID == id || (id == "" && o.Finished.IsZero()) {
				c := *o
				out = &c
			}
		}
	})
	return out
}

func (s *Server) saveSysDiskOp(o *store.SysDiskOp) {
	o.Updated = time.Now()
	_ = s.store.Update(func(st *store.State) error {
		for i, x := range st.SysDiskOps {
			if x.ID == o.ID {
				c := *o
				st.SysDiskOps[i] = &c
				return nil
			}
		}
		c := *o
		st.SysDiskOps = append(st.SysDiskOps, &c)
		if n := len(st.SysDiskOps); n > store.MaxSysDiskOps {
			st.SysDiskOps = st.SysDiskOps[n-store.MaxSysDiskOps:]
		}
		return nil
	})
}

func (s *Server) sysDiskRun(ctx context.Context, j *jobs.Job, o *store.SysDiskOp) *sysdisk.Run {
	return &sysdisk.Run{Ctx: ctx, Logf: j.Logf, Progress: j.SetProgress, Op: o, Save: func() { s.saveSysDiskOp(o) }}
}

// sysDiskFail ends an operation in error, keeping the old disk as it is.
func (s *Server) sysDiskFail(o *store.SysDiskOp, err error) error {
	o.Phase, o.Error, o.Finished = "failed", err.Error(), time.Now()
	s.saveSysDiskOp(o)
	s.store.Log("error", "storage", o.User, "System disk: "+err.Error())
	return err
}

func kindTitle(kind string) string {
	if kind == "mirror" {
		return "Mirroring the system disk"
	}
	return "Moving the system to another disk"
}

// sysDiskCopyJob prepares the new disk and copies the system onto it.
func (s *Server) sysDiskCopyJob(o *store.SysDiskOp) jobs.Snapshot {
	j := s.jobs.Start("sysdisk", kindTitle(o.Kind), o.User, func(ctx context.Context, j *jobs.Job) error {
		r := s.sysDiskRun(ctx, j, o)
		l, _, err := sysdisk.Detect()
		if err != nil {
			return s.sysDiskFail(o, err)
		}
		j.Logf("Preparing %s (S/N %s)", o.New, o.NewSerial)
		if err := r.Prepare(l); err != nil {
			r.Cleanup(l)
			return s.sysDiskFail(o, err)
		}
		o.Phase = "copy"
		s.saveSysDiskOp(o)
		if err := r.Copy(l, false); err != nil {
			r.Cleanup(l)
			return s.sysDiskFail(o, err)
		}
		o.Phase, o.Message = "ready", "The system is copied: restart to start from the new disk."
		s.saveSysDiskOp(o)
		s.store.Log("info", "storage", o.User, fmt.Sprintf("System disk: copy onto %s finished", o.New))
		return nil
	})
	o.JobID = j.ID
	s.saveSysDiskOp(o)
	return j
}

// sysDiskSwitchJob makes the final copy, sets the next start and restarts.
func (s *Server) sysDiskSwitchJob(o *store.SysDiskOp) jobs.Snapshot {
	j := s.jobs.Start("sysdisk", "Switching to the new system disk", o.User, func(ctx context.Context, j *jobs.Job) error {
		r := s.sysDiskRun(ctx, j, o)
		l, _, err := sysdisk.Detect()
		if err != nil {
			return s.sysDiskFail(o, err)
		}
		if o.New = sysdisk.DiskBySerial(o.NewSerial, o.New); o.New == "" {
			return s.sysDiskFail(o, errors.New("the new disk is no longer detected"))
		}
		j.Logf("Do not power off the NAS: final copy and boot loader")
		if err := r.Switch(l, system.BootID()); err != nil {
			r.UndoOneShot()
			o.Phase, o.Error = "ready", "The switch failed: "+err.Error()+". Nothing changed on the old disk; you can try again or cancel."
			s.saveSysDiskOp(o)
			return err
		}
		o.Message = "Restarting from the new disk…"
		s.saveSysDiskOp(o)
		s.store.Log("warning", "storage", o.User, fmt.Sprintf("System disk: restarting from %s", o.New))
		j.Logf("Restarting from %s in a few seconds", o.New)
		go func() {
			time.Sleep(3 * time.Second)
			_, _ = sys.Run("systemctl", "reboot")
		}()
		return nil
	})
	o.JobID = j.ID
	s.saveSysDiskOp(o)
	return j
}

// sysDiskCompleteJob adds the old disk to the mirror and follows the rebuild.
func (s *Server) sysDiskCompleteJob(o *store.SysDiskOp, resume bool) jobs.Snapshot {
	j := s.jobs.Start("sysdisk", "Completing the system mirror", o.User, func(ctx context.Context, j *jobs.Job) error {
		r := s.sysDiskRun(ctx, j, o)
		if !resume {
			l, _, err := sysdisk.Detect()
			if err != nil {
				return s.sysDiskFail(o, err)
			}
			o.Phase = "completing"
			s.saveSysDiskOp(o)
			if err := r.CompleteMirror(l); err != nil {
				return s.sysDiskFail(o, err)
			}
			s.saveSysDiskOp(o)
		}
		j.Logf("Rebuilding the mirror onto %s…", o.Old)
		for {
			p, done, err := r.MirrorSynced()
			if err != nil {
				return s.sysDiskFail(o, err)
			}
			j.SetProgress(p)
			if done {
				break
			}
			select {
			case <-ctx.Done():
				return errors.New("stopped following the rebuild: it goes on in the background")
			case <-time.After(5 * time.Second):
			}
		}
		o.Phase, o.Finished = "done", time.Now()
		o.Message = "The system is mirrored on two disks."
		s.saveSysDiskOp(o)
		msg := fmt.Sprintf("The system disk is now mirrored (%s and %s)", o.New, o.Old)
		s.store.Log("info", "storage", o.User, msg)
		s.notify("info", "System mirror", msg)
		return nil
	})
	o.JobID = j.ID
	s.saveSysDiskOp(o)
	return j
}

// sysDiskResume runs at start: did the restart land on the new disk?
func (s *Server) sysDiskResume() {
	// A mirrored system keeps an identical EFI system partition and a boot
	// entry on each disk (also after a disk replacement).
	if _, err := os.Stat("/usr/lib/strata/scripts/esp-sync"); err == nil {
		if out, err := sys.Run("/usr/lib/strata/scripts/esp-sync", "--setup"); err != nil {
			s.store.Log("warning", "storage", "", "Could not copy the EFI system partition onto the mirror disk: "+err.Error())
		} else if strings.Contains(out, "boot entry") {
			s.store.Log("info", "storage", "", strings.TrimSpace(out))
		}
	}
	o := s.sysDiskOp("")
	if o == nil {
		return
	}
	logf := func(f string, a ...any) { s.store.Log("info", "storage", "", "System disk: "+fmt.Sprintf(f, a...)) }
	r := &sysdisk.Run{Ctx: context.Background(), Logf: func(string, ...any) {}, Op: o, Save: func() { s.saveSysDiskOp(o) }}
	switch o.Phase {
	case "prepare", "copy", "switching":
		l, _, _ := sysdisk.Detect()
		r.Cleanup(l)
		_ = s.sysDiskFail(o, errors.New("interrupted by a restart: start again"))
	case "rebooting":
		if o.BootID == system.BootID() {
			r.UndoOneShot()
			_ = s.sysDiskFail(o, errors.New("interrupted before the restart: start again"))
			return
		}
		switch root := sysdisk.RootUUID(); {
		case root == o.RootUUID:
			r.Booted()
			o.Phase = "booted"
			if o.Kind == "mirror" {
				o.Message = "The system started from the mirror on the new disk. Check that everything works, then complete the mirror: the old disk is erased and added to it."
			} else {
				o.Message = "The system started from the new disk. Power off the NAS and remove the old disk, or erase it to use it for data."
			}
			s.saveSysDiskOp(o)
			if o.New != "" {
				_ = storage.SetBusy(o.New, "system disk change")
			}
			logf("the system started from %s", o.New)
			s.notify("info", "System disk", o.Message)
		case root == o.OldRootUUID:
			r.UndoOneShot()
			o.Phase = "fell-back"
			o.Message = "The system started from the old disk again: the start from the new disk did not work (or was not chosen). The old disk is unchanged; try again or cancel."
			s.saveSysDiskOp(o)
			logf("started from the old disk again")
			s.notify("warning", "System disk", o.Message)
		default:
			_ = s.sysDiskFail(o, errors.New("the system started from an unknown disk"))
		}
	case "completing":
		s.sysDiskCompleteJob(o, true)
	}
}

func (s *Server) sysDiskRoutes() {
	s.admin("GET /api/system/disk", func(r *http.Request) (any, error) {
		l, _, err := sysdisk.Detect()
		if err != nil {
			return nil, err
		}
		pf := s.sysDiskFacts(l)
		var ops []store.SysDiskOp
		var backups []store.SysBackup
		s.store.Read(func(st *store.State) {
			for i := len(st.SysDiskOps) - 1; i >= 0; i-- {
				ops = append(ops, *st.SysDiskOps[i])
			}
			for i := len(st.SysBackups) - 1; i >= 0; i-- {
				backups = append(backups, *st.SysBackups[i])
			}
		})
		var smart *storage.DiskVerdict
		if len(l.Disks) > 0 {
			if v, ok := pf.Smart[l.Disks[0].Path]; ok {
				smart = &v
			}
		}
		return map[string]any{"layout": l, "mirror": sysdisk.PlanMirror(pf, ""), "move": sysdisk.PlanMove(pf, ""),
			"ops": ops, "backups": backups, "smart": smart}, nil
	})
	s.admin("GET /api/system/disk/plan", func(r *http.Request) (any, error) {
		l, _, err := sysdisk.Detect()
		if err != nil {
			return nil, err
		}
		pf := s.sysDiskFacts(l)
		if r.URL.Query().Get("kind") == "mirror" {
			return sysdisk.PlanMirror(pf, r.URL.Query().Get("target")), nil
		}
		return sysdisk.PlanMove(pf, r.URL.Query().Get("target")), nil
	})
	s.admin("POST /api/system/disk/start", func(r *http.Request) (any, error) {
		var req struct {
			Kind    string `json:"kind"`
			Target  string `json:"target"`
			Confirm string `json:"confirm"` // the target disk name, typed by the user
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Kind != "mirror" && req.Kind != "move" {
			return nil, badRequest("unknown operation")
		}
		l, _, err := sysdisk.Detect()
		if err != nil {
			return nil, err
		}
		// Plan again with fresh facts: the browser's plan is never trusted.
		var p sysdisk.Plan
		if req.Kind == "mirror" {
			p = sysdisk.PlanMirror(s.sysDiskFacts(l), req.Target)
		} else {
			p = sysdisk.PlanMove(s.sysDiskFacts(l), req.Target)
		}
		if p.Refusal != "" {
			return nil, badRequest(p.Refusal)
		}
		if p.Target == nil || req.Confirm != p.Target.Name {
			return nil, badRequest("type the name of the new disk to confirm: it will be erased")
		}
		if err := storage.SetBusy(p.Target.Path, "system disk change"); err != nil {
			return nil, badRequest(err.Error())
		}
		old := l.Disks[0]
		o := &store.SysDiskOp{ID: shortID(), Kind: req.Kind, Phase: "prepare", Warnings: p.Warnings, Boot: l.Boot, FSType: l.Root.FSType, Subvol: l.Root.Subvol,
			Parts: p.Parts, Old: old.Path, OldSerial: old.Serial, OldModel: old.Model, OldSize: old.Size,
			New: p.Target.Path, NewSerial: p.Target.Serial, NewModel: p.Target.Model, OldRootUUID: sysdisk.RootUUID(),
			Started: time.Now(), User: userOf(r)}
		if old.ESP != "" {
			o.OldESP = espPartUUID(old.ESP)
		}
		if o.OldRootUUID == "" {
			storage.ClearBusy(p.Target.Path)
			return nil, badRequest("the UUID of the system file system could not be read")
		}
		s.audit(r, "storage", fmt.Sprintf("%s: %s %s → %s %s (%s)", kindTitle(req.Kind), old.Path, old.Serial, p.Target.Path, p.Target.Serial, p.Target.Model))
		j := s.sysDiskCopyJob(o)
		return map[string]any{"job": j, "id": o.ID}, nil
	})
	s.admin("POST /api/system/disk/{id}/switch", func(r *http.Request) (any, error) {
		o := s.sysDiskOp(r.PathValue("id"))
		if o == nil || !o.Finished.IsZero() {
			return nil, notFound("operation not found")
		}
		if o.Phase != "ready" && o.Phase != "fell-back" {
			return nil, badRequest("the copy is not ready")
		}
		o.Phase, o.Error = "switching", ""
		s.saveSysDiskOp(o)
		s.audit(r, "storage", "System disk: switch to "+o.New+" and restart")
		return map[string]any{"job": s.sysDiskSwitchJob(o)}, nil
	})
	s.admin("POST /api/system/disk/{id}/complete", func(r *http.Request) (any, error) {
		var req struct {
			Confirm string `json:"confirm"` // the old disk name
			Erase   bool   `json:"erase"`   // move: erase the old disk
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		o := s.sysDiskOp(r.PathValue("id"))
		if o == nil || !o.Finished.IsZero() {
			return nil, notFound("operation not found")
		}
		if o.Phase != "booted" {
			return nil, badRequest("the system does not run from the new disk yet")
		}
		if sysdisk.RootUUID() != o.RootUUID {
			return nil, badRequest("the system does not run from the new disk")
		}
		old := sysdisk.DiskBySerial(o.OldSerial, o.Old)
		oldName := strings.TrimPrefix(old, "/dev/")
		if (o.Kind == "mirror" || req.Erase) && (old == "" || req.Confirm != oldName) {
			return nil, badRequest("type the name of the old disk to confirm: it will be erased")
		}
		storage.ClearBusy(o.New)
		if o.Kind == "mirror" {
			s.audit(r, "storage", "System disk: completing the mirror with "+old)
			return map[string]any{"job": s.sysDiskCompleteJob(o, false)}, nil
		}
		j := s.jobs.Start("sysdisk", "Finishing the move of the system", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			run := s.sysDiskRun(ctx, j, o)
			if req.Erase {
				l, _, err := sysdisk.Detect()
				if err != nil {
					return err
				}
				if err := run.EraseOld(l); err != nil {
					return err
				}
				o.Message = "The system runs from the new disk; the old disk was erased."
			} else {
				o.Message = fmt.Sprintf("The system runs from the new disk. Power off the NAS and remove the old disk %s (S/N %s).", firstNonEmpty(o.OldModel, "disk"), firstNonEmpty(o.OldSerial, "unknown"))
			}
			o.Phase, o.Finished = "done", time.Now()
			s.saveSysDiskOp(o)
			s.store.Log("info", "storage", o.User, o.Message)
			return nil
		})
		s.audit(r, "storage", "System disk: move finished")
		return map[string]any{"job": j}, nil
	})
	s.admin("POST /api/system/disk/{id}/back", func(r *http.Request) (any, error) {
		o := s.sysDiskOp(r.PathValue("id"))
		if o == nil || o.Phase != "booted" {
			return nil, badRequest("going back is possible only while the old disk is unchanged")
		}
		l, _, err := sysdisk.Detect()
		if err != nil {
			return nil, err
		}
		run := &sysdisk.Run{Ctx: r.Context(), Logf: func(f string, a ...any) { s.store.Log("info", "storage", userOf(r), fmt.Sprintf(f, a...)) }, Op: o, Save: func() {}}
		if err := run.GoBack(l); err != nil {
			return nil, badRequest(err.Error())
		}
		o.Phase, o.BootID = "rebooting", system.BootID()
		o.Message = "Restarting from the old disk…"
		// The old system's own record says "rebooting" too: it sees it
		// started from the old disk and offers to try again or cancel.
		s.saveSysDiskOp(o)
		s.audit(r, "storage", "System disk: back to the old disk, restarting")
		go func() {
			time.Sleep(2 * time.Second)
			_, _ = sys.Run("systemctl", "reboot")
		}()
		return nil, nil
	})
	s.admin("POST /api/system/disk/{id}/cancel", func(r *http.Request) (any, error) {
		o := s.sysDiskOp(r.PathValue("id"))
		if o == nil || !o.Finished.IsZero() {
			return nil, notFound("operation not found")
		}
		switch o.Phase {
		case "ready", "fell-back", "failed":
		default:
			return nil, badRequest("this operation cannot be cancelled now")
		}
		if sysdisk.RootUUID() != o.OldRootUUID {
			return nil, badRequest("the system does not run from the old disk")
		}
		l, _, err := sysdisk.Detect()
		if err != nil {
			return nil, err
		}
		j := s.jobs.Start("sysdisk", "Cancelling the system disk change", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			run := s.sysDiskRun(ctx, j, o)
			run.Cleanup(l)
			o.Phase, o.Finished, o.Message = "cancelled", time.Now(), "Cancelled: the system stays on the old disk, the new disk was erased."
			s.saveSysDiskOp(o)
			return nil
		})
		s.audit(r, "storage", "System disk: operation cancelled")
		return map[string]any{"job": j}, nil
	})
	s.admin("DELETE /api/system/disk/{id}", func(r *http.Request) (any, error) {
		return nil, s.store.Update(func(st *store.State) error {
			for i, x := range st.SysDiskOps {
				if x.ID == r.PathValue("id") {
					if x.Finished.IsZero() {
						return badRequest("this operation is not finished")
					}
					st.SysDiskOps = append(st.SysDiskOps[:i], st.SysDiskOps[i+1:]...)
					return nil
				}
			}
			return notFound("operation not found")
		})
	})
	s.admin("POST /api/system/disk/backup", func(r *http.Request) (any, error) {
		var req struct {
			Folder string `json:"folder"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		l, _, err := sysdisk.Detect()
		if err != nil {
			return nil, err
		}
		folder, err := sysdisk.CheckBackupFolder(l, req.Folder)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		user := userOf(r)
		j := s.jobs.Start("sysbackup", "System backup", user, func(ctx context.Context, j *jobs.Job) error {
			m, dir, err := sysdisk.Backup(ctx, j.Logf, l, folder, s.version)
			if err != nil {
				s.store.Log("error", "system", user, "System backup failed: "+err.Error())
				return err
			}
			_ = s.store.Update(func(st *store.State) error {
				st.SysBackups = append(st.SysBackups, &store.SysBackup{ID: shortID(), Path: dir, Size: m.Size, Created: m.Created, Hostname: m.Hostname, Version: m.Version, SHA256: m.SHA256})
				if n := len(st.SysBackups); n > 20 {
					st.SysBackups = st.SysBackups[n-20:]
				}
				return nil
			})
			s.store.Log("info", "system", user, "System backup written to "+dir)
			return nil
		})
		s.audit(r, "system", "System backup to "+folder)
		return map[string]any{"job": j}, nil
	})
}

func espPartUUID(dev string) string {
	out, _ := sys.Run("lsblk", "-n", "-d", "-o", "PARTUUID", dev)
	return strings.TrimSpace(out)
}
