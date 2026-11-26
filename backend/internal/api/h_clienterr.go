package api

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// clientErrors limits the error reports of the interface: a page that fails
// in a loop must not fill the log.
var clientErrors = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: map[string][]time.Time{}}

const clientErrorsMax = 10 // per user and 10 minutes

func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func allowClientError(user string, now time.Time) bool {
	clientErrors.Lock()
	defer clientErrors.Unlock()
	var kept []time.Time
	for _, t := range clientErrors.m[user] {
		if now.Sub(t) < 10*time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= clientErrorsMax {
		clientErrors.m[user] = kept
		return false
	}
	clientErrors.m[user] = append(kept, now)
	return true
}

func (s *Server) clientErrorRoutes() {
	// An error of the web interface (or the desktop app's window): the page,
	// the message and the stack, nothing else, into the NAS log.
	s.user("POST /api/client-errors", func(r *http.Request) (any, error) {
		var req struct {
			Page    string `json:"page"`
			Message string `json:"message"`
			Stack   string `json:"stack"`
			Kind    string `json:"kind"` // "render", "error", "rejection"
		}
		r.Body = http.MaxBytesReader(nil, r.Body, 16<<10)
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		user := userOf(r)
		if !allowClientError(user, time.Now()) {
			return nil, nil // dropped quietly
		}
		// The path only: no query string or fragment (they may hold names).
		page, _, _ := strings.Cut(req.Page, "?")
		page, _, _ = strings.Cut(page, "#")
		page = clip(page, 200)
		msg := clip(strings.TrimSpace(req.Message), 500)
		kind := map[string]string{"render": "render", "error": "error", "rejection": "rejection"}[req.Kind]
		slog.Error("interface error", "user", user, "page", page, "kind", kind, "message", msg, "stack", clip(req.Stack, 4000))
		s.store.Log("error", "interface", user, "Interface error on "+page+": "+msg)
		return nil, nil
	})
}
