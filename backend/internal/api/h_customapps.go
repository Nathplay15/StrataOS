package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	"strataos/internal/docker"
	"strataos/internal/jobs"
)

// installedApps maps each installed catalog app to its stack.
func installedApps(stacks []docker.Stack) map[string]string {
	installed := map[string]string{}
	for _, st := range stacks {
		if st.App != "" {
			installed[st.App] = st.Name
		}
	}
	return installed
}

// appLookup reads a value of an installed app's .env (for linked defaults).
func appLookup(installed map[string]string) func(app, key string) (string, bool) {
	return func(app, key string) (string, bool) {
		stack, ok := installed[app]
		if !ok {
			return "", false
		}
		return docker.EnvValue(stack, key), true
	}
}

type customAppView struct {
	Stack string `json:"stack"`
	docker.CustomApp
	Status string `json:"status"`
}

// customApps lists the stacks saved as custom apps ("My apps").
func customApps(stacks []docker.Stack) []customAppView {
	out := []customAppView{}
	for _, st := range stacks {
		if st.Custom != nil {
			out = append(out, customAppView{Stack: st.Name, CustomApp: *st.Custom, Status: st.Status})
		}
	}
	return out
}

// customAppRoutes: custom apps are compose stacks with App Center metadata.
// Updating and removing them go through the stack routes, like catalog apps.
func (s *Server) customAppRoutes() {
	s.admin("POST /api/apps/custom", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		var req struct {
			Stack   string `json:"stack"`
			Compose string `json:"compose"`
			Env     string `json:"env"`
			docker.CustomApp
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := docker.ValidStack(req.Stack); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := req.CustomApp.Validate(); err != nil {
			return nil, badRequest(err.Error())
		}
		if len(req.Compose) > 256<<10 || len(req.Env) > 64<<10 {
			return nil, badRequest("the compose file is too large")
		}
		if _, err := os.Stat(filepath.Join(docker.StacksDir, req.Stack)); err == nil {
			return nil, badRequest("an app or stack named " + req.Stack + " already exists")
		}
		// Same validation as the stacks (docker compose config).
		if err := docker.SaveStack(r.Context(), req.Stack, req.Compose, req.Env, ""); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := docker.WriteCustom(req.Stack, req.CustomApp); err != nil {
			_ = docker.DeleteStack(r.Context(), req.Stack, false)
			return nil, badRequest(err.Error())
		}
		s.audit(r, "apps", "Added custom app "+req.Name+" ("+req.Stack+")")
		return s.jobs.Start("app", "Install "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return docker.StackAction(ctx, req.Stack, "up", j)
		}), nil
	})
	// Also saves an existing stack as a custom app.
	s.admin("PUT /api/apps/custom/{stack}", func(r *http.Request) (any, error) {
		stack := r.PathValue("stack")
		var req docker.CustomApp
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := docker.WriteCustom(stack, req); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "apps", "Saved custom app "+req.Name+" ("+stack+")")
		return nil, nil
	})
	// Removes the app from "My apps" and keeps the stack.
	s.admin("DELETE /api/apps/custom/{stack}", func(r *http.Request) (any, error) {
		stack := r.PathValue("stack")
		if err := docker.RemoveCustom(stack); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "apps", "Removed "+stack+" from My apps")
		return nil, nil
	})
}
