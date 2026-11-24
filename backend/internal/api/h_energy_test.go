package api

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"strataos/internal/health"
	"strataos/internal/store"
)

func TestEnergyNightPlan(t *testing.T) {
	s := newTestServer(t)
	s.hist = health.NewStore(t.TempDir())
	s.energyRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		return nil
	})
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.1", time.Hour)}}

	// Invalid window: refused before anything is saved.
	code, _ := admin.do("PUT", "/api/energy", map[string]any{
		"spindownMin": 0,
		"night":       map[string]any{"enabled": true, "from": "22:00", "to": "22:00", "spindownMin": 20},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("same from/to: %d", code)
	}
	code, _ = admin.do("PUT", "/api/energy", map[string]any{
		"spindownMin": 0,
		"night":       map[string]any{"enabled": true, "from": "22:00", "to": "07:00", "spindownMin": 999},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("bad spindown: %d", code)
	}

	// Valid plan: saved and reported.
	code, _ = admin.do("PUT", "/api/energy", map[string]any{
		"spindownMin": 30,
		"night":       map[string]any{"enabled": true, "from": "22:00", "to": "07:00", "spindownMin": 20},
	})
	if code != http.StatusOK && code != 200 {
		// applyEnergy may answer 200 with a warning body; both are fine here.
		if code != http.StatusOK {
			t.Fatalf("valid plan: %d", code)
		}
	}
	code, out := admin.do("GET", "/api/energy", nil)
	if code != 200 {
		t.Fatalf("GET /api/energy: %d", code)
	}
	cfg, _ := out["config"].(map[string]any)
	night, _ := cfg["night"].(map[string]any)
	if night["enabled"] != true || night["from"] != "22:00" || night["spindownMin"] != float64(20) {
		t.Fatalf("night plan not saved: %v", night)
	}
	if _, ok := out["nightActive"]; !ok {
		t.Fatal("nightActive missing")
	}

	// Consumption endpoint answers (now may be 0 without hardware).
	code, pw := admin.do("GET", "/api/energy/power", nil)
	if code != 200 {
		t.Fatalf("GET /api/energy/power: %d", code)
	}
	if _, ok := pw["history"]; !ok {
		t.Fatalf("power history missing: %v", pw)
	}
}

func TestSpindownPendingPerDisk(t *testing.T) {
	st := &spindownState{applied: map[string]int{}}
	st.set([]string{"/dev/sda", "/dev/sdb"}, 20)
	// sdb was asleep at the change to 60: it stays pending until applied.
	st.set([]string{"/dev/sda"}, 60)
	if p := st.pending([]string{"/dev/sda", "/dev/sdb", "/dev/sdc"}, 60); len(p) != 2 || p[0] != "/dev/sdb" || p[1] != "/dev/sdc" {
		t.Fatalf("pending: %v", p)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); st.set([]string{"/dev/sda"}, i); _ = st.pending([]string{"/dev/sda"}, i) }(i)
	}
	wg.Wait()
}
