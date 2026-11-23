package api

import (
	"strings"
	"testing"
)

func TestUserQuotaAlerts(t *testing.T) {
	gb := int64(1e9)
	rs := []userQuotaReading{
		{user: "alice", target: "vol1", name: "Data", used: 85 * gb, limit: 100 * gb},  // 85 %: silent
		{user: "bob", target: "vol1", name: "Data", used: 92 * gb, limit: 100 * gb},    // 92 %: warning
		{user: "carol", target: "vol1", name: "Data", used: 100 * gb, limit: 100 * gb}, // 100 %: error
		{user: "dan", target: "vol1", name: "Data", used: 5 * gb, limit: 0},            // no quota: ignored
	}
	alerts, next := userQuotaAlerts(map[string]float64{}, rs)
	if len(alerts) != 2 {
		t.Fatalf("alerts = %+v, want 2", alerts)
	}
	if alerts[0].Subject != "Quota almost reached" || alerts[0].Level != "warning" || !strings.Contains(alerts[0].Message, "bob") || !strings.Contains(alerts[0].Message, "92%") {
		t.Fatalf("90%% alert: %+v", alerts[0])
	}
	if alerts[1].Subject != "Quota reached" || alerts[1].Level != "error" || !strings.Contains(alerts[1].Message, "carol") {
		t.Fatalf("100%% alert: %+v", alerts[1])
	}
	if next["alice|vol1"] != 85 || next["bob|vol1"] != 92 {
		t.Fatalf("next = %v", next)
	}
	if _, ok := next["dan|vol1"]; ok {
		t.Fatal("no-limit reading must not be tracked")
	}

	// Same readings again: no duplicate alert.
	alerts, _ = userQuotaAlerts(next, rs)
	if len(alerts) != 0 {
		t.Fatalf("repeat: %+v, want none", alerts)
	}

	// Back under 90 % then over again: a new warning (others unchanged).
	under := []userQuotaReading{
		{user: "alice", target: "vol1", name: "Data", used: 85 * gb, limit: 100 * gb},
		{user: "bob", target: "vol1", name: "Data", used: 50 * gb, limit: 100 * gb},
		{user: "carol", target: "vol1", name: "Data", used: 100 * gb, limit: 100 * gb},
	}
	alerts, next = userQuotaAlerts(next, under)
	if len(alerts) != 0 {
		t.Fatalf("under 90%%: %+v", alerts)
	}
	alerts, _ = userQuotaAlerts(next, rs)
	if len(alerts) != 1 || alerts[0].Subject != "Quota almost reached" || !strings.Contains(alerts[0].Message, "bob") {
		t.Fatalf("crossing again: %+v", alerts)
	}
}
