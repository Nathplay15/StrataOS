package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"

	"strataos/internal/backup"
	"strataos/internal/jobs"
	"strataos/internal/store"
)

// Reasons a job cannot be stopped from the interface (English; translated
// by the UI). The types missing from jobNoCancel can be stopped: their work
// runs through sys commands bound to the job's context (terminated with
// their children) or loops that check it.
const (
	noStopPackages = "An update cannot be stopped: interrupting the package manager could leave the system broken"
	noStopInstall  = "Installing or removing a component cannot be stopped: interrupting the package manager could leave the system broken"
	noStopKernel   = "Changing the kernel cannot be stopped: the NAS could fail to start"
	noStopStorage  = "A storage operation cannot be stopped half-way: the disks could be left in an inconsistent state"
	noStopCreate   = "Stopping now could leave the virtual machine, container or app half created: wait for the end, then delete it if needed"
	noStopOwnPage  = "This operation is cancelled from its own page"
	noStopShort    = "This task cannot be stopped safely: wait for it to finish"
)

var jobNoCancel = map[string]string{
	"apt":       noStopPackages,
	"install":   noStopInstall,
	"uninstall": noStopInstall,
	rpiJobType:  noStopKernel,
	"raid":      noStopStorage,
	"volume":    noStopStorage,
	"zfs":       noStopStorage,
	"btrfs":     noStopStorage,
	"storage":   noStopStorage,
	"quota":     noStopStorage,
	"ceph":      noStopStorage,
	"vm":        noStopCreate,
	"lxc":       noStopCreate,
	"stack":     noStopCreate,
	"docker":    noStopCreate,
	"app":       noStopCreate,
	"fleet":     noStopOwnPage,
	"vmcluster": noStopOwnPage,
	"directory": noStopShort,
	"config":    noStopShort,
	"ha":        noStopShort,
	"acme":      noStopShort,
	"media":     noStopShort,
}

func jobCancelPolicy(typ string) (bool, string) {
	if reason, ok := jobNoCancel[typ]; ok {
		return false, reason
	}
	return true, ""
}

func (s *Server) cancelJob(r *http.Request, id string) error {
	err := s.jobs.Cancel(id, userOf(r))
	var nc *jobs.NotCancellable
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		return notFound("job not found")
	case errors.Is(err, jobs.ErrFinished):
		return &Error{Status: http.StatusConflict, Msg: err.Error()}
	case errors.As(err, &nc):
		return &Error{Status: http.StatusConflict, Msg: nc.Reason}
	case err != nil:
		return err
	}
	if j, ok := s.jobs.Get(id); ok {
		s.audit(r, "system", "Stopped the task "+j.Title)
	}
	return nil
}

// runningJobs maps the key of a task in the running map ("backup-<id>",
// "cloud-<id>"…) to the job running it, so that pages can stop it.
var runningJobs sync.Map

func trackJob(key string, j jobs.Snapshot) jobs.Snapshot {
	runningJobs.Store(key, j.ID)
	return j
}

// runningJob is the id of the job running the task of key, or "".
func (s *Server) runningJob(key string) string {
	if id, ok := runningJobs.Load(key); ok && s.jobs.Running(id.(string)) {
		return id.(string)
	}
	return ""
}

// stoppedRun turns the history entry of a run stopped by an administrator
// into "stopped" rather than an error.
func stoppedRun(j *jobs.Job, run *store.ScheduleRun, err error) bool {
	if by, ok := j.Cancelled(); ok && err != nil {
		run.Status, run.Message = "stopped", "Stopped by "+by
		return true
	}
	return false
}

// unlockSource removes the restic lock a stopped restore (or restore test)
// may have left in the repository of a restore source ("backup:<id>",
// "image:<task>/<vm>").
func (s *Server) unlockSource(source string, logf func(string, ...any)) {
	kind, rest, _ := strings.Cut(source, ":")
	if kind != "backup" && kind != "image" {
		return
	}
	tid, _, _ := strings.Cut(rest, "/")
	var t *store.BackupTask
	s.store.Read(func(st *store.State) {
		if x := st.Backup(tid); x != nil && x.Kind == "restic" {
			c := resolveTask(st, x)
			t = &c
		}
	})
	if t != nil {
		backup.Unlock(*t, logf)
	}
}
