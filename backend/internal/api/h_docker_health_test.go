package api

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/docker"
	"strataos/internal/store"
)

// fakeDocker serves a minimal Docker API on a unix socket.
func fakeDocker(t *testing.T, inspect string, logs []string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "abc123"}})
	})
	mux.HandleFunc("/containers/abc123/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(inspect))
	})
	mux.HandleFunc("/containers/abc123/logs", func(w http.ResponseWriter, r *http.Request) {
		for _, l := range logs {
			hdr := make([]byte, 8)
			hdr[0] = 1
			binary.BigEndian.PutUint32(hdr[4:], uint32(len(l)+1))
			_, _ = w.Write(hdr)
			_, _ = w.Write([]byte(l + "\n"))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	old := docker.Socket
	docker.Socket = sock
	t.Cleanup(func() { docker.Socket = old })
}

func TestCheckContainersHealth(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	fakeDocker(t,
		`{"Name":"/jellyfin","State":{"Status":"running","RestartCount":7,"StartedAt":"`+started+`","Health":{"Status":"none"}}}`,
		[]string{"2026-11-23T11:59:01Z boom", "2026-11-23T11:59:02 exiting"})
	s := newTestServer(t)
	s.docker = docker.New()

	// Seen 20 minutes ago with no restart: 7 since then.
	containerRestarts = &docker.RestartTracker{}
	containerRestarts.Restarts("abc123", 0, time.Now().Add(-20*time.Minute))
	alerted := map[string]int{}
	s.checkContainersHealth(alerted)
	if alerted["abc123"] != 7 {
		t.Fatalf("alerted = %v, want abc123:7", alerted)
	}
	var events []store.Event
	s.store.Read(func(st *store.State) { events = st.Events })
	found := false
	for _, e := range events {
		if e.Category == "docker" && strings.Contains(e.Message, "Container jellyfin crashes repeatedly (7 restarts in the last hour)") && !strings.Contains(e.Message, "boom") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no docker crash alert (without the log lines), events: %+v", events)
	}

	// Same crash episode: no new alert.
	before := len(events)
	s.checkContainersHealth(alerted)
	s.store.Read(func(st *store.State) { events = st.Events })
	if len(events) != before {
		t.Fatalf("repeated alert for the same episode: %d -> %d", before, len(events))
	}

	// A worse episode (more restarts) alerts again.
	started = time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339Nano)
	fakeDocker(t, `{"Name":"/jellyfin","State":{"Status":"restarting","RestartCount":9,"StartedAt":"`+started+`","Health":{"Status":"none"}}}`, nil)
	s.docker = docker.New() // drop the keep-alive connection of the previous fake socket
	s.checkContainersHealth(alerted)
	s.store.Read(func(st *store.State) { events = st.Events })
	if len(events) != before+1 || !strings.Contains(events[len(events)-1].Message, "restarting in a loop") {
		t.Fatalf("new episode must alert: %+v", events[len(events)-1])
	}

	// Healthy again (no restart over the last hour): the episode is forgotten.
	containerRestarts = &docker.RestartTracker{}
	old := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	fakeDocker(t, `{"Name":"/jellyfin","State":{"Status":"running","RestartCount":9,"StartedAt":"`+old+`","Health":{"Status":"healthy"}}}`, nil)
	s.docker = docker.New()
	s.checkContainersHealth(alerted)
	if _, ok := alerted["abc123"]; ok {
		t.Fatal("healthy container still alerted")
	}

	// containersHealth reports the unhealthy ones for the interface.
	fakeDocker(t, `{"Name":"/jellyfin","State":{"Status":"running","RestartCount":6,"StartedAt":"`+started+`","Health":{"Status":"unhealthy"}}}`, []string{"healthcheck failed"})
	s.docker = docker.New()
	h := s.containersHealth(t.Context())
	if len(h) != 1 || h[0].Name != "jellyfin" || h[0].Reason != "its health check fails" || h[0].LastLogs != "healthcheck failed" {
		t.Fatalf("containersHealth = %+v", h)
	}
}

func TestCheckContainersHealthNoDocker(t *testing.T) {
	old := docker.Socket
	docker.Socket = filepath.Join(t.TempDir(), "absent.sock")
	t.Cleanup(func() { docker.Socket = old })
	s := newTestServer(t)
	s.checkContainersHealth(map[string]int{}) // must not panic nor alert
}
