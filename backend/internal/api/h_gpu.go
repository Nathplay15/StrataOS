package api

// Graphics processors for video: what the hardware page shows, the choice of
// the GPU for the NAS's own playback (mediaplay), and the graphics card
// given to media server apps (Jellyfin, Plex) through an override compose
// file that passes /dev/dri and the render group.

import (
	"context"
	"fmt"
	"net/http"
	"os/user"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"strataos/internal/docker"
	"strataos/internal/gpu"
	"strataos/internal/jobs"
	"strataos/internal/mediaplay"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// gpuApps are the catalog apps that can use the graphics card, with the
// compose service that gets it and how its own settings are filled in.
var gpuApps = map[string]string{"jellyfin": "jellyfin", "plex": "plex"}

var gpuCache struct {
	sync.Mutex
	at      time.Time
	gen     int // bumped by gpuForget: a detection started before is not kept
	gpus    []gpu.GPU
	nonFree bool
	vainfo  bool
	vaapi   bool // ffmpeg has h264_vaapi
}

// gpuForget makes the next gpuDetect look again (after a component install).
func gpuForget() {
	gpuCache.Lock()
	gpuCache.at = time.Time{}
	gpuCache.gen++
	gpuCache.Unlock()
}

// gpuDetect returns the GPUs, detected again after 10 minutes or when fresh.
// The detection (vainfo, apt-cache, ffmpeg) runs outside the lock.
func gpuDetect(fresh bool) ([]gpu.GPU, bool, bool) {
	gpuCache.Lock()
	if !fresh && !gpuCache.at.IsZero() && time.Since(gpuCache.at) <= 10*time.Minute {
		defer gpuCache.Unlock()
		return slices.Clone(gpuCache.gpus), gpuCache.nonFree, gpuCache.vainfo
	}
	gen := gpuCache.gen
	gpuCache.Unlock()

	gpus := gpu.Detect()
	vainfo := sys.Has("vainfo")
	nonFree := false
	if slices.ContainsFunc(gpus, func(g gpu.GPU) bool { return g.Vendor == gpu.VendorIntel }) {
		// Installable only when the non-free component is in the APT sources.
		out, _ := sys.Run("apt-cache", "policy", "intel-media-va-driver-non-free")
		nonFree = strings.Contains(out, "Candidate:") && !strings.Contains(out, "Candidate: (none)")
	}
	out, _ := sys.Run("ffmpeg", "-hide_banner", "-encoders")
	vaapi := strings.Contains(out, "h264_vaapi")

	gpuCache.Lock()
	defer gpuCache.Unlock()
	if gpuCache.gen == gen {
		gpuCache.gpus, gpuCache.vainfo, gpuCache.nonFree, gpuCache.vaapi = gpus, vainfo, nonFree, vaapi
		gpuCache.at = time.Now()
	}
	return slices.Clone(gpus), nonFree, vainfo
}

// gpuVAAPI tells whether ffmpeg has the VA-API encoder (from the last detection).
func gpuVAAPI() bool {
	gpuCache.Lock()
	defer gpuCache.Unlock()
	return gpuCache.vaapi
}

func pkgInstalled(p string) bool {
	out, err := sys.Run("dpkg-query", "-W", "-f=${Status}", p)
	return err == nil && strings.Contains(out, "install ok installed")
}

// gpuChoice is the encoder of the built-in player from the settings.
func (s *Server) gpuChoice() mediaplay.HW {
	var c store.Transcoding
	s.store.Read(func(st *store.State) { c = st.Transcoding })
	if c.Accel == "off" {
		return mediaplay.HW{}
	}
	gpus, _, _ := gpuDetect(false)
	g, ok := gpu.Best(gpus, c.Device)
	if !ok {
		return mediaplay.HW{}
	}
	if g.VA.CanEncode(gpu.H264) && gpuVAAPI() {
		return mediaplay.HW{VAAPI: true, Device: g.Node, LowPower: g.VA.LowPower}
	}
	if g.NVENC {
		return mediaplay.HW{NVENC: true}
	}
	return mediaplay.HW{}
}

// gpuSetup hands the choice to the player and reports its fallbacks.
func (s *Server) gpuSetup() {
	mediaplay.Default.OnFallback = func(reason string, off bool) {
		if off {
			msg := "Video conversion by the graphics processor failed, the processor (CPU) converts videos from now on: " + reason
			s.store.Log("warning", "media", "", msg)
			s.notify("warning", "Graphics processor not used", msg)
			return
		}
		s.store.Log("warning", "media", "", "A video could not be converted by the graphics processor, the processor (CPU) converted it: "+reason)
	}
	mediaplay.Default.Configure(s.gpuChoice)
}

func renderGIDs() []string {
	var out []string
	for _, g := range []string{"render", "video"} {
		if gr, err := user.LookupGroup(g); err == nil {
			out = append(out, gr.Gid)
		}
	}
	return out
}

// gpuOverride renders the compose override that gives an app the GPU.
func gpuOverride(service string, gids []string) (string, error) {
	svc := map[string]any{"devices": []string{"/dev/dri:/dev/dri"}}
	if len(gids) > 0 {
		svc["group_add"] = gids
	}
	b, err := yaml.Marshal(map[string]any{"services": map[string]any{service: svc}})
	if err != nil {
		return "", err
	}
	return "# Written by StrataOS: graphics card for video conversion (Hardware page). Do not edit.\n" + string(b), nil
}

// gpuAppView is an installed media server on the hardware page.
type gpuAppView struct {
	Stack   string `json:"stack"`
	App     string `json:"app"`
	Enabled bool   `json:"enabled"`
}

func (s *Server) gpuAppsView() []gpuAppView {
	var on []string
	s.store.Read(func(st *store.State) { on = st.Transcoding.Apps })
	out := []gpuAppView{}
	for _, stack := range docker.StackNames() {
		app := docker.StackApp(stack)
		if _, ok := gpuApps[app]; ok {
			out = append(out, gpuAppView{Stack: stack, App: app, Enabled: slices.Contains(on, stack)})
		}
	}
	return out
}

// gpuAppSettings is what to choose in the app, from the GPU it will use.
type gpuAppSettings struct {
	// Method: qsv (Intel with the iHD driver), vaapi, or "" (no usable GPU).
	Method   string   `json:"method"`
	Device   string   `json:"device,omitempty"`
	Decode   []string `json:"decode"`
	HEVC     bool     `json:"hevc"` // may encode HEVC
	LowPower bool     `json:"lowPower,omitempty"`
}

func appSettingsFor(gpus []gpu.GPU) gpuAppSettings {
	for _, g := range gpus {
		if g.VA == nil || (g.Vendor != gpu.VendorIntel && g.Vendor != gpu.VendorAMD) {
			continue
		}
		st := gpuAppSettings{Method: "vaapi", Device: g.Node, Decode: g.VA.Decode, HEVC: g.VA.CanEncode(gpu.HEVC), LowPower: g.VA.LowPower}
		if g.Vendor == gpu.VendorIntel && g.VA.Name == "iHD" {
			st.Method = "qsv"
		}
		return st
	}
	return gpuAppSettings{Decode: []string{}}
}

// setAppGPU writes or removes the override of a stack and restarts it.
func (s *Server) setAppGPU(r *http.Request, stack string, on bool) (*jobs.Snapshot, error) {
	app := docker.StackApp(stack)
	svc, ok := gpuApps[app]
	if !ok {
		return nil, badRequest("this app cannot use the graphics card")
	}
	content := ""
	if on {
		if len(gpu.Scan()) == 0 {
			return nil, badRequest("no graphics processor with a render node (/dev/dri/renderD*) on this NAS")
		}
		var err error
		if content, err = gpuOverride(svc, renderGIDs()); err != nil {
			return nil, err
		}
	}
	if err := docker.WriteGPUOverride(stack, content); err != nil {
		return nil, err
	}
	err := s.store.Update(func(st *store.State) error {
		st.Transcoding.Apps = slices.DeleteFunc(st.Transcoding.Apps, func(x string) bool { return x == stack })
		if on {
			st.Transcoding.Apps = append(st.Transcoding.Apps, stack)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	title := "Graphics card given to " + stack
	if !on {
		title = "Graphics card taken back from " + stack
	}
	s.audit(r, "apps", title)
	j := s.jobs.Start("app", title, userOf(r), func(ctx context.Context, j *jobs.Job) error {
		return docker.StackAction(ctx, stack, "up", j)
	})
	return &j, nil
}

func (s *Server) gpuRoutes() {
	s.gpuSetup()
	s.admin("GET /api/hardware/gpu", func(r *http.Request) (any, error) {
		gpus, nonFree, vainfo := gpuDetect(r.URL.Query().Get("fresh") == "1")
		type view struct {
			gpu.GPU
			Hints []gpu.Hint `json:"hints"`
		}
		out := []view{}
		for _, g := range gpus {
			h := gpu.Advise(g, vainfo, pkgInstalled, nonFree)
			if h == nil {
				h = []gpu.Hint{}
			}
			out = append(out, view{g, h})
		}
		var c store.Transcoding
		s.store.Read(func(st *store.State) { c = st.Transcoding })
		return map[string]any{
			"gpus": out, "settings": c, "status": mediaplay.Default.Status(),
			"ffmpeg": mediaplay.Installed(), "vainfo": vainfo, "nonFree": nonFree,
			"renderGroup": len(renderGIDs()) > 0,
			"apps":        s.gpuAppsView(), "appSettings": appSettingsFor(gpus),
		}, nil
	})

	s.admin("PUT /api/hardware/gpu", func(r *http.Request) (any, error) {
		var req store.Transcoding
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Accel != "" && req.Accel != "off" {
			return nil, badRequest("invalid acceleration mode")
		}
		if req.Device != "" && !slices.ContainsFunc(gpu.Scan(), func(g gpu.GPU) bool { return g.Node == req.Device }) {
			return nil, badRequest("unknown graphics device")
		}
		err := s.store.Update(func(st *store.State) error {
			st.Transcoding.Accel, st.Transcoding.Device = req.Accel, req.Device
			return nil
		})
		if err != nil {
			return nil, err
		}
		gpuDetect(true)
		s.gpuSetup()
		s.audit(r, "media", fmt.Sprintf("Video conversion: graphics processor %q, device %q", firstNonEmpty(req.Accel, "auto"), req.Device))
		return nil, nil
	})

	// Turns the graphics card of an installed media server on or off.
	s.admin("PUT /api/hardware/gpu/apps/{stack}", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		stack := r.PathValue("stack")
		if docker.ValidStack(stack) != nil {
			return nil, notFound("app not found")
		}
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		j, err := s.setAppGPU(r, stack, req.Enabled)
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": j}, nil
	})
}
