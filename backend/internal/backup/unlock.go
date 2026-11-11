package backup

import (
	"context"
	"time"

	"strataos/internal/store"
)

// Unlock removes the locks a stopped restic command left in the repository
// of t ("restic unlock" only drops stale locks: those of processes that no
// longer run on this host, or older than 30 minutes, so another NAS
// writing to a shared datastore keeps its own). It runs with its own short
// context, since the job's is cancelled by then.
func Unlock(t store.BackupTask, logf func(string, ...any)) {
	if t.Kind != "" && t.Kind != "restic" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := restic(ctx, &t, nil, "unlock"); err != nil {
		logf("warning: could not remove the repository lock: %v", err)
		return
	}
	logf("Repository lock removed")
}
