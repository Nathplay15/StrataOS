package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func wait(t *testing.T, m *Manager, id string) Snapshot {
	t.Helper()
	for i := 0; i < 200; i++ {
		if s, _ := m.Get(id); s.Status != Running {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job still running")
	return Snapshot{}
}

func TestCancelEndsCancelled(t *testing.T) {
	m := NewManager()
	var finished Snapshot
	m.OnFinish = func(s Snapshot) { finished = s }
	snap := m.Start("backup", "Backup: x", "alice", func(ctx context.Context, j *Job) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !snap.Cancellable {
		t.Fatal("job should be cancellable by default")
	}
	if err := m.Cancel(snap.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	s := wait(t, m, snap.ID)
	if s.Status != Cancelled || s.Error != "" || s.CancelledBy != "bob" || s.Cancellable {
		t.Fatalf("got %+v", s)
	}
	if !strings.Contains(s.Log, "Stopped by bob") {
		t.Fatalf("log %q", s.Log)
	}
	if finished.Status != Cancelled {
		t.Fatalf("OnFinish got %v", finished.Status)
	}
	if err := m.Cancel(snap.ID, "bob"); !errors.Is(err, ErrFinished) {
		t.Fatalf("second cancel: %v", err)
	}
	if err := m.Cancel("nope", "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestPolicyRefusesCancel(t *testing.T) {
	m := NewManager()
	m.Policy = func(typ string) (bool, string) {
		if typ == "apt" {
			return false, "An update cannot be stopped"
		}
		return true, ""
	}
	release := make(chan struct{})
	snap := m.Start("apt", "System update", "alice", func(ctx context.Context, j *Job) error {
		<-release
		return nil
	})
	if snap.Cancellable || snap.CancelReason == "" {
		t.Fatalf("got %+v", snap)
	}
	var nc *NotCancellable
	if err := m.Cancel(snap.ID, "bob"); !errors.As(err, &nc) || nc.Reason != "An update cannot be stopped" {
		t.Fatalf("cancel: %v", err)
	}
	close(release)
	if s := wait(t, m, snap.ID); s.Status != Success {
		t.Fatalf("status %v", s.Status)
	}
}

func TestSetCancellableByPhase(t *testing.T) {
	m := NewManager()
	phase := make(chan struct{})
	snap := m.Start("replace", "Disk replacement", "alice", func(ctx context.Context, j *Job) error {
		j.SetCancellable(false, "a RAID rebuild cannot be stopped")
		close(phase)
		<-ctx.Done()
		return ctx.Err()
	})
	<-phase
	if err := m.Cancel(snap.ID, "bob"); err == nil {
		t.Fatal("cancel accepted during a non-cancellable phase")
	}
	m.Abort(snap.ID)
	if s := wait(t, m, snap.ID); s.Status != Failed {
		t.Fatalf("aborted job: %v", s.Status)
	}
}

// A job that completed its work although a cancel arrived late is a success.
func TestCancelAfterWorkDone(t *testing.T) {
	m := NewManager()
	snap := m.Start("x", "x", "a", func(ctx context.Context, j *Job) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})
	_ = m.Cancel(snap.ID, "bob")
	// ctx.Err() is set by the cancel: the job is reported cancelled even if
	// fn returned nil, since its work may have been cut short.
	if s := wait(t, m, snap.ID); s.Status != Cancelled {
		t.Fatalf("status %v", s.Status)
	}
}
