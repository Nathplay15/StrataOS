package docker

import (
	"fmt"
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

// CrashReason tells why a container is unhealthy, or "" when it is fine.
// A container crashes repeatedly when it is restarting in a loop now, when
// its health check fails, or when it restarted many times within the last hour.
func CrashReason(st InspectState, now time.Time) string {
	if st.Health != nil && st.Health.Status == "unhealthy" {
		return "its health check fails"
	}
	if st.Status == "restarting" {
		return "it is restarting in a loop"
	}
	if st.Error != "" {
		return "it failed to start: " + st.Error
	}
	if st.RestartCount >= crashRestarts && now.Sub(st.StartedAt) <= crashWindow {
		return fmt.Sprintf("%d restarts in the last hour", st.RestartCount)
	}
	if st.OOMKilled {
		return "it was killed: out of memory"
	}
	return ""
}
