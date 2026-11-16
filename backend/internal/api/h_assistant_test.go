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
