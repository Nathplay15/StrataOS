package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"strataos/internal/docker"
)

// containerHealth is one unhealthy container, as shown in the interface.
type containerHealth struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	RestartCount int       `json:"restartCount"`
	StartedAt    time.Time `json:"startedAt"`
	Reason       string    `json:"reason"`
	LastLogs     string    `json:"lastLogs"`
}

// dockerHealthLoop watches the containers that crash repeatedly and raises a
// StrataOS alert with their recent logs (roadmap 0.15, "App health").
func (s *Server) dockerHealthLoop() {
	alerted := map[string]int{} // container id -> RestartCount at the last alert
	for {
		time.Sleep(2 * time.Minute)
		s.checkContainersHealth(alerted)
	}
}

// checkContainersHealth inspects every container and alerts on the ones that
// crash repeatedly, once per crash episode (a new episode = more restarts).
func (s *Server) checkContainersHealth(alerted map[string]int) {
	if !docker.Available() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, h := range s.containersHealth(ctx) {
		if h.Reason == "" {
			delete(alerted, h.ID) // healthy again: forget the episode
			continue
		}
		if prev, ok := alerted[h.ID]; ok && prev >= h.RestartCount {
			continue
		}
		alerted[h.ID] = h.RestartCount
		msg := fmt.Sprintf("Container %s crashes repeatedly (%s). Last log lines: %s", h.Name, h.Reason, strings.ReplaceAll(h.LastLogs, "\n", " | "))
		s.store.Log("warning", "docker", "", msg)
		s.notify("warning", "Container crashing", msg)
	}
}

// containersHealth inspects every container and returns them with their crash
// reason ("" = healthy) and last log lines; the interface shows the unhealthy.
func (s *Server) containersHealth(ctx context.Context) []containerHealth {
	var list []struct {
		ID string `json:"Id"`
	}
	if err := s.docker.Call(ctx, "GET", "/containers/json", url.Values{"all": {"1"}}, nil, &list); err != nil {
		return nil
	}
	out := []containerHealth{}
	for _, c := range list {
		var ins struct {
			Name  string              `json:"Name"`
			State docker.InspectState `json:"State"`
		}
		if err := s.docker.Call(ctx, "GET", "/containers/"+url.PathEscape(c.ID)+"/json", nil, nil, &ins); err != nil {
			continue
		}
		reason := docker.CrashReason(ins.State, time.Now())
		h := containerHealth{
			ID:           c.ID,
			Name:         strings.TrimPrefix(ins.Name, "/"),
			Status:       ins.State.Status,
			RestartCount: ins.State.RestartCount,
			StartedAt:    ins.State.StartedAt,
			Reason:       reason,
		}
		if reason != "" {
			h.LastLogs = s.containerTailLogs(ctx, c.ID, 5)
		}
		out = append(out, h)
	}
	return out
}

// containerTailLogs returns the last lines of a container's log (both streams).
func (s *Server) containerTailLogs(ctx context.Context, id string, tail int) string {
	body, err := s.docker.Stream(ctx, "GET", "/containers/"+url.PathEscape(id)+"/logs",
		url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {fmt.Sprint(tail)}}, nil)
	if err != nil {
		return ""
	}
	defer body.Close()
	var sb strings.Builder
	_ = docker.Demux(body, false, func(_, line string) {
		if sb.Len() < 600 {
			sb.WriteString(strings.TrimRight(line, "\n"))
			sb.WriteByte('\n')
		}
	})
	return strings.TrimRight(sb.String(), "\n")
}

// dockerHealthRoutes: the containers that crash repeatedly, for the interface.
func (s *Server) dockerHealthRoutes() {
	s.admin("GET /api/docker/health", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		out := []containerHealth{}
		for _, h := range s.containersHealth(r.Context()) {
			if h.Reason != "" {
				out = append(out, h)
			}
		}
		return out, nil
	})
}
