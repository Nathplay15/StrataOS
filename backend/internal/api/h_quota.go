package api

import (
	"context"
	"fmt"
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

// userQuotaReading is one account's quota on one target.
type userQuotaReading struct {
	user, target, name string
	used, limit        int64
}

// userQuotaAlerts compares the readings with prev (key "user|target" -> pct)
// and returns the alerts to send when 90 % or 100 % is crossed upwards, plus
// the new percentages. Pure, tested.
func userQuotaAlerts(prev map[string]float64, rs []userQuotaReading) ([]alert, map[string]float64) {
	next := map[string]float64{}
	var out []alert
	for _, r := range rs {
		if r.limit <= 0 {
			continue
		}
		key := r.user + "|" + r.target
		pct := float64(r.used) / float64(r.limit) * 100
		next[key] = pct
		p := prev[key]
		switch {
		case pct >= 100 && p < 100:
			out = append(out, alert{Level: "error", Subject: "Quota reached", Key: "uq-full:" + key,
				Message: fmt.Sprintf("Account %s reached its quota on %s (%.1f GB of %.1f GB)", r.user, r.name, float64(r.used)/1e9, float64(r.limit)/1e9)})
		case pct >= 90 && p < 90:
			out = append(out, alert{Level: "warning", Subject: "Quota almost reached", Key: "uq-90:" + key,
				Message: fmt.Sprintf("Account %s is at %.0f%% of its quota on %s (%.1f GB of %.1f GB)", r.user, pct, r.name, float64(r.used)/1e9, float64(r.limit)/1e9)})
		}
	}
	return out, next
}

// checkUserQuotas measures the per-user quotas of every account and alerts
// when one reaches 90 % or 100 % (roadmap 0.15, "User quotas in the
// interface"). prev carries the last percentages, so an alert is sent once.
func (s *Server) checkUserQuotas(prev map[string]float64) map[string]float64 {
	var users []string
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if !u.Disabled && u.Username != "" {
				users = append(users, u.Username)
			}
		}
	})
	var rs []userQuotaReading
	for _, t := range s.quotaTargets() {
		if !t.Supported || !t.Enabled {
			continue
		}
		for _, u := range users {
			used, limit := storage.GetUserQuota(t, u)
			if limit > 0 {
				rs = append(rs, userQuotaReading{user: u, target: t.Target, name: t.Name, used: used, limit: limit})
			}
		}
	}
	alerts, next := userQuotaAlerts(prev, rs)
	for _, a := range alerts {
		s.store.Log(a.Level, "quota", "", a.Message)
		s.notify(a.Level, a.Subject, a.Message)
	}
	return next
}
