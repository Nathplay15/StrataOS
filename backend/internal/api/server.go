// Package api is the HTTP layer of StrataOS.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"strataos/internal/certs"
	"strataos/internal/docker"
	"strataos/internal/jobs"
	"strataos/internal/network"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func badRequest(msg string) error { return &Error{Status: http.StatusBadRequest, Msg: msg} }
func notFound(msg string) error   { return &Error{Status: http.StatusNotFound, Msg: msg} }

type Server struct {
	// bg tracks notifications being sent (tests wait for them before their data folder goes).
	bg sync.WaitGroup

	store    *store.Store
	jobs     *jobs.Manager
	monitor  *system.Monitor
	docker   *docker.Client
	sessions *sessions
	limiter  *limiter
	web      fs.FS
	version  string
	dataDir  string
	mux      *http.ServeMux
	certs    *certs.Manager
	// httpPort is the plain HTTP port, given to media receivers in stream URLs.
	httpPort string
	// diskAlerts: failing or weakening disks found by the last health check.
	diskAlerts atomic.Pointer[[]alert]
}

type Config struct {
	Store   *store.Store
	Web     fs.FS
	Version string
	DataDir string
	Certs   *certs.Manager
	// HTTPPort is the port of the plain HTTP listener ("" = 7070).
	HTTPPort string
}

func New(c Config) *Server {
	s := &Server{
		store: c.Store, jobs: jobs.NewManager(), monitor: system.NewMonitor(), docker: docker.New(),
		sessions: newSessions(filepath.Join(c.DataDir, "sessions.json")), limiter: &limiter{hits: map[string][]time.Time{}},
		web: c.Web, version: c.Version, dataDir: c.DataDir, mux: http.NewServeMux(), certs: c.Certs, httpPort: c.HTTPPort,
	}
	if s.httpPort == "" {
		s.httpPort = "7070"
	}
	s.jobs.OnFinish = func(j jobs.Snapshot) {
		level := "info"
		msg := j.Title + ": finished"
		if j.Status == jobs.Failed {
			level = "error"
			msg = j.Title + ": failed — " + j.Error
		}
		s.store.Log(level, "job", j.User, msg)
		if level == "error" {
			s.notify("error", "Task failed", msg)
		}
	}
	network.OnRollback = func(reason string) {
		msg := "Network change rolled back (" + reason + "): the previous configuration is active again"
		s.store.Log("warning", "network", "", msg)
		s.notify("warning", "Network restored", msg)
	}
	network.Recover()
	s.routes()
	go s.healthWatcher()
	go s.schedulerLoop()
	go s.haLoop()
	go s.haContinuousLoop()
	go s.ransomLoop()
	go s.ransomFileServicesLoop()
	go s.trapLoop()
	go s.ddnsLoop()
	go s.vpnLoop()
	go s.vpnDockerEvents()
	go s.photoLoop()
	go s.shareQuotaLoop()
	go s.energyLoop()
	go s.versionLinksLoop()
	go s.lockedSharesNotice()
	go s.guardLoop()
	go s.iscsiLoop()
	go s.configAutoLoop()
	go s.hardwareLoop()
	go s.vmAutostartLoop()
	go s.lxcAutostartLoop()
	go s.nvrLoop()
	go s.fleetResume()
	go s.autoUpdateLoop()
	go s.consoleLoop()
	go s.mobileUploadsLoop()
	go s.rpiKernelResume()
	go s.cephLoop()
	go s.dockerUpdatesLoop() // Android app v2 (part A): container updates
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Referrer-Policy", "same-origin")
	s.mux.ServeHTTP(w, r)
}

// h adapts a handler returning (value, error) into JSON responses.
type handler func(r *http.Request) (any, error)

func (s *Server) wrap(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		v, err := h(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		writeJSON(w, http.StatusOK, v)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var e *Error
	var de *docker.APIError
	var ce *sys.CmdError
	switch {
	case errors.As(err, &e):
		status = e.Status
	case errors.As(err, &de):
		status = de.Status
		if status < 400 || status > 599 {
			status = 500
		}
	case errors.As(err, &ce):
		status = http.StatusUnprocessableEntity
	}
	if status >= 500 {
		slog.Error("request failed", "err", err)
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && err != io.EOF {
		return badRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

func qInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Route registration helpers.
func (s *Server) public(pattern string, h handler) { s.mux.Handle(pattern, s.wrap(h)) }
func (s *Server) user(pattern string, h handler) {
	s.mux.Handle(pattern, s.authenticate(s.wrap(h), false))
}
func (s *Server) admin(pattern string, h handler) {
	s.mux.Handle(pattern, s.authenticate(s.wrap(h), true))
}
func (s *Server) adminRaw(pattern string, h http.HandlerFunc) {
	s.mux.Handle(pattern, s.authenticate(h, true))
}
func (s *Server) userRaw(pattern string, h http.HandlerFunc) {
	s.mux.Handle(pattern, s.authenticate(h, false))
}

// audit records an action performed by the current user.
func (s *Server) audit(r *http.Request, category, msg string) {
	s.store.Log("info", category, userOf(r), msg)
}

func (s *Server) routes() {
	s.authRoutes()
	s.systemRoutes()
	s.storageRoutes()
	s.userRoutes()
	s.shareRoutes()
	s.dockerRoutes()
	s.customAppRoutes()
	s.fileRoutes()
	s.editorRoutes()
	s.viewerRoutes()
	s.officeRoutes()
	s.scheduleRoutes()
	s.backupRoutes()
	s.timeMachineRoutes()
	s.quotaRoutes()
	s.twoFactorRoutes()
	s.passkeyRoutes()
	s.certRoutes()
	s.upsRoutes()
	s.vmRoutes()
	s.lxcRoutes()
	s.cloudRoutes()
	s.davRoutes()
	s.fileServiceRoutes()
	s.directoryRoutes()
	s.antivirusRoutes()
	s.haRoutes()
	s.ransomRoutes()
	s.vpnRoutes()
	s.photoRoutes()
	s.restoreRoutes()
	s.updateRoutes()
	s.reportRoutes()
	s.linkRoutes()
	s.albumRoutes()
	s.energyRoutes()
	s.versionRoutes()
	s.guardRoutes()
	s.vaultRoutes()
	s.metricsRoutes()
	s.fleetRoutes()
	s.remoteRoutes()
	s.iscsiRoutes()
	s.configRoutes()
	s.storageToolsRoutes()
	s.uninstallRoutes()
	s.hardwareRoutes()
	s.mediaRoutes()
	s.nvrRoutes()
	s.consoleRoutes()
	s.mobileRoutes()
	s.mobileUploadRoutes()
	s.syncRoutes()
	s.rpiKernelRoutes()
	// Android app v2 (part A): file manager additions, container updates.
	s.fileMoreRoutes()
	s.dockerUpdateRoutes()
	s.clusterRoutes()
	s.cephRoutes()
	s.mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, notFound("unknown endpoint"))
	}))
	s.mux.Handle("/", http.HandlerFunc(s.static))
}

// static serves the embedded single page application.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if s.web == nil {
		http.Error(w, "web UI not built", http.StatusNotFound)
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := s.web.Open(p); err == nil {
		st, _ := f.Stat()
		f.Close()
		if st != nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.web, p)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.web, "index.html")
}
