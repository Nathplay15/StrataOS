package api

import (
	"context"
	"testing"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/store"
)

func TestJobCancelPolicy(t *testing.T) {
	for typ, want := range map[string]bool{"backup": true, "cloudsync": true, "restore": true, "datastore-verify": true, "burnin": true,
		"apt": false, "install": false, "uninstall": false, "raid": false, rpiJobType: false, "docker": false, "vmcluster": false} {
		ok, reason := jobCancelPolicy(typ)
		if ok != want || (!ok && reason == "") {
			t.Errorf("%s: cancellable=%v reason=%q", typ, ok, reason)
		}
	}
}

func TestStoppedRun(t *testing.T) {
	m := jobs.NewManager()
	done := make(chan store.ScheduleRun, 1)
	snap := m.Start("backup", "Backup: x", "alice", func(ctx context.Context, j *jobs.Job) error {
		<-ctx.Done()
		run := store.ScheduleRun{Status: "success"}
		if !stoppedRun(j, &run, ctx.Err()) {
			run.Status = "failed"
		}
		done <- run
		return ctx.Err()
	})
	if err := m.Cancel(snap.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	select {
	case run := <-done:
		if run.Status != "stopped" || run.Message != "Stopped by bob" {
			t.Fatalf("run %+v", run)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job not stopped")
	}
}
