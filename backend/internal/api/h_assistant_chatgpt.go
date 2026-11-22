package api

// ChatGPT account support for the assistant: instead of a pasted API key,
// the NAS reuses the sign-in flow of OpenAI's own coding tools (an OAuth
// authorization-code exchange with PKCE on auth.openai.com). The
// administrator opens the sign-in address on any machine, signs in, and
// pastes the address of the callback (a localhost address the NAS never
// sees) into the form; the NAS exchanges the code for tokens, keeps them in
// its settings and refreshes them itself. The calls then go to the Codex
// backend of chatgpt.com, whose "Responses" schema is translated to and from
// the assistant's messages and tools below.
//
// Only this client id and loopback redirect are accepted by the sign-in
// audiences: the browser of the user lands on his own machine, the code is
// exchanged server-side by the NAS (PKCE keeps the exchange bound to the
// verifier the NAS generated).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"strataos/internal/store"
)

var (
	chatgptAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	chatgptTokenURL     = "https://auth.openai.com/oauth/token"
	chatgptAPIBase      = "https://chatgpt.com/backend-api/codex"
)

const (
	// Public client id and loopback redirect of OpenAI's coding CLI.
	chatgptClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatgptRedirect = "http://localhost:1455/auth/callback"
	// Model chosen when an account is connected and none was set (or the
	// model belongs to the previous endpoint); it stays changeable live.
	chatgptDefaultModel = "gpt-5.1-codex-max"
)

// chatgptWait is one started sign-in: the PKCE verifier matching the code
// challenge sent in the sign-in address, keyed by state, for ten minutes.
type chatgptWait struct {
	verifier string
	expire   time.Time
}

// chatgptStart builds the sign-in address of a new pending sign-in and
// remembers its code verifier for the paste-back.
func (s *Server) chatgptStart() string {
	verifier := token() + token() // 128 hex chars, within the PKCE 43..128 range
	sum := sha256.Sum256([]byte(verifier))
	state := token()[:16]
	s.chatgptMu.Lock()
	if s.chatgptWaits == nil {
		s.chatgptWaits = map[string]chatgptWait{}
	}
	for k, v := range s.chatgptWaits {
		if time.Now().After(v.expire) {
			delete(s.chatgptWaits, k)
		}
	}
	s.chatgptWaits[state] = chatgptWait{verifier, time.Now().Add(10 * time.Minute)}
	s.chatgptMu.Unlock()
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {chatgptClientID},
		"redirect_uri":          {chatgptRedirect},
		"scope":                 {"openid profile email offline_access"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	return chatgptAuthorizeURL + "?" + q.Encode()
}

// chatgptFinish takes the callback address the user pasted back (it contains
// the code and the state), exchanges the code for tokens and stores them.
func (s *Server) chatgptFinish(pasted string) (store.Settings, error) {
	u, err := url.Parse(strings.TrimSpace(pasted))
	if err != nil {
		return store.Settings{}, badRequest("the ChatGPT sign-in failed: paste the whole callback address")
	}
	q := u.Query()
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		return store.Settings{}, badRequest("the ChatGPT sign-in failed: the pasted address carries no code")
	}
	s.chatgptMu.Lock()
	wait, ok := s.chatgptWaits[state]
	delete(s.chatgptWaits, state)
	s.chatgptMu.Unlock()
	if !ok || time.Now().After(wait.expire) {
		return store.Settings{}, badRequest("the ChatGPT sign-in failed: start a new sign-in (state unknown or expired)")
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := chatgptTokenRequest(map[string]any{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  chatgptRedirect,
		"client_id":     chatgptClientID,
		"code_verifier": wait.verifier,
	}, &tok); err != nil {
		return store.Settings{}, err
	}
	if tok.Error != nil && tok.Error.Message != "" {
		return store.Settings{}, badRequest("the ChatGPT sign-in failed: " + tok.Error.Message)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		return store.Settings{}, badRequest("the ChatGPT sign-in failed: the tokens are incomplete")
	}
	claims := jwtClaims(tok.IDToken)
	set := store.Settings{
		AssistantProvider: "chatgpt",
		ChatGPTAccess:     tok.AccessToken,
		ChatGPTRefresh:    tok.RefreshToken,
		ChatGPTAccount:    strClaim(claims, "chatgpt_account_id"),
		ChatGPTEmail:      strClaim(claims, "email"),
		ChatGPTExpires:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
	}
	s.store.Read(func(st *store.State) {
		set.AssistantModel = st.Settings.AssistantModel
	})
	// A model of the previous endpoint does not exist on chatgpt.com.
	if set.AssistantModel == "" || !strings.HasPrefix(set.AssistantModel, "gpt-") {
		set.AssistantModel = chatgptDefaultModel
	}
	return set, nil
}

// chatgptToken returns a valid access token, renewing it from the refresh
// token when it is missing, expired or about to (one minute of leeway).
func (s *Server) chatgptToken(ctx context.Context, force bool) (string, error) {
	var set store.Settings
	s.store.Read(func(st *store.State) { set = st.Settings })
	if set.ChatGPTRefresh == "" {
		return "", badRequest("no ChatGPT account is connected")
	}
	if !force && set.ChatGPTAccess != "" && time.Until(set.ChatGPTExpires) > time.Minute {
		return set.ChatGPTAccess, nil
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := chatgptTokenRequest(map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": set.ChatGPTRefresh,
		"client_id":     chatgptClientID,
		"scope":         "openid profile email offline_access",
	}, &tok); err != nil {
		return "", err
	}
	// A refused refresh token (revoked session, password change) means a new
	// sign-in, not a transient failure.
	if tok.Error != nil && tok.Error.Message != "" {
		return "", badRequest("the ChatGPT session expired: connect the account again")
	}
	if tok.AccessToken == "" {
		return "", badRequest("the ChatGPT session expired: connect the account again")
	}
	// A rotated refresh token replaces the old one; the account id and the
	// email do not change (they come from the sign-in id_token).
	access, refresh := tok.AccessToken, tok.RefreshToken
	if refresh == "" {
		refresh = set.ChatGPTRefresh
	}
	if err := s.store.Update(func(st *store.State) error {
		st.Settings.ChatGPTAccess = access
		st.Settings.ChatGPTRefresh = refresh
		st.Settings.ChatGPTExpires = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		return nil
	}); err != nil {
		return "", err
	}
	return access, nil
}

// chatgptTokenRequest posts one token exchange/refresh to auth.openai.com.
func chatgptTokenRequest(payload map[string]any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, chatgptTokenURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := llmClient.Do(req)
	if err != nil {
		return llmDoError(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return badRequest("the ChatGPT sign-in failed: invalid token answer")
	}
	return nil
}

// chatgptDo posts one request to the chatgpt.com Responses endpoint with a
// valid account token: a 401 triggers one renewal and a single retry.
func (s *Server) chatgptDo(ctx context.Context, set store.Settings, body []byte) (*http.Response, error) {
	send := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptAPIBase+"/responses", bytes.NewReader(body))
		if err != nil {
			return nil, badRequest("invalid assistant request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		if set.ChatGPTAccount != "" {
			req.Header.Set("chatgpt-account-id", set.ChatGPTAccount)
		}
		return llmClient.Do(req)
	}
	acc, err := s.chatgptToken(ctx, false)
	if err != nil {
		return nil, err
	}
	resp, err := send(acc)
	if err != nil {
		return nil, llmDoError(err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if acc, err = s.chatgptToken(ctx, true); err != nil {
			return nil, err
		}
		resp, err = send(acc)
		if err != nil {
			return nil, llmDoError(err)
		}
	}
	return resp, nil
}

// chatgptItem is one output item of the Responses schema: the answer text of
// the model or one tool call it made.
type chatgptItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
}

// textText joins the text parts of a message item ("output_text").
func (it *chatgptItem) text() string {
	var b strings.Builder
	for _, c := range it.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// chatgptCalls returns the tool calls of a list of output items, translated
// back to the assistant's representation.
func chatgptCalls(items []chatgptItem) []llmToolCall {
	var out []llmToolCall
	for i, it := range items {
		if it.Type != "function_call" || it.Name == "" {
			continue
		}
		var tc llmToolCall
		tc.ID = it.CallID
		tc.Index = i
		tc.Type = "function"
		tc.Function.Name = it.Name
		tc.Function.Arguments = it.Arguments
		out = append(out, tc)
	}
	return out
}

// chatgptTools converts the assistant tools to the flat form of the Responses
// schema (the function fields are not nested under "function").
func chatgptTools() []map[string]any {
	out := []map[string]any{}
	for _, t := range assistantTools() {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Function.Name,
			"description": t.Function.Description,
			"parameters":  t.Function.Parameters,
		})
	}
	return out
}

// chatgptInput converts the conversation to the input items of the Responses
// schema: user and assistant texts, the calls the model made and their
// results.
func chatgptInput(messages []assistantMessage) []map[string]any {
	var out []map[string]any
	for _, m := range messages {
		switch {
		case m.Role == "tool":
			out = append(out, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			if m.Content != "" {
				out = append(out, map[string]any{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": m.Content}}})
			}
			for _, tc := range m.ToolCalls {
				out = append(out, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Function.Name, "arguments": tc.Function.Arguments})
			}
		default:
			kind := "input_text"
			if m.Role == "assistant" {
				kind = "output_text"
			}
			out = append(out, map[string]any{"role": m.Role, "content": []map[string]any{{"type": kind, "text": m.Content}}})
		}
	}
	return out
}

// chatgptBody builds the request of the Responses endpoint.
func chatgptBody(set store.Settings, messages []assistantMessage, stream bool) ([]byte, error) {
	payload := map[string]any{
		"model":               set.AssistantModel,
		"instructions":        assistantPrompt,
		"input":               chatgptInput(messages),
		"tools":               chatgptTools(),
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"store":               false,
		"reasoning":           map[string]any{"summary": "auto"},
	}
	if stream {
		payload["stream"] = true
	}
	return json.Marshal(payload)
}

// chatgptChat calls the chatgpt.com Responses endpoint without streaming.
func (s *Server) chatgptChat(ctx context.Context, set store.Settings, messages []assistantMessage) (*llmResponse, error) {
	body, err := chatgptBody(set, messages, false)
	if err != nil {
		return nil, err
	}
	resp, err := s.chatgptDo(ctx, set, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Output []chatgptItem `json:"output"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, badRequest(fmt.Sprintf("the assistant endpoint answered %d with an invalid body", resp.StatusCode))
	}
	if out.Error != nil {
		return nil, badRequest(fmt.Sprintf("the assistant endpoint answered %d: %s", resp.StatusCode, out.Error.Message))
	}
	if resp.StatusCode/100 != 2 {
		return nil, badRequest(fmt.Sprintf("the assistant endpoint answered %d", resp.StatusCode))
	}
	msg := &assistantMessage{}
	for _, it := range out.Output {
		if it.Type == "message" {
			msg.Content += it.text()
		}
	}
	msg.ToolCalls = chatgptCalls(out.Output)
	return &llmResponse{Choices: []struct {
		Message assistantMessage `json:"message"`
	}{{*msg}}}, nil
}

// chatgptStream is the streaming variant of chatgptChat: the deltas of the
// answer (and of the reasoning summaries of the thinking models) are
// forwarded as they arrive, and the tool calls come from the completed
// output items.
func (s *Server) chatgptStream(ctx context.Context, set store.Settings, messages []assistantMessage, onDelta func(string), onReasoning func(string)) (*assistantMessage, error) {
	body, err := chatgptBody(set, messages, true)
	if err != nil {
		return nil, err
	}
	resp, err := s.chatgptDo(ctx, set, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Surface the provider error message when there is one.
		var out struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}
		detail := ""
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) == nil && out.Error != nil {
			detail = ": " + out.Error.Message
		}
		return nil, badRequest(fmt.Sprintf("the assistant endpoint answered %d%s", resp.StatusCode, detail))
	}
	msg := &assistantMessage{}
	var calls []llmToolCall
	fail := func(text string) error {
		if text == "" {
			text = "the assistant endpoint stream failed"
		}
		return badRequest(text)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 64<<20))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			// deltas of the answer text and of the reasoning summaries
			Delta string `json:"delta,omitempty"`
			// response.output_item.done: the completed item
			Item *chatgptItem `json:"item,omitempty"`
			// response.failed
			Response *struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error,omitempty"`
			} `json:"response,omitempty"`
			// top-level error events
			Message string `json:"message,omitempty"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "response.output_text.delta" && ev.Delta != "":
			msg.Content += ev.Delta
			onDelta(ev.Delta)
		case ev.Type == "response.reasoning_summary_text.delta" && ev.Delta != "":
			msg.Reasoning += ev.Delta
			onReasoning(ev.Delta)
		case ev.Type == "response.output_item.done" && ev.Item != nil && ev.Item.Type == "function_call":
			calls = append(calls, llmToolCall{ID: ev.Item.CallID, Index: len(calls), Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{ev.Item.Name, ev.Item.Arguments}})
		case ev.Type == "response.failed":
			text := ""
			if ev.Response != nil && ev.Response.Error != nil {
				text = "the assistant endpoint answered: " + ev.Response.Error.Message
			}
			return nil, fail(text)
		case ev.Type == "error":
			return nil, fail(ev.Message)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, badRequest("the assistant endpoint stream failed")
	}
	msg.ToolCalls = calls
	return msg, nil
}

// jwtClaims decodes the payload claims of a JWT (no signature check: the
// token arrives directly from the issuer over TLS, only its claims are read).
func jwtClaims(idToken string) map[string]any {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return map[string]any{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return map[string]any{}
	}
	claims := map[string]any{}
	_ = json.Unmarshal(raw, &claims)
	return claims
}

// strClaim returns one string claim of a decoded JWT payload.
func strClaim(claims map[string]any, name string) string {
	if v, ok := claims[name].(string); ok {
		return v
	}
	return ""
}

// chatgptRoutes adds the account sign-in endpoints (administrators only:
// they decide whose account pays the answers).
func (s *Server) chatgptRoutes() {
	s.admin("POST /api/assistant/chatgpt/start", func(r *http.Request) (any, error) {
		return map[string]string{"url": s.chatgptStart()}, nil
	})

	s.admin("POST /api/assistant/chatgpt/finish", func(r *http.Request) (any, error) {
		var req struct {
			URL string `json:"url"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		set, err := s.chatgptFinish(req.URL)
		if err != nil {
			return nil, err
		}
		if err := s.store.Update(func(st *store.State) error {
			st.Settings.AssistantProvider = set.AssistantProvider
			st.Settings.AssistantModel = set.AssistantModel
			st.Settings.ChatGPTAccess = set.ChatGPTAccess
			st.Settings.ChatGPTRefresh = set.ChatGPTRefresh
			st.Settings.ChatGPTAccount = set.ChatGPTAccount
			st.Settings.ChatGPTEmail = set.ChatGPTEmail
			st.Settings.ChatGPTExpires = set.ChatGPTExpires
			return nil
		}); err != nil {
			return nil, err
		}
		s.audit(r, "assistant", "ChatGPT account connected"+emailSuffix(set.ChatGPTEmail))
		return map[string]string{"email": set.ChatGPTEmail, "model": set.AssistantModel}, nil
	})

	s.admin("POST /api/assistant/chatgpt/disconnect", func(r *http.Request) (any, error) {
		err := s.store.Update(func(st *store.State) error {
			st.Settings.AssistantProvider = ""
			st.Settings.ChatGPTAccess = ""
			st.Settings.ChatGPTRefresh = ""
			st.Settings.ChatGPTAccount = ""
			st.Settings.ChatGPTEmail = ""
			st.Settings.ChatGPTExpires = time.Time{}
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "assistant", "ChatGPT account disconnected")
		return nil, nil
	})
}

// emailSuffix keeps the audit line short (" (bob@example.com)") and empty
// when the account carries no email address.
func emailSuffix(email string) string {
	if email == "" {
		return ""
	}
	return " (" + email + ")"
}
