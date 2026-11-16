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

func TestMCPWriteToolConfirmation(t *testing.T) {
	s, ts, _ := mcpServer(t)
	wtok := createToken(t, s, "bob", []string{"mcp", "write"}, 0)
	cs := mcpClient(t, ts.URL, wtok)

	// First call: preview only, with a confirmation token.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_folder", Arguments: map[string]any{"path": "/Data/newdir"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, "CONFIRMATION REQUIRED") {
		t.Fatalf("expected the confirmation preview, got: %.200s", txt)
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

	// Second call with the token: it runs (here it fails on the missing
	// share, which proves it reached the API).
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_folder", Arguments: map[string]any{"path": "/Data/newdir", "confirmToken": confirm},
	})
	if err != nil {
		t.Fatalf("confirmed call: %v", err)
	}
	txt = res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, "/api/files/mkdir") {
		t.Fatalf("expected the API answer, got: %.200s", txt)
	}

	// The token is single-use.
	res, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_folder", Arguments: map[string]any{"path": "/Data/newdir", "confirmToken": confirm},
	})
	if res == nil || !res.IsError {
		t.Fatal("a confirmation token must be single-use")
	}
}

var _ = json.Marshal
