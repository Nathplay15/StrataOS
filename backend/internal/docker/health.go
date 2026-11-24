package docker

import (
	"fmt"
	"sync"
	"time"
)

// HealthState is the healthcheck part of a container inspect.
type HealthState struct {
	Status string `json:"Status"`
}

// InspectState is the part of GET /containers/{id}/json the health watcher reads.
type InspectState struct {
	Status       string       `json:"Status"`
	Running      bool         `json:"Running"`
	RestartCount int          `json:"RestartCount"`
	StartedAt    time.Time    `json:"StartedAt"`
	Error        string       `json:"Error"`
	OOMKilled    bool         `json:"OOMKilled"`
	Health       *HealthState `json:"Health"`
}

// crashRestarts is the restart count within crashWindow that means "crashing
// repeatedly".
const (
	crashRestarts = 5
	crashWindow   = time.Hour
)

// RestartTracker counts the restarts of each container over the last hour
// from successive observations of RestartCount (a total since creation).
type RestartTracker struct {
	mu   sync.Mutex
	seen map[string][]restartObs
}

type restartObs struct {
	at    time.Time
	count int
}

// Restarts records the RestartCount of a container seen at now and returns
// how many restarts happened within the last hour as far as observed (a
// container seen for the first time has 0, whatever its history).
func (t *RestartTracker) Restarts(id string, count int, now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[string][]restartObs{}
	}
	obs := t.seen[id]
	// Keep the newest observation older than the window as the baseline.
	for len(obs) > 1 && now.Sub(obs[1].at) >= crashWindow {
		obs = obs[1:]
	}
	if len(obs) > 0 && count < obs[len(obs)-1].count {
		obs = nil // recreated container: its count started again
	}
	obs = append(obs, restartObs{now, count})
	if len(obs) > 200 {
		obs = obs[len(obs)-200:]
	}
	t.seen[id] = obs
	return count - obs[0].count
}

// Forget drops the containers not in keep (removed ones).
func (t *RestartTracker) Forget(keep map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.seen {
		if !keep[id] {
			delete(t.seen, id)
		}
	}
}

// CrashReason tells why a container is unhealthy, or "" when it is fine.
// A container crashes repeatedly when it is restarting in a loop now, when
// its health check fails, or when it restarted many times within the last
// hour (restarts: counted by a RestartTracker).
func CrashReason(st InspectState, restarts int) string {
	if st.Health != nil && st.Health.Status == "unhealthy" {
		return "its health check fails"
	}
	if st.Status == "restarting" {
		return "it is restarting in a loop"
	}
	if st.Error != "" {
		return "it failed to start: " + st.Error
	}
	if restarts >= crashRestarts {
		return fmt.Sprintf("%d restarts in the last hour", restarts)
	}
	if st.OOMKilled {
		return "it was killed: out of memory"
	}
	return ""
}
