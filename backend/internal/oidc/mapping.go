package oidc

import (
	"errors"
	"slices"
	"sort"
	"strings"
)

// Mapping turns claims into a StrataOS identity.
type Mapping struct {
	UsernameClaim string // default preferred_username (then the email's local part)
	EmailClaim    string // default email
	GroupsClaim   string // default groups
	AdminGroups   []string
	AllowedGroups []string
	GroupMap      map[string]string // provider group → StrataOS group
}

// Identity is who signed in, as StrataOS sees it.
type Identity struct {
	Subject  string
	Username string // normalised, to be checked by accounts.ValidName
	Email    string
	Name     string
	Groups   []string
	Admin    bool     // member of an administrators group
	Mapped   []string // StrataOS groups from GroupMap
}

var ErrNotAllowed = errors.New("your account is not in a group allowed to sign in to this NAS")

// NormalizeUsername makes a Linux account name of a provider username:
// lowercase, other characters replaced by "-", 32 characters at most.
func NormalizeUsername(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out != "" && !(out[0] >= 'a' && out[0] <= 'z' || out[0] == '_') {
		out = "u" + out
	}
	if len(out) > 32 {
		out = strings.TrimRight(out[:32], "-")
	}
	return out
}

// groupEq compares group names case-insensitively, ignoring the leading
// slash of Keycloak group paths.
func groupEq(a, b string) bool {
	return strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(a), "/"), strings.TrimPrefix(strings.TrimSpace(b), "/"))
}

func inGroups(groups []string, wanted []string) bool {
	for _, g := range groups {
		for _, w := range wanted {
			if w != "" && groupEq(g, w) {
				return true
			}
		}
	}
	return false
}

// MapClaims applies a mapping to the claims of an ID token (merged with
// userinfo).
func MapClaims(c Claims, m Mapping) (Identity, error) {
	id := Identity{Subject: c.String("sub"), Name: c.String("name")}
	if id.Subject == "" {
		return Identity{}, errors.New("no subject")
	}
	ec := m.EmailClaim
	if ec == "" {
		ec = "email"
	}
	id.Email = c.String(ec)
	uc := m.UsernameClaim
	if uc == "" {
		uc = "preferred_username"
	}
	raw := c.String(uc)
	if raw == "" && m.UsernameClaim == "" && id.Email != "" {
		raw, _, _ = strings.Cut(id.Email, "@")
	}
	if raw == "" {
		return Identity{}, errors.New("the provider sent no user name (claim " + uc + ")")
	}
	id.Username = NormalizeUsername(raw)
	if id.Username == "" {
		return Identity{}, errors.New("unusable user name " + raw)
	}
	gc := m.GroupsClaim
	if gc == "" {
		gc = "groups"
	}
	id.Groups = c.Strings(gc)
	if len(m.AllowedGroups) > 0 && !inGroups(id.Groups, m.AllowedGroups) {
		return Identity{}, ErrNotAllowed
	}
	id.Admin = inGroups(id.Groups, m.AdminGroups)
	for from, to := range m.GroupMap {
		if inGroups(id.Groups, []string{from}) && to != "" && !slices.Contains(id.Mapped, to) {
			id.Mapped = append(id.Mapped, to)
		}
	}
	sort.Strings(id.Mapped)
	return id, nil
}

// DefaultMFAValues are the amr values (RFC 8176) and acr values that mean a
// second factor was checked.
var DefaultMFAValues = []string{"mfa", "otp", "hwk", "swk", "fpt", "face", "pin", "sc", "mca", "iris", "retina", "vbm"}

// MFA tells whether the provider says it checked a second factor (an amr
// value, or the acr, among values), and whether that was a hardware key.
func MFA(c Claims, values []string) (mfa, hardware bool) {
	if len(values) == 0 {
		values = DefaultMFAValues
	}
	amr := c.Strings("amr")
	for _, a := range amr {
		if slices.ContainsFunc(values, func(v string) bool { return strings.EqualFold(v, a) }) {
			mfa = true
		}
		if strings.EqualFold(a, "hwk") || strings.EqualFold(a, "sc") {
			hardware = true
		}
	}
	if acr := c.String("acr"); acr != "" && slices.ContainsFunc(values, func(v string) bool { return v == acr }) {
		mfa = true
	}
	return mfa, mfa && hardware
}
