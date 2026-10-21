package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/confbackup"
	"strataos/internal/docker"
	"strataos/internal/ha"
	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/system"
)

// exportConfig builds the configuration document of this NAS; with a
// passphrase, the secrets go into its encrypted section.
func (s *Server) exportConfig(withSecrets bool, passphrase string) (*confbackup.Document, error) {
	if withSecrets && len(passphrase) < confbackup.MinPassphrase {
		return nil, badRequest(fmt.Sprintf("the passphrase must be at least %d characters", confbackup.MinPassphrase))
	}
	cfg, sec := s.currentConfig(withSecrets)
	host, _ := hostname()
	d := &confbackup.Document{Format: confbackup.Format, Version: confbackup.Version, StrataOS: s.version, Created: time.Now().UTC().Truncate(time.Second), Hostname: host, Config: cfg}
	if withSecrets {
		sealed, err := confbackup.Seal(sec, passphrase)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		d.Secrets = sealed
	}
	return d, nil
}

// currentConfig reads the configuration from the store and the system. The
// password hashes of the accounts are only read when secrets are wanted.
func (s *Server) currentConfig(withSecrets bool) (confbackup.Config, confbackup.Secrets) {
	src := confbackup.Source{Now: time.Now(), Linux: map[string]confbackup.LinuxUser{}, SMB: map[string]string{}}
	host, _ := hostname()
	src.System = &confbackup.System{Hostname: host}
	if l, err := os.Readlink("/etc/localtime"); err == nil {
		src.System.Timezone = strings.TrimPrefix(l, "/usr/share/zoneinfo/")
	}

	var local []string
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if u.Source == "" {
				local = append(local, u.Username)
			}
		}
	})
	src.Groups = []confbackup.Group{}
	if gl, err := accounts.ListGroups(); err == nil {
		for _, g := range gl {
			if !g.System {
				src.Groups = append(src.Groups, confbackup.Group{Name: g.Name, GID: g.GID, Members: g.Members})
			}
		}
	}
	shadow := map[string]string{}
	if withSecrets && len(local) > 0 {
		if acc, err := ha.ExportAccounts(local, nil); err == nil {
			for _, a := range acc.Users {
				shadow[a.Name] = a.Shadow
			}
			for _, l := range strings.Split(acc.SMB, "\n") {
				if n, _, ok := strings.Cut(l, ":"); ok {
					src.SMB[n] = l
				}
			}
		}
	}
	for _, name := range local {
		uid, gid, err := accounts.IDs(name)
		if err != nil {
			continue
		}
		src.Linux[name] = confbackup.LinuxUser{UID: uid, GID: gid, Groups: accounts.UserGroups(name), Shadow: shadow[name]}
	}

	src.Quotas = []confbackup.Quota{}
	for _, t := range s.quotaTargets() {
		if !t.Supported || !t.Enabled {
			continue
		}
		for _, name := range local {
			if _, limit := storage.GetUserQuota(t, name); limit > 0 {
				src.Quotas = append(src.Quotas, confbackup.Quota{User: name, Kind: t.Kind, MountPoint: t.MountPoint, Limit: limit})
			}
		}
	}
	if storage.ZFSAvailable() {
		pools, _ := storage.ListZPools(false)
		for _, p := range pools {
			src.Pools = append(src.Pools, confbackup.Volume{Name: p.Name, Kind: "zfs", FSType: "zfs", MountPoint: storage.MountRoot + "/" + p.Name})
		}
	}
	for _, name := range docker.StackNames() {
		dir := filepath.Join(docker.StacksDir, name)
		yml, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
		if err != nil {
			continue
		}
		env, _ := os.ReadFile(filepath.Join(dir, ".env"))
		app, _ := os.ReadFile(filepath.Join(dir, ".strata-app"))
		src.Stacks = append(src.Stacks, confbackup.Stack{Name: name, App: strings.TrimSpace(string(app)), Compose: string(yml), Env: string(env)})
	}

	var cfg confbackup.Config
	var sec confbackup.Secrets
	s.store.Read(func(st *store.State) {
		src.State = *st
		if st.Network != nil {
			n := *st.Network
			src.Network = &n
		}
		cfg, sec = confbackup.Build(src)
	})
	return cfg, sec
}

// configEnv describes this NAS to the import plan.
func (s *Server) configEnv(self string, secrets bool) confbackup.Env {
	var vols []store.Volume
	var dir string
	var prevNet []string
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			vols = append(vols, *v)
		}
		dir = st.Directory.Type
		if st.Network != nil {
			for _, b := range st.Network.Bonds {
				prevNet = append(prevNet, b.Members...)
			}
		}
	})
	quota := map[string]bool{}
	for _, t := range s.quotaTargets() {
		if t.Supported && t.Enabled {
			quota[t.MountPoint] = true
		}
	}
	ifaces := prevNet
	for _, i := range system.GetNetwork().Interfaces {
		if i.Kind == "physical" && !strings.HasPrefix(i.Name, "bond") {
			ifaces = append(ifaces, i.Name)
		}
	}
	return confbackup.Env{
		Self: self, Secrets: secrets, Docker: docker.Available(), Directory: dir, Interfaces: ifaces,
		OnVolume: s.pathOnVolume,
		Exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil && strings.HasPrefix(filepath.Clean(p), storage.MountRoot+"/")
		},
		VolumeID: func(mount string) string {
			for _, v := range vols {
				if v.MountPoint == mount && v.FSType == "btrfs" && storage.IsMountpoint(mount) {
					return v.ID
				}
			}
			return ""
		},
		QuotaOn: func(mount string) bool { return quota[mount] },
	}
}

// importRequest is what the import dialog sends (the whole file each time:
// nothing is kept on the server between the preview and the import).
type importRequest struct {
	Document   json.RawMessage   `json:"document"`
	Passphrase string            `json:"passphrase"`
	PathMap    map[string]string `json:"pathMap"`
	Sections   []string          `json:"sections"`
}

type importPlan struct {
	doc        *confbackup.Document
	plain      *confbackup.Config
	full       *confbackup.Config
	secrets    *confbackup.Secrets
	env        confbackup.Env
	sections   []confbackup.Section
	pathMapped map[string]string
}

func (s *Server) planImport(req importRequest, self string) (*importPlan, error) {
	doc, err := confbackup.Parse(req.Document)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	p := &importPlan{doc: doc, pathMapped: req.PathMap}
	// The volume IDs of this NAS are needed before the plan (btrfs tasks).
	env := s.configEnv(self, false)
	p.plain, p.full, p.secrets, err = doc.Prepare(req.Passphrase, req.PathMap, env.VolumeID)
	if errors.Is(err, confbackup.ErrPassphrase) {
		s.store.Log("warning", "config", self, "Configuration import: wrong passphrase")
		return nil, badRequest(err.Error())
	}
	if err != nil {
		return nil, badRequest(err.Error())
	}
	env.Secrets = p.secrets != nil
	p.env = env
	cur, _ := s.currentConfig(false)
	p.sections = confbackup.Plan(p.plain, &cur, env)
	return p, nil
}

type volumeChoice struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	MountPoint string `json:"mountPoint"`
	Found      bool   `json:"found"`
}

func (s *Server) configRoutes() {
	export := func(r *http.Request, withSecrets bool, pass string) (any, error) {
		d, err := s.exportConfig(withSecrets, pass)
		if err != nil {
			return nil, err
		}
		if withSecrets {
			s.audit(r, "config", "Configuration exported with its secrets")
		} else {
			s.audit(r, "config", "Configuration exported")
		}
		return d, nil
	}
	// Without secrets (readable, diffable): GET works with curl.
	s.admin("GET /api/config/export", func(r *http.Request) (any, error) { return export(r, false, "") })
	s.admin("POST /api/config/export", func(r *http.Request) (any, error) {
		var req struct {
			Secrets    bool   `json:"secrets"`
			Passphrase string `json:"passphrase"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return export(r, req.Secrets, req.Passphrase)
	})

	s.admin("POST /api/config/preview", func(r *http.Request) (any, error) {
		var req importRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		p, err := s.planImport(req, userOf(r))
		if err != nil {
			return nil, err
		}
		// Volumes of the file, and those of this NAS they can be mapped to.
		vols := []volumeChoice{}
		for _, v := range p.doc.Config.Volumes {
			vols = append(vols, volumeChoice{Name: v.Name, Kind: v.Kind, MountPoint: v.MountPoint, Found: s.pathOnVolume(v.MountPoint)})
		}
		targets := []volumeChoice{}
		s.store.Read(func(st *store.State) {
			for _, v := range st.Volumes {
				targets = append(targets, volumeChoice{Name: v.Name, Kind: "volume", MountPoint: v.MountPoint})
			}
		})
		if storage.ZFSAvailable() {
			pools, _ := storage.ListZPools(false)
			for _, pl := range pools {
				targets = append(targets, volumeChoice{Name: pl.Name, Kind: "zfs", MountPoint: storage.MountRoot + "/" + pl.Name})
			}
		}
		targets = slices.DeleteFunc(targets, func(v volumeChoice) bool { return !s.pathOnVolume(v.MountPoint) })
		for i := range targets {
			targets[i].Found = true
		}
		return map[string]any{
			"strataos": p.doc.StrataOS, "version": p.doc.Version, "created": p.doc.Created, "hostname": p.doc.Hostname,
			"encrypted": p.doc.Secrets != nil, "secrets": p.secrets != nil,
			"volumes": vols, "targets": targets, "sections": p.sections,
		}, nil
	})

	s.admin("POST /api/config/import", func(r *http.Request) (any, error) {
		var req importRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Sections) == 0 {
			return nil, badRequest("choose at least one section")
		}
		self := userOf(r)
		p, err := s.planImport(req, self)
		if err != nil {
			return nil, err
		}
		ip := r.RemoteAddr
		s.audit(r, "config", "Configuration import from "+p.doc.Hostname+" started")
		return s.jobs.Start("config", "Import the configuration of "+p.doc.Hostname, self, func(ctx context.Context, j *jobs.Job) error {
			return s.applyImport(ctx, j, p, req.Sections, self, ip)
		}), nil
	})

	s.configAutoRoutes()
}

// recorder captures the answer of a replayed request.
type recorder struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (w *recorder) Header() http.Header {
	if w.h == nil {
		w.h = http.Header{}
	}
	return w.h
}
func (w *recorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}
func (w *recorder) WriteHeader(code int) { w.status = code }

// replayer sends requests to the API on behalf of the importing
// administrator, so that imported settings go through exactly the
// validation and side effects of the pages that edit them.
type replayer struct {
	s     *Server
	token string
	ip    string
}

func (s *Server) newReplayer(user, ip string) *replayer {
	return &replayer{s: s, token: s.sessions.create(user, "admin", clientIP(&http.Request{RemoteAddr: ip}), time.Hour), ip: ip}
}

func (rp *replayer) close() { rp.s.sessions.delete(rp.token) }

// call returns the decoded answer, or the error the API gave.
func (rp *replayer) call(method, path string, body any) (map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, "http://strata.internal"+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.RemoteAddr = rp.ip
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Strata", "1")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: rp.token})
	w := &recorder{}
	rp.s.mux.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.body.Bytes(), &out)
	if w.status >= 400 {
		if e, ok := out["error"].(string); ok {
			return nil, errors.New(e)
		}
		return nil, fmt.Errorf("HTTP %d", w.status)
	}
	return out, nil
}

// applicable reports whether an item of a plan is to be applied.
func applicable(it confbackup.Item) bool {
	return it.Action == confbackup.Create || it.Action == confbackup.Update
}

func findItem(p *importPlan, section, key string) *confbackup.Item {
	for i := range p.sections {
		if p.sections[i].ID == section {
			for j := range p.sections[i].Items {
				if p.sections[i].Items[j].Key == key {
					return &p.sections[i].Items[j]
				}
			}
		}
	}
	return nil
}
