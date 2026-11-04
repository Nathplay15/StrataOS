package api

// Two-factor authentication for administrators: a setup step shown at sign-in
// to administrators without a second factor (TOTP or passkey), which they can
// postpone, and the setting that requires it. Once required and after the
// grace period, such an administrator still signs in with the password but
// may only set up a second factor (gate.SetupAllowed): nobody is ever
// locked out by the policy itself. A lost phone or passkey is recovered with
// a code generated on the NAS console (h_console.go) or by another
// administrator.

import (
	"net/http"
	"time"

	"strataos/internal/gate"
	"strataos/internal/store"
)

func policyOf(st *store.State) gate.Policy {
	p := st.TwoFactor
	return gate.Policy{Require: p.Require, Since: p.Since, GraceDays: p.GraceDays}
}

func accountOf(u *store.User) gate.Account {
	return gate.Account{Admin: u.Role == "admin", TOTP: u.TOTPSecret != "", Passkeys: len(u.Passkeys), Prompted: u.TwoFactorPrompted, Postponed: u.TwoFactorPostponed}
}

// twoFactorPrompt computes the setup step of a user; mark records the first
// time an administrator without a second factor is prompted (the grace
// period of a required policy starts there at the earliest).
func (s *Server) twoFactorPrompt(name string, mark bool) *gate.Prompt {
	now := time.Now()
	if mark {
		var first bool
		s.store.Read(func(st *store.State) {
			if u := st.User(name); u != nil {
				a := accountOf(u)
				first = a.Admin && !a.Protected() && a.Prompted.IsZero()
			}
		})
		if first {
			_ = s.store.Update(func(st *store.State) error {
				if u := st.User(name); u != nil && u.TwoFactorPrompted.IsZero() {
					u.TwoFactorPrompted = now
				}
				return nil
			})
		}
	}
	var p gate.Prompt
	s.store.Read(func(st *store.State) {
		if u := st.User(name); u != nil {
			p = gate.TwoFactorPrompt(policyOf(st), accountOf(u), now)
		}
	})
	if !p.Show && !p.Required && p.Postponed.IsZero() {
		return nil
	}
	return &p
}

// setupOnly reports whether a session may only set up its second factor.
func setupOnly(st *store.State, u *store.User, now time.Time) bool {
	return u.Role == "admin" && gate.Enforced(policyOf(st), accountOf(u), now)
}

// passkeyOnlyBlocked: with the policy enforced, an administrator protected by
// passkeys alone must use one; the password alone would bypass the policy.
func passkeyOnlyBlocked(st *store.State, u *store.User, now time.Time) bool {
	p := policyOf(st)
	a := accountOf(u)
	return a.Admin && p.Require && !a.TOTP && a.Passkeys > 0 && !now.Before(gate.Deadline(p, a, now))
}

var errSecondFactorKept = badRequest("two-factor authentication is required for administrators: add another passkey or an authenticator app first")

// keepsSecondFactor refuses removing the last second factor of an
// administrator while the policy requires one.
func keepsSecondFactor(st *store.State, u *store.User, totp bool, passkeys int) error {
	if u.Role != "admin" || !st.TwoFactor.Require {
		return nil
	}
	if !totp && passkeys == 0 {
		return errSecondFactorKept
	}
	return nil
}

type adminFactor struct {
	Username  string    `json:"username"`
	TOTP      bool      `json:"totp"`
	Passkeys  int       `json:"passkeys"`
	Deadline  time.Time `json:"deadline,omitzero"`
	Postponed time.Time `json:"postponedUntil,omitzero"`
	Enforced  bool      `json:"enforced"`
}

func (s *Server) accessRoutes() {
	s.user("GET /api/me/2fa/prompt", func(r *http.Request) (any, error) {
		p := s.twoFactorPrompt(userOf(r), true)
		if p == nil {
			return gate.Prompt{}, nil
		}
		return p, nil
	})

	s.user("POST /api/me/2fa/postpone", func(r *http.Request) (any, error) {
		var until time.Time
		err := s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil {
				return notFound("user not found")
			}
			a := accountOf(u)
			if a.Prompted.IsZero() {
				u.TwoFactorPrompted, a.Prompted = time.Now(), time.Now()
			}
			t, err := gate.Postpone(policyOf(st), a, time.Now())
			if err != nil {
				return &Error{Status: http.StatusForbidden, Msg: err.Error()}
			}
			u.TwoFactorPostponed, until = t, t
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "auth", "Two-factor setup postponed until "+until.Format("2006-01-02"))
		return map[string]any{"postponedUntil": until}, nil
	})

	s.admin("GET /api/security/2fa-policy", func(r *http.Request) (any, error) {
		now := time.Now()
		var out struct {
			Require   bool          `json:"require"`
			GraceDays int           `json:"graceDays"`
			Since     time.Time     `json:"since,omitzero"`
			Recovery  bool          `json:"consoleRecovery"`
			Admins    []adminFactor `json:"admins"`
		}
		out.Admins = []adminFactor{}
		s.store.Read(func(st *store.State) {
			p := policyOf(st)
			out.Require, out.GraceDays, out.Since, out.Recovery = p.Require, st.TwoFactor.GraceDays, p.Since, !st.Console.NoRecovery
			for _, u := range st.Users {
				if u.Role != "admin" || u.Disabled {
					continue
				}
				a := accountOf(u)
				f := adminFactor{Username: u.Username, TOTP: a.TOTP, Passkeys: a.Passkeys, Enforced: gate.Enforced(p, a, now)}
				if !a.Protected() {
					pr := gate.TwoFactorPrompt(p, a, now)
					f.Deadline, f.Postponed = pr.Deadline, pr.Postponed
				}
				out.Admins = append(out.Admins, f)
			}
		})
		return out, nil
	})

	s.admin("PUT /api/security/2fa-policy", func(r *http.Request) (any, error) {
		var req struct {
			Require   bool `json:"require"`
			GraceDays int  `json:"graceDays"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.GraceDays < -1 || req.GraceDays > 90 {
			return nil, badRequest("the grace period is 0 to 90 days")
		}
		me := userOf(r)
		var turnedOn, changed bool
		err := s.store.Update(func(st *store.State) error {
			u := st.User(me)
			if req.Require && (u == nil || !accountOf(u).Protected()) {
				return badRequest("set up two-factor authentication or a passkey for your own account first")
			}
			if req.Require && st.Console.NoRecovery {
				// Never lock out the last administrator: without recovery
				// codes, only another administrator can help one who
				// loses their second factor.
				if enabledAdmins(st) < 2 {
					return badRequest("allow recovery codes on the NAS console first (Settings → Console screen): with a single administrator, they are the way back after losing a phone or passkey")
				}
			}
			turnedOn = req.Require && !st.TwoFactor.Require
			changed = req.Require != st.TwoFactor.Require || req.GraceDays != st.TwoFactor.GraceDays
			st.TwoFactor.Require, st.TwoFactor.GraceDays = req.Require, req.GraceDays
			if turnedOn {
				st.TwoFactor.Since = time.Now()
			}
			if !req.Require {
				st.TwoFactor.Since = time.Time{}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if changed {
			if req.Require {
				s.audit(r, "auth", "Two-factor authentication required for administrators")
			} else {
				s.audit(r, "auth", "Two-factor authentication no longer required for administrators")
			}
		}
		return nil, nil
	})
}

const errSetupFirst = "two-factor authentication is required for administrators: set it up to continue"

func enabledAdmins(st *store.State) int {
	n := 0
	for _, u := range st.Users {
		if u.Role == "admin" && !u.Disabled {
			n++
		}
	}
	return n
}
