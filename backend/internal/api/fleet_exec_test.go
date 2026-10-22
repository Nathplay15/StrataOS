package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos/internal/store"
)

// fakePeer is a watched NAS answering the console's calls: its update job
// succeeds (or fails) on the next poll and moves it to the new version.
type fakePeer struct {
	name, role string
	peers      []string
	fail       bool
	mu         sync.Mutex
	version    string
	jobState   string
}

type fakeFleet struct {
	mu      sync.Mutex
	events  []string // "start x", "done x"
	running map[string]bool
	overlap []string // HA partners updating at the same time
}

func (f *fakeFleet) serve(t *testing.T, p *fakePeer, partners map[string]string) (*httptest.Server, string) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-"+p.name {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		out := map[string]any{}
		switch {
		case r.URL.Path == "/api/fleet/peer/updates":
			out = map[string]any{"version": p.version, "installed": p.version, "candidate": map[bool]string{true: "", false: "0.6.1"}[p.version == "0.6.1"],
				"bootId": "boot", "access": map[string]bool{"updates": true}, "errors": 0}
		case r.URL.Path == "/api/fleet/peer/updates/start":
			f.mu.Lock()
			f.events = append(f.events, "start "+p.name)
			if partner := partners[p.name]; partner != "" && f.running[partner] {
				f.overlap = append(f.overlap, p.name+"+"+partner)
			}
			f.running[p.name] = true
			f.mu.Unlock()
			p.jobState = "running"
			out = map[string]any{"id": "job-" + p.name, "status": "running"}
		case strings.HasPrefix(r.URL.Path, "/api/fleet/peer/jobs/"):
			// One poll while running, then the end of the job.
			out = map[string]any{"id": "job-" + p.name, "status": p.jobState, "progress": -1}
			if p.jobState == "running" {
				p.jobState = "success"
				if p.fail {
					p.jobState, out["error"] = "failed", "dpkg error"
				} else {
					p.version = "0.6.1"
				}
				f.mu.Lock()
				f.events = append(f.events, "done "+p.name)
				f.running[p.name] = false
				f.mu.Unlock()
			} else if p.jobState == "failed" {
				out["error"] = "dpkg error"
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	sum := sha256.Sum256(srv.Certificate().Raw)
	t.Cleanup(srv.Close)
	return srv, hex.EncodeToString(sum[:])
}

func runFleet(t *testing.T, peers []*fakePeer, rolling bool, partners map[string]string) (*fakeFleet, store.FleetUpdateRun) {
	t.Helper()
	s := newTestServer(t)
	f := &fakeFleet{running: map[string]bool{}}
	urls := map[string]string{}
	var members []store.FleetMember
	var nodes []fleetNode
	for _, p := range peers {
		srv, fp := f.serve(t, p, partners)
		urls[p.name] = srv.URL
		members = append(members, store.FleetMember{ID: "id-" + p.name, Name: p.name, Host: p.name, Cert: fp, Token: "tok-" + p.name})
		nodes = append(nodes, fleetNode{Hostname: p.name, Host: p.name, HARole: p.role, HAPeers: p.peers})
	}
	oldURL, oldPoll := fleetURL, fleetPoll
	fleetURL, fleetPoll = func(m store.FleetMember) string { return urls[m.Host] }, time.Millisecond
	t.Cleanup(func() { fleetURL, fleetPoll = oldURL, oldPoll })
	run := store.FleetUpdateRun{ID: "run", Kind: "strata", Status: "running", Lanes: fleetLanes(nodes, rolling)}
	for _, m := range members {
		run.Items = append(run.Items, store.FleetUpdateItem{Member: m.ID, Name: m.Name, Host: m.Host, State: "pending"})
	}
	_ = s.store.Update(func(st *store.State) error {
		st.Fleet.Members, st.Fleet.Updates = members, []store.FleetUpdateRun{run}
		return nil
	})
	done := make(chan struct{})
	go func() { s.fleetExec("run"); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("grouped update did not finish")
	}
	run, _ = s.fleetRun("run")
	return f, run
}

func states(run store.FleetUpdateRun) string {
	var out []string
	for _, it := range run.Items {
		out = append(out, it.Name+"="+it.State)
	}
	return strings.Join(out, " ")
}

func TestFleetExecHAPairNeverTogether(t *testing.T) {
	peers := []*fakePeer{
		{name: "p", role: "primary", peers: []string{"s"}, version: "0.6.0"},
		{name: "x", version: "0.6.0"},
		{name: "s", role: "secondary", peers: []string{"p"}, version: "0.6.0"},
	}
	f, run := runFleet(t, peers, false, map[string]string{"p": "s", "s": "p"})
	if run.Status != "success" || states(run) != "p=success x=success s=success" {
		t.Fatalf("run %s: %s", run.Status, states(run))
	}
	if len(f.overlap) > 0 {
		t.Fatalf("HA partners updated together: %v", f.overlap)
	}
	order := strings.Join(f.events, ",")
	if strings.Index(order, "done s") > strings.Index(order, "start p") || !strings.Contains(order, "start s") {
		t.Fatalf("primary started before its secondary was done: %s", order)
	}
	for _, it := range run.Items {
		if it.From != "0.6.0" || it.Version != "0.6.1" || it.Target != "0.6.1" || it.Progress != 100 {
			t.Errorf("%s: %+v", it.Name, it)
		}
	}
}

func TestFleetExecRollingStopsOnFailure(t *testing.T) {
	peers := []*fakePeer{
		{name: "a", version: "0.6.0"},
		{name: "b", version: "0.6.0", fail: true},
		{name: "c", version: "0.6.0"},
		{name: "d", version: "0.6.1"}, // already up to date: never started
	}
	f, run := runFleet(t, peers, true, nil)
	if run.Status != "failed" || states(run) != "a=success b=failed c=skipped d=skipped" {
		t.Fatalf("run %s: %s", run.Status, states(run))
	}
	if run.Items[1].Error != "dpkg error" {
		t.Errorf("error %q", run.Items[1].Error)
	}
	if got := strings.Join(f.events, ","); got != "start a,done a,start b,done b" {
		t.Errorf("events %s", got)
	}
	if run.Finished == nil {
		t.Error("run not finished")
	}
}

func TestFleetExecUpToDate(t *testing.T) {
	f, run := runFleet(t, []*fakePeer{{name: "a", version: "0.6.1"}}, false, nil)
	if run.Status != "success" || run.Items[0].Note != "Already up to date" || len(f.events) != 0 {
		t.Fatalf("run %s %+v %v", run.Status, run.Items[0], f.events)
	}
}
