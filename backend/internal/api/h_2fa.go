package api

import (
	"crypto/rand"
	"net/http"
	"time"

	"strataos/internal/store"
	"strataos/internal/totp"
)

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
		codes, hashes, err := userRecoveryCodes()
		if err != nil {
			return nil, err
		}
		err = s.store.Update(func(st *store.State) error {
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
			if err := keepsSecondFactor(st, u, false, len(u.Passkeys), len(u.SecurityKeys)); err != nil {
				return err
			}
			u.TOTPSecret, u.TOTPPending, u.TOTPLast = "", "", 0
			if len(u.SecurityKeys) == 0 {
				u.RecoveryCodes = nil // they stand in for security keys too
			}
			return nil
		})
		if err == nil {
			s.audit(r, "auth", "Two-factor authentication disabled")
		}
		return nil, err
	})

	// An administrator can reset the second factor of a user who lost their
	// phone or security key: the app, the security keys and the recovery codes.
	s.admin("POST /api/users/{name}/2fa/reset", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil {
				return notFound("user not found")
			}
			u.TOTPSecret, u.TOTPPending, u.TOTPLast, u.RecoveryCodes = "", "", 0, nil
			u.SecurityKeys = nil
			return nil
		})
		if err == nil {
			s.sessions.dropUser(name)
			s.audit(r, "users", "Two-factor authentication reset for "+name)
		}
		return nil, err
	})
}
