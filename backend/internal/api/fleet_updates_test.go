package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"strataos/internal/jobs"
	"strataos/internal/store"
)

// The peer routes a console calls with its token: authentication,
// permissions, job ownership and rate limit. Nothing here reaches apt (an
// invalid kind is refused after the permission check).
func TestFleetPeerPermissions(t *testing.T) {
	s := newTestServer(t)
	s.fleetRoutes()
	fleetLimits = fleetRate{}
	_ = s.store.Update(func(st *store.State) error {
		st.Fleet.Managers = []store.FleetManager{
			{ID: "ro", Name: "ro", TokenHash: hashToken("tok-ro")},
			{ID: "upd", Name: "upd", TokenHash: hashToken("tok-upd"), AllowUpdates: true},
			{ID: "rb", Name: "rb", TokenHash: hashToken("tok-rb"), AllowReboot: true},
		}
		return nil
	})
	call := func(method, path, tok, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.10:1234"
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	bad := `{"kind":"rm -rf /"}`
	for _, c := range []struct {
		name, method, path, tok, body string
		want                          int
	}{
		{"no token", "POST", "/api/fleet/peer/updates/start", "", bad, 401},
		{"wrong token", "POST", "/api/fleet/peer/updates/start", "nope", bad, 401},
		{"read-only console cannot update", "POST", "/api/fleet/peer/updates/start", "tok-ro", bad, 403},
		{"read-only console cannot check", "POST", "/api/fleet/peer/updates/check", "tok-ro", "", 403},
		{"read-only console cannot follow jobs", "GET", "/api/fleet/peer/jobs/x", "tok-ro", "", 403},
		{"read-only console cannot restart", "POST", "/api/fleet/peer/reboot", "tok-ro", "", 403},
		{"restart needs the update permission too", "POST", "/api/fleet/peer/reboot", "tok-rb", "", 403},
		{"update permission without restart", "POST", "/api/fleet/peer/reboot", "tok-upd", "", 403},
		{"allowed, but only known kinds", "POST", "/api/fleet/peer/updates/start", "tok-upd", bad, 400},
		{"unknown job", "GET", "/api/fleet/peer/jobs/x", "tok-upd", "", 404},
	} {
		if rec := call(c.method, c.path, c.tok, c.body); rec.Code != c.want {
			t.Errorf("%s: got %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}

	// A console sees only the jobs it started.
	_ = s.store.Update(func(st *store.State) error {
		st.Fleet.Managers = append(st.Fleet.Managers, store.FleetManager{ID: "other", Name: "other", TokenHash: hashToken("tok-other"), AllowUpdates: true})
		return nil
	})
	j := s.jobs.Start("apt", "StrataOS update", "upd (console)", func(ctx context.Context, j *jobs.Job) error { return nil })
	fleetJobs.Store(j.ID, "upd")
	if rec := call("GET", "/api/fleet/peer/jobs/"+j.ID, "tok-upd", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), j.ID) {
		t.Errorf("own job: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("GET", "/api/fleet/peer/jobs/"+j.ID, "tok-other", ""); rec.Code != http.StatusNotFound {
		t.Errorf("job of another console: %d", rec.Code)
	}
	plain := s.jobs.Start("raid", "Create RAID", "admin", func(ctx context.Context, j *jobs.Job) error { return nil })
	if rec := call("GET", "/api/fleet/peer/jobs/"+plain.ID, "tok-upd", ""); rec.Code != http.StatusNotFound {
		t.Errorf("job of an administrator: %d", rec.Code)
	}

	// Actions are rate-limited per console.
	fleetLimits = fleetRate{}
	codes := map[int]int{}
	for i := 0; i < fleetActionsPerMin+1; i++ {
		codes[call("POST", "/api/fleet/peer/updates/start", "tok-upd", bad).Code]++
	}
	if codes[400] != fleetActionsPerMin || codes[429] != 1 {
		t.Errorf("rate limit: %v", codes)
	}
	if rec := call("POST", "/api/fleet/peer/updates/start", "tok-other", bad); rec.Code != 400 {
		t.Errorf("another console limited too: %d", rec.Code)
	}
}
