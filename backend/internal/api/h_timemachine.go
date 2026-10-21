package api

// Time Machine section of the Backup page: shares that receive Mac backups,
// the Macs found on them, their size limit, and a quick destination creation.

import (
	"net/http"
	"strings"
	"time"

	"strataos/internal/backup"
	"strataos/internal/crypt"
	"strataos/internal/shares"
	"strataos/internal/store"
)

type tmShare struct {
	Name        string            `json:"name"`
	Path        string            `json:"path"`
	MaxGB       float64           `json:"maxGb"`
	Permissions map[string]string `json:"permissions"`
	Locked      bool              `json:"locked"`
	Used        int64             `json:"used"`
	Backups     []backup.TMBackup `json:"backups"`
}

func (s *Server) timeMachineRoutes() {
	s.admin("GET /api/backups/timemachine", func(r *http.Request) (any, error) {
		var list []store.Share
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				if sh.TimeMachine && sh.SMB {
					list = append(list, *sh)
				}
			}
		})
		out := []tmShare{}
		for _, sh := range list {
			v := tmShare{Name: sh.Name, Path: sh.Path, MaxGB: sh.TimeMachineMaxGB, Permissions: sh.Permissions, Backups: []backup.TMBackup{}}
			v.Locked = sh.Encrypted && !crypt.Mounted(sh.Path)
			if !v.Locked {
				v.Backups = backup.TimeMachineBackups(sh.Path)
			}
			for _, b := range v.Backups {
				v.Used += b.Size
			}
			out = append(out, v)
		}
		return out, nil
	})

	s.admin("PUT /api/backups/timemachine/{name}", func(r *http.Request) (any, error) {
		var req struct {
			MaxGB float64 `json:"maxGb"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.MaxGB < 0 || req.MaxGB > 1e6 {
			return nil, badRequest("invalid size limit")
		}
		name := r.PathValue("name")
		err := s.store.Update(func(st *store.State) error {
			sh := st.Share(name)
			if sh == nil || !sh.TimeMachine {
				return notFound("Time Machine share not found")
			}
			sh.TimeMachineMaxGB = req.MaxGB
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "backup", "Time Machine size limit of "+name+" set")
		return nil, s.applyShares()
	})

	// Creates a folder and its SMB share set up for Time Machine.
	s.admin("POST /api/backups/timemachine", func(r *http.Request) (any, error) {
		var req struct {
			Name  string   `json:"name"`
			Path  string   `json:"path"`
			MaxGB float64  `json:"maxGb"`
			Users []string `json:"users"` // users and @groups allowed to back up
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		sh := store.Share{
			Name: strings.TrimSpace(req.Name), SMB: true, Browseable: true, TimeMachine: true,
			TimeMachineMaxGB: req.MaxGB, Description: "Time Machine", Permissions: map[string]string{},
		}
		if !shareNameRe.MatchString(sh.Name) || reservedShares[strings.ToLower(sh.Name)] {
			return nil, badRequest("invalid share name")
		}
		p, err := shares.ValidatePath(req.Path)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		sh.Path = p
		if !s.pathOnVolume(p) {
			return nil, badRequest("the folder must be inside a mounted volume")
		}
		if req.MaxGB < 0 || req.MaxGB > 1e6 {
			return nil, badRequest("invalid size limit")
		}
		if len(req.Users) == 0 {
			return nil, badRequest("choose who can back up to this destination")
		}
		for _, u := range req.Users {
			sh.Permissions[u] = "rw"
		}
		if err := validPerms(sh.Permissions); err != nil {
			return nil, err
		}
		var conflict error
		s.store.Read(func(st *store.State) { conflict = shareConflict(st, sh.Name, true) })
		if conflict != nil {
			return nil, conflict
		}
		if err := shares.Prepare(&sh); err != nil {
			return nil, badRequest(err.Error())
		}
		err = s.store.Update(func(st *store.State) error {
			if err := shareConflict(st, sh.Name, true); err != nil {
				return err
			}
			sh.CreatedAt = time.Now()
			c := sh
			st.Shares = append(st.Shares, &c)
			return nil
		})
		if err != nil {
			return nil, err
		}
		if err := s.applyShares(); err != nil {
			return nil, err
		}
		s.audit(r, "backup", "Created Time Machine destination "+sh.Name)
		return sh, nil
	})
}
