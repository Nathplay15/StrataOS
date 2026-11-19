package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"strataos/internal/store"
)

// assistantServer wires the assistant routes with a fake OpenAI-compatible
// endpoint and returns the server, its test URL and a token for bob.
func assistantServer(t *testing.T, llmURL string, configure bool) (*Server, *httptest.Server, string) {
	t.Helper()
	s := newTestServer(t)
	s.systemRoutes()
	s.tokenRoutes()
	s.assistantRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "bob", PasswordHash: hash})
		if configure {
			st.Settings.AssistantURL = llmURL
			st.Settings.AssistantKey = "sk-test"
			st.Settings.AssistantModel = "test-model"
		}
		return nil
	})
	ts := httptest.NewServer(http.HandlerFunc(s.ServeHTTP))
	t.Cleanup(ts.Close)
	return s, ts, createToken(t, s, "bob", []string{"read", "write"}, 0)
}

func assistantDo(t *testing.T, tsURL, tok, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	r, _ := http.NewRequest(method, tsURL+path, rd)
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

// fakeStep is one scripted answer: either a tool call or a final content.
type fakeStep struct {
	tool    string
	args    string
	content string
}

// fakeLLM answers each call with the next scripted step and records the
// requests it received.
type fakeLLM struct {
	mu     sync.Mutex
	calls  int
	bodies []map[string]any
	steps  []fakeStep
}

func (f *fakeLLM) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	n := f.calls
	f.calls++
	f.bodies = append(f.bodies, body)
	step := f.steps[min(n, len(f.steps)-1)]
	f.mu.Unlock()
	msg := map[string]any{"role": "assistant"}
	if step.content != "" {
		msg["content"] = step.content
	} else {
		msg["content"] = ""
		msg["tool_calls"] = []map[string]any{{
			"id": fmt.Sprintf("call_%d", n), "type": "function",
			"function": map[string]any{"name": step.tool, "arguments": step.args},
		}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": msg}}})
}

func TestAssistantConfig(t *testing.T) {
	_, ts, tok := assistantServer(t, "http://unused", true)
	resp, body := assistantDo(t, ts.URL, tok, "GET", "/api/assistant/config", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("config: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Enabled bool   `json:"enabled"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Enabled || out.Model != "test-model" {
		t.Fatalf("config = %+v", out)
	}
}

func TestAssistantChatNotConfigured(t *testing.T) {
	_, ts, tok := assistantServer(t, "", false)
	resp, body := assistantDo(t, ts.URL, tok, "POST", "/api/assistant/chat",
		map[string]any{"messages": []map[string]string{{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 400 {
		t.Fatalf("chat without configuration: %d %s", resp.StatusCode, body)
	}
}

func TestAssistantChatToolLoop(t *testing.T) {
	fl := &fakeLLM{steps: []fakeStep{
		{tool: "nas_status", args: "{}"},
		{content: "all good"},
	}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()
	_, ts, tok := assistantServer(t, llm.URL, true)
	resp, body := assistantDo(t, ts.URL, tok, "POST", "/api/assistant/chat",
		map[string]any{"messages": []map[string]string{{"role": "user", "content": "how is my NAS?"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Reply   string `json:"reply"`
		Actions []struct {
			Token string `json:"token"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Reply != "all good" || len(out.Actions) != 0 {
		t.Fatalf("chat = %+v", out)
	}
	if len(fl.bodies) != 2 {
		t.Fatalf("the model was called %d times", len(fl.bodies))
	}
	// The second call must carry the tool result of nas_status.
	second, _ := json.Marshal(fl.bodies[1])
	if !strings.Contains(string(second), `"role":"tool"`) || !strings.Contains(string(second), "version") {
		t.Fatalf("the tool result is missing from the second call: %.400s", second)
	}
}

func TestAssistantWriteConfirmation(t *testing.T) {
	fl := &fakeLLM{steps: []fakeStep{{tool: "create_folder", args: `{"path":"/Data/newdir"}`}}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()
	_, ts, tok := assistantServer(t, llm.URL, true)
	resp, body := assistantDo(t, ts.URL, tok, "POST", "/api/assistant/chat",
		map[string]any{"messages": []map[string]string{{"role": "user", "content": "create a folder"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Actions []struct {
			Token   string `json:"token"`
			Tool    string `json:"tool"`
			Summary string `json:"summary"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Actions) != 1 || !strings.HasPrefix(out.Actions[0].Token, "cfm_") {
		t.Fatalf("expected one armed action, got: %s", body)
	}
	if out.Actions[0].Tool != "create_folder" || !strings.Contains(out.Actions[0].Summary, "/Data/newdir") {
		t.Fatalf("unexpected action: %+v", out.Actions[0])
	}
	tok1 := out.Actions[0].Token

	// Confirming runs the tool through the API (here it fails on the missing
	// share, which proves it reached the API).
	resp, body = assistantDo(t, ts.URL, tok, "POST", "/api/assistant/confirm", map[string]any{"token": tok1})
	if resp.StatusCode != 200 || !strings.Contains(string(body), "result") || strings.Contains(string(body), "unknown tool") {
		t.Fatalf("confirm: %d %s", resp.StatusCode, body)
	}

	// The token is single-use.
	resp, _ = assistantDo(t, ts.URL, tok, "POST", "/api/assistant/confirm", map[string]any{"token": tok1})
	if resp.StatusCode != 400 {
		t.Fatalf("a confirmation token must be single-use: %d", resp.StatusCode)
	}
}

func TestAssistantConfirmOfAnotherUser(t *testing.T) {
	fl := &fakeLLM{steps: []fakeStep{{tool: "create_folder", args: `{"path":"/Data/newdir"}`}}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()
	s, ts, tok := assistantServer(t, llm.URL, true)
	resp, body := assistantDo(t, ts.URL, tok, "POST", "/api/assistant/chat",
		map[string]any{"messages": []map[string]string{{"role": "user", "content": "create a folder"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Actions []struct {
			Token string `json:"token"`
		} `json:"actions"`
	}
	_ = json.Unmarshal(body, &out)
	if len(out.Actions) != 1 {
		t.Fatalf("expected one armed action, got: %s", body)
	}
	// Another user cannot consume the token.
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "alice", PasswordHash: hash})
		return nil
	})
	other := createToken(t, s, "alice", []string{"read", "write"}, 0)
	resp, _ = assistantDo(t, ts.URL, other, "POST", "/api/assistant/confirm", map[string]any{"token": out.Actions[0].Token})
	if resp.StatusCode != 400 {
		t.Fatalf("a token armed by bob must not work for alice: %d", resp.StatusCode)
	}
}

func TestAssistantConversations(t *testing.T) {
	fl := &fakeLLM{steps: []fakeStep{{content: "here is the answer"}}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()
	s, ts, tok := assistantServer(t, llm.URL, true)

	// No conversation yet.
	resp, body := assistantDo(t, ts.URL, tok, "GET", "/api/assistant/conversations", nil)
	if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("empty list: %d %s", resp.StatusCode, body)
	}

	// One chat creates a conversation.
	resp, body = assistantDo(t, ts.URL, tok, "POST", "/api/assistant/chat",
		map[string]any{"messages": []map[string]string{{"role": "user", "content": "how is my NAS?"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Reply          string `json:"reply"`
		ConversationID string `json:"conversationId"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Reply != "here is the answer" || out.ConversationID == "" {
		t.Fatalf("chat = %+v", out)
	}
	convID := out.ConversationID

	// The list shows it with the first user message as title.
	resp, body = assistantDo(t, ts.URL, tok, "GET", "/api/assistant/conversations", nil)
	var list []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(body, &list); err != nil || len(list) != 1 {
		t.Fatalf("list: %d %s %v", resp.StatusCode, body, err)
	}
	if list[0].ID != convID || list[0].Title != "how is my NAS?" {
		t.Fatalf("list = %+v", list)
	}

	// Following the conversation keeps the same id and appends messages.
	resp, body = assistantDo(t, ts.URL, tok, "POST", "/api/assistant/chat",
		map[string]any{"conversationId": convID, "messages": []map[string]string{
			{"role": "user", "content": "how is my NAS?"}, {"role": "assistant", "content": "here is the answer"},
			{"role": "user", "content": "and the disks?"},
		}})
	if resp.StatusCode != 200 {
		t.Fatalf("follow-up chat: %d %s", resp.StatusCode, body)
	}
	out = struct {
		Reply          string `json:"reply"`
		ConversationID string `json:"conversationId"`
	}{}
	if err := json.Unmarshal(body, &out); err != nil || out.ConversationID != convID {
		t.Fatalf("follow-up = %s %v", body, err)
	}

	// The saved conversation holds the user/assistant messages only.
	resp, body = assistantDo(t, ts.URL, tok, "GET", "/api/assistant/conversations/"+convID, nil)
	var chat store.AssistantChat
	if err := json.Unmarshal(body, &chat); err != nil {
		t.Fatal(err)
	}
	if chat.ID != convID || len(chat.Messages) != 4 {
		t.Fatalf("chat = %+v", chat)
	}
	if chat.Messages[2].Content != "and the disks?" || chat.Messages[3].Content != "here is the answer" {
		t.Fatalf("messages = %+v", chat.Messages)
	}

	// Another user cannot read the conversation.
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "alice", PasswordHash: hash})
		return nil
	})
	other := createToken(t, s, "alice", []string{"read"}, 0)
	resp, _ = assistantDo(t, ts.URL, other, "GET", "/api/assistant/conversations/"+convID, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("alice must not read bob's conversation: %d", resp.StatusCode)
	}

	// Delete removes it for its owner.
	resp, _ = assistantDo(t, ts.URL, tok, "DELETE", "/api/assistant/conversations/"+convID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = assistantDo(t, ts.URL, tok, "GET", "/api/assistant/conversations/"+convID, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("deleted conversation must be gone: %d", resp.StatusCode)
	}
}

// fakeStreamLLM answers each call with the next scripted SSE body.
type fakeStreamLLM struct {
	mu     sync.Mutex
	calls  int
	bodies []string
}

func (f *fakeStreamLLM) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	n := f.calls
	f.calls++
	b := f.bodies[min(n, len(f.bodies)-1)]
	f.mu.Unlock()
	if b == "" {
		b = "data: [DONE]\n\n"
	}
	if v, ok := body["stream"].(bool); !ok || !v {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"stream expected"}}`))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(b))
}

func TestAssistantChatStream(t *testing.T) {
	fl := &fakeStreamLLM{bodies: []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"nas_status\",\"arguments\":\"\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{}\"}}]}}]}\n\n" +
			"data: [DONE]\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\" all good\"}}]}\n\n" +
			"data: [DONE]\n\n",
	}}
	llm := httptest.NewServer(http.HandlerFunc(fl.handler))
	defer llm.Close()
	_, ts, tok := assistantServer(t, llm.URL, true)

	r, _ := http.NewRequest("POST", ts.URL+"/api/assistant/chat/stream",
		strings.NewReader(`{"messages":[{"role":"user","content":"how is my NAS?"}]}`))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{
		`{"type":"delta","text":"Hel"}`,
		`{"type":"delta","text":"lo"}`,
		`{"type":"status","tool":"nas_status"}`,
		`{"type":"delta","text":" all good"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing event %s in:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `"type":"done"`) || !strings.Contains(body, `"conversationId"`) {
		t.Fatalf("missing done event in:\n%s", body)
	}

	// The conversation was saved with the full answer (deltas joined).
	var done struct {
		ConversationID string `json:"conversationId"`
	}
	if i := strings.LastIndex(body, `"type":"done"`); i >= 0 {
		start := strings.LastIndex(body[:i], "data: ")
		if err := json.Unmarshal([]byte(body[start+6:]), &done); err != nil {
			t.Fatal(err)
		}
	}
	resp2, body2 := assistantDo(t, ts.URL, tok, "GET", "/api/assistant/conversations/"+done.ConversationID, nil)
	var chat store.AssistantChat
	if err := json.Unmarshal(body2, &chat); err != nil || resp2.StatusCode != 200 {
		t.Fatalf("saved conversation: %d %s", resp2.StatusCode, body2)
	}
	if len(chat.Messages) != 2 || chat.Messages[1].Content != "Hello all good" {
		t.Fatalf("messages = %+v", chat.Messages)
	}
	if len(fl.bodies) < 2 {
		t.Fatalf("the model must be called again after the tool result")
	}
}

func TestLLMEndpoint(t *testing.T) {
	// The setting is a base URL: /chat/completions is appended.
	if got := llmEndpoint("https://api.mistral.ai/v1"); got != "https://api.mistral.ai/v1/chat/completions" {
		t.Errorf("base URL: got %s", got)
	}
	// A full endpoint pasted from the provider documentation is kept as is,
	// with or without a trailing slash, instead of being doubled.
	for _, in := range []string{
		"https://api.mistral.ai/v1/chat/completions",
		"https://api.mistral.ai/v1/chat/completions/",
		" https://api.mistral.ai/v1/chat/completions ",
	} {
		if got := llmEndpoint(in); got != "https://api.mistral.ai/v1/chat/completions" {
			t.Errorf("full endpoint %q: got %s", in, got)
		}
	}
}

func TestFnToolSchema(t *testing.T) {
	// Strict providers (e.g. Mistral) reject "required": null with
	// "invalid tool schema: None is not of type 'array'": a tool without
	// required arguments must not carry the key at all.
	b, err := json.Marshal(fnTool("no_args", "A tool without arguments.", nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"required"`) {
		t.Errorf("empty required must be omitted, got %s", b)
	}
	// Tools with required arguments keep them, as a real array.
	b, err = json.Marshal(fnTool("with_args", "A tool with arguments.", map[string]any{"path": strProp("Path")}, "path"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"required":["path"]`) {
		t.Errorf("required args missing, got %s", b)
	}
}
