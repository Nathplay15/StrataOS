package api

import (
	"net/http"

	"strataos/internal/antivirus"
)

func (s *Server) antivirusRoutes() {
	s.admin("GET /api/antivirus", func(r *http.Request) (any, error) {
		return map[string]any{"status": antivirus.GetStatus(), "quarantine": antivirus.List()}, nil
	})
	s.admin("POST /api/antivirus/quarantine/{id}/restore", func(r *http.Request) (any, error) {
		p, err := antivirus.Restore(r.PathValue("id"))
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "antivirus", "Restored "+p+" from quarantine")
		return map[string]string{"path": p}, nil
	})
	s.admin("DELETE /api/antivirus/quarantine/{id}", func(r *http.Request) (any, error) {
		if err := antivirus.Delete(r.PathValue("id")); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "antivirus", "Deleted a quarantined file")
		return nil, nil
	})
}
