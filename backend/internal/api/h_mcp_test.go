package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"strataos/internal/store"
)

func mcpServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	s := newTestServer(t)
	s.systemRoutes()
	s.tokenRoutes()
	s.mcpRoutes()
	hash, _ := hashPassword("Secret-Pass-1")
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "bob", PasswordHash: hash})
		return nil
	})
	ts := httptest.NewServer(http.HandlerFunc(s.ServeHTTP))
	t.Cleanup(ts.Close)
	return s, ts, createToken(t, s, "bob", []string{"mcp"}, 0)
}

// mcpClient connects an in-process MCP client to the test server with tok.
func mcpClient(t *testing.T, url, tok string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	tr := &mcp.StreamableClientTransport{
		Endpoint:   url + "/api/mcp",
		HTTPClient: &http.Client{Transport: bearerRoundTripper{tok}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cs, err := client.Connect(ctx, tr, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

type bearerRoundTripper struct{ tok string }

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPRequiresScope(t *testing.T) {
	s, _, _ := mcpServer(t)
	read := createToken(t, s, "bob", []string{"read"}, 0)
	r, _ := http.NewRequest("POST", "https://host/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"t","version":"0"}}}`))
	r.Header.Set("Authorization", "Bearer "+read)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("read-scope token on /api/mcp: %d", w.Code)
	}
}

func TestMCPStatusTool(t *testing.T) {
	_, ts, tok := mcpServer(t)
	cs := mcpClient(t, ts.URL, tok)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "nas_status"})
	if err != nil {
		t.Fatalf("nas_status: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty result")
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, "version") {
		t.Fatalf("unexpected content: %.200s", txt)
	}
}

// A write tool never runs on the model's word: the confirmation token it
// gets only works once a person approved the request in the web interface
// (review 0.12.0: the MCP confirmation was returned to the model).
func TestMCPWriteToolConfirmation(t *testing.T) {
	s, ts, _ := mcpServer(t)
	wtok := createToken(t, s, "bob", []string{"mcp", "write"}, 0)
	cs := mcpClient(t, ts.URL, wtok)

	// First call: a request waiting for approval, with a token.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_folder", Arguments: map[string]any{"path": "/Data/newdir"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, "APPROVAL REQUIRED") {
		t.Fatalf("expected the approval request, got: %.200s", txt)
	}
	i := strings.Index(txt, "cfm_")
	if i < 0 {
		t.Fatalf("no confirmation token in: %.200s", txt)
	}
	j := i + 4
	for j < len(txt) && (txt[j] == '_' || (txt[j] >= '0' && txt[j] <= '9') || (txt[j] >= 'a' && txt[j] <= 'f')) {
		j++
	}
	confirm := txt[i:j]
	call := func() *mcp.CallToolResult {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "create_folder", Arguments: map[string]any{"path": "/Data/newdir", "confirmToken": confirm},
		})
		if err != nil {
			t.Fatalf("confirmed call: %v", err)
		}
		return res
	}

	// The model echoing the token is not enough.
	if res = call(); !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "not approved") {
		t.Fatalf("ran without approval: %+v", res.Content[0])
	}
	// Nor can an API token approve it (that is what MCP clients hold).
	if w := tokenReq(t, s, "POST", "/api/assistant/approvals/"+confirm, wtok, map[string]bool{"approve": true}); w.Code != http.StatusForbidden {
		t.Fatalf("token approval: %d %s", w.Code, w.Body.String())
	}
	// The user sees it in the web interface and approves it.
	bob := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("bob", "", "192.0.2.9", time.Hour)}}
	w := bob.req("GET", "/api/assistant/approvals", nil)
	var list []mcpRequest
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].ID != confirm || !strings.Contains(list[0].Summary, "/Data/newdir") {
		t.Fatalf("approvals: %d %s", w.Code, w.Body.String())
	}
	if w = bob.req("POST", "/api/assistant/approvals/"+confirm, map[string]bool{"approve": true}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}

	// Now it runs (here it fails on the missing share, which proves it
	// reached the API).
	if txt = call().Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "/api/files/mkdir") {
		t.Fatalf("expected the API answer, got: %.200s", txt)
	}
	// The token is single-use.
	if res = call(); !res.IsError {
		t.Fatal("a confirmation token must be single-use")
	}

	// A declined request is gone.
	res, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "vm_action", Arguments: map[string]any{"id": "web", "action": "stop"}})
	txt = res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, "Virtual machine web: shutdown") {
		t.Fatalf("vm stop must be mapped to shutdown: %s", txt)
	}
	w = bob.req("GET", "/api/assistant/approvals", nil)
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("approvals: %s", w.Body.String())
	}
	if w = bob.req("POST", "/api/assistant/approvals/"+list[0].ID, map[string]bool{"approve": false}); w.Code != 200 {
		t.Fatalf("decline: %d", w.Code)
	}
	if w = bob.req("GET", "/api/assistant/approvals", nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("declined request kept: %s", w.Body.String())
	}
}

// Only the actions that exist, on names that cannot reach another route.
func TestMCPCheckWrite(t *testing.T) {
	for _, c := range []struct {
		tool, id, action string
		ok               bool
		want             string
	}{
		{"vm_action", "web", "start", true, "start"},
		{"vm_action", "web", "force-stop", true, "destroy"},
		{"vm_action", "web", "clone", false, ""},
		{"vm_action", "web", "template", false, ""},
		{"vm_action", "web", "rename", false, ""},
		{"vm_action", "../x", "start", false, ""},
		{"vm_action", "web/clone", "start", false, ""},
		{"docker_container_action", "db", "restart", true, "restart"},
		{"docker_container_action", "db", "kill", false, ""},
	} {
		got, err := mcpCheckWrite(c.tool, mcpWriteIn{ID: c.id, Action: c.action})
		if (err == nil) != c.ok || (c.ok && got.Action != c.want) {
			t.Errorf("%s %s %s: %+v %v", c.tool, c.id, c.action, got, err)
		}
	}
}

// Pending confirmations are capped per user, not only in all.
func TestConfirmationsPerUserCap(t *testing.T) {
	var c mcpConfirmations
	for i := 0; i < maxPendingPerUser; i++ {
		if _, err := c.arm("mallory", "create_folder", mcpWriteIn{Path: "/x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.arm("mallory", "create_folder", mcpWriteIn{Path: "/x"}); err == nil {
		t.Fatal("per-user cap not applied")
	}
	if _, err := c.arm("bob", "create_folder", mcpWriteIn{Path: "/x"}); err != nil {
		t.Fatalf("another user is blocked: %v", err)
	}
}

func TestMCPSessionTimeout(t *testing.T) {
	if mcpSessionTimeout <= 0 {
		t.Fatal("MCP sessions must time out")
	}
}

var _ = json.Marshal

func TestMCPNewToolsListed(t *testing.T) {
	_, ts, tok := mcpServer(t)
	cs := mcpClient(t, ts.URL, tok)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range list.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"list_vms", "list_zfs_snapshots", "list_notifications", "vm_action", "create_zfs_snapshot"} {
		if !names[want] {
			t.Errorf("tool %s is not exposed", want)
		}
	}
}
