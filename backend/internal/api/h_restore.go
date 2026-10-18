package api

import (
	"net/http"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/sys"
	"strataos/internal/system"
)

// restorePointBeforeUpdate snapshots the system before an update when possible.
func (s *Server) restorePointBeforeUpdate(j *jobs.Job) {
	if ok, _ := system.RestoreSupported(); !ok {
		return
	}
	name, err := system.CreateRestorePoint("before-update")
	if err != nil {
		j.Logf("Cannot create a restore point: %v", err)
		return
	}
	j.Logf("Restore point created: %s", name)
}

func (s *Server) restoreRoutes() {
	s.admin("GET /api/system/restore-points", func(r *http.Request) (any, error) {
		ok, why := system.RestoreSupported()
		mode, _ := system.Mode()
		out := map[string]any{"supported": ok, "reason": why, "root": system.Root(), "mode": mode, "points": []system.RestorePoint{}}
		if ok {
			list, err := system.ListRestorePoints()
			if err != nil {
				return nil, err
			}
			out["points"] = list
		}
		return out, nil
	})
	s.admin("POST /api/system/restore-points", func(r *http.Request) (any, error) {
		name, err := system.CreateRestorePoint("manual")
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "system", "Created restore point "+name)
		return map[string]string{"name": name}, nil
	})
	s.admin("DELETE /api/system/restore-points/{name}", func(r *http.Request) (any, error) {
		if err := system.DeleteRestorePoint(r.PathValue("name")); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "system", "Deleted restore point "+r.PathValue("name"))
		return nil, nil
	})
	// Puts the system back to a restore point and reboots.
	s.admin("POST /api/system/restore-points/{name}/rollback", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if err := system.RollbackTo(name); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "system", "Rolled the system back to "+name+", rebooting")
		s.store.Log("warning", "system", userOf(r), "Back to restore point "+name+": rebooting")
		if !system.RebootsItself() {
			go func() {
				time.Sleep(2 * time.Second)
				_, _ = sys.Run("systemctl", "reboot")
			}()
		}
		return nil, nil
	})
}
