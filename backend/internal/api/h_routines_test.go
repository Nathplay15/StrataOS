package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"strataos/internal/store"
)

func routinesServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		return nil
	})
	s.routineRoutes()
	return s
}

func routineAdmin(t *testing.T, s *Server) *jarClient {
	t.Helper()
	return &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}
}

func TestRoutineTriggered(t *testing.T) {
	st := routineState{
		cpu: 92.4, mem: 40,
		volumes:      map[string]int{"tank": 95, "media": 50},
		diskFailed:   map[string]bool{"/dev/sda": true},
		diskWeak:     map[string]bool{"/dev/sdb": true},
		backupFailed: map[string]bool{"b1": true},
	}
	tests := []struct {
		rt   store.Routine
		want bool
	}{
		{store.Routine{Trigger: "cpu", Value: 90}, true},
		{store.Routine{Trigger: "cpu", Value: 95}, false},
		{store.Routine{Trigger: "mem", Value: 90}, false},
		{store.Routine{Trigger: "volume-full", Value: 90}, true},
		{store.Routine{Trigger: "volume-full", Value: 90, Target: "media"}, false},
		{store.Routine{Trigger: "volume-full", Value: 90, Target: "tank"}, true},
		{store.Routine{Trigger: "disk-failed"}, true},
		{store.Routine{Trigger: "disk-failed", Target: "/dev/sdb"}, false},
		{store.Routine{Trigger: "disk-failed", Target: "/dev/sda"}, true},
		{store.Routine{Trigger: "disk-weak"}, true},
		{store.Routine{Trigger: "backup-failed"}, true},
		{store.Routine{Trigger: "backup-failed", Target: "b1"}, true},
		{store.Routine{Trigger: "backup-failed", Target: "other"}, false},
		{store.Routine{Trigger: "nope"}, false},
	}
	for _, tt := range tests {
		if got, _ := routineTriggered(&tt.rt, st); got != tt.want {
			t.Errorf("trigger %q value %d target %q: got %v, want %v", tt.rt.Trigger, tt.rt.Value, tt.rt.Target, got, tt.want)
		}
	}
}

func TestRoutineTextPlaceholders(t *testing.T) {
	rt := &store.Routine{Name: "Volume plein"}
	if got := routineText("", rt, "Volume tank is 95% full"); got != "Volume plein: Volume tank is 95% full" {
		t.Errorf("default text: %q", got)
	}
	if got := routineText("{name} — {detail}", rt, "95 %"); got != "Volume plein — 95 %" {
		t.Errorf("placeholders: %q", got)
	}
}

func TestRoutineCooldown(t *testing.T) {
	s := newTestServer(t)
	rt := &store.Routine{ID: "r1", Name: "CPU high", Enabled: true, Trigger: "cpu", Value: 50, Actions: []store.RoutineAction{{Kind: "notify"}}}
	_ = s.store.Update(func(st *store.State) error { st.Routines = append(st.Routines, rt); return nil })
	st := routineState{cpu: 80}

	now := time.Now()
	s.evalRoutinesState(st, now)
	s.store.Read(func(ss *store.State) {
		if ss.Routines[0].LastFired.IsZero() {
			t.Fatal("routine did not fire")
		}
	})
	// Still in the cooldown window: nothing changes.
	s.evalRoutinesState(st, now.Add(10*time.Minute))
	s.store.Read(func(ss *store.State) {
		if !ss.Routines[0].LastFired.Equal(now) {
			t.Fatal("routine fired again within the cooldown")
		}
	})
	// Cooldown elapsed (default 60 min): it fires again.
	s.evalRoutinesState(st, now.Add(61*time.Minute))
	s.store.Read(func(ss *store.State) {
		if ss.Routines[0].LastFired.Equal(now) {
			t.Fatal("routine did not fire after the cooldown")
		}
	})
	// A disabled routine never fires.
	_ = s.store.Update(func(st *store.State) error {
		st.Routines[0].Enabled = false
		st.Routines[0].LastFired = time.Time{}
		return nil
	})
	s.evalRoutinesState(st, now.Add(2*time.Hour))
	s.store.Read(func(ss *store.State) {
		if !ss.Routines[0].LastFired.IsZero() {
			t.Fatal("disabled routine fired")
		}
	})
}

func TestRoutineWebhookAction(t *testing.T) {
	var called atomic.Int32
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		_ = json.NewDecoder(r.Body).Decode(&body)
	}))
	defer srv.Close()

	s := newTestServer(t)
	rt := &store.Routine{ID: "r2", Name: "Disk failed", Enabled: true, Trigger: "disk-failed", Actions: []store.RoutineAction{{Kind: "webhook", URL: srv.URL}}}
	_ = s.store.Update(func(st *store.State) error { st.Routines = append(st.Routines, rt); return nil })
	s.evalRoutinesState(routineState{diskFailed: map[string]bool{"/dev/sda": true}}, time.Now())
	s.bg.Wait()
	if called.Load() != 1 {
		t.Fatalf("webhook called %d times, want 1", called.Load())
	}
	if body["routine"] != "Disk failed" || body["host"] == "" {
		t.Errorf("webhook body: %v", body)
	}
}

func TestRoutineCRUD(t *testing.T) {
	s := routinesServer(t)
	admin := routineAdmin(t, s)

	if code, out := admin.do("POST", "/api/routines", store.Routine{Name: "", Trigger: "cpu", Value: 90, Actions: []store.RoutineAction{{Kind: "notify"}}}); code != http.StatusBadRequest {
		t.Errorf("missing name: %d %v", code, out)
	}
	if code, _ := admin.do("POST", "/api/routines", store.Routine{Name: "x", Trigger: "nope", Actions: []store.RoutineAction{{Kind: "notify"}}}); code != http.StatusBadRequest {
		t.Errorf("unknown trigger: %d", code)
	}
	if code, _ := admin.do("POST", "/api/routines", store.Routine{Name: "x", Trigger: "cpu", Value: 200, Actions: []store.RoutineAction{{Kind: "notify"}}}); code != http.StatusBadRequest {
		t.Errorf("bad threshold: %d", code)
	}
	if code, _ := admin.do("POST", "/api/routines", store.Routine{Name: "x", Trigger: "cpu", Value: 90}); code != http.StatusBadRequest {
		t.Errorf("no action: %d", code)
	}
	if code, _ := admin.do("POST", "/api/routines", store.Routine{Name: "x", Trigger: "cpu", Value: 90, Actions: []store.RoutineAction{{Kind: "webhook", URL: "ftp://x"}}}); code != http.StatusBadRequest {
		t.Errorf("bad webhook URL: %d", code)
	}

	var created store.Routine
	if code, out := admin.do("POST", "/api/routines", store.Routine{Name: "CPU high", Enabled: true, Trigger: "cpu", Value: 90, Actions: []store.RoutineAction{{Kind: "notify", Message: "{name}: {detail}"}}}); code != http.StatusOK {
		t.Fatalf("create: %d %v", code, out)
	} else if b, _ := json.Marshal(out["id"]); json.Unmarshal(b, &created.ID) != nil || created.ID == "" {
		t.Fatalf("create returned no id: %v", out)
	}

	if w := admin.req("GET", "/api/routines", nil); w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	} else {
		var list []store.Routine
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Name != "CPU high" {
			t.Fatalf("list: %v %s", err, w.Body.String())
		}
	}

	if _, out := admin.do("PUT", "/api/routines/"+created.ID, store.Routine{Name: "CPU very high", Enabled: true, Trigger: "cpu", Value: 95, Actions: []store.RoutineAction{{Kind: "notify"}}}); out == nil {
		t.Fatal("update failed")
	}
	s.store.Read(func(st *store.State) {
		if st.Routines[0].Name != "CPU very high" || st.Routines[0].Value != 95 {
			t.Fatalf("update not applied: %+v", st.Routines[0])
		}
	})

	if w := admin.req("POST", "/api/routines/"+created.ID+"/fire", nil); w.Code != http.StatusOK {
		t.Fatalf("fire: %d %s", w.Code, w.Body.String())
	}

	if w := admin.req("DELETE", "/api/routines/"+created.ID, nil); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := admin.req("DELETE", "/api/routines/"+created.ID, nil); w.Code != http.StatusNotFound {
		t.Errorf("delete twice: %d, want 404", w.Code)
	}
	if w := admin.req("GET", "/api/routines", nil); w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	} else {
		var list []store.Routine
		_ = json.Unmarshal(w.Body.Bytes(), &list)
		if len(list) != 0 {
			t.Fatalf("list after delete: %s", w.Body.String())
		}
	}
}

// Evaluating and firing the routines while an administrator rewrites them
// must not race (go test -race): both work on copies taken under the lock.
func TestRoutineEvalWhilePut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	s := routinesServer(t)
	admin := routineAdmin(t, s)
	rt := store.Routine{Name: "Disk", Enabled: true, Trigger: "disk-failed", Actions: []store.RoutineAction{{Kind: "webhook", URL: srv.URL}}}
	_, out := admin.do("POST", "/api/routines", rt)
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("create: %v", out)
	}
	st := routineState{diskFailed: map[string]bool{"/dev/sda": true}}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 20 {
			s.evalRoutinesState(st, time.Now().Add(time.Duration(i)*2*time.Hour))
		}
	})
	for i := range 20 {
		rt.Name = "Disk " + string(rune('a'+i))
		admin.do("PUT", "/api/routines/"+id, rt)
	}
	wg.Wait()
	s.bg.Wait()
}

func TestRoutineWebhookRedirects(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			http.Redirect(w, r, "/done", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/done" {
			hits.Add(10)
			return
		}
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if _, err := routineWebhookClient().Post(srv.URL+"/away", "application/json", nil); err == nil || hits.Load() != 0 {
		t.Fatalf("redirect to another host followed: %v, hits %d", err, hits.Load())
	}
	if resp, err := routineWebhookClient().Post(srv.URL+"/same", "application/json", nil); err != nil || hits.Load() != 10 {
		t.Fatalf("redirect on the same host: %v, hits %d", err, hits.Load())
	} else {
		resp.Body.Close()
	}
}

// A routine that never ran has no lastFired: the page shows "Never".
func TestRoutineNeverFired(t *testing.T) {
	b, _ := json.Marshal(store.Routine{Name: "x"})
	if strings.Contains(string(b), "lastFired") {
		t.Fatalf("zero lastFired sent: %s", b)
	}
}
