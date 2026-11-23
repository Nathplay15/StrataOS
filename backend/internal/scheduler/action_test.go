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

func TestValidateReport(t *testing.T) {
	good := store.Schedule{Name: "Weekly summary", Type: "report", Cron: "0 8 * * 1", Prompt: "Summarize the NAS health", TargetKind: "vm", Target: "x"}
	if err := Validate(&good, nil); err != nil {
		t.Fatalf("valid report schedule: %v", err)
	}
	if good.TargetKind != "" || good.Target != "" {
		t.Errorf("a report has no target: %+v", good)
	}
	bad := store.Schedule{Name: "x", Type: "report", Cron: "0 8 * * 1", Prompt: strings.Repeat("a", 2001)}
	if err := Validate(&bad, nil); err == nil {
		t.Fatal("an overlong prompt must be refused")
	}
}

func TestExecuteReport(t *testing.T) {
	old := ReportRunner
	t.Cleanup(func() { ReportRunner = old })
	var gotPrompt string
	ReportRunner = func(_ context.Context, prompt string, _ func(string, ...any)) (string, error) {
		gotPrompt = prompt
		return "sent", nil
	}
	s := store.Schedule{Type: "report", Prompt: "hello"}
	msg, err := Execute(context.Background(), s, nil, func(string, ...any) {})
	if err != nil || msg != "sent" || gotPrompt != "hello" {
		t.Fatalf("Execute report: %q %v %q", msg, err, gotPrompt)
	}
	ReportRunner = nil
	if _, err := Execute(context.Background(), s, nil, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("nil runner: %v", err)
	}
}
