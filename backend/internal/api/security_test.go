package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/store"
)

// newTestServer builds a Server without the background loops of New (which
// probe the host's storage).
func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{store: st, jobs: jobs.NewManager(), sessions: newSessions(""),
		limiter: &limiter{hits: map[string][]time.Time{}}, dataDir: dir, mux: http.NewServeMux()}
}

func TestLimiterBoundsConcurrentAttempts(t *testing.T) {
	l := &limiter{hits: map[string][]time.Time{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.attempt("10.0.0.1") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != limiterMax {
		t.Fatalf("%d concurrent attempts allowed, want %d", allowed, limiterMax)
	}
	if !l.attempt("10.0.0.2") {
		t.Fatal("another client was blocked")
	}
	l.reset("10.0.0.1")
	if !l.attempt("10.0.0.1") {
		t.Fatal("reset did not unblock the client")
	}
}

func TestAuthBodiesAreBounded(t *testing.T) {
	s := newTestServer(t)
	s.authRoutes()
	big := `{"username":"` + strings.Repeat("a", 1<<20) + `","password":"x"}`
	for _, p := range []string{"/api/login", "/api/setup"} {
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest("POST", p, strings.NewReader(big)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "too large") {
			t.Errorf("%s: got %d %s, want 400 body too large", p, rec.Code, rec.Body.String())
		}
	}
}

func TestJobsHiddenFromOtherUsers(t *testing.T) {
	s := newTestServer(t)
	s.systemRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"}, &store.User{Username: "alice", Role: "user"})
		return nil
	})
	done := make(chan struct{})
	j := s.jobs.Start("raid", "Create RAID secret", "admin", func(ctx context.Context, j *jobs.Job) error {
		j.Logf("mdadm --create /dev/md/secret")
		close(done)
		return nil
	})
	<-done
	get := func(user, role, path string) (int, string) {
		tok := s.sessions.create(user, role, "127.0.0.1", time.Hour)
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := get("alice", "user", "/api/jobs"); code != 200 || strings.Contains(body, j.ID) {
		t.Errorf("user listed an admin job: %d %s", code, body)
	}
	if code, _ := get("alice", "user", "/api/jobs/"+j.ID); code != http.StatusNotFound {
		t.Errorf("user read an admin job: %d", code)
	}
	if code, body := get("admin", "admin", "/api/jobs/"+j.ID); code != 200 || !strings.Contains(body, "mdadm") {
		t.Errorf("admin cannot read the job: %d %s", code, body)
	}
}
