package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"strataos/internal/scheduler"
	"strataos/internal/store"
)

// Scheduled AI reports (roadmap 0.15, "scheduled AI reports"): a schedule of
// type "report" asks the assistant for a summary of the NAS (health, new
// photos, failed backups by default) and sends it as a notification — and as
// an email when that channel is configured. The report only reads: write
// tools and the web are refused in the turn (assistantTurn.readonly).

// reportServer is the running server, so the package-level ReportRunner can
// reach it (the scheduler is a package-level runner like ActionRunner).
var reportServer atomic.Pointer[Server]

func init() {
	scheduler.ReportRunner = func(ctx context.Context, prompt string, logf func(string, ...any)) (string, error) {
		s := reportServer.Load()
		if s == nil {
			return "", fmt.Errorf("the NAS is starting")
		}
		return s.runScheduledReport(ctx, prompt, logf)
	}
}

// defaultReportPrompt is asked when the schedule has no custom instruction.
const defaultReportPrompt = "Write a short report about this NAS for its administrator: overall health (disks, RAID arrays, ZFS pools, volumes), the recent events and alerts, the backups of the last 30 days (failures first), the new photos of the last 7 days, and anything that needs attention. Use your read tools to check before writing, and cite the files you read."

// reportLangNames maps the interface language to its name, for the prompt.
var reportLangNames = map[string]string{
	"fr": "French", "en": "English", "es": "Spanish", "de": "German",
	"it": "Italian", "pt": "Portuguese", "nl": "Dutch", "zh": "Chinese",
}

// runScheduledReport runs one report: the assistant answers one prompt with
// its read tools (as a short-lived administrator session), and the answer is
// sent as a notification.
func (s *Server) runScheduledReport(ctx context.Context, prompt string, logf func(string, ...any)) (string, error) {
	set, err := s.assistantSettings()
	if err != nil {
		return "", err
	}
	var lang, admin string
	s.store.Read(func(st *store.State) {
		lang = st.MessageLang()
		for _, u := range st.Users {
			if u.Role == "admin" && !u.Disabled {
				admin = u.Username
				break
			}
		}
	})
	if admin == "" {
		return "", fmt.Errorf("no administrator account")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultReportPrompt
	}
	prompt += " Answer in " + reportLangNames[lang] + "."

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// The tools of the turn run as this short-lived administrator session.
	tok := s.sessions.create(admin, "admin", "127.0.0.1", 10*time.Minute)
	defer s.sessions.delete(tok)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://strata.internal/api/assistant/chat", nil)
	if err != nil {
		return "", err
	}
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	logf("Asking the assistant (%s)…", set.AssistantModel)
	t := &assistantTurn{
		set: set, inner: s.assistantReq(req), readonly: true,
		conv: []assistantMessage{{Role: "system", Content: assistantPrompt}, {Role: "user", Content: prompt}},
	}
	if err := s.assistantLoop(ctx, t); err != nil {
		return "", err
	}
	reply := strings.TrimSpace(t.reply)
	if reply == "" {
		return "", fmt.Errorf("the assistant returned no answer")
	}
	host, _ := hostname()
	subject := fmt.Sprintf("AI report of %s", host)
	s.store.Log("info", "assistant", admin, "Scheduled AI report sent ("+subject+")")
	s.notify("", subject, reply) // explicit send: no level filter, email included
	logf("Report sent as a notification (%d characters)", len(reply))
	slog.Info("scheduled AI report sent", "host", host, "chars", len(reply))
	return fmt.Sprintf("AI report sent as a notification (%d characters)", len(reply)), nil
}
