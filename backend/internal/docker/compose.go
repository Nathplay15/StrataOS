package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"strataos/internal/sys"
)

// StacksDir holds one directory per compose stack.
var StacksDir = "/var/lib/strata/stacks"

var stackNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func ValidStack(n string) error {
	if !stackNameRe.MatchString(n) {
		return fmt.Errorf("invalid stack name %q (lowercase letters, digits, - and _)", n)
	}
	return nil
}

func composeCmd() (string, []string, error) {
	if _, err := sys.Run("docker", "compose", "version"); err == nil {
		return "docker", []string{"compose"}, nil
	}
	if sys.Has("docker-compose") {
		return "docker-compose", nil, nil
	}
	return "", nil, fmt.Errorf("docker compose is not installed")
}

func compose(ctx context.Context, name string, out io.Writer, args ...string) (string, error) {
	cmd, pre, err := composeCmd()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(StacksDir, name)
	full := append(append([]string{}, pre...), "-p", name, "--project-directory", dir, "-f", filepath.Join(dir, "compose.yaml"))
	full = append(full, args...)
	return sys.RunOpts(ctx, sys.Opts{Dir: dir, Timeout: 30 * time.Minute, Output: out}, cmd, full...)
}

type Stack struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Services   int        `json:"services"`
	Running    int        `json:"running"`
	App        string     `json:"app,omitempty"`
	Custom     *CustomApp `json:"custom,omitempty"`
	ModifiedAt time.Time  `json:"modifiedAt"`
}

type StackDetail struct {
	Stack
	Compose    string           `json:"compose"`
	Env        string           `json:"env"`
	Containers []map[string]any `json:"containers"`
}

func ListStacks(ctx context.Context, c *Client) ([]Stack, error) {
	entries, err := os.ReadDir(StacksDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var containers []struct {
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	if Available() {
		_ = c.Call(ctx, "GET", "/containers/json", map[string][]string{"all": {"1"}}, nil, &containers)
	}
	stacks := []Stack{}
	for _, e := range entries {
		if !e.IsDir() || ValidStack(e.Name()) != nil {
			continue
		}
		info, _ := e.Info()
		s := Stack{Name: e.Name(), Status: "stopped"}
		if info != nil {
			s.ModifiedAt = info.ModTime()
		}
		if app, err := os.ReadFile(filepath.Join(StacksDir, e.Name(), ".strata-app")); err == nil {
			s.App = strings.TrimSpace(string(app))
		}
		s.Custom = ReadCustom(e.Name())
		for _, ct := range containers {
			if ct.Labels["com.docker.compose.project"] != s.Name {
				continue
			}
			s.Services++
			if ct.State == "running" {
				s.Running++
			}
		}
		switch {
		case s.Running > 0 && s.Running == s.Services:
			s.Status = "running"
		case s.Running > 0:
			s.Status = "partial"
		}
		stacks = append(stacks, s)
	}
	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Name < stacks[j].Name })
	return stacks, nil
}

func GetStack(ctx context.Context, c *Client, name string) (*StackDetail, error) {
	if err := ValidStack(name); err != nil {
		return nil, err
	}
	dir := filepath.Join(StacksDir, name)
	yml, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		return nil, fmt.Errorf("stack %s not found", name)
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	d := &StackDetail{Compose: string(yml), Env: string(env), Containers: []map[string]any{}}
	stacks, _ := ListStacks(ctx, c)
	for _, s := range stacks {
		if s.Name == name {
			d.Stack = s
		}
	}
	d.Name = name
	if Available() {
		filters, _ := json.Marshal(map[string][]string{"label": {"com.docker.compose.project=" + name}})
		_ = c.Call(ctx, "GET", "/containers/json", map[string][]string{"all": {"1"}, "filters": {string(filters)}}, nil, &d.Containers)
	}
	return d, nil
}

// SaveStack writes compose.yaml and .env, validating the result with `compose config`.
func SaveStack(ctx context.Context, name, yml, env, app string) error {
	if err := ValidStack(name); err != nil {
		return err
	}
	dir := filepath.Join(StacksDir, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	prevYml, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	prevEnv, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(yml), 0o640); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		return err
	}
	if app != "" {
		_ = os.WriteFile(filepath.Join(dir, ".strata-app"), []byte(app), 0o640)
	}
	if _, err := compose(ctx, name, nil, "config", "-q"); err != nil {
		if len(prevYml) > 0 {
			_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), prevYml, 0o640)
			_ = os.WriteFile(filepath.Join(dir, ".env"), prevEnv, 0o600)
		} else {
			_ = os.RemoveAll(dir)
		}
		return fmt.Errorf("invalid compose file: %w", err)
	}
	return nil
}

// StackAction runs up, down, restart, pull, stop or start on a stack.
func StackAction(ctx context.Context, name, action string, out io.Writer) error {
	if err := ValidStack(name); err != nil {
		return err
	}
	var args []string
	switch action {
	case "up":
		args = []string{"up", "-d", "--remove-orphans"}
	case "down":
		args = []string{"down", "--remove-orphans"}
	case "stop":
		args = []string{"stop"}
	case "start":
		args = []string{"start"}
	case "restart":
		args = []string{"restart"}
	case "pull":
		args = []string{"pull"}
	case "update":
		if _, err := compose(ctx, name, out, "pull"); err != nil {
			return err
		}
		args = []string{"up", "-d", "--remove-orphans"}
	default:
		return fmt.Errorf("unknown stack action %q", action)
	}
	_, err := compose(ctx, name, out, args...)
	return err
}

func StackLogs(ctx context.Context, name string, tail int) (string, error) {
	if err := ValidStack(name); err != nil {
		return "", err
	}
	if tail <= 0 || tail > 5000 {
		tail = 300
	}
	return compose(ctx, name, nil, "logs", "--no-color", "--timestamps", "--tail", fmt.Sprint(tail))
}

func DeleteStack(ctx context.Context, name string, volumes bool) error {
	if err := ValidStack(name); err != nil {
		return err
	}
	args := []string{"down", "--remove-orphans"}
	if volumes {
		args = append(args, "-v")
	}
	if _, err := os.Stat(filepath.Join(StacksDir, name, "compose.yaml")); err == nil {
		if _, err := compose(ctx, name, nil, args...); err != nil {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(StacksDir, name))
}

// EnvValue reads KEY from a stack's .env file ("" when absent).
func EnvValue(stack, key string) string {
	if ValidStack(stack) != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(StacksDir, stack, ".env"))
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// StackNames lists the stacks managed by StrataOS.
func StackNames() []string {
	entries, _ := os.ReadDir(StacksDir)
	var out []string
	for _, e := range entries {
		if e.IsDir() && ValidStack(e.Name()) == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

// StackRunning reports whether a stack has running containers.
func StackRunning(ctx context.Context, name string) bool {
	out, err := compose(ctx, name, nil, "ps", "-q", "--status", "running")
	return err == nil && strings.TrimSpace(out) != ""
}

// StackImages returns the image IDs used by a stack's containers (to detect updates).
func StackImages(ctx context.Context, name string) string {
	out, _ := compose(ctx, name, nil, "ps", "-q")
	var ids []string
	for _, id := range strings.Fields(out) {
		img, err := sys.RunCtx(ctx, "docker", "inspect", "-f", "{{.Image}}", id)
		if err == nil {
			ids = append(ids, strings.TrimSpace(img))
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// PruneDanglingImages removes the images left behind by updates.
func PruneDanglingImages(ctx context.Context) {
	_, _ = sys.RunCtx(ctx, "docker", "image", "prune", "-f")
}
