package api

import (
	"log/slog"
	"net/http"
	"os/user"
	"strings"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
)

func (s *Server) setupNeeded() bool {
	var needed bool
	s.store.Read(func(st *store.State) { needed = len(st.Users) == 0 })
	return needed
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil, MaxAge: int(ttl.Seconds()),
	})
}

func (s *Server) sessionTTL() time.Duration {
	h := 12
	s.store.Read(func(st *store.State) {
		if st.Settings.SessionHours > 0 {
			h = st.Settings.SessionHours
		}
	})
	return time.Duration(h) * time.Hour
}

// maxAuthBody bounds the unauthenticated setup and login bodies (the other JSON
// routes go through wrap, which allows 4 MiB to authenticated users only).
const maxAuthBody = 64 << 10

type meResp struct {
	Username  string `json:"username"`
	FullName  string `json:"fullName"`
	Role      string `json:"role"`
	TwoFactor bool   `json:"twoFactor"`
}

func (s *Server) authRoutes() {
	s.public("GET /api/setup", func(r *http.Request) (any, error) {
		host, _ := hostname()
		return map[string]any{"needed": s.setupNeeded(), "hostname": host, "version": s.version}, nil
	})

	// First-run wizard: creates the initial administrator.
	s.mux.Handle("POST /api/setup", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			FullName string `json:"fullName"`
			Hostname string `json:"hostname"`
			Timezone string `json:"timezone"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if !s.setupNeeded() {
			writeErr(w, &Error{Status: http.StatusConflict, Msg: "setup already completed"})
			return
		}
		if err := accounts.ValidName(req.Username); err != nil {
			writeErr(w, badRequest(err.Error()))
			return
		}
		if err := accounts.ValidPassword(req.Password); err != nil {
			writeErr(w, badRequest(err.Error()))
			return
		}
		if err := accounts.Create(req.Username, req.FullName, req.Password, nil, true); err != nil {
			writeErr(w, err)
			return
		}
		// The first administrator also gets sudo: on systems installed from the StrataOS
		// ISO, root has no password and this account is the console/SSH way in.
		// (sudo is a reserved group that StrataOS never lets users pick.)
		if _, err := user.LookupGroup("sudo"); err == nil {
			if _, err := sys.Run("usermod", "-a", "-G", "sudo", req.Username); err != nil {
				slog.Warn("add first admin to sudo", "err", err)
			}
		}
		_ = s.addToDockerGroup(req.Username)
		hash, err := hashPassword(req.Password)
		if err != nil {
			writeErr(w, err)
			return
		}
		err = s.store.Update(func(st *store.State) error {
			if len(st.Users) > 0 {
				return &Error{Status: http.StatusConflict, Msg: "setup already completed"}
			}
			st.Users = append(st.Users, &store.User{Username: req.Username, FullName: req.FullName, Role: "admin", PasswordHash: hash, SSH: true, CreatedAt: time.Now()})
			return nil
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		if req.Hostname != "" {
			_ = system.SetHostname(req.Hostname)
		}
		if req.Timezone != "" {
			_ = system.SetTimezone(req.Timezone)
		}
		s.store.Log("info", "system", req.Username, "Initial setup completed")
		s.applyShares()
		ttl := s.sessionTTL()
		s.setCookie(w, r, s.sessions.create(req.Username, "admin", clientIP(r), ttl), ttl)
		writeJSON(w, http.StatusOK, meResp{Username: req.Username, FullName: req.FullName, Role: "admin"})
	}))

	s.mux.Handle("POST /api/login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many failed attempts, try again in a few minutes"})
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Remember bool   `json:"remember"`
			Code     string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		req.Username = strings.ToLower(strings.TrimSpace(req.Username))
		var u store.User
		var found bool
		s.store.Read(func(st *store.State) {
			if p := st.User(req.Username); p != nil {
				u, found = *p, true
			}
		})
		if (!found || u.Source != "") && !u.Disabled {
			// Directory account (Active Directory / LDAP).
			if du, ok := s.directoryLogin(r.Context(), req.Username, req.Password); ok {
				u, found = du, true
			} else {
				found = false
			}
		} else if found && !u.Disabled && !checkPassword(u.PasswordHash, req.Password) {
			found = false
		}
		if !found || u.Disabled {
			if !found {
				// Constant-ish time to avoid user enumeration.
				checkPassword(dummyHash(), req.Password)
			}
			s.store.Log("warning", "auth", req.Username, "Failed login from "+ip)
			writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: errBadLogin.Error()})
			return
		}
		if u.TOTPSecret != "" {
			if strings.TrimSpace(req.Code) == "" {
				// Password is right: ask for the second factor (does not count as a failure).
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "code de double authentification requis", "twoFactor": true})
				return
			}
			if !s.checkSecondFactor(u.Username, req.Code) {
				s.store.Log("warning", "auth", u.Username, "Wrong two-factor code from "+ip)
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "code de double authentification incorrect", "twoFactor": true})
				return
			}
		}
		s.limiter.reset(ip)
		ttl := s.sessionTTL()
		if req.Remember {
			ttl = 30 * 24 * time.Hour
		}
		s.setCookie(w, r, s.sessions.create(u.Username, u.Role, ip, ttl), ttl)
		s.store.Log("info", "auth", u.Username, "Signed in from "+ip)
		writeJSON(w, http.StatusOK, meResp{Username: u.Username, FullName: u.FullName, Role: u.Role, TwoFactor: u.TOTPSecret != ""})
	}))

	s.userRaw("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(cookieName); err == nil {
			s.sessions.delete(c.Value)
		}
		s.setCookie(w, r, "", -time.Second)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	s.user("GET /api/me", func(r *http.Request) (any, error) {
		var m meResp
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				m = meResp{Username: u.Username, FullName: u.FullName, Role: u.Role, TwoFactor: u.TOTPSecret != ""}
			}
		})
		return m, nil
	})

	s.user("POST /api/me/password", func(r *http.Request) (any, error) {
		var req struct {
			Current string `json:"current"`
			New     string `json:"new"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := userOf(r)
		var hash, source string
		s.store.Read(func(st *store.State) {
			if u := st.User(name); u != nil {
				hash, source = u.PasswordHash, u.Source
			}
		})
		if source != "" {
			return nil, badRequest("this account's password is managed by the directory")
		}
		if !checkPassword(hash, req.Current) {
			return nil, badRequest("current password is incorrect")
		}
		if err := s.changePassword(name, req.New); err != nil {
			return nil, err
		}
		s.audit(r, "auth", "Changed own password")
		return nil, nil
	})
}

func (s *Server) changePassword(name, pw string) error {
	if err := accounts.ValidPassword(pw); err != nil {
		return badRequest(err.Error())
	}
	if err := accounts.SetPassword(name, pw); err != nil {
		return err
	}
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	return s.store.Update(func(st *store.State) error {
		if u := st.User(name); u != nil {
			u.PasswordHash = h
		}
		return nil
	})
}
