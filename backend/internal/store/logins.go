package store

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Login is one sign-in attempt seen by the NAS, successful or not: the
// history shown in the account menu (SessionsModal) and to administrators.
type Login struct {
	Time time.Time `json:"time"`
	User string    `json:"user"`
	IP   string    `json:"ip"`
	// UA is the browser or app signature of the attempt (truncated).
	UA string `json:"ua,omitempty"`
	OK bool   `json:"ok"`
	// NewDevice is true when a successful sign-in comes from a browser/app
	// signature this account never used before.
	NewDevice bool `json:"newDevice,omitempty"`
	// How: password | twofactor | key | passkey | oidc | <provider name>.
	How string `json:"how"`
}

// maxLogins bounds the login history kept in the state file.
const maxLogins = 500

// maxDevicesPerUser bounds the device signatures remembered per account.
const maxDevicesPerUser = 20

// maxUA is the length kept of the User-Agent signature.
const maxUA = 200

// deviceHash identifies a browser/app from its User-Agent.
func deviceHash(ua string) string {
	sum := sha256.Sum256([]byte(ua))
	return hex.EncodeToString(sum[:])[:16]
}

// Login records a sign-in attempt in the history (newest last) and returns
// the stored entry. A successful attempt from a device signature the account
// never used is marked NewDevice and the signature is remembered (bounded);
// failed attempts never mark a device as known.
func (s *Store) Login(user, ip, ua string, ok bool, how string) Login {
	if user == "" {
		user = "(unknown)"
	}
	if how == "" {
		how = "password"
	}
	if len(ua) > maxUA {
		ua = ua[:maxUA]
	}
	entry := Login{Time: time.Now(), User: user, IP: ip, UA: ua, OK: ok, How: how}
	_ = s.Update(func(st *State) error {
		if st.Devices == nil {
			st.Devices = map[string][]string{}
		}
		if ok && user != "(unknown)" {
			h := deviceHash(ua)
			seen := false
			for _, d := range st.Devices[user] {
				if d == h {
					seen = true
					break
				}
			}
			if !seen {
				entry.NewDevice = true
				st.Devices[user] = append(st.Devices[user], h)
				if len(st.Devices[user]) > maxDevicesPerUser {
					st.Devices[user] = st.Devices[user][len(st.Devices[user])-maxDevicesPerUser:]
				}
			}
		}
		st.Logins = append(st.Logins, entry)
		if len(st.Logins) > maxLogins {
			st.Logins = st.Logins[len(st.Logins)-maxLogins:]
		}
		return nil
	})
	return entry
}

// LoginsOf returns the sign-in attempts of one account, newest first; every
// attempt when user is empty. okOnly keeps only the successful ones when true.
func (st *State) LoginsOf(user string, max int, okOnly bool) []Login {
	out := []Login{}
	for i := len(st.Logins) - 1; i >= 0 && len(out) < max; i-- {
		if (user == "" || st.Logins[i].User == user) && (!okOnly || st.Logins[i].OK) {
			out = append(out, st.Logins[i])
		}
	}
	return out
}
