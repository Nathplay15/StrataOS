package api

import (
	"context"
	"net/http"
	"strings"

	"strataos/internal/accounts"
	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/store"
)

// quotaTargets lists every place quotas could apply, with their current state.
func (s *Server) quotaTargets() []storage.QuotaTarget {
	out := []storage.QuotaTarget{}
	var vols []store.Volume
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			vols = append(vols, *v)
		}
	})
	for _, v := range vols {
		t := storage.QuotaTarget{Kind: v.FSType, Target: v.ID, Name: v.Name, MountPoint: v.MountPoint}
		switch v.FSType {
		case "ext4", "xfs":
			t.Supported = true
			t.Enabled = storage.IsMountpoint(v.MountPoint) && storage.QuotaActive(v.MountPoint, v.FSType)
		}
		out = append(out, t)
	}
	if storage.ZFSAvailable() {
		ds, _ := storage.ListZDatasets("", "filesystem")
		for _, d := range ds {
			out = append(out, storage.QuotaTarget{Kind: "zfs", Target: d.Name, Name: d.Name, MountPoint: d.Mountpoint, Supported: true, Enabled: true})
		}
	}
	return out
}

func (s *Server) findQuotaTarget(kind, target string) (storage.QuotaTarget, error) {
	for _, t := range s.quotaTargets() {
		if t.Kind == kind && t.Target == target {
			if !t.Supported {
				return t, badRequest("per-user quotas are not available on " + t.Kind + " (use ext4, XFS or ZFS)")
			}
			return t, nil
		}
	}
	return storage.QuotaTarget{}, notFound("volume not found")
}

func (s *Server) userQuotas(user string) []storage.UserQuota {
	out := []storage.UserQuota{}
	for _, t := range s.quotaTargets() {
		q := storage.UserQuota{QuotaTarget: t}
		if t.Supported && t.Enabled {
			q.Used, q.Limit = storage.GetUserQuota(t, user)
		}
		out = append(out, q)
	}
	return out
}

func (s *Server) quotaRoutes() {
	s.admin("GET /api/quotas/targets", func(r *http.Request) (any, error) { return s.quotaTargets(), nil })

	s.admin("POST /api/storage/volumes/{id}/quota", func(r *http.Request) (any, error) {
		v, err := s.volume(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		if v.FSType != "ext4" && v.FSType != "xfs" {
			return nil, badRequest("per-user quotas need ext4 or XFS (ZFS datasets support them natively)")
		}
		s.audit(r, "storage", "Enabling user quotas on "+v.Name)
		return s.jobs.Start("quota", "Enable quotas on "+v.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			if err := storage.EnableQuota(ctx, v.MountPoint, v.UUID, v.FSType, j.Logf); err != nil {
				return err
			}
			// Remounting interrupted the shares on this volume: reload them.
			_ = s.applyShares()
			return nil
		}), nil
	})

	s.admin("GET /api/users/{name}/quotas", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if !accounts.LinuxUserExists(name) {
			return nil, notFound("user not found")
		}
		return s.userQuotas(name), nil
	})

	s.admin("PUT /api/users/{name}/quotas", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		var req struct {
			Kind   string `json:"kind"`
			Target string `json:"target"`
			Limit  int64  `json:"limit"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var known bool
		s.store.Read(func(st *store.State) { known = st.User(name) != nil })
		if !known {
			return nil, notFound("user not found")
		}
		t, err := s.findQuotaTarget(req.Kind, req.Target)
		if err != nil {
			return nil, err
		}
		if err := storage.SetUserQuota(t, name, req.Limit); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "users", "Quota of "+name+" on "+t.Name+" set")
		return nil, nil
	})

	// Every user can see their own quotas (shown in the file manager).
	s.user("GET /api/me/quotas", func(r *http.Request) (any, error) {
		var out []storage.UserQuota
		for _, q := range s.userQuotas(userOf(r)) {
			if q.Limit > 0 && !strings.HasPrefix(q.Name, ".") {
				out = append(out, q)
			}
		}
		if out == nil {
			out = []storage.UserQuota{}
		}
		return out, nil
	})
}
