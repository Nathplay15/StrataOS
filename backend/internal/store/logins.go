package store

import "time"

// Login is one sign-in attempt seen by the NAS, successful or not: the
// history shown in the account menu (SessionsModal) and to administrators.
type Login struct {
	Time time.Time `json:"time"`
	User string    `json:"user"`
	IP   string    `json:"ip"`
	OK   bool      `json:"ok"`
	// How: password | twofactor | key | passkey | oidc | <provider name>.
	How string `json:"how"`
}

// maxLogins bounds the login history kept in the state file.
const maxLogins = 500

// Login records a sign-in attempt in the history (newest last).
func (s *Store) Login(user, ip string, ok bool, how string) {
	if user == "" {
		user = "(unknown)"
	}
	if how == "" {
		how = "password"
	}
	_ = s.Update(func(st *State) error {
		st.Logins = append(st.Logins, Login{Time: time.Now(), User: user, IP: ip, OK: ok, How: how})
		if len(st.Logins) > maxLogins {
			st.Logins = st.Logins[len(st.Logins)-maxLogins:]
		}
		return nil
	})
}

// LoginsOf returns the sign-in attempts of one account, newest first; every
// attempt when user is empty.
func (st *State) LoginsOf(user string, max int) []Login {
	out := []Login{}
	for i := len(st.Logins) - 1; i >= 0 && len(out) < max; i-- {
		if user == "" || st.Logins[i].User == user {
			out = append(out, st.Logins[i])
		}
	}
	return out
}
