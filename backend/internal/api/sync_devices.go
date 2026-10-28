package api

import (
	"net/http"
	"sort"

	"strataos/internal/store"
)

// syncDeviceRoutes: administrators see the paired phones and computers of
// every account and can revoke them (a lost laptop, a departed employee).
func (s *Server) syncDeviceRoutes() {
	s.admin("GET /api/devices", func(r *http.Request) (any, error) {
		type view struct {
			User string `json:"user"`
			deviceView
		}
		out := []view{}
		s.store.Read(func(st *store.State) {
			for _, u := range st.Users {
				for _, d := range u.MobileDevices {
					out = append(out, view{u.Username, viewDevice(d)})
				}
			}
		})
		sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
		return out, nil
	})

	s.admin("DELETE /api/devices/{user}/{id}", func(r *http.Request) (any, error) {
		user := r.PathValue("user")
		if err := s.removeDevice(user, r.PathValue("id"), "Device revoked by "+userOf(r)+": "); err != nil {
			return nil, err
		}
		s.audit(r, "auth", "Device of "+user+" revoked")
		return nil, nil
	})
}
