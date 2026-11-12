package sys

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A cancelled command takes its children with it (process group).
func TestCancelKillsProcessGroup(t *testing.T) {
	if !Has("sh") || !Has("sleep") {
		t.Skip("no sh")
	}
	pidFile := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := RunOpts(ctx, Opts{Timeout: time.Minute}, "sh", "-c", `sleep 100 & echo $! > "$1"; wait`, "sh", pidFile)
		done <- err
	}()
	var child int
	for i := 0; i < 100 && child == 0; i++ {
		time.Sleep(20 * time.Millisecond)
		if b, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(b)) != "" {
			child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
	if child == 0 {
		t.Fatal("child did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command not stopped")
	}
	for i := 0; i < 50; i++ {
		if syscall.Kill(child, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child %d still alive", child)
}

// A child ignoring SIGTERM is killed after the grace period.
func TestCancelKillsStubbornChild(t *testing.T) {
	if !Has("sh") {
		t.Skip("no sh")
	}
	old := KillGrace
	KillGrace = 300 * time.Millisecond
	defer func() { KillGrace = old }()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := RunOpts(ctx, Opts{Timeout: time.Minute}, "sh", "-c", `trap '' TERM; sleep 30 & wait; sleep 30`)
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("err=%v after %s", err, time.Since(start))
	}
}

func TestTimeoutStillReported(t *testing.T) {
	_, err := RunOpts(context.Background(), Opts{Timeout: 200 * time.Millisecond}, "sleep", "5")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}
