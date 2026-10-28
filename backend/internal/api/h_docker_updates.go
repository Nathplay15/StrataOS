package api

// One-click container updates. A check compares, for every container, the
// digest the registry serves for its tag with the digests of the image it
// runs. "Update" pulls the tag and recreates the container with the same
// settings (or runs compose pull + up for a StrataOS stack), keeps the old
// container and image until the new one runs, and rolls back when it does
// not start. A periodic check notifies the administrators of new updates.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/docker"
	"strataos/internal/jobs"
)

// Update states of a container.
const (
	updCurrent     = "current"     // runs the image the registry serves
	updAvailable   = "available"   // the registry has a newer image
	updUnsupported = "unsupported" // local image, pinned digest: cannot tell
	updError       = "error"       // the registry could not be asked
)

const dockerUpdateEvery = 12 * time.Hour

// containerRefRe: a container ID or name, as the Docker API accepts them.
var containerRefRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type containerUpdate struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	Stack  string `json:"stack,omitempty"` // StrataOS stack (App Center app) it belongs to
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Remote string `json:"remote,omitempty"` // digest in the registry
}

type dockerUpdateState struct {
	Checked    time.Time         `json:"checked,omitzero"`
	Running    bool              `json:"running"`
	Containers []containerUpdate `json:"containers"`
	Stacks     map[string]bool   `json:"stacks"`    // stack → an update is available
	Available  int               `json:"available"` // containers with an update
	Notified   []string          `json:"notified,omitempty"`
}

var (
	dockerUpdMu    sync.Mutex // guards dockerUpd
	dockerUpd      dockerUpdateState
	dockerUpdLoad  sync.Once
	dockerUpdating sync.Mutex // one check or update at a time
)

func (s *Server) dockerUpdatesFile() string { return filepath.Join(s.dataDir, "docker-updates.json") }

// dockerUpdates returns a copy of the last check results.
func (s *Server) dockerUpdates() dockerUpdateState {
	dockerUpdLoad.Do(func() {
		if b, err := os.ReadFile(s.dockerUpdatesFile()); err == nil {
			dockerUpdMu.Lock()
			_ = json.Unmarshal(b, &dockerUpd)
			dockerUpd.Running = false
			dockerUpdMu.Unlock()
		}
	})
	dockerUpdMu.Lock()
	defer dockerUpdMu.Unlock()
	c := dockerUpd
	c.Containers = slices.Clone(dockerUpd.Containers)
	if c.Containers == nil {
		c.Containers = []containerUpdate{}
	}
	c.Stacks = map[string]bool{}
	for k, v := range dockerUpd.Stacks {
		c.Stacks[k] = v
	}
	return c
}

func (s *Server) setDockerUpdates(f func(st *dockerUpdateState)) {
	s.dockerUpdates() // loaded
	dockerUpdMu.Lock()
	f(&dockerUpd)
	dockerUpd.Available, dockerUpd.Stacks = 0, map[string]bool{}
	for _, c := range dockerUpd.Containers {
		if c.Status == updAvailable {
			dockerUpd.Available++
			if c.Stack != "" {
				dockerUpd.Stacks[c.Stack] = true
			}
		}
	}
	b, _ := json.Marshal(dockerUpd)
	dockerUpdMu.Unlock()
	_ = os.WriteFile(s.dockerUpdatesFile(), b, 0o600)
}

// inspectContainer and inspectImage return docker inspect data as maps, so
// that a recreated container keeps fields this code does not know about.
func (s *Server) inspectContainer(ctx context.Context, id string) (map[string]any, error) {
	var out map[string]any
	err := s.docker.Call(ctx, "GET", "/containers/"+url.PathEscape(id)+"/json", nil, nil, &out)
	return out, err
}

func (s *Server) inspectImage(ctx context.Context, id string) (map[string]any, error) {
	var out map[string]any
	err := s.docker.Call(ctx, "GET", "/images/"+url.PathEscape(id)+"/json", nil, nil, &out)
	return out, err
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys[:len(keys)-1] {
		m, _ = m[k].(map[string]any)
	}
	v, _ := m[keys[len(keys)-1]].(string)
	return v
}

// managedStack returns the StrataOS stack a container belongs to ("" if none).
func managedStack(ctr map[string]any) string {
	p, _ := asMap(asMap(ctr["Config"])["Labels"])["com.docker.compose.project"].(string)
	if p == "" || docker.ValidStack(p) != nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(docker.StacksDir, p, "compose.yaml")); err != nil {
		return ""
	}
	return p
}

// checkContainer works out the update state of one container. digests
// caches registry answers per image reference within a check.
func (s *Server) checkContainer(ctx context.Context, id string, digests map[string][2]string) containerUpdate {
	ctr, err := s.inspectContainer(ctx, id)
	if err != nil {
		return containerUpdate{ID: id, Status: updError, Error: err.Error()}
	}
	cu := containerUpdate{ID: str(ctr, "Id"), Name: strings.TrimPrefix(str(ctr, "Name"), "/"), Image: str(ctr, "Config", "Image"), Stack: managedStack(ctr)}
	ref, err := docker.ParseRef(cu.Image)
	if err != nil || ref.Digest != "" {
		cu.Status = updUnsupported
		return cu
	}
	img, err := s.inspectImage(ctx, str(ctr, "Image"))
	if err != nil {
		cu.Status, cu.Error = updError, err.Error()
		return cu
	}
	var repoDigests []string
	for _, d := range asSlice(img["RepoDigests"]) {
		if v, ok := d.(string); ok {
			repoDigests = append(repoDigests, v)
		}
	}
	if len(repoDigests) == 0 {
		cu.Status = updUnsupported // built here or loaded from a file: no registry
		return cu
	}
	key := ref.Registry + "/" + ref.Repo + ":" + ref.Tag
	got, ok := digests[key]
	if !ok {
		d, err := docker.RemoteDigest(ctx, ref)
		got = [2]string{d, ""}
		if err != nil {
			got[1] = err.Error()
		}
		digests[key] = got
	}
	switch {
	case got[1] != "":
		cu.Status, cu.Error = updError, got[1]
	case docker.HasDigest(repoDigests, ref, got[0]):
		cu.Status, cu.Remote = updCurrent, got[0]
	default:
		cu.Status, cu.Remote = updAvailable, got[0]
	}
	return cu
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// checkDockerUpdates checks every container and returns the results.
func (s *Server) checkDockerUpdates(ctx context.Context, logf func(string, ...any)) ([]containerUpdate, error) {
	var list []struct {
		ID string `json:"Id"`
	}
	if err := s.docker.Call(ctx, "GET", "/containers/json", url.Values{"all": {"1"}}, nil, &list); err != nil {
		return nil, err
	}
	s.setDockerUpdates(func(st *dockerUpdateState) { st.Running = true })
	digests := map[string][2]string{}
	out := []containerUpdate{}
	for _, c := range list {
		cu := s.checkContainer(ctx, c.ID, digests)
		switch cu.Status {
		case updAvailable:
			logf("%s (%s): update available", cu.Name, cu.Image)
		case updError:
			logf("%s (%s): %s", cu.Name, cu.Image, cu.Error)
		case updCurrent:
			logf("%s (%s): up to date", cu.Name, cu.Image)
		}
		out = append(out, cu)
	}
	slices.SortFunc(out, func(a, b containerUpdate) int { return strings.Compare(a.Name, b.Name) })
	s.setDockerUpdates(func(st *dockerUpdateState) { st.Containers, st.Checked, st.Running = out, time.Now(), false })
	return out, nil
}

// notifyDockerUpdates tells about updates not notified before.
func (s *Server) notifyDockerUpdates(list []containerUpdate) {
	var fresh, keys []string
	known := s.dockerUpdates().Notified
	for _, c := range list {
		if c.Status != updAvailable {
			continue
		}
		k := c.Image + "@" + c.Remote
		keys = append(keys, k)
		if !slices.Contains(known, k) && !slices.Contains(fresh, c.Name) {
			fresh = append(fresh, c.Name)
		}
	}
	s.setDockerUpdates(func(st *dockerUpdateState) { st.Notified = keys })
	if len(fresh) == 0 {
		return
	}
	msg := fmt.Sprintf("Updates are available for %d container(s): %s", len(fresh), strings.Join(fresh, ", "))
	s.store.Log("info", "docker", "", msg)
	s.notify("info", "Container updates available", msg)
}

func (s *Server) dockerUpdatesLoop() {
	time.Sleep(10 * time.Minute)
	for {
		if docker.Available() && dockerUpdating.TryLock() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			list, err := s.checkDockerUpdates(ctx, func(string, ...any) {})
			cancel()
			dockerUpdating.Unlock()
			if err != nil {
				slog.Warn("container update check", "err", err)
				s.setDockerUpdates(func(st *dockerUpdateState) { st.Running = false })
			} else {
				s.notifyDockerUpdates(list)
			}
		}
		time.Sleep(dockerUpdateEvery)
	}
}

// waitRunning watches a new container for a few seconds: it must keep
// running (not restarting), and become healthy when it has a health check.
func (s *Server) waitRunning(ctx context.Context, id string, logf func(string, ...any)) error {
	deadline := time.Now().Add(90 * time.Second)
	stable := 0
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		ctr, err := s.inspectContainer(ctx, id)
		if err != nil {
			return err
		}
		st, _ := ctr["State"].(map[string]any)
		running, _ := st["Running"].(bool)
		restarting, _ := st["Restarting"].(bool)
		if !running || restarting {
			code, _ := st["ExitCode"].(float64)
			return fmt.Errorf("the new container stopped (exit code %d)", int(code))
		}
		health := str(st, "Health", "Status")
		switch health {
		case "unhealthy":
			return errors.New("the new container is unhealthy")
		case "starting":
			continue
		}
		if stable++; stable >= 8 {
			logf("The new container runs%s.", map[bool]string{true: " and is healthy", false: ""}[health == "healthy"])
			return nil
		}
	}
	return errors.New("the new container did not become healthy in time")
}

// updateContainer pulls the image of a container and recreates it.
func (s *Server) updateContainer(ctx context.Context, id string, j *jobs.Job) error {
	ctr, err := s.inspectContainer(ctx, id)
	if err != nil {
		return err
	}
	if stack := managedStack(ctr); stack != "" {
		return s.updateStack(ctx, stack, j)
	}
	id, name, image, oldImage := str(ctr, "Id"), strings.TrimPrefix(str(ctr, "Name"), "/"), str(ctr, "Config", "Image"), str(ctr, "Image")
	ref, err := docker.ParseRef(image)
	if err != nil || ref.Digest != "" {
		return fmt.Errorf("%s runs %s: not an image tag that can be updated", name, image)
	}
	oldImg, err := s.inspectImage(ctx, oldImage)
	if err != nil {
		return err
	}
	j.Logf(">> Pulling %s", image)
	if err := s.docker.Pull(ctx, image, func(line string, pct float64) {
		if line != "" {
			j.Logf("%s", line)
		}
		if pct >= 0 {
			j.SetProgress(pct * 0.6)
		}
	}); err != nil {
		return err
	}
	newImg, err := s.inspectImage(ctx, image)
	if err != nil {
		return err
	}
	if str(newImg, "Id") == oldImage {
		j.Logf("%s already runs the latest %s.", name, image)
		s.markUpdated(id, id)
		return nil
	}
	body, extra := docker.RecreateSpec(ctr, oldImg)
	wasRunning, _ := ctr["State"].(map[string]any)["Running"].(bool)
	backup := name + "-strata-old"
	j.Logf(">> Replacing %s (the old container is kept as %s until the new one runs)", name, backup)
	if wasRunning {
		if err := s.docker.Call(ctx, "POST", "/containers/"+id+"/stop", url.Values{"t": {"30"}}, nil, nil); err != nil {
			return err
		}
	}
	if err := s.docker.Call(ctx, "POST", "/containers/"+id+"/rename", url.Values{"name": {backup}}, nil, nil); err != nil {
		s.restartOld(id, wasRunning)
		return err
	}
	rollback := func(newID string, cause error) error {
		j.Logf(">> %v: rolling back to the previous container", cause)
		bg := context.Background()
		if newID != "" {
			s.logTail(bg, newID, j)
			_ = s.docker.Call(bg, "DELETE", "/containers/"+newID, url.Values{"force": {"1"}}, nil, nil)
		}
		_ = s.docker.Call(bg, "POST", "/containers/"+id+"/rename", url.Values{"name": {name}}, nil, nil)
		s.restartOld(id, wasRunning)
		s.store.Log("error", "docker", j.User, fmt.Sprintf("Update of container %s failed, previous version restored: %v", name, cause))
		return fmt.Errorf("update failed, previous version restored: %w", cause)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := s.docker.Call(ctx, "POST", "/containers/create", url.Values{"name": {name}}, body, &created); err != nil {
		return rollback("", err)
	}
	for net, ep := range extra {
		if err := s.docker.Call(ctx, "POST", "/networks/"+url.PathEscape(net)+"/connect", nil, map[string]any{"Container": created.ID, "EndpointConfig": ep}, nil); err != nil {
			return rollback(created.ID, err)
		}
	}
	j.SetProgress(75)
	if wasRunning {
		if err := s.docker.Call(ctx, "POST", "/containers/"+created.ID+"/start", nil, nil, nil); err != nil {
			return rollback(created.ID, err)
		}
		if err := s.waitRunning(ctx, created.ID, j.Logf); err != nil {
			return rollback(created.ID, err)
		}
	}
	bg := context.Background()
	_ = s.docker.Call(bg, "DELETE", "/containers/"+id, nil, nil, nil) // volumes stay
	// Only when nothing else uses it and no tag points to it any more.
	if err := s.docker.Call(bg, "DELETE", "/images/"+url.PathEscape(oldImage), nil, nil, nil); err == nil {
		j.Logf("Old image %s removed", shortName(strings.TrimPrefix(oldImage, "sha256:")))
	}
	s.markUpdated(id, created.ID)
	s.store.Log("info", "docker", j.User, fmt.Sprintf("Container %s updated (%s)", name, image))
	j.Logf(">> %s updated", name)
	return nil
}

func (s *Server) restartOld(id string, wasRunning bool) {
	if wasRunning {
		_ = s.docker.Call(context.Background(), "POST", "/containers/"+id+"/start", nil, nil, nil)
	}
}

// logTail copies the last lines of a failed container into the job log.
func (s *Server) logTail(ctx context.Context, id string, j *jobs.Job) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, err := s.docker.Stream(ctx, "GET", "/containers/"+id+"/logs", url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {"20"}}, nil)
	if err != nil {
		return
	}
	defer body.Close()
	ctr, _ := s.inspectContainer(ctx, id)
	tty, _ := asMap(ctr["Config"])["Tty"].(bool)
	_ = docker.Demux(body, tty, func(_, line string) { j.Logf("  | %s", line) })
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// markUpdated records an updated container as current (new ID when recreated).
func (s *Server) markUpdated(oldID, newID string) {
	s.setDockerUpdates(func(st *dockerUpdateState) {
		for i := range st.Containers {
			if st.Containers[i].ID == oldID {
				st.Containers[i].ID, st.Containers[i].Status, st.Containers[i].Error = newID, updCurrent, ""
			}
		}
	})
}

// stackContainers returns the IDs and images (reference → image ID) of the
// containers of a compose project.
func (s *Server) stackContainers(ctx context.Context, stack string) (ids []string, images map[string]string, err error) {
	var list []struct {
		ID      string `json:"Id"`
		Image   string `json:"Image"`
		ImageID string `json:"ImageID"`
	}
	f, _ := json.Marshal(map[string][]string{"label": {"com.docker.compose.project=" + stack}})
	if err := s.docker.Call(ctx, "GET", "/containers/json", url.Values{"all": {"1"}, "filters": {string(f)}}, nil, &list); err != nil {
		return nil, nil, err
	}
	images = map[string]string{}
	for _, c := range list {
		ids = append(ids, c.ID)
		if ctr, err := s.inspectContainer(ctx, c.ID); err == nil {
			images[str(ctr, "Config", "Image")] = str(ctr, "Image")
		}
	}
	return ids, images, nil
}

// updateStack runs compose pull and up on a StrataOS stack; when its
// containers do not run afterwards, the previous images get their tags back
// and the stack goes up again on them.
func (s *Server) updateStack(ctx context.Context, stack string, j *jobs.Job) error {
	if err := docker.ValidStack(stack); err != nil {
		return badRequest(err.Error())
	}
	ids, before, err := s.stackContainers(ctx, stack)
	if err != nil {
		return err
	}
	j.Logf(">> Pulling the images of %s", stack)
	if err := docker.StackAction(ctx, stack, "pull", j); err != nil {
		return err
	}
	j.SetProgress(50)
	j.Logf(">> Recreating the containers of %s whose image changed", stack)
	upErr := docker.StackAction(ctx, stack, "up", j)
	if upErr == nil {
		var newIDs []string
		newIDs, _, upErr = s.stackContainers(ctx, stack)
		for _, id := range newIDs {
			if upErr == nil && !slices.Contains(ids, id) {
				upErr = s.waitRunning(ctx, id, func(string, ...any) {})
			}
		}
	}
	if upErr != nil {
		j.Logf(">> %v: rolling back to the previous images", upErr)
		bg := context.Background()
		for ref, img := range before {
			if r, err := docker.ParseRef(ref); err == nil && r.Digest == "" {
				_ = s.docker.Call(bg, "POST", "/images/"+url.PathEscape(img)+"/tag", url.Values{"repo": {r.Name()}, "tag": {r.Tag}}, nil, nil)
			}
		}
		if err := docker.StackAction(bg, stack, "up", j); err != nil {
			j.Logf("warning: %v", err)
		}
		s.store.Log("error", "docker", j.User, fmt.Sprintf("Update of stack %s failed, previous images restored: %v", stack, upErr))
		return fmt.Errorf("update failed, previous images restored: %w", upErr)
	}
	_, after, _ := s.stackContainers(ctx, stack)
	for ref, img := range before {
		if after[ref] != "" && after[ref] != img {
			_ = s.docker.Call(context.Background(), "DELETE", "/images/"+url.PathEscape(img), nil, nil, nil)
		}
	}
	s.setDockerUpdates(func(st *dockerUpdateState) {
		st.Containers = slices.DeleteFunc(st.Containers, func(c containerUpdate) bool { return c.Stack == stack })
	})
	digests := map[string][2]string{}
	newIDs, _, _ := s.stackContainers(ctx, stack)
	for _, id := range newIDs {
		cu := s.checkContainer(ctx, id, digests)
		s.setDockerUpdates(func(st *dockerUpdateState) { st.Containers = append(st.Containers, cu) })
	}
	s.store.Log("info", "docker", j.User, fmt.Sprintf("Stack %s updated", stack))
	j.Logf(">> %s updated", stack)
	return nil
}

func (s *Server) dockerUpdateRoutes() {
	s.admin("GET /api/docker/updates", func(r *http.Request) (any, error) {
		st := s.dockerUpdates()
		st.Notified = nil
		return st, nil
	})
	busy := &Error{Status: http.StatusConflict, Msg: "an update check or update is already running"}
	s.admin("POST /api/docker/updates/check", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		if !dockerUpdating.TryLock() {
			return nil, busy
		}
		return s.jobs.Start("docker", "Check for container updates", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			defer dockerUpdating.Unlock()
			list, err := s.checkDockerUpdates(ctx, j.Logf)
			if err != nil {
				s.setDockerUpdates(func(st *dockerUpdateState) { st.Running = false })
				return err
			}
			n := 0
			for _, c := range list {
				if c.Status == updAvailable {
					n++
				}
			}
			j.Logf(">> %d update(s) available", n)
			s.notifyDockerUpdates(list)
			return nil
		}), nil
	})
	// Update one container (by ID or name) or one stack.
	s.admin("POST /api/docker/updates/apply", func(r *http.Request) (any, error) {
		var req struct {
			Container string `json:"container"`
			Stack     string `json:"stack"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		if (req.Container == "") == (req.Stack == "") {
			return nil, badRequest("give a container or a stack")
		}
		if req.Stack != "" && docker.ValidStack(req.Stack) != nil {
			return nil, badRequest("invalid stack name")
		}
		if req.Container != "" && !containerRefRe.MatchString(req.Container) {
			return nil, badRequest("invalid container")
		}
		if !dockerUpdating.TryLock() {
			return nil, busy
		}
		what := firstNonEmpty(req.Stack, req.Container)
		s.audit(r, "docker", "Updating "+what)
		return s.jobs.Start("docker", "Update "+shortName(what), userOf(r), func(ctx context.Context, j *jobs.Job) error {
			defer dockerUpdating.Unlock()
			if req.Stack != "" {
				return s.updateStack(ctx, req.Stack, j)
			}
			return s.updateContainer(ctx, req.Container, j)
		}), nil
	})
	s.admin("POST /api/docker/updates/apply-all", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		if !dockerUpdating.TryLock() {
			return nil, busy
		}
		s.audit(r, "docker", "Updating all containers")
		return s.jobs.Start("docker", "Update all containers", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			defer dockerUpdating.Unlock()
			var failed []string
			stacks := map[string]bool{}
			for _, c := range s.dockerUpdates().Containers {
				if c.Status != updAvailable {
					continue
				}
				var err error
				switch {
				case c.Stack != "" && stacks[c.Stack]:
					continue
				case c.Stack != "":
					stacks[c.Stack] = true
					err = s.updateStack(ctx, c.Stack, j)
				default:
					err = s.updateContainer(ctx, c.ID, j)
				}
				if err != nil {
					j.Logf("%s: %v", firstNonEmpty(c.Stack, c.Name), err)
					failed = append(failed, firstNonEmpty(c.Stack, c.Name))
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("update failed for %s", strings.Join(failed, ", "))
			}
			return nil
		}), nil
	})
}
