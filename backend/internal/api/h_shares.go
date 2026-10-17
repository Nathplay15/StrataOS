package api

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"strataos/internal/shares"
	"strataos/internal/storage"
	"strataos/internal/store"
)

var shareNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _\-.]{0,62}$`)

var reservedShares = map[string]bool{"global": true, "homes": true, "printers": true, "print$": true, "ipc$": true}

// applyShares regenerates Samba and NFS configuration; errors are logged.
func (s *Server) applyShares() error {
	var err error
	s.store.Read(func(st *store.State) { err = shares.Apply(st) })
	var skipped shares.SkippedError
	if errors.As(err, &skipped) {
		// The configuration was applied without these shares: report, don't fail.
		slog.Warn("apply shares", "skipped", err)
		s.store.Log("warning", "shares", "", "Partages non publiés : "+err.Error())
		return nil
	}
	if err != nil {
		slog.Error("apply shares", "err", err)
		s.store.Log("error", "shares", "", "Échec de l'application de la configuration des partages : "+err.Error())
	}
	return err
}

var (
	localNameRe = regexp.MustCompile(`^@?[a-z_][a-z0-9_-]{0,31}$`)
	// DOMAIN\user or @DOMAIN\group (Active Directory names may contain spaces).
	domainNameRe = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9._-]{0,14}\\[^\\/:*?"<>|,@\x00-\x1f]{1,64}$`)
)

func validPerms(p map[string]string) error {
	for who, perm := range p {
		if perm != "rw" && perm != "ro" && perm != "none" {
			return badRequest("invalid permission for " + who)
		}
		if !localNameRe.MatchString(who) && !domainNameRe.MatchString(who) {
			return badRequest("invalid user or group " + who)
		}
	}
	return nil
}

func (s *Server) shareRoutes() {
	s.user("GET /api/shares", func(r *http.Request) (any, error) {
		var out []store.Share
		admin := isAdmin(r)
		me := userOf(r)
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				if admin || sh.Guest || sh.Permissions[me] == "rw" || sh.Permissions[me] == "ro" {
					out = append(out, *sh)
				}
			}
		})
		if out == nil {
			out = []store.Share{}
		}
		return out, nil
	})

	save := func(r *http.Request, create bool) (any, error) {
		var req store.Share
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Name = strings.TrimSpace(req.Name)
		if !create {
			req.Name = r.PathValue("name")
		}
		if !shareNameRe.MatchString(req.Name) || reservedShares[strings.ToLower(req.Name)] {
			return nil, badRequest("invalid share name")
		}
		p, err := shares.ValidatePath(req.Path)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		req.Path = p
		if !s.pathOnVolume(p) {
			return nil, badRequest("the share path must be inside a mounted volume")
		}
		if req.Permissions == nil {
			req.Permissions = map[string]string{}
		}
		if err := validPerms(req.Permissions); err != nil {
			return nil, err
		}
		if err := shares.ValidateNFS(req.NFSClients, req.NFSOptions); err != nil {
			return nil, badRequest(err.Error())
		}
		// Prepare (which refuses symlinked paths) must succeed before the share is
		// stored: a share saved despite an error would be published to Samba/NFS by
		// the next applyShares.
		var conflict error
		s.store.Read(func(st *store.State) { conflict = shareConflict(st, req.Name, create) })
		if conflict != nil {
			return nil, conflict
		}
		if err := shares.Prepare(&req); err != nil {
			return nil, badRequest(err.Error())
		}
		err = s.store.Update(func(st *store.State) error {
			if err := shareConflict(st, req.Name, create); err != nil {
				return err
			}
			existing := st.Share(req.Name)
			if create {
				req.CreatedAt = time.Now()
				c := req
				st.Shares = append(st.Shares, &c)
				return nil
			}
			req.CreatedAt = existing.CreatedAt
			*existing = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		if err := s.applyShares(); err != nil {
			return nil, err
		}
		verb := "Updated"
		if create {
			verb = "Created"
		}
		s.audit(r, "shares", verb+" share "+req.Name)
		return req, nil
	}
	s.admin("POST /api/shares", func(r *http.Request) (any, error) { return save(r, true) })
	s.admin("PUT /api/shares/{name}", func(r *http.Request) (any, error) { return save(r, false) })

	s.admin("DELETE /api/shares/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		err := s.store.Update(func(st *store.State) error {
			if st.Share(name) == nil {
				return notFound("share not found")
			}
			st.Shares = slices.DeleteFunc(st.Shares, func(x *store.Share) bool { return x.Name == name })
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "shares", "Deleted share "+name+" (files kept)")
		return nil, s.applyShares()
	})

	s.admin("GET /api/shares/sessions", func(r *http.Request) (any, error) { return shares.SMBSessions(), nil })
}

// shareConflict checks that a new share name is free, or that an updated share exists.
func shareConflict(st *store.State, name string, create bool) error {
	if !create {
		if st.Share(name) == nil {
			return notFound("share not found")
		}
		return nil
	}
	for _, sh := range st.Shares {
		if strings.EqualFold(sh.Name, name) {
			return badRequest("a share with this name already exists")
		}
	}
	return nil
}

// pathOnVolume reports whether p is inside a mounted volume or ZFS dataset.
func (s *Server) pathOnVolume(p string) bool {
	var ok bool
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			if (p == v.MountPoint || strings.HasPrefix(p, v.MountPoint+"/")) && storage.IsMountpoint(v.MountPoint) {
				ok = true
			}
		}
	})
	if ok {
		return true
	}
	if pools, _ := storage.ListZPools(false); pools != nil {
		for _, pl := range pools {
			mp := storage.MountRoot + "/" + pl.Name
			if p == mp || strings.HasPrefix(p, mp+"/") {
				return true
			}
		}
	}
	return false
}

// reapplySharePermissions re-runs share preparation at startup so permission fixes
// shipped by an upgrade reach existing shares. Shares whose volume is not mounted
// are skipped: creating their folder would write to the system disk instead.
func (s *Server) reapplySharePermissions() {
	var list []store.Share
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			list = append(list, *sh)
		}
	})
	for _, sh := range list {
		if !s.pathOnVolume(sh.Path) {
			continue
		}
		if err := shares.Prepare(&sh); err != nil {
			s.store.Log("warning", "shares", "", "Share "+sh.Name+": "+err.Error())
		}
	}
}
