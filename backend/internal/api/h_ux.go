package api

import (
	"net/http"

	"strataos/internal/i18n"
	"strataos/internal/store"
)

// uxRoutes: the notification centre (notif_center.go) and the interface
// language of the signed-in user.
func (s *Server) uxRoutes() {
	s.notifCenterRoutes()
	// The browser reports its interface language: the first administrator's
	// is the language of the messages sent by the NAS until one is chosen
	// in Settings (store.State.MessageLang).
	s.user("PUT /api/me/lang", func(r *http.Request) (any, error) {
		var req struct {
			Lang string `json:"lang"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if !i18n.Valid(req.Lang) {
			return nil, badRequest("invalid language")
		}
		return nil, s.store.Update(func(st *store.State) error {
			if u := st.User(userOf(r)); u != nil {
				u.Lang = req.Lang
			}
			return nil
		})
	})
}
