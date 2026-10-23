package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"strings"
	"sync"
	"time"
)

// Administrator recovery codes, generated on the NAS console (physical access)
// and used once on the sign-in page. Only the hash of the current code is
// kept, in memory: restarting the daemon cancels it.
const (
	recoveryTTL = 15 * time.Minute
	// At most recoveryMaxIssued codes per hour, and recoveryMaxFails wrong
	// codes burn the current one (59 random bits already defeat guessing).
	recoveryMaxIssued = 5
	recoveryMaxFails  = 5
	// Without look-alike characters (0/O, 1/I/L).
	recoveryAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	recoveryLen      = 12
)

type recoveryCodes struct {
	mu      sync.Mutex
	now     func() time.Time
	hash    []byte
	expires time.Time
	fails   int
	issued  []time.Time
}

func newRecoveryCodes(now func() time.Time) *recoveryCodes { return &recoveryCodes{now: now} }

var errRecoveryRate = &Error{Status: 429, Msg: "too many recovery codes generated, try again later"}

// issue makes a new code (the previous one is cancelled) and returns it
// formatted for display, XXXX-XXXX-XXXX.
func (rc *recoveryCodes) issue() (string, time.Time, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	now := rc.now()
	var recent []time.Time
	for _, t := range rc.issued {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	rc.issued = recent
	if len(recent) >= recoveryMaxIssued {
		return "", time.Time{}, errRecoveryRate
	}
	code := make([]byte, 0, recoveryLen)
	limit := 256 - 256%len(recoveryAlphabet) // uniform: bytes above are drawn again
	for b := make([]byte, 1); len(code) < recoveryLen; {
		_, _ = rand.Read(b)
		if int(b[0]) < limit {
			code = append(code, recoveryAlphabet[int(b[0])%len(recoveryAlphabet)])
		}
	}
	h := sha256.Sum256(code)
	rc.hash, rc.expires, rc.fails = h[:], now.Add(recoveryTTL), 0
	rc.issued = append(rc.issued, now)
	s := string(code)
	return s[:4] + "-" + s[4:8] + "-" + s[8:], rc.expires, nil
}

// normalizeCode accepts what people type: lower case, spaces, dashes.
func normalizeCode(s string) string {
	s = strings.ToUpper(s)
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		return r
	}, s)
}

// matchLocked tells whether code is the current valid code; a wrong code
// counts towards burning it.
func (rc *recoveryCodes) matchLocked(code string) bool {
	if rc.hash == nil || !rc.now().Before(rc.expires) {
		rc.hash = nil
		return false
	}
	h := sha256.Sum256([]byte(normalizeCode(code)))
	if subtle.ConstantTimeCompare(h[:], rc.hash) == 1 {
		return true
	}
	if rc.fails++; rc.fails >= recoveryMaxFails {
		rc.hash = nil
	}
	return false
}

// check validates a code without using it (first step of the web form).
func (rc *recoveryCodes) check(code string) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.matchLocked(code)
}

// consume validates a code and uses it up: true once per code.
func (rc *recoveryCodes) consume(code string) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if !rc.matchLocked(code) {
		return false
	}
	rc.hash = nil
	return true
}

// clear cancels the current code (recovery codes switched off).
func (rc *recoveryCodes) clear() {
	rc.mu.Lock()
	rc.hash = nil
	rc.mu.Unlock()
}
