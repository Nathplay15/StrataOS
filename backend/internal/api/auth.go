package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"strataos/internal/proxy"
	"strataos/internal/store"
	"strataos/internal/sys"
)

const cookieName = "strata_session"

type session struct {
	User    string    `json:"user"`
	Role    string    `json:"role"`
	Expires time.Time `json:"expires"`
	Created time.Time `json:"created"`
	IP      string    `json:"ip"`
}

// sessions are keyed by the SHA-256 of the cookie token and persisted to disk
// (0600), so restarting or upgrading the daemon does not log everybody out and
// a leaked sessions file does not reveal usable tokens.
type sessions struct {
	mu   sync.Mutex
	m    map[string]*session
	path string
}

func newSessions(path string) *sessions {
	s := &sessions{m: map[string]*session{}, path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s.m)
	}
	s.mu.Lock()
	s.pruneLocked()
	s.mu.Unlock()
	return s
}

func token() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func key(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *sessions) pruneLocked() {
	for k, v := range s.m {
		if time.Now().After(v.Expires) {
			delete(s.m, k)
		}
	}
}

func (s *sessions) saveLocked() {
	if s.path == "" {
		return
	}
	data, err := json.Marshal(s.m)
	if err == nil {
		err = sys.WriteFileAtomic(s.path, data, 0o600)
	}
	if err != nil {
		slog.Warn("save sessions", "err", err)
	}
}

func (s *sessions) create(user, role, ip string, ttl time.Duration) string {
	t := token()
	s.mu.Lock()
	s.pruneLocked()
	s.m[key(t)] = &session{User: user, Role: role, Expires: time.Now().Add(ttl), Created: time.Now(), IP: ip}
	s.saveLocked()
	s.mu.Unlock()
	return t
}

func (s *sessions) get(t string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.m[key(t)]
	if !ok {
		return nil
	}
	if time.Now().After(ss.Expires) {
		delete(s.m, key(t))
		s.saveLocked()
		return nil
	}
	return ss
}

func (s *sessions) delete(t string) {
	s.mu.Lock()
	delete(s.m, key(t))
	s.saveLocked()
	s.mu.Unlock()
}

// dropUser invalidates every session of a user (password change, deletion, role change).
func (s *sessions) dropUser(user string) {
	s.mu.Lock()
	for k, v := range s.m {
		if v.User == user {
			delete(s.m, k)
		}
	}
	s.saveLocked()
	s.mu.Unlock()
}

// limiter blocks an IP after too many failed logins.
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

const (
	limiterWindow = 10 * time.Minute
	limiterMax    = 8
)

// recentLocked returns the attempts of ip inside the window, dropping older ones.
func (l *limiter) recentLocked(ip string, now time.Time) []time.Time {
	var recent []time.Time
	for _, t := range l.hits[ip] {
		if now.Sub(t) < limiterWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(l.hits, ip)
	} else {
		l.hits[ip] = recent
	}
	return recent
}

// attempt records a login attempt before the (expensive) password check and reports
// whether it may proceed. Counting up front bounds the number of concurrent bcrypt
// checks a single client can trigger; a successful login calls reset.
func (l *limiter) attempt(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.hits) > 10000 { // forget idle clients so the map cannot grow forever
		for k := range l.hits {
			l.recentLocked(k, now)
		}
	}
	if len(l.recentLocked(ip, now)) >= limiterMax {
		return false
	}
	l.hits[ip] = append(l.hits[ip], now)
	return true
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	delete(l.hits, ip)
	l.mu.Unlock()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	// Through a tunnel running on the NAS (cloudflared…), the client is in
	// the forwarding headers: otherwise every visitor would look local.
	if f := proxy.ForwardedFor(host, r.Header); f != "" {
		return f
	}
	return host
}

func hashPassword(p string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(p), 12)
	return string(h), err
}

func checkPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

type ctxKey struct{}

func sessionFrom(ctx context.Context) *session {
	s, _ := ctx.Value(ctxKey{}).(*session)
	return s
}

func userOf(r *http.Request) string {
	if s := sessionFrom(r.Context()); s != nil {
		return s.User
	}
	return ""
}

func isAdmin(r *http.Request) bool {
	s := sessionFrom(r.Context())
	return s != nil && s.Role == "admin"
}

var errUnauthorized = &Error{Status: http.StatusUnauthorized, Msg: "authentication required"}
var errForbidden = &Error{Status: http.StatusForbidden, Msg: "administrator rights required"}

// authenticate attaches the session to the request or rejects it.
func (s *Server) authenticate(next http.Handler, adminOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			s.bearerAuth(w, r, next, adminOnly) // mobile app (mobile.go)
			return
		}
		c, err := r.Cookie(cookieName)
		var ss *session
		if err == nil {
			ss = s.sessions.get(c.Value)
		}
		if ss == nil {
			writeErr(w, errUnauthorized)
			return
		}
		// Reject sessions of users disabled or deleted since login.
		var ok bool
		s.store.Read(func(st *store.State) {
			u := st.User(ss.User)
			ok = u != nil && !u.Disabled
		})
		if !ok {
			s.sessions.delete(c.Value)
			writeErr(w, errUnauthorized)
			return
		}
		if adminOnly && ss.Role != "admin" {
			writeErr(w, errForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "cross-origin request rejected"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ss)))
	})
}

// sameOrigin is a CSRF guard on top of SameSite=Strict cookies.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		ref := r.Header.Get("Referer")
		if ref == "" {
			return r.Header.Get("X-Strata") != "" // API clients must send this header
		}
		origin = ref
	}
	origin = strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	if i := strings.Index(origin, "/"); i >= 0 {
		origin = origin[:i]
	}
	return origin == r.Host
}

var errBadLogin = errors.New("invalid username or password")

var dummyHash = sync.OnceValue(func() string {
	h, _ := hashPassword(token())
	return h
})
