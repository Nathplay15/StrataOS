package api

import (
	"fmt"
	"net/http"
	"strconv"

	"strataos/internal/store"
)

// recordLogin stores a sign-in attempt with the browser/app signature of the
// request, and alerts the account when it succeeds from a new device.
func (s *Server) recordLogin(r *http.Request, user string, ok bool, how string) {
	l := s.store.Login(user, clientIP(r), r.UserAgent(), ok, how)
	if l.OK && l.NewDevice {
		msg := fmt.Sprintf("Account %s signed in from a new device (%s, method %s)", l.User, l.IP, l.How)
		s.store.Log("warning", "auth", l.User, "New device sign-in from "+l.IP)
		s.notify("warning", "Sign-in from a new device", msg)
	}
}

// sessionRoutes: the live web sessions and the sign-in history. Every user
// sees and ends their own sessions; administrators see every account.
func (s *Server) sessionRoutes() {
	// currentSessionKey is the SHA-256 identifying the caller's session.
	currentSessionKey := func(r *http.Request) string {
		if c, err := r.Cookie(cookieName); err == nil {
			return key(c.Value)
		}
		return ""
	}

	s.user("GET /api/sessions", func(r *http.Request) (any, error) {
		list := s.sessions.list(currentSessionKey(r))
		if !isAdmin(r) {
			own := []sessionView{}
			me := userOf(r)
			for _, v := range list {
				if v.User == me {
					own = append(own, v)
				}
			}
			list = own
		}
		return list, nil
	})

	s.user("DELETE /api/sessions/others", func(r *http.Request) (any, error) {
		n := s.sessions.dropOtherUser(userOf(r), currentSessionKey(r))
		if n > 0 {
			s.audit(r, "auth", "Signed out of "+strconv.Itoa(n)+" other session(s)")
		}
		return map[string]int{"ended": n}, nil
	})

	s.user("DELETE /api/sessions/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		v := s.sessions.one(id)
		if v == nil {
			return nil, notFound("session not found")
		}
		if v.User != userOf(r) && !isAdmin(r) {
			return nil, errForbidden
		}
		s.sessions.dropOne(id)
		s.audit(r, "auth", "Session of "+v.User+" ended ("+v.IP+")")
		return nil, nil
	})

	s.user("GET /api/me/logins", func(r *http.Request) (any, error) {
		var out []store.Login
		s.store.Read(func(st *store.State) { out = st.LoginsOf(userOf(r), 100, r.URL.Query().Get("ok") == "1") })
		return out, nil
	})

	s.admin("GET /api/logins", func(r *http.Request) (any, error) {
		user := r.URL.Query().Get("user")
		var out []store.Login
		s.store.Read(func(st *store.State) { out = st.LoginsOf(user, 200, r.URL.Query().Get("ok") == "1") })
		return out, nil
	})
}

// one returns a copy of the live session identified by its SHA-256.
func (s *sessions) one(id string) *sessionView {
	for _, v := range s.list("") {
		if v.ID == id {
			return &v
		}
	}
	return nil
}
