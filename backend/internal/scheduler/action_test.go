package scheduler

import (
	"context"
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestValidateAction(t *testing.T) {
	good := store.Schedule{Name: "Stop test VM", Type: "action", Cron: "0 23 * * *", TargetKind: "vm", Target: "test", Action: "stop"}
	if err := Validate(&good, nil); err != nil {
		t.Fatalf("valid action schedule: %v", err)
	}
	for _, bad := range []store.Schedule{
		{Name: "x", Type: "action", Cron: "0 23 * * *", TargetKind: "vm", Target: "test", Action: "explode"},
		{Name: "x", Type: "action", Cron: "0 23 * * *", TargetKind: "vm", Target: "", Action: "stop"},
		{Name: "x", Type: "action", Cron: "0 23 * * *", TargetKind: "nas", Target: "test", Action: "stop"},
		{Name: "x", Type: "action", Cron: "bad", TargetKind: "vm", Target: "test", Action: "stop"},
	} {
		if err := Validate(&bad, nil); err == nil {
			t.Fatalf("invalid accepted: %+v", bad)
		}
	}
}

func TestExecuteAction(t *testing.T) {
	old := ActionRunner
	t.Cleanup(func() { ActionRunner = old })
	var gotKind, gotTarget, gotAction string
	ActionRunner = func(_ context.Context, kind, target, action string, _ func(string, ...any)) (string, error) {
		gotKind, gotTarget, gotAction = kind, target, action
		return "done", nil
	}
	s := store.Schedule{Type: "action", TargetKind: "lxc", Target: "web", Action: "restart"}
	msg, err := Execute(context.Background(), s, nil, func(string, ...any) {})
	if err != nil || msg != "done" || gotKind != "lxc" || gotTarget != "web" || gotAction != "restart" {
		t.Fatalf("Execute: %q %v %v", msg, err, []string{gotKind, gotTarget, gotAction})
	}
	ActionRunner = nil
	if _, err := Execute(context.Background(), s, nil, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("nil runner: %v", err)
	}
}
