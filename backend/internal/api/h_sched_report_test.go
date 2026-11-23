package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

// A scheduled AI report runs the assistant read-only: a write tool asked by
// the model is refused, the report is still produced and sent to the feed.
func TestScheduledReport(t *testing.T) {
	fl := &fakeLLM{steps: []fakeStep{
		{tool: "trash_file", args: `{"path":"/Docs/secret.txt"}`}, // must be refused
		{content: "Everything is fine on this NAS."},
	}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()

	s := newTestServer(t)
	s.systemRoutes()
	s.assistantRoutes()
	s.scheduleRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "root", Role: "admin", PasswordHash: hash})
		st.Settings.AssistantURL = llm.URL
		st.Settings.AssistantKey = "sk-test"
		st.Settings.AssistantModel = "test-model"
		return nil
	})

	// The schedule can be created through the API with a prompt.
	tok := s.sessions.create("root", "admin", "127.0.0.1", time.Hour)
	req := httptest.NewRequest("POST", "/api/schedules", strings.NewReader(`{"name":"Weekly summary","type":"report","cron":"0 8 * * 1","enabled":true,"prompt":"Summarize the NAS","targetKind":"vm","target":"x"}`))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	req.Header.Set("X-Strata", "1")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("create report schedule: %d %s", rec.Code, rec.Body.String())
	}
	var saved store.Schedule
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Type != "report" || saved.Prompt != "Summarize the NAS" || saved.Target != "" || saved.TargetKind != "" {
		t.Fatalf("saved = %+v", saved)
	}

	_, before := s.mobileFeedSince(0, 1)
	msg, err := s.runScheduledReport(context.Background(), "", func(string, ...any) {})
	if err != nil {
		t.Fatalf("runScheduledReport: %v", err)
	}
	if !strings.Contains(msg, "notification") {
		t.Errorf("summary = %q", msg)
	}
	// The model was called twice; the second request carries the refusal of
	// the write tool, not an armed action.
	fl.mu.Lock()
	second := fl.bodies[1]
	fl.mu.Unlock()
	b, _ := json.Marshal(second["messages"])
	if !strings.Contains(string(b), "Refused: a scheduled report only reads the NAS.") {
		t.Errorf("the write tool was not refused: %s", b)
	}
	// The report reached the notification feed (notify is asynchronous).
	found := false
	for i := 0; i < 40 && !found; i++ {
		evs, _ := s.mobileFeedSince(before, 10)
		for _, e := range evs {
			if strings.HasPrefix(e.Subject, "AI report of ") && e.Message == "Everything is fine on this NAS." && e.Kind == "report" {
				found = true
			}
		}
		if !found {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !found {
		evs, _ := s.mobileFeedSince(before, 10)
		t.Errorf("no AI report in the feed: %+v", evs)
	}
}
