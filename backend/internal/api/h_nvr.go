package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/docker"
	"strataos/internal/jobs"
	"strataos/internal/mediaplay"
	"strataos/internal/nvr"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// Surveillance: cameras recorded by the built-in recorder (ffmpeg, stream
// copy) or by Frigate, live view, playback. Administrators only.

var cams = struct {
	mgr    *nvr.Manager
	syncMu sync.Mutex
	// Frigate event polling.
	filter     *nvr.EventFilter
	frigateMu  sync.Mutex
	frigateErr string
}{mgr: nvr.NewManager()}

const maxCameras = 32

func (s *Server) nvrState() (n store.NVR) {
	s.store.Read(func(st *store.State) {
		n = st.NVR
		n.Cameras = nil
		for _, c := range st.NVR.Cameras {
			cc := *c
			n.Cameras = append(n.Cameras, &cc)
		}
		n.Frigate.Objects = append([]string(nil), st.NVR.Frigate.Objects...)
	})
	return
}

func (s *Server) nvrCamera(id string) (*store.Camera, store.NVR, error) {
	n := s.nvrState()
	for _, c := range n.Cameras {
		if c.ID == id {
			return c, n, nil
		}
	}
	return nil, n, notFound("camera not found")
}

// nvrFolder validates the recordings folder: inside a mounted volume (never
// the system disk when a volume is missing), reached without symbolic links.
func nvrFolder(p string) (string, error) {
	if p == "" {
		return "", badRequest("choose a recordings folder")
	}
	clean := filepath.Clean("/" + p)
	if !strings.HasPrefix(clean, storage.MountRoot+"/") || strings.ContainsAny(clean, "\n\r\"'$`\\") {
		return "", badRequest("the recordings folder must be on a NAS volume")
	}
	// The deepest existing folder must be its own real path, and some folder
	// above the recordings a mounted volume.
	exist := clean
	for {
		if _, err := os.Lstat(exist); err == nil {
			break
		}
		exist = filepath.Dir(exist)
	}
	if real, err := filepath.EvalSymlinks(exist); err != nil || real != exist {
		return "", badRequest("the recordings folder must not go through a symbolic link")
	}
	if info, err := os.Stat(exist); err != nil || !info.IsDir() {
		return "", badRequest("the recordings folder must be on a NAS volume")
	}
	for d := clean; d != storage.MountRoot && d != "/"; d = filepath.Dir(d) {
		if storage.IsMountpoint(d) {
			return clean, nil
		}
	}
	return "", badRequest("the recordings folder must be on a mounted NAS volume")
}

func camFolder(n store.NVR, c *store.Camera) string {
	if n.Folder == "" {
		return ""
	}
	return filepath.Join(n.Folder, c.ID)
}

// nvrSync makes the running recorders match the cameras.
func (s *Server) nvrSync() {
	cams.syncMu.Lock()
	defer cams.syncMu.Unlock()
	n := s.nvrState()
	if n.AlertMinutes > 0 {
		cams.mgr.SetAlertAfter(time.Duration(n.AlertMinutes) * time.Minute)
	}
	var recs []nvr.Recording
	folder, err := nvrFolder(n.Folder)
	if nvr.Installed() && err == nil {
		for _, c := range n.Cameras {
			if !c.Enabled || c.Engine != "" {
				continue
			}
			recs = append(recs, nvr.Recording{ID: c.ID, Name: c.Name, Stream: nvr.WithCredentials(c.URL, c.Username, c.Password),
				Dir: filepath.Join(folder, c.ID), SegmentSeconds: n.SegmentSeconds, Audio: c.Audio})
		}
	}
	cams.mgr.Sync(recs)
}

func (s *Server) nvrLoop() {
	cams.mgr.OnDown = func(id, name string, since time.Time, reason string) {
		msg := fmt.Sprintf("Camera %s has been unreachable since %s: %s", name, since.Format("15:04"), reason)
		s.store.Log("warning", "nvr", "", msg)
		s.notify("warning", "Camera offline", msg)
	}
	cams.mgr.OnUp = func(id, name string, down time.Duration) {
		msg := fmt.Sprintf("Camera %s is back online after %d min", name, int(down.Round(time.Minute).Minutes()))
		s.store.Log("info", "nvr", "", msg)
		s.notify("info", "Camera back online", msg)
	}
	time.Sleep(15 * time.Second)
	for i := 0; ; i++ {
		if len(s.nvrState().Cameras) > 0 {
			if i%2 == 0 {
				s.nvrSync() // volumes mounted later, ffmpeg installed since
			}
			if i%40 == 0 {
				s.nvrRetention()
			}
			s.frigatePoll()
		}
		time.Sleep(15 * time.Second)
	}
}

// nvrRetention deletes the oldest segments of each camera beyond its limits.
func (s *Server) nvrRetention() int {
	n := s.nvrState()
	folder, err := nvrFolder(n.Folder)
	if err != nil {
		return 0
	}
	deleted := 0
	for _, c := range n.Cameras {
		if c.KeepDays <= 0 && c.MaxGB <= 0 {
			continue
		}
		dir := filepath.Join(folder, c.ID)
		segs, err := nvr.ListSegments(dir, cams.mgr.Recording(c.ID))
		if err != nil {
			continue
		}
		old := nvr.Expired(segs, time.Now(), c.KeepDays, int64(c.MaxGB*(1<<30)))
		if len(old) == 0 {
			continue
		}
		rt, err := os.OpenRoot(dir)
		if err != nil {
			continue
		}
		for _, seg := range old {
			if rt.Remove(seg.Name) == nil {
				deleted++
			}
		}
		rt.Close()
		slog.Info("camera retention", "camera", c.Name, "deleted", len(old))
	}
	return deleted
}

type cameraView struct {
	store.Camera
	Status      nvr.Status `json:"status"`
	Size        int64      `json:"size"`
	Segments    int        `json:"segments"`
	Oldest      *time.Time `json:"oldest,omitempty"`
	FrigateName string     `json:"frigateName,omitempty"`
}

func cameraViewOf(n store.NVR, c *store.Camera, names map[string]string) cameraView {
	v := cameraView{Camera: *c, Status: cams.mgr.Status(c.ID)}
	if v.Password != "" {
		v.Password = masked
	}
	if c.Engine == "frigate" {
		v.FrigateName = names[c.ID]
		if c.Enabled {
			v.Status = nvr.Status{State: "frigate"}
		}
	}
	if !c.Enabled {
		v.Status = nvr.Status{State: "disabled"}
	}
	if d := camFolder(n, c); d != "" {
		if segs, err := nvr.ListSegments(d, false); err == nil {
			v.Segments = len(segs)
			for _, sg := range segs {
				v.Size += sg.Size
			}
			if len(segs) > 0 {
				t := segs[0].Start
				v.Oldest = &t
			}
		}
	}
	return v
}

func frigateNames(n store.NVR) map[string]string {
	var ids, names []string
	for _, c := range n.Cameras {
		ids = append(ids, c.ID)
		names = append(names, c.Name)
	}
	return nvr.FrigateNames(ids, names)
}

type cameraReq struct {
	Name     string  `json:"name"`
	URL      string  `json:"url"`
	SubURL   string  `json:"subUrl"`
	Username string  `json:"username"`
	Password string  `json:"password"`
	Enabled  *bool   `json:"enabled"`
	Engine   string  `json:"engine"`
	Audio    bool    `json:"audio"`
	KeepDays int     `json:"keepDays"`
	MaxGB    float64 `json:"maxGb"`
}

// apply validates req into c; old is the stored camera (nil when adding).
func (req cameraReq) apply(c, old *store.Camera) error {
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 64 {
		return badRequest("the camera name must have 1 to 64 characters")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return badRequest("the camera name contains invalid characters")
		}
	}
	main, user, pass, err := nvr.ParseURL(req.URL)
	if err != nil {
		return badRequest(err.Error())
	}
	sub := ""
	if strings.TrimSpace(req.SubURL) != "" {
		if sub, _, _, err = nvr.ParseURL(req.SubURL); err != nil {
			return badRequest("sub-stream: " + err.Error())
		}
	}
	// Credentials typed in the URL go to the (masked) fields.
	if req.Username == "" && user != "" {
		req.Username, req.Password = user, pass
	}
	if req.Password == masked {
		req.Password = ""
		if old != nil {
			req.Password = old.Password
		}
	}
	if err := nvr.ValidCredential(req.Username); err != nil {
		return badRequest(err.Error())
	}
	if err := nvr.ValidCredential(req.Password); err != nil {
		return badRequest(err.Error())
	}
	switch req.Engine {
	case "", "builtin":
		req.Engine = ""
	case "frigate":
	default:
		return badRequest("the engine must be builtin or frigate")
	}
	if req.KeepDays < 0 || req.KeepDays > 3650 || req.MaxGB < 0 || req.MaxGB > 1e6 {
		return badRequest("invalid retention")
	}
	c.Name, c.URL, c.SubURL, c.Username, c.Password = name, main, sub, req.Username, req.Password
	c.Engine, c.Audio, c.KeepDays, c.MaxGB = req.Engine, req.Audio, req.KeepDays, req.MaxGB
	if req.Enabled != nil {
		c.Enabled = *req.Enabled
	}
	return nil
}

func nameTaken(st *store.State, name, except string) bool {
	for _, c := range st.NVR.Cameras {
		if c.ID != except && strings.EqualFold(c.Name, name) {
			return true
		}
	}
	return false
}

// segmentFile opens a segment of a camera through os.Root (no link leads outside).
func (s *Server) segmentFile(r *http.Request) (*os.File, os.FileInfo, *store.Camera, error) {
	c, n, err := s.nvrCamera(r.PathValue("id"))
	if err != nil {
		return nil, nil, nil, err
	}
	name := r.PathValue("name")
	if _, ok := nvr.ParseSegmentName(name, time.Local); !ok {
		return nil, nil, nil, notFound("recording not found")
	}
	d := camFolder(n, c)
	if d == "" {
		return nil, nil, nil, notFound("recording not found")
	}
	rt, err := os.OpenRoot(d)
	if err != nil {
		return nil, nil, nil, notFound("recording not found")
	}
	defer rt.Close()
	f, err := rt.Open(name)
	if err != nil {
		return nil, nil, nil, notFound("recording not found")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, nil, notFound("recording not found")
	}
	return f, info, c, nil
}

func videoType(name string) string {
	if strings.HasSuffix(name, ".mkv") {
		return "video/x-matroska"
	}
	return "video/mp4"
}

func (s *Server) nvrRoutes() {
	s.frigateRoutes()
	s.admin("GET /api/nvr", func(r *http.Request) (any, error) {
		n := s.nvrState()
		names := frigateNames(n)
		list := []cameraView{}
		for _, c := range n.Cameras {
			list = append(list, cameraViewOf(n, c, names))
		}
		folderErr := ""
		if n.Folder != "" {
			if _, err := nvrFolder(n.Folder); err != nil {
				folderErr = err.Error()
			}
		}
		var vols []map[string]any
		for _, t := range s.scanTargets() {
			if t.Kind == "share" {
				continue
			}
			u, _ := storage.StatFS(t.Path)
			vols = append(vols, map[string]any{"path": t.Path, "label": t.Label, "free": u.Free})
		}
		return map[string]any{
			"installed": nvr.Installed(), "folder": n.Folder, "folderError": folderErr,
			"alertMinutes": max(n.AlertMinutes, 0), "segmentSeconds": n.SegmentSeconds,
			"cameras": list, "volumes": vols, "frigate": s.frigateView(r.Context(), n),
		}, nil
	})
	s.admin("PUT /api/nvr/settings", func(r *http.Request) (any, error) {
		var req struct {
			Folder         string `json:"folder"`
			AlertMinutes   int    `json:"alertMinutes"`
			SegmentSeconds int    `json:"segmentSeconds"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		folder, err := nvrFolder(req.Folder)
		if err != nil {
			return nil, err
		}
		if req.AlertMinutes < 0 || req.AlertMinutes > 1440 {
			return nil, badRequest("the alert delay must be 1 to 1440 minutes")
		}
		if req.SegmentSeconds != 0 && (req.SegmentSeconds < 10 || req.SegmentSeconds > 3600) {
			return nil, badRequest("the segment length must be 10 to 3600 seconds")
		}
		if err := os.MkdirAll(folder, 0o755); err != nil {
			return nil, err
		}
		err = s.store.Update(func(st *store.State) error {
			st.NVR.Folder, st.NVR.AlertMinutes, st.NVR.SegmentSeconds = folder, req.AlertMinutes, req.SegmentSeconds
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "nvr", "Surveillance recordings folder: "+folder)
		go s.nvrSync()
		return nil, nil
	})

	// ---- Cameras ----
	s.admin("POST /api/nvr/cameras", func(r *http.Request) (any, error) {
		var req cameraReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		c := &store.Camera{ID: shortID(), Enabled: true, CreatedAt: time.Now()}
		if err := req.apply(c, nil); err != nil {
			return nil, err
		}
		err := s.store.Update(func(st *store.State) error {
			if len(st.NVR.Cameras) >= maxCameras {
				return badRequest(fmt.Sprintf("at most %d cameras", maxCameras))
			}
			if nameTaken(st, c.Name, "") {
				return badRequest("a camera with this name already exists")
			}
			st.NVR.Cameras = append(st.NVR.Cameras, c)
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "nvr", "Added camera "+c.Name)
		go s.nvrSync()
		if c.Engine == "frigate" {
			s.frigateApply(userOf(r))
		}
		n := s.nvrState()
		return cameraViewOf(n, c, frigateNames(n)), nil
	})
	s.admin("PUT /api/nvr/cameras/{id}", func(r *http.Request) (any, error) {
		var req cameraReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		id := r.PathValue("id")
		var name string
		var frigate bool
		err := s.store.Update(func(st *store.State) error {
			for _, c := range st.NVR.Cameras {
				if c.ID != id {
					continue
				}
				next := *c
				if err := req.apply(&next, c); err != nil {
					return err
				}
				if nameTaken(st, next.Name, id) {
					return badRequest("a camera with this name already exists")
				}
				frigate = c.Engine == "frigate" || next.Engine == "frigate"
				*c = next
				name = c.Name
				return nil
			}
			return notFound("camera not found")
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "nvr", "Changed camera "+name)
		cams.mgr.Forget(id) // the live view and recorder restart with the new settings
		go s.nvrSync()
		if frigate {
			s.frigateApply(userOf(r))
		}
		return nil, nil
	})
	s.admin("DELETE /api/nvr/cameras/{id}", func(r *http.Request) (any, error) {
		c, n, err := s.nvrCamera(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		err = s.store.Update(func(st *store.State) error {
			for i, x := range st.NVR.Cameras {
				if x.ID == c.ID {
					st.NVR.Cameras = append(st.NVR.Cameras[:i], st.NVR.Cameras[i+1:]...)
					return nil
				}
			}
			return notFound("camera not found")
		})
		if err != nil {
			return nil, err
		}
		cams.mgr.Forget(c.ID)
		go s.nvrSync()
		msg := "Deleted camera " + c.Name
		if r.URL.Query().Get("recordings") == "1" {
			if d := camFolder(n, c); d != "" {
				if _, err := nvrFolder(d); err == nil {
					_ = os.RemoveAll(d)
					msg = "Deleted camera " + c.Name + " and its recordings"
				}
			}
		}
		s.audit(r, "nvr", msg)
		if c.Engine == "frigate" {
			s.frigateApply(userOf(r))
		}
		return nil, nil
	})
	// Connection test: one picture of the stream, as JPEG.
	s.adminRaw("POST /api/nvr/test", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var req struct {
			cameraReq
			ID     string `json:"id"`
			Stream string `json:"stream"` // main | sub
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		var old *store.Camera
		if req.ID != "" {
			c, _, err := s.nvrCamera(req.ID)
			if err != nil {
				writeErr(w, err)
				return
			}
			old = c
		}
		if !nvr.Installed() {
			writeErr(w, badRequest("ffmpeg is not installed"))
			return
		}
		if req.Name == "" {
			req.Name = "test"
		}
		var c store.Camera
		if err := req.cameraReq.apply(&c, old); err != nil {
			writeErr(w, err)
			return
		}
		stream := c.URL
		if req.Stream == "sub" {
			if c.SubURL == "" {
				writeErr(w, badRequest("no sub-stream"))
				return
			}
			stream = c.SubURL
		}
		img, err := nvr.Snapshot(r.Context(), nvr.WithCredentials(stream, c.Username, c.Password))
		if err != nil {
			writeErr(w, &Error{Status: http.StatusBadGateway, Msg: "connection failed: " + err.Error()})
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(img)
	})
	s.adminRaw("GET /api/nvr/cameras/{id}/live.jpg", func(w http.ResponseWriter, r *http.Request) {
		c, _, err := s.nvrCamera(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		if !c.Enabled {
			writeErr(w, badRequest("the camera is disabled"))
			return
		}
		if !nvr.Installed() {
			writeErr(w, badRequest("ffmpeg is not installed"))
			return
		}
		stream, sub := c.URL, false
		if c.SubURL != "" {
			stream, sub = c.SubURL, true
		}
		p, err := cams.mgr.Live(c.ID, nvr.WithCredentials(stream, c.Username, c.Password), sub, 10*time.Second)
		if err != nil {
			writeErr(w, &Error{Status: http.StatusServiceUnavailable, Msg: err.Error()})
			return
		}
		img, err := os.ReadFile(p)
		if err != nil {
			writeErr(w, &Error{Status: http.StatusServiceUnavailable, Msg: "no picture from the camera yet"})
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(img)
	})

	// ---- Recordings ----
	s.admin("GET /api/nvr/cameras/{id}/recordings", func(r *http.Request) (any, error) {
		c, n, err := s.nvrCamera(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		segs := []nvr.Segment{}
		if d := camFolder(n, c); d != "" {
			if segs, err = nvr.ListSegments(d, cams.mgr.Recording(c.ID)); err != nil {
				return nil, err
			}
		}
		days := nvr.Days(segs, time.Local)
		day := r.URL.Query().Get("day")
		if day == "" {
			day = time.Now().Format("2006-01-02")
			if len(days) > 0 {
				day = days[0]
			}
		}
		list, err := nvr.Day(segs, day, time.Local)
		if err != nil {
			return nil, badRequest("invalid day")
		}
		return map[string]any{"days": days, "day": day, "segments": list}, nil
	})
	s.adminRaw("GET /api/nvr/cameras/{id}/recordings/{name}", func(w http.ResponseWriter, r *http.Request) {
		f, info, c, err := s.segmentFile(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", videoType(info.Name()))
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": c.Name + " " + info.Name()}))
		}
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
	// A clip cut out of a segment (stream copy), downloaded as MP4.
	s.adminRaw("GET /api/nvr/cameras/{id}/recordings/{name}/clip", func(w http.ResponseWriter, r *http.Request) {
		f, info, c, err := s.segmentFile(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		p := f.Name()
		f.Close()
		from, to := qInt(r, "from", 0), qInt(r, "to", 0)
		if from < 0 || to <= from || to-from > 3*3600 {
			writeErr(w, badRequest("invalid clip range"))
			return
		}
		cmd := exec.CommandContext(r.Context(), "ffmpeg", nvr.ClipArgs(p, from, to-from)...)
		w.Header().Set("Content-Type", "video/mp4")
		base := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fmt.Sprintf("%s %s +%ds.mp4", c.Name, base, from)}))
		cmd.Stdout = w
		if err := cmd.Run(); err != nil && r.Context().Err() == nil {
			slog.Warn("clip export failed", "err", err)
		}
	})
	// How a segment reaches the browser (hevc=1/av1=1: codecs it decodes).
	s.admin("GET /api/nvr/cameras/{id}/recordings/{name}/info", func(r *http.Request) (any, error) {
		f, info, _, err := s.segmentFile(r)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		pr, err := nvr.ProbeSegment(r.Context(), f, info)
		if err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: "unreadable recording"}
		}
		plan, err := mediaplay.Decide(pr, info.Name(), mediaplay.CapsFrom(r), -1)
		if err != nil || plan.Video == nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: "the recording has no video"}
		}
		audio := ""
		if plan.Audio != nil {
			audio = plan.Audio.Codec
		}
		return map[string]any{"video": plan.Video.Codec, "audio": audio, "duration": pr.Duration, "mode": plan.Mode, "direct": plan.Mode == mediaplay.Native}, nil
	})
	// A segment for the browser: the file itself when it plays as it is,
	// else fragmented MP4 (remux or H.264 transcode) from ?start= seconds.
	s.adminRaw("GET /api/nvr/cameras/{id}/recordings/{name}/play", func(w http.ResponseWriter, r *http.Request) {
		f, info, _, err := s.segmentFile(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer f.Close()
		if v := r.URL.Query().Get("start"); v != "" {
			if x, err := strconv.ParseFloat(v, 64); err != nil || x < 0 || x > 24*3600 {
				writeErr(w, badRequest("invalid start"))
				return
			}
		}
		pr, err := nvr.ProbeSegment(r.Context(), f, info)
		if err != nil {
			writeErr(w, &Error{Status: http.StatusUnprocessableEntity, Msg: "unreadable recording"})
			return
		}
		plan, err := mediaplay.Decide(pr, info.Name(), mediaplay.CapsFrom(r), -1)
		if err != nil {
			writeErr(w, &Error{Status: http.StatusUnprocessableEntity, Msg: "the recording has no video"})
			return
		}
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		if plan.Mode == mediaplay.Native {
			w.Header().Set("Content-Type", plan.Type)
			http.ServeContent(w, r, info.Name(), info.ModTime(), f)
			return
		}
		err = mediaplay.Default.Stream(w, r, f, plan, mediaplay.StartFrom(r))
		switch {
		case errors.Is(err, mediaplay.ErrBusy):
			writeErr(w, &Error{Status: http.StatusServiceUnavailable, Msg: err.Error()})
		case err != nil:
			slog.Warn("recording playback failed", "file", f.Name(), "err", err)
			w.Header().Del("Content-Type")
			writeErr(w, &Error{Status: http.StatusUnprocessableEntity, Msg: "this recording cannot be converted for the browser"})
		}
	})
	s.admin("POST /api/nvr/cleanup", func(r *http.Request) (any, error) {
		return map[string]int{"deleted": s.nvrRetention()}, nil
	})

	// ---- Frigate ----
	s.admin("GET /api/nvr/frigate/config", func(r *http.Request) (any, error) {
		o := s.frigateOptions(s.nvrState())
		return map[string]any{"config": nvr.FrigateConfig(o), "detector": o.Detector}, nil
	})
	s.admin("PUT /api/nvr/frigate", func(r *http.Request) (any, error) {
		var req store.FrigateState
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		switch req.Detector {
		case "", "auto":
			req.Detector = ""
		case "cpu", "openvino", "edgetpu":
		default:
			return nil, badRequest("unknown detector")
		}
		known := map[string]bool{}
		for _, o := range nvr.FrigateObjects {
			known[o] = true
		}
		for _, o := range req.Objects {
			if !known[o] {
				return nil, badRequest("unknown object: " + o)
			}
		}
		if req.NotifyMinutes < 0 || req.NotifyMinutes > 1440 {
			return nil, badRequest("invalid notification interval")
		}
		if err := s.store.Update(func(st *store.State) error { st.NVR.Frigate = req; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "nvr", "Changed the Frigate options")
		s.frigateApply(userOf(r))
		return nil, nil
	})
	s.admin("POST /api/nvr/frigate/install", func(r *http.Request) (any, error) {
		if err := s.requireDocker(); err != nil {
			return nil, err
		}
		n := s.nvrState()
		folder, err := nvrFolder(n.Folder)
		if err != nil {
			return nil, badRequest("choose the recordings folder first")
		}
		s.audit(r, "apps", "Installing Frigate")
		return s.jobs.Start("app", "Install Frigate", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			cams.frigateMu.Lock()
			defer cams.frigateMu.Unlock()
			return s.frigateDeploy(ctx, j, folder, true)
		}), nil
	})
}

// ---- Frigate ----

func frigateInstalled() bool {
	b, err := os.ReadFile(filepath.Join(docker.StacksDir, docker.FrigateStack, ".strata-app"))
	return err == nil && strings.TrimSpace(string(b)) == "frigate"
}

func (s *Server) frigateOptions(n store.NVR) nvr.FrigateOptions {
	names := frigateNames(n)
	o := nvr.FrigateOptions{Detector: nvr.ResolveDetector(n.Frigate.Detector, nvr.IntelGPU(), nvr.CoralUSB()), Objects: n.Frigate.Objects}
	if len(o.Objects) == 0 {
		o.Objects = []string{"person", "car"}
	}
	for _, c := range n.Cameras {
		if c.Enabled && c.Engine == "frigate" {
			o.Cameras = append(o.Cameras, nvr.FrigateCamera{Name: names[c.ID], Main: c.URL, Sub: c.SubURL, User: c.Username, Pass: c.Password, KeepDays: c.KeepDays})
		}
	}
	return o
}

func (s *Server) frigateView(ctx context.Context, n store.NVR) map[string]any {
	o := s.frigateOptions(n)
	v := map[string]any{
		"installed": frigateInstalled(), "docker": docker.Available(), "detector": n.Frigate.Detector, "resolved": o.Detector,
		"intelGpu": nvr.IntelGPU(), "coral": nvr.CoralUSB(), "objects": o.Objects, "allObjects": nvr.FrigateObjects,
		"notify": n.Frigate.Notify, "notifyMinutes": n.Frigate.NotifyMinutes, "cameras": len(o.Cameras),
	}
	if frigateInstalled() {
		v["running"] = docker.StackRunning(ctx, docker.FrigateStack)
		v["webPort"] = docker.EnvValue(docker.FrigateStack, "WEB_PORT")
		v["mediaDir"] = docker.EnvValue(docker.FrigateStack, "MEDIA_DIR")
	}
	cams.frigateMu.Lock()
	if cams.frigateErr != "" {
		v["error"] = cams.frigateErr
	}
	cams.frigateMu.Unlock()
	return v
}

// frigateWrite writes config.yml and the credential files.
func (s *Server) frigateWrite(configDir string) error {
	o := s.frigateOptions(s.nvrState())
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(nvr.FrigateSecretsDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(nvr.FrigateSecretsDir, 0o700)
	secrets := nvr.FrigateSecrets(o)
	old, _ := os.ReadDir(nvr.FrigateSecretsDir)
	for _, e := range old {
		if _, keep := secrets[e.Name()]; !keep && strings.HasPrefix(e.Name(), "FRIGATE_") {
			_ = os.Remove(filepath.Join(nvr.FrigateSecretsDir, e.Name()))
		}
	}
	for k, v := range secrets {
		if err := sys.WriteFileAtomic(filepath.Join(nvr.FrigateSecretsDir, k), []byte(v), 0o600); err != nil {
			return err
		}
	}
	return sys.WriteFileAtomic(filepath.Join(configDir, "config.yml"), []byte(nvr.FrigateConfig(o)), 0o644)
}

// frigateDeploy writes the stack (devices for this hardware), the
// configuration, and starts or restarts Frigate. An App Center install keeps
// its folders and ports.
func (s *Server) frigateDeploy(ctx context.Context, j *jobs.Job, folder string, up bool) error {
	app := docker.FindApp("frigate")
	if app == nil {
		return errors.New("frigate is not in the catalog")
	}
	values := map[string]string{
		"WEB_PORT": "8971", "API_PORT": "5005",
		"CONFIG_DIR": filepath.Join(folder, "frigate", "config"), "MEDIA_DIR": filepath.Join(folder, "frigate", "media"),
	}
	if frigateInstalled() {
		for k := range values {
			if v := docker.EnvValue(docker.FrigateStack, k); v != "" {
				values[k] = v
			}
		}
	}
	env, dirs, err := app.RenderEnv(values)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	o := s.frigateOptions(s.nvrState())
	var devices []string
	if gpu := nvr.IntelGPU(); gpu != "" {
		devices = append(devices, gpu)
		j.Logf("Intel GPU %s passed through (video decoding, OpenVINO)", gpu)
	}
	if o.Detector == "edgetpu" {
		devices = append(devices, "/dev/bus/usb")
		j.Logf("Coral USB accelerator passed through")
	}
	j.Logf("Detector: %s, %d camera(s)", o.Detector, len(o.Cameras))
	if err := s.frigateWrite(values["CONFIG_DIR"]); err != nil {
		return err
	}
	if err := docker.SaveStack(ctx, docker.FrigateStack, docker.FrigateCompose(devices, 64+32*len(o.Cameras)), env, "frigate"); err != nil {
		return err
	}
	action := "restart"
	if up {
		action = "up"
		j.Logf("Deploying Frigate (large image: the first download takes a while)...")
	}
	return docker.StackAction(ctx, docker.FrigateStack, action, j)
}

// frigateApply regenerates Frigate's configuration and restarts it, when installed.
func (s *Server) frigateApply(user string) {
	if !frigateInstalled() || !docker.Available() {
		return
	}
	s.jobs.Start("app", "Update the Frigate configuration", user, func(ctx context.Context, j *jobs.Job) error {
		cams.frigateMu.Lock()
		defer cams.frigateMu.Unlock()
		dir := docker.EnvValue(docker.FrigateStack, "CONFIG_DIR")
		if !filepath.IsAbs(dir) {
			return errors.New("the Frigate stack has no config folder")
		}
		if err := s.frigateWrite(dir); err != nil {
			return err
		}
		if !docker.StackRunning(ctx, docker.FrigateStack) {
			return nil
		}
		return docker.StackAction(ctx, docker.FrigateStack, "restart", j)
	})
}

// frigateAPI is the address of Frigate's API on the loopback (its internal
// port, without sign-in: StrataOS checks the administrator itself).
func frigateAPI() (string, bool) {
	if !frigateInstalled() {
		return "", false
	}
	port, err := strconv.Atoi(docker.EnvValue(docker.FrigateStack, "API_PORT"))
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), true
}

// frigateCamera returns a camera recorded by Frigate and its name there.
func (s *Server) frigateCamera(id string) (*store.Camera, string, error) {
	c, n, err := s.nvrCamera(id)
	if err != nil {
		return nil, "", err
	}
	if c.Engine != "frigate" {
		return nil, "", badRequest("this camera is not recorded by Frigate")
	}
	return c, frigateNames(n)[c.ID], nil
}

// frigateRoutes: the recordings of Frigate (events with a clip), listed in
// the Recordings tab and played through StrataOS.
func (s *Server) frigateRoutes() {
	s.admin("GET /api/nvr/cameras/{id}/frigate/events", func(r *http.Request) (any, error) {
		_, name, err := s.frigateCamera(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		day := r.URL.Query().Get("day")
		if day == "" {
			day = time.Now().Format("2006-01-02")
		}
		from, err := time.ParseInLocation("2006-01-02", day, time.Local)
		if err != nil {
			return nil, badRequest("invalid day")
		}
		out := map[string]any{"installed": frigateInstalled(), "day": day, "events": []any{}}
		if p := docker.EnvValue(docker.FrigateStack, "WEB_PORT"); frigateInstalled() && p != "" {
			out["webPort"] = p
		}
		base, ok := frigateAPI()
		if !ok {
			return out, nil
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+nvr.FrigateEventsPath(name, float64(from.Unix()), float64(from.AddDate(0, 0, 1).Unix())), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out["error"] = "Frigate does not answer: is it running?"
			return out, nil
		}
		defer resp.Body.Close()
		var events []nvr.FrigateEvent
		if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&events) != nil {
			out["error"] = fmt.Sprintf("unexpected answer from Frigate (HTTP %d)", resp.StatusCode)
			return out, nil
		}
		type ev struct {
			ID    string     `json:"id"`
			Label string     `json:"label"`
			Score float64    `json:"score"`
			Start time.Time  `json:"start"`
			End   *time.Time `json:"end,omitempty"`
		}
		list := []ev{}
		for _, e := range nvr.FrigateClips(events, name) {
			v := ev{ID: e.ID, Label: e.Label, Score: e.Score(), Start: time.UnixMilli(int64(e.StartTime * 1000))}
			if e.EndTime > 0 {
				t := time.UnixMilli(int64(e.EndTime * 1000))
				v.End = &t
			}
			list = append(list, v)
		}
		out["events"] = list
		return out, nil
	})
	// The clip (MP4) or thumbnail of an event, read from Frigate's API.
	s.adminRaw("GET /api/nvr/cameras/{id}/frigate/events/{eid}/{file}", func(w http.ResponseWriter, r *http.Request) {
		c, name, err := s.frigateCamera(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		eid, file := r.PathValue("eid"), r.PathValue("file")
		if !nvr.ValidFrigateEventID(eid) || (file != "clip.mp4" && file != "thumbnail.jpg") {
			writeErr(w, notFound("event not found"))
			return
		}
		base, ok := frigateAPI()
		if !ok {
			writeErr(w, notFound("Frigate is not installed"))
			return
		}
		// The event must belong to this camera.
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/events/"+eid, nil)
		resp, err := http.DefaultClient.Do(req)
		var e nvr.FrigateEvent
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
			}
			resp.Body.Close()
		}
		cancel()
		if e.ID != eid || e.Camera != name {
			writeErr(w, notFound("event not found"))
			return
		}
		req, _ = http.NewRequestWithContext(r.Context(), http.MethodGet, base+"/api/events/"+eid+"/"+file, nil)
		if rg := r.Header.Get("Range"); rg != "" {
			req.Header.Set("Range", rg)
		}
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			writeErr(w, &Error{Status: http.StatusBadGateway, Msg: "Frigate does not answer: is it running?"})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			writeErr(w, &Error{Status: http.StatusBadGateway, Msg: fmt.Sprintf("unexpected answer from Frigate (HTTP %d)", resp.StatusCode)})
			return
		}
		h := w.Header()
		for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
			if v := resp.Header.Get(k); v != "" {
				h.Set(k, v)
			}
		}
		if file == "clip.mp4" {
			h.Set("Content-Type", "video/mp4")
		} else {
			h.Set("Content-Type", "image/jpeg")
		}
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		if r.URL.Query().Get("download") == "1" {
			at := time.UnixMilli(int64(e.StartTime * 1000)).Format("20060102-150405")
			h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": c.Name + " " + at + " " + e.Label + filepath.Ext(file)}))
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

// frigatePoll reads Frigate's new events and notifies them.
func (s *Server) frigatePoll() {
	n := s.nvrState()
	if !n.Frigate.Notify || !frigateInstalled() {
		cams.filter = nil
		return
	}
	base, ok := frigateAPI()
	if !ok {
		return
	}
	if cams.filter == nil {
		cams.filter = nvr.NewEventFilter(time.Now())
	}
	f := cams.filter
	url := fmt.Sprintf("%s/api/events?limit=100&include_thumbnails=0&after=%.3f", base, f.After())
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return // Frigate stopped or starting
	}
	defer resp.Body.Close()
	var events []nvr.FrigateEvent
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&events) != nil {
		return
	}
	names := map[string]string{}
	ids := frigateNames(n)
	for _, c := range n.Cameras {
		names[ids[c.ID]] = c.Name
	}
	every := time.Duration(max(n.Frigate.NotifyMinutes, 0)) * time.Minute
	if every == 0 {
		every = 10 * time.Minute
	}
	for _, e := range f.New(events) {
		if !f.ShouldNotify(e.Camera, time.Now(), every) {
			continue
		}
		cam := names[e.Camera]
		if cam == "" {
			cam = e.Camera
		}
		level := "info"
		if e.Label == "person" {
			level = "warning"
		}
		msg := fmt.Sprintf("Frigate detected %s on camera %s (%d%%)", e.Label, cam, int(e.Score()*100))
		s.store.Log(level, "nvr", "", msg)
		s.notify(level, "Surveillance event", msg)
	}
}
