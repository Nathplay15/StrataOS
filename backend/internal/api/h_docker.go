package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"

	"strataos/internal/docker"
	"strataos/internal/jobs"
)

func (s *Server) requireDocker() error {
	if !docker.Available() {
		return &Error{Status: http.StatusServiceUnavailable, Msg: "Docker Engine is not running"}
	}
	return nil
}

// proxyGET forwards a GET to the Docker API and returns the decoded JSON.
func (s *Server) dockerGet(r *http.Request, path string, q url.Values) (any, error) {
	if err := s.requireDocker(); err != nil {
		return nil, err
	}
	var out any
	err := s.docker.Call(r.Context(), "GET", path, q, nil, &out)
	return out, err
}

func (s *Server) dockerRoutes() {
	s.admin("GET /api/docker/info", func(r *http.Request) (any, error) {
		if !docker.Available() {
			return map[string]any{"available": false}, nil
		}
		var info, version, df any
		ctx := r.Context()
		if err := s.docker.Call(ctx, "GET", "/info", nil, nil, &info); err != nil {
			return map[string]any{"available": false, "error": err.Error()}, nil
		}
		_ = s.docker.Call(ctx, "GET", "/version", nil, nil, &version)
		_ = s.docker.Call(ctx, "GET", "/system/df", nil, nil, &df)
		return map[string]any{"available": true, "info": info, "version": version, "df": df, "compose": composeAvailable()}, nil
	})

	// ---- Containers ----
	s.admin("GET /api/docker/containers", func(r *http.Request) (any, error) {
		return s.dockerGet(r, "/containers/json", url.Values{"all": {"1"}, "size": {"0"}})
	})
	s.admin("GET /api/docker/containers/{id}", func(r *http.Request) (any, error) {
		return s.dockerGet(r, "/containers/"+url.PathEscape(r.PathValue("id"))+"/json", nil)
	})
	s.admin("GET /api/docker/containers/{id}/stats", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		return s.docker.Stats(r.Context(), r.PathValue("id"))
	})
	s.admin("GET /api/docker/stats", func(r *http.Request) (any, error) {
		// Stats of all running containers, fetched in parallel.
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		var list []struct {
			ID string `json:"Id"`
		}
		if err := s.docker.Call(r.Context(), "GET", "/containers/json", nil, nil, &list); err != nil {
			return nil, err
		}
		type res struct {
			id string
			st *docker.Stats
		}
		ch := make(chan res, len(list))
		for _, c := range list {
			go func(id string) {
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				st, _ := s.docker.Stats(ctx, id)
				ch <- res{id, st}
			}(c.ID)
		}
		out := map[string]*docker.Stats{}
		for range list {
			x := <-ch
			if x.st != nil {
				out[x.id] = x.st
			}
		}
		return out, nil
	})
	s.admin("POST /api/docker/containers/{id}/{action}", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		id, action := r.PathValue("id"), r.PathValue("action")
		switch action {
		case "start", "stop", "restart", "kill", "pause", "unpause":
		default:
			return nil, badRequest("unknown action")
		}
		s.audit(r, "docker", fmt.Sprintf("Container %s: %s", shortName(id), action))
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		return nil, s.docker.Call(ctx, "POST", "/containers/"+url.PathEscape(id)+"/"+action, nil, nil, nil)
	})
	s.admin("DELETE /api/docker/containers/{id}", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		id := r.PathValue("id")
		q := url.Values{"force": {"1"}}
		if r.URL.Query().Get("volumes") == "1" {
			q.Set("v", "1")
		}
		s.audit(r, "docker", "Removed container "+shortName(id))
		return nil, s.docker.Call(r.Context(), "DELETE", "/containers/"+url.PathEscape(id), q, nil, nil)
	})
	s.admin("POST /api/docker/containers", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		var req docker.CreateReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		s.audit(r, "docker", "Creating container "+req.Name+" from "+req.Image)
		return s.jobs.Start("docker", "Create container "+firstNonEmpty(req.Name, req.Image), userOf(r), func(ctx context.Context, j *jobs.Job) error {
			j.Logf("Pulling %s", req.Image)
			if err := s.docker.Pull(ctx, req.Image, func(line string, pct float64) {
				if line != "" {
					j.Logf("%s", line)
				}
				if pct >= 0 {
					j.SetProgress(pct * 0.9)
				}
			}); err != nil {
				return err
			}
			id, err := s.docker.Create(ctx, req)
			if err == nil {
				j.Logf("Container %s created", shortName(id))
			}
			return err
		}), nil
	})
	s.adminRaw("GET /api/docker/containers/{id}/logs", s.containerLogs)
	s.adminRaw("GET /api/docker/containers/{id}/exec", s.containerExec)

	// ---- Images ----
	s.admin("GET /api/docker/images", func(r *http.Request) (any, error) { return s.dockerGet(r, "/images/json", nil) })
	s.admin("POST /api/docker/images/pull", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		var req struct {
			Image string `json:"image"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Image == "" || strings.ContainsAny(req.Image, " \n") {
			return nil, badRequest("invalid image name")
		}
		return s.jobs.Start("docker", "Pull "+req.Image, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return s.docker.Pull(ctx, req.Image, func(line string, pct float64) {
				if line != "" {
					j.Logf("%s", line)
				}
				if pct >= 0 {
					j.SetProgress(pct)
				}
			})
		}), nil
	})
	s.admin("DELETE /api/docker/images/{id}", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		s.audit(r, "docker", "Removed image "+shortName(r.PathValue("id")))
		return nil, s.docker.Call(r.Context(), "DELETE", "/images/"+url.PathEscape(r.PathValue("id")), url.Values{"force": {"1"}}, nil, nil)
	})
	s.admin("POST /api/docker/prune", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		var req struct {
			Containers, Images, Volumes, Networks, BuildCache bool
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		ctx := r.Context()
		res := map[string]any{}
		do := func(key, path string, q url.Values) {
			var out any
			if err := s.docker.Call(ctx, "POST", path, q, nil, &out); err == nil {
				res[key] = out
			} else {
				res[key] = map[string]string{"error": err.Error()}
			}
		}
		if req.Containers {
			do("containers", "/containers/prune", nil)
		}
		if req.Images {
			do("images", "/images/prune", url.Values{"filters": {`{"dangling":["false"]}`}})
		}
		if req.Volumes {
			do("volumes", "/volumes/prune", url.Values{"filters": {`{"all":["true"]}`}})
		}
		if req.Networks {
			do("networks", "/networks/prune", nil)
		}
		if req.BuildCache {
			do("buildCache", "/build/prune", nil)
		}
		s.audit(r, "docker", "Pruned unused Docker resources")
		return res, nil
	})

	// ---- Volumes & networks ----
	s.admin("GET /api/docker/volumes", func(r *http.Request) (any, error) { return s.dockerGet(r, "/volumes", nil) })
	s.admin("POST /api/docker/volumes", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, s.docker.Call(r.Context(), "POST", "/volumes/create", nil, map[string]any{"Name": req.Name}, nil)
	})
	s.admin("DELETE /api/docker/volumes/{name}", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		return nil, s.docker.Call(r.Context(), "DELETE", "/volumes/"+url.PathEscape(r.PathValue("name")), nil, nil, nil)
	})
	s.admin("GET /api/docker/networks", func(r *http.Request) (any, error) { return s.dockerGet(r, "/networks", nil) })
	s.admin("POST /api/docker/networks", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		var req struct {
			Name    string `json:"name"`
			Driver  string `json:"driver"`
			Subnet  string `json:"subnet"`
			Gateway string `json:"gateway"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Driver == "" {
			req.Driver = "bridge"
		}
		body := map[string]any{"Name": req.Name, "Driver": req.Driver, "CheckDuplicate": true}
		if req.Subnet != "" {
			cfg := map[string]string{"Subnet": req.Subnet}
			if req.Gateway != "" {
				cfg["Gateway"] = req.Gateway
			}
			body["IPAM"] = map[string]any{"Config": []any{cfg}}
		}
		return nil, s.docker.Call(r.Context(), "POST", "/networks/create", nil, body, nil)
	})
	s.admin("DELETE /api/docker/networks/{id}", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		return nil, s.docker.Call(r.Context(), "DELETE", "/networks/"+url.PathEscape(r.PathValue("id")), nil, nil, nil)
	})

	// ---- Compose stacks ----
	s.admin("GET /api/docker/stacks", func(r *http.Request) (any, error) { return docker.ListStacks(r.Context(), s.docker) })
	s.admin("GET /api/docker/stacks/{name}", func(r *http.Request) (any, error) {
		return docker.GetStack(r.Context(), s.docker, r.PathValue("name"))
	})
	saveStack := func(r *http.Request, name string) (any, error) {
		var req struct {
			Name    string `json:"name"`
			Compose string `json:"compose"`
			Env     string `json:"env"`
			Deploy  bool   `json:"deploy"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if name == "" {
			name = req.Name
			if _, err := os.Stat(docker.StacksDir + "/" + name); err == nil {
				return nil, badRequest("a stack with this name already exists")
			}
		}
		if err := docker.SaveStack(r.Context(), name, req.Compose, req.Env, ""); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "docker", "Saved stack "+name)
		if !req.Deploy {
			return nil, nil
		}
		return s.jobs.Start("stack", "Deploy stack "+name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return docker.StackAction(ctx, name, "up", j)
		}), nil
	}
	s.admin("POST /api/docker/stacks", func(r *http.Request) (any, error) { return saveStack(r, "") })
	s.admin("PUT /api/docker/stacks/{name}", func(r *http.Request) (any, error) { return saveStack(r, r.PathValue("name")) })
	s.admin("POST /api/docker/stacks/{name}/{action}", func(r *http.Request) (any, error) {
		name, action := r.PathValue("name"), r.PathValue("action")
		if err := docker.ValidStack(name); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "docker", fmt.Sprintf("Stack %s: %s", name, action))
		return s.jobs.Start("stack", fmt.Sprintf("Stack %s: %s", name, action), userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return docker.StackAction(ctx, name, action, j)
		}), nil
	})
	s.admin("GET /api/docker/stacks/{name}/logs", func(r *http.Request) (any, error) {
		out, err := docker.StackLogs(r.Context(), r.PathValue("name"), qInt(r, "tail", 300))
		return map[string]string{"logs": out}, err
	})
	s.admin("DELETE /api/docker/stacks/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		s.audit(r, "docker", "Deleted stack "+name)
		return nil, docker.DeleteStack(r.Context(), name, r.URL.Query().Get("volumes") == "1")
	})

	// ---- App store ----
	s.admin("GET /api/apps", func(r *http.Request) (any, error) {
		stacks, _ := docker.ListStacks(r.Context(), s.docker)
		installed := map[string]string{}
		for _, st := range stacks {
			if st.App != "" {
				installed[st.App] = st.Name
			}
		}
		// Web port actually chosen at install time (read from the stack's .env).
		ports := map[string]string{}
		for appID, stack := range installed {
			if app := docker.FindApp(appID); app != nil && app.WebPort != "" {
				if p := docker.EnvValue(stack, app.WebPort); p != "" {
					ports[appID] = p
				}
			}
		}
		return map[string]any{"apps": docker.Catalog, "installed": installed, "ports": ports}, nil
	})
	s.admin("POST /api/apps/{id}/install", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		app := docker.FindApp(r.PathValue("id"))
		if app == nil {
			return nil, notFound("unknown app")
		}
		var req struct {
			Name   string            `json:"name"`
			Values map[string]string `json:"values"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Name == "" {
			req.Name = app.ID
		}
		if err := docker.ValidStack(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if _, err := os.Stat(docker.StacksDir + "/" + req.Name); err == nil {
			return nil, badRequest("an app or stack named " + req.Name + " already exists")
		}
		env, dirs, err := app.RenderEnv(req.Values)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "apps", "Installing "+app.Name)
		return s.jobs.Start("app", "Install "+app.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			for _, d := range dirs {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return err
				}
				_ = os.Chown(d, 1000, 1000)
			}
			if err := docker.SaveStack(ctx, req.Name, app.Compose, env, app.ID); err != nil {
				return err
			}
			j.Logf("Deploying %s...", app.Name)
			return docker.StackAction(ctx, req.Name, "up", j)
		}), nil
	})
}

func shortName(id string) string {
	if len(id) == 64 {
		return id[:12]
	}
	return id
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// containerLogs streams container logs over a websocket.
func (s *Server) containerLogs(w http.ResponseWriter, r *http.Request) {
	if err := s.requireDocker(); err != nil {
		writeErr(w, err)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	id := r.PathValue("id")
	var info struct {
		Config struct {
			Tty bool `json:"Tty"`
		} `json:"Config"`
	}
	if err := s.docker.Call(ctx, "GET", "/containers/"+url.PathEscape(id)+"/json", nil, nil, &info); err != nil {
		_ = c.Write(ctx, websocket.MessageText, []byte("error: "+err.Error()))
		return
	}
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "500"
	}
	body, err := s.docker.Stream(ctx, "GET", "/containers/"+url.PathEscape(id)+"/logs",
		url.Values{"stdout": {"1"}, "stderr": {"1"}, "follow": {"1"}, "timestamps": {"1"}, "tail": {tail}}, nil)
	if err != nil {
		_ = c.Write(ctx, websocket.MessageText, []byte("error: "+err.Error()))
		return
	}
	defer body.Close()
	_ = docker.Demux(body, info.Config.Tty, func(stream, line string) {
		msg, _ := json.Marshal(map[string]string{"s": stream, "l": line})
		_ = c.Write(ctx, websocket.MessageText, msg)
	})
	c.Close(websocket.StatusNormalClosure, "")
}

// containerExec opens an interactive shell inside a container.
func (s *Server) containerExec(w http.ResponseWriter, r *http.Request) {
	if err := s.requireDocker(); err != nil {
		writeErr(w, err)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	shell := r.URL.Query().Get("shell")
	if shell != "/bin/bash" && shell != "/bin/ash" {
		shell = "/bin/sh"
	}
	id := r.PathValue("id")
	execID, err := s.docker.Exec(ctx, id, []string{shell})
	if err != nil {
		_ = c.Write(ctx, websocket.MessageBinary, []byte("error: "+err.Error()+"\r\n"))
		return
	}
	conn, br, err := s.docker.Hijack(ctx, execID)
	if err != nil {
		_ = c.Write(ctx, websocket.MessageBinary, []byte("error: "+err.Error()+"\r\n"))
		return
	}
	defer conn.Close()
	s.audit(r, "docker", "Opened shell in container "+shortName(id))
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := br.Read(buf)
			if n > 0 {
				if c.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					break
				}
			}
			if err != nil {
				if err != io.EOF {
					_ = c.Write(ctx, websocket.MessageBinary, []byte("\r\n[session closed]\r\n"))
				}
				break
			}
		}
		cancel()
	}()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m termMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "input":
			if _, err := conn.Write([]byte(m.Data)); err != nil {
				return
			}
		case "resize":
			_ = s.docker.ExecResize(ctx, execID, m.Cols, m.Rows)
		}
	}
}
