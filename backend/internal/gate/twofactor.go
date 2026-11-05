// Package gate holds the pure logic of sign-in policies, remote access and
// share links: two-factor prompts for administrators, remote access advice and
// reachability diagnostics, rate limits and link limits. The api package
// gathers the facts and applies the decisions.
package gate

import (
	"errors"
	"time"
)

// PostponeFor is how long "Remind me later" hides the two-factor setup step.
const PostponeFor = 7 * 24 * time.Hour

// DefaultGrace is the delay given to administrators once two-factor
// authentication becomes required.
const DefaultGrace = 7 * 24 * time.Hour

// Policy is the NAS setting "Require two-factor for administrators".
type Policy struct {
	Require bool
	Since   time.Time // when it was switched on
	// GraceDays: 0 = DefaultGrace, -1 = none (enforced at once).
	GraceDays int
	// RequireKey: only a security key or a passkey satisfies it, not TOTP.
	RequireKey bool
}

// Grace returns the delay before the requirement is enforced.
func (p Policy) Grace() time.Duration {
	switch {
	case p.GraceDays < 0:
		return 0
	case p.GraceDays == 0:
		return DefaultGrace
	}
	return time.Duration(p.GraceDays) * 24 * time.Hour
}

// Account is what the policy looks at in a user.
type Account struct {
	Admin    bool
	TOTP     bool
	Passkeys int
	Keys     int // security keys (second factor after the password)
	// Prompted: first time the setup step was shown (zero: never).
	Prompted  time.Time
	Postponed time.Time // setup step hidden until then
}

// Protected reports whether the account has a second factor.
func (a Account) Protected() bool { return a.TOTP || a.Passkeys > 0 || a.Keys > 0 }

// Satisfied reports whether the account has the second factor the policy
// asks for: any of them, or a hardware one (security key or passkey) when
// the policy requires a key.
func (p Policy) Satisfied(a Account) bool {
	if p.Require && p.RequireKey {
		return a.Keys > 0 || a.Passkeys > 0
	}
	return a.Protected()
}

// TOTPAccepted reports whether an authenticator app code may complete an
// administrator's sign-in: not once a key is required and enforced.
func TOTPAccepted(p Policy, a Account, now time.Time) bool {
	if !a.Admin || !p.Require || !p.RequireKey || !p.Satisfied(a) {
		return true
	}
	return now.Before(Deadline(p, a, now))
}

// Prompt tells the interface what to do about the two-factor setup step.
type Prompt struct {
	Show        bool      `json:"show"`
	CanPostpone bool      `json:"canPostpone"`
	Required    bool      `json:"required"`
	KeyRequired bool      `json:"keyRequired,omitempty"` // a security key or passkey is needed
	Enforced    bool      `json:"enforced"`              // only the setup is allowed until done
	Deadline    time.Time `json:"deadline,omitzero"`
	Postponed   time.Time `json:"postponedUntil,omitzero"`
}

// Deadline is the end of the grace period of an account: counted from the
// later of the switch-on and the first prompt, so an administrator created
// long after the policy still gets the whole delay once prompted.
func Deadline(p Policy, a Account, now time.Time) time.Time {
	start := a.Prompted
	if p.Since.After(start) {
		start = p.Since
	}
	if start.IsZero() {
		start = now
	}
	return start.Add(p.Grace())
}

// TwoFactorPrompt decides whether an administrator sees the setup step.
func TwoFactorPrompt(p Policy, a Account, now time.Time) Prompt {
	keyReq := p.Require && p.RequireKey && a.Admin
	if !a.Admin || p.Satisfied(a) {
		return Prompt{Required: p.Require && a.Admin, KeyRequired: keyReq}
	}
	out := Prompt{Required: p.Require, KeyRequired: keyReq, CanPostpone: true}
	if p.Require {
		out.Deadline = Deadline(p, a, now)
		if !now.Before(out.Deadline) {
			out.Show, out.Enforced, out.CanPostpone = true, true, false
			return out
		}
	}
	if now.Before(a.Postponed) {
		out.Postponed = a.Postponed
		return out
	}
	out.Show = true
	return out
}

// Enforced reports whether the account may only set up its second factor.
func Enforced(p Policy, a Account, now time.Time) bool {
	return p.Require && a.Admin && !p.Satisfied(a) && !now.Before(Deadline(p, a, now))
}

var ErrCannotPostpone = errors.New("two-factor authentication is required now: it cannot be postponed")

// Postpone returns the date until which the setup step is hidden: a week,
// never beyond the end of the grace period.
func Postpone(p Policy, a Account, now time.Time) (time.Time, error) {
	if Enforced(p, a, now) {
		return time.Time{}, ErrCannotPostpone
	}
	until := now.Add(PostponeFor)
	if p.Require {
		if d := Deadline(p, a, now); d.Before(until) {
			until = d
		}
	}
	return until, nil
}

// SetupPaths are the requests an administrator whose second factor is
// required may still make: who am I, set up TOTP or a passkey, sign out.
var SetupPaths = map[string]bool{
	"GET /api/me":                       true,
	"GET /api/me/2fa/prompt":            true,
	"POST /api/me/2fa/setup":            true,
	"POST /api/me/2fa/enable":           true,
	"GET /api/me/passkeys":              true,
	"POST /api/me/passkeys/begin":       true,
	"POST /api/me/passkeys/finish":      true,
	"GET /api/me/security-keys":         true,
	"POST /api/me/security-keys/begin":  true,
	"POST /api/me/security-keys/finish": true,
	"GET /api/me/oidc":                  true,
	"POST /api/logout":                  true,
}

// SetupAllowed reports whether a route (its ServeMux pattern) is allowed to
// an administrator whose second factor is enforced.
func SetupAllowed(pattern string) bool { return SetupPaths[pattern] }
