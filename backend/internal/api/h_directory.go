package api

import (
	"context"
	"net/http"
	"time"

	"strataos/internal/directory"
	"strataos/internal/jobs"
	"strataos/internal/store"
)

func maskDirectory(d store.Directory) store.Directory {
	if d.BindPassword != "" {
		d.BindPassword = masked
	}
	return d
}

func (s *Server) directoryRoutes() {
	s.admin("GET /api/directory", func(r *http.Request) (any, error) {
		var d store.Directory
		s.store.Read(func(st *store.State) { d = st.Directory })
		out := map[string]any{"config": maskDirectory(d), "adInstalled": directory.ADInstalled(), "ldapInstalled": directory.LDAPInstalled(), "joined": !d.JoinedAt.IsZero()}
		if !d.JoinedAt.IsZero() {
			out["status"] = directory.CheckStatus(d)
		}
		return out, nil
	})

	s.admin("POST /api/directory/join", func(r *http.Request) (any, error) {
		var req struct {
			store.Directory
			Admin    string `json:"admin"`
			Password string `json:"password"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		c := req.Directory
		var current store.Directory
		s.store.Read(func(st *store.State) { current = st.Directory })
		if !current.JoinedAt.IsZero() {
			return nil, badRequest("leave the current domain first")
		}
		if c.BindPassword == masked {
			c.BindPassword = current.BindPassword
		}
		if err := directory.Validate(&c); err != nil {
			return nil, badRequest(err.Error())
		}
		if c.Type == "ad" && !directory.ADInstalled() || c.Type == "ldap" && !directory.LDAPInstalled() {
			return nil, badRequest("install the directory components first")
		}
		admin, password := req.Admin, req.Password
		s.audit(r, "directory", "Joining "+c.Type+" "+c.Realm+c.URI)
		return s.jobs.Start("directory", "Jonction à l'annuaire", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			c.JoinedAt = time.Now()
			if err := s.store.Update(func(st *store.State) error { st.Directory = c; return nil }); err != nil {
				return err
			}
			var err error
			if c.Type == "ad" {
				err = directory.JoinAD(ctx, c, admin, password, s.applyShares, j.Logf)
			} else {
				err = directory.ApplyLDAP(c, true)
			}
			if err != nil {
				// Back to a standalone server.
				_ = s.store.Update(func(st *store.State) error { st.Directory = store.Directory{}; return nil })
				if c.Type == "ad" {
					_ = directory.LeaveAD(context.Background(), c, "", "", s.applyShares)
				} else {
					_ = directory.ApplyLDAP(c, false)
				}
				return err
			}
			s.store.Log("info", "directory", userOf(r), "Joined "+c.Type+" directory "+c.Realm+c.URI)
			return nil
		}), nil
	})

	s.admin("POST /api/directory/leave", func(r *http.Request) (any, error) {
		var req struct {
			Admin    string `json:"admin"`
			Password string `json:"password"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var c store.Directory
		if err := s.store.Update(func(st *store.State) error { c = st.Directory; st.Directory = store.Directory{}; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "directory", "Left directory "+c.Realm+c.URI)
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		var err error
		if c.Type == "ad" {
			err = directory.LeaveAD(ctx, c, req.Admin, req.Password, s.applyShares)
		} else {
			err = directory.ApplyLDAP(c, false)
		}
		if err != nil {
			return map[string]string{"warning": err.Error()}, nil
		}
		return nil, nil
	})

	s.admin("GET /api/directory/search", func(r *http.Request) (any, error) {
		var c store.Directory
		s.store.Read(func(st *store.State) { c = st.Directory })
		if c.JoinedAt.IsZero() {
			return map[string][]string{"users": {}, "groups": {}}, nil
		}
		q := r.URL.Query().Get("q")
		users, groups := directory.Search(c, false, q), directory.Search(c, true, q)
		if users == nil {
			users = []string{}
		}
		if groups == nil {
			groups = []string{}
		}
		return map[string][]string{"users": users, "groups": groups}, nil
	})
}
