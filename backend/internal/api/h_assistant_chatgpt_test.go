package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos/internal/store"
)

// fakeIDToken builds a signed-looking JWT carrying the claims the ChatGPT
// sign-in reads (the fake servers write it, no signature is checked).
func fakeIDToken(email, accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"email": email, "chatgpt_account_id": accountID})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// chatgptServer wires the assistant and ChatGPT routes with an administrator.
func chatgptServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	s := newTestServer(t)
	s.systemRoutes()
	s.tokenRoutes()
	s.assistantRoutes()
	s.chatgptRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "bob", PasswordHash: hash, Role: "admin"})
		return nil
	})
	ts := httptest.NewServer(http.HandlerFunc(s.ServeHTTP))
	t.Cleanup(ts.Close)
	// The admin scope is what lets an API token act as its administrator.
	return s, ts, createToken(t, s, "bob", []string{"read", "write", "admin"}, 0)
}

// overrideChatgptURLs points the sign-in and the responses endpoint at the
// fake servers, and restores them when the test ends.
func overrideChatgptURLs(t *testing.T, tokenURL, apiBase string) {
	t.Helper()
	oldT, oldA := chatgptTokenURL, chatgptAPIBase
	chatgptTokenURL, chatgptAPIBase = tokenURL, apiBase
	t.Cleanup(func() { chatgptTokenURL, chatgptAPIBase = oldT, oldA })
}

// tokenAnswer writes one OAuth token answer with an id_token of the account.
func tokenAnswer(w http.ResponseWriter, access, refresh string, expire int) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": access, "refresh_token": refresh,
		"id_token": fakeIDToken("bob@example.com", "acct-1"), "expires_in": expire,
	})
}

func TestChatGPTSignIn(t *testing.T) {
	var verifiers []string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["grant_type"] != "authorization_code" {
			t.Errorf("grant type = %v", req["grant_type"])
		}
		if v, _ := req["code_verifier"].(string); len(v) < 43 {
			t.Errorf("code_verifier too short: %q", v)
		} else {
			verifiers = append(verifiers, v)
		}
		tokenAnswer(w, "at-1", "rt-1", 3600)
	}))
	t.Cleanup(auth.Close)
	overrideChatgptURLs(t, auth.URL, "http://unused.invalid")

	s, ts, tok := chatgptServer(t)
	out, code := assistantPost(t, ts, tok, "POST", "/api/assistant/chatgpt/start", nil)
	if code != 200 {
		t.Fatalf("start: %d %s", code, out)
	}
	var start struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(out, &start)
	u, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("sign-in url: %v", err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		t.Fatalf("sign-in url: %s", start.URL)
	}

	// Pasting the callback back exchanges the code and stores the tokens.
	callback := "http://localhost:1455/auth/callback?code=the-code&state=" + q.Get("state")
	out, code = assistantPost(t, ts, tok, "POST", "/api/assistant/chatgpt/finish", map[string]any{"url": callback})
	if code != 200 {
		t.Fatalf("finish: %d %s", code, out)
	}
	var set store.Settings
	s.store.Read(func(st *store.State) { set = st.Settings })
	if set.AssistantProvider != "chatgpt" || set.ChatGPTAccess != "at-1" || set.ChatGPTRefresh != "rt-1" {
		t.Fatalf("tokens: %+v", set)
	}
	if set.ChatGPTAccount != "acct-1" || set.ChatGPTEmail != "bob@example.com" {
		t.Fatalf("id token claims: %+v", set)
	}
	if set.AssistantModel != chatgptDefaultModel {
		t.Fatalf("model = %q, want %q", set.AssistantModel, chatgptDefaultModel)
	}
	if time.Until(set.ChatGPTExpires) < 59*time.Minute {
		t.Fatalf("expiry = %v", set.ChatGPTExpires)
	}
	if len(verifiers) != 1 {
		t.Fatalf("verifiers = %v", verifiers)
	}

	// The config endpoint reports the account as the provider.
	out, code = assistantPost(t, ts, tok, "GET", "/api/assistant/config", nil)
	if code != 200 || !strings.Contains(string(out), `"provider":"chatgpt"`) || !strings.Contains(string(out), `"enabled":true`) {
		t.Fatalf("config: %d %s", code, out)
	}

	// The tokens never leave the server (masked in the settings answer).
	out, code = assistantPost(t, ts, tok, "GET", "/api/settings", nil)
	if code != 200 {
		t.Fatalf("settings: %d", code)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	for _, f := range []string{"chatgptAccess", "chatgptRefresh"} {
		if got[f] != "********" {
			t.Fatalf("%s = %v, want the mask", f, got[f])
		}
	}

	// A state is one-time only.
	if _, code = assistantPost(t, ts, tok, "POST", "/api/assistant/chatgpt/finish", map[string]any{"url": callback}); code != 400 {
		t.Fatalf("replayed finish: %d", code)
	}
}

func TestChatGPTSignInRejected(t *testing.T) {
	s, ts, tok := chatgptServer(t)
	// No sign-in was started: an unknown state is refused.
	out, code := assistantPost(t, ts, tok, "POST", "/api/assistant/chatgpt/finish",
		map[string]any{"url": "http://localhost:1455/auth/callback?code=x&state=unknown"})
	if code != 400 || !strings.Contains(string(out), "failed") {
		t.Fatalf("unknown state: %d %s", code, out)
	}
	// A pasted address without a code is refused too.
	out, code = assistantPost(t, ts, tok, "POST", "/api/assistant/chatgpt/finish",
		map[string]any{"url": "https://example.com/nope"})
	if code != 400 {
		t.Fatalf("no code: %d %s", code, out)
	}
	// A non-admin cannot start a sign-in at all.
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "lee", PasswordHash: hash})
		return nil
	})
	lee := createToken(t, s, "lee", []string{"read", "admin"}, 0)
	if _, code = assistantPost(t, ts, lee, "POST", "/api/assistant/chatgpt/start", nil); code != 403 {
		t.Fatalf("non-admin start: %d", code)
	}
}

func TestChatGPTRefreshAndStream(t *testing.T) {
	var seen []string // Authorization headers of the API calls, in order
	var refreshes int
	var accountID string
	sse := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`data: {"type":"response.output_text.delta","delta":" world"}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call-1","name":"nas_status","arguments":"{}"}}`,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n\n")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		accountID = r.Header.Get("chatgpt-account-id")
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			Model        string           `json:"model"`
			Instructions string           `json:"instructions"`
			Input        []map[string]any `json:"input"`
			Stream       bool             `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "gpt-test" || !body.Stream || body.Instructions != assistantPrompt {
			t.Errorf("body = %+v", body)
		}
		if len(body.Input) == 0 || body.Input[0]["role"] != "user" {
			t.Errorf("input = %+v", body.Input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(api.Close)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["grant_type"] != "refresh_token" || req["refresh_token"] != "rt-old" {
			t.Errorf("refresh request = %v", req)
		}
		refreshes++
		tokenAnswer(w, "at-new", "rt-new", 3600)
	}))
	t.Cleanup(auth.Close)
	overrideChatgptURLs(t, auth.URL, api.URL)

	s := newTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Settings.AssistantProvider = "chatgpt"
		st.Settings.AssistantModel = "gpt-test"
		st.Settings.ChatGPTAccess = "at-old"
		st.Settings.ChatGPTRefresh = "rt-old"
		st.Settings.ChatGPTAccount = "acct-1"
		st.Settings.ChatGPTExpires = time.Now().Add(-time.Minute) // expired
		return nil
	})

	var deltas, reasoning []string
	msg, err := s.chatgptStream(context.Background(), func() store.Settings {
		var set store.Settings
		s.store.Read(func(st *store.State) { set = st.Settings })
		return set
	}(), []assistantMessage{{Role: "user", Content: "hi"}},
		func(text string) { deltas = append(deltas, text) },
		func(text string) { reasoning = append(reasoning, text) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if msg.Content != "Hello world" {
		t.Fatalf("content = %q", msg.Content)
	}
	if msg.Reasoning != "thinking" || len(deltas) != 2 || len(reasoning) != 1 {
		t.Fatalf("events: %q %q", deltas, reasoning)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call-1" || msg.ToolCalls[0].Function.Name != "nas_status" {
		t.Fatalf("tool calls: %+v", msg.ToolCalls)
	}
	// The expired access token triggered exactly one refresh before the call,
	// and the new tokens were saved.
	if refreshes != 1 || len(seen) != 1 || seen[0] != "Bearer at-new" {
		t.Fatalf("refresh: %d, auth %v", refreshes, seen)
	}
	if accountID != "acct-1" {
		t.Fatalf("account header = %q", accountID)
	}
	var set store.Settings
	s.store.Read(func(st *store.State) { set = st.Settings })
	if set.ChatGPTAccess != "at-new" || set.ChatGPTRefresh != "rt-new" || time.Until(set.ChatGPTExpires) < 59*time.Minute {
		t.Fatalf("saved tokens: %+v", set)
	}
}

func TestChatGPTInputMapping(t *testing.T) {
	conv := []assistantMessage{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "let me check", ToolCalls: []llmToolCall{{
			ID: "call-9", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "list_files", Arguments: `{"path":"/Docs"}`},
		}}},
		{Role: "tool", ToolCallID: "call-9", Content: "files..."},
	}
	in := chatgptInput(conv)
	if len(in) != 4 {
		t.Fatalf("items = %d", len(in))
	}
	if in[0]["role"] != "user" || in[3]["type"] != "function_call_output" || in[3]["call_id"] != "call-9" {
		t.Fatalf("map = %+v", in)
	}
	if in[1]["role"] != "assistant" {
		// the assistant text stays a message, the call its own item
		t.Fatalf("item 1 = %+v", in[1])
	}
	if in[2]["type"] != "function_call" || in[2]["name"] != "list_files" {
		t.Fatalf("item 2 = %+v", in[2])
	}
	// the tools use the flat Responses form
	tools := chatgptTools()
	if len(tools) == 0 || tools[0]["name"] == "" || tools[0]["type"] != "function" {
		t.Fatalf("tools = %+v", tools[0])
	}
}

// assistantPost is assistantDo without the query plumbing (JSON in, bytes out).
func assistantPost(t *testing.T, ts *httptest.Server, tok, method, path string, body any) ([]byte, int) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	r, _ := http.NewRequest(method, ts.URL+path, rd)
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
	return b, resp.StatusCode
}

// Concurrent refreshes with a rotating refresh token: one at a time, each
// with the newest token (the provider refuses a reused one).
func TestChatGPTConcurrentRefresh(t *testing.T) {
	s := newTestServer(t)
	var mu sync.Mutex
	current, n := "r0", 0
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		if body["refresh_token"] != current {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "refresh token reused"}})
			return
		}
		n++
		current = fmt.Sprintf("r%d", n)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("a%d", n), "refresh_token": current, "expires_in": 1})
	}))
	defer tokens.Close()
	old := chatgptTokenURL
	chatgptTokenURL = tokens.URL
	t.Cleanup(func() { chatgptTokenURL = old })
	_ = s.store.Update(func(st *store.State) error { st.Settings.ChatGPTRefresh = "r0"; return nil })
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.chatgptToken(context.Background(), true); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("refresh failed: %v", err)
	}
}
