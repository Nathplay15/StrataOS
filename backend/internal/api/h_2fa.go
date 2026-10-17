package api

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/totp"
)

// checkSecondFactor accepts a TOTP code or a single-use recovery code.
func (s *Server) checkSecondFactor(user, code string) bool {
	code = strings.TrimSpace(code)
	ok := false
	_ = s.store.Update(func(st *store.State) error {
		u := st.User(user)
		if u == nil || u.TOTPSecret == "" {
			return nil
		}
		if valid, counter := totp.Verify(u.TOTPSecret, code, time.Now(), u.TOTPLast); valid {
			u.TOTPLast = counter
			ok = true
			return nil
		}
		norm := strings.ToLower(strings.ReplaceAll(code, " ", ""))
		for i, h := range u.RecoveryCodes {
			if checkPassword(h, norm) {
				u.RecoveryCodes = append(u.RecoveryCodes[:i], u.RecoveryCodes[i+1:]...)
				ok = true
				s.store.LogLocked(st, "warning", "auth", user, fmt.Sprintf("Recovery code used (%d left)", len(u.RecoveryCodes)))
				return nil
			}
		}
		return nil
	})
	return ok
}

func recoveryCode() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 11)
	for i, c := range b {
		if i == 5 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(c)%len(alphabet)])
	}
	return string(out)
}

func (s *Server) twoFactorRoutes() {
	// Step 1: generate a secret (not active until a valid code is entered).
	s.user("POST /api/me/2fa/setup", func(r *http.Request) (any, error) {
		name := userOf(r)
		secret := totp.NewSecret()
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil {
				return notFound("user not found")
			}
			u.TOTPPending = secret
			return nil
		})
		if err != nil {
			return nil, err
		}
		host, _ := hostname()
		return map[string]string{"secret": secret, "uri": totp.URI(secret, name+"@"+host, "StrataOS")}, nil
	})

	// Step 2: prove the app works; returns single-use recovery codes, shown once.
	s.user("POST /api/me/2fa/enable", func(r *http.Request) (any, error) {
		var req struct {
			Code string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := userOf(r)
		codes := make([]string, 8)
		hashes := make([]string, 8)
		for i := range codes {
			codes[i] = recoveryCode()
			h, err := hashPassword(strings.ReplaceAll(codes[i], " ", ""))
			if err != nil {
				return nil, err
			}
			hashes[i] = h
		}
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil || u.TOTPPending == "" {
				return badRequest("start the setup first")
			}
			ok, counter := totp.Verify(u.TOTPPending, req.Code, time.Now(), 0)
			if !ok {
				return badRequest("incorrect code: check the time of your phone and retry")
			}
			u.TOTPSecret, u.TOTPPending, u.TOTPLast, u.RecoveryCodes = u.TOTPPending, "", counter, hashes
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "auth", "Two-factor authentication enabled")
		return map[string]any{"recoveryCodes": codes}, nil
	})

	s.user("POST /api/me/2fa/disable", func(r *http.Request) (any, error) {
		var req struct {
			Password string `json:"password"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := userOf(r)
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil || !checkPassword(u.PasswordHash, req.Password) {
				return badRequest("incorrect password")
			}
			u.TOTPSecret, u.TOTPPending, u.TOTPLast, u.RecoveryCodes = "", "", 0, nil
			return nil
		})
		if err == nil {
			s.audit(r, "auth", "Two-factor authentication disabled")
		}
		return nil, err
	})

	// An administrator can reset the second factor of a user who lost their phone.
	s.admin("POST /api/users/{name}/2fa/reset", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil {
				return notFound("user not found")
			}
			u.TOTPSecret, u.TOTPPending, u.TOTPLast, u.RecoveryCodes = "", "", 0, nil
			return nil
		})
		if err == nil {
			s.sessions.dropUser(name)
			s.audit(r, "users", "Two-factor authentication reset for "+name)
		}
		return nil, err
	})
}
