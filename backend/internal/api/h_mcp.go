package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This file exposes the API to AI assistants through the Model Context
// Protocol (streamable HTTP transport) on /api/mcp. Tools run as the token's
// user by calling the API itself in-process (apiCall), so every permission
// and audit rule of a normal request applies. The read tools only need the
// mcp scope; the write tools also need the write scope and an explicit
// confirmation token obtained by calling the tool once without one.

// mcpReqKey carries the incoming HTTP request into the tool handlers: the
// streamable transport propagates the request context, and the tools need
// the session, the address and the audit trail of the caller.
type mcpReqKey struct{}

// mcpCredKey carries the caller's credential (bearer token or session
// cookie): the tools call the API in-process, and the inner request must
// pass the same authentication as the outer one.
type mcpCredKey struct{}

func requestFrom(ctx context.Context) *http.Request {
	r, _ := ctx.Value(mcpReqKey{}).(*http.Request)
	return r
}

// sessionScopes returns the scopes of the caller (nil: unrestricted).
func sessionScopes(r *http.Request) []string {
	if ss := sessionFrom(r.Context()); ss != nil {
		return ss.Scopes
	}
	return nil
}

func (s *Server) mcpRoutes() {
	s.userRaw("/api/mcp", func(w http.ResponseWriter, r *http.Request) {
		ss := sessionFrom(r.Context())
		if ss == nil || (ss.Scopes != nil && !hasScope(ss.Scopes, "mcp")) {
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "an API token with the mcp scope is required"})
			return
		}
		cred := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if c, err := r.Cookie(cookieName); err == nil && cred == "" {
			cred = "cookie:" + c.Value
		}
		ctx := context.WithValue(r.Context(), mcpCredKey{}, cred)
		wrapped := r.WithContext(ctx)
		s.mcpHandler().ServeHTTP(w, wrapped.WithContext(context.WithValue(ctx, mcpReqKey{}, wrapped)))
	})
}

// mcpHandler builds the MCP server and its tools once.
func (s *Server) mcpHandler() http.Handler {
	s.mcpOnce.Do(func() {
		srv := mcp.NewServer(&mcp.Implementation{Name: "strataos", Title: "StrataOS NAS", Version: s.version}, nil)
		s.mcpAddReadTools(srv)
		s.mcpAddWriteTools(srv)
		s.mcpHTTP = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return srv }, nil)
	})
	return s.mcpHTTP
}

// apiCall runs one API request in-process as the caller's session: the same
// mux, the same permissions, the same audit trail.
func (s *Server) apiCall(r *http.Request, method, rawPath string, body any) (int, any) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, map[string]string{"error": err.Error()}
		}
		rd = bytes.NewReader(b)
	}
	path, query := rawPath, ""
	if i := strings.IndexByte(rawPath, '?'); i >= 0 {
		path, query = rawPath[:i], rawPath[i+1:]
	}
	req, err := http.NewRequest(method, "http://localhost"+path, rd)
	if err != nil {
		return 0, map[string]string{"error": err.Error()}
	}
	if q, err := url.ParseQuery(query); err == nil {
		req.URL.RawQuery = q.Encode()
	}
	req.RemoteAddr = r.RemoteAddr
	if cred, _ := r.Context().Value(mcpCredKey{}).(string); cred != "" {
		if rest, ok := strings.CutPrefix(cred, "cookie:"); ok {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: rest})
		} else {
			req.Header.Set("Authorization", "Bearer "+cred)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		req.Header.Set("X-Strata", "mcp") // sameOrigin guard for API clients
	}
	req = req.WithContext(context.WithValue(r.Context(), mcpInternalKey{}, true))

	w := &captureWriter{header: http.Header{}}
	s.mux.ServeHTTP(w, req)
	var out any
	if w.body.Len() > 0 {
		if err := json.Unmarshal(w.body.Bytes(), &out); err != nil {
			out = w.body.String()
		}
	}
	return w.status, out
}

// captureWriter records what a handler wrote (the tools only call endpoints
// that answer small JSON bodies or small text).
type captureWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *captureWriter) Header() http.Header         { return w.header }
func (w *captureWriter) WriteHeader(status int)      { w.status = status }
func (w *captureWriter) Write(b []byte) (int, error) { w.body.Write(b); return len(b), nil }
func (w *captureWriter) Flush()                      {}

// textResult builds a tool result carrying one JSON (or plain) text.
func textResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

// textErr turns an API failure into a tool error the model can read.
func textErr(path string, status int, out any) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{
		Text: fmt.Sprintf("API %s answered %d: %v", path, status, out),
	}}}, nil, nil
}

// mcpGet calls a GET endpoint and returns its answer, or an error result.
func (s *Server) mcpGet(r *http.Request, path string) (*mcp.CallToolResult, any, error) {
	status, out := s.apiCall(r, http.MethodGet, path, nil)
	if status == 0 {
		return nil, nil, fmt.Errorf("internal error calling %s", path)
	}
	if status/100 != 2 {
		return textErr(path, status, out)
	}
	return textResult(out)
}

// mcpCall runs a write endpoint after the confirmation check.
func (s *Server) mcpCall(r *http.Request, method, path string, body any) (*mcp.CallToolResult, any, error) {
	status, out := s.apiCall(r, method, path, body)
	if status == 0 {
		return nil, nil, fmt.Errorf("internal error calling %s", path)
	}
	if status/100 != 2 {
		return textErr(path, status, out)
	}
	return textResult(out)
}

func (s *Server) mcpAddReadTools(srv *mcp.Server) {
	type empty struct{}
	tool := func(name, desc string, fn func(*http.Request) (*mcp.CallToolResult, any, error)) {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: desc}, func(ctx context.Context, req *mcp.CallToolRequest, in empty) (*mcp.CallToolResult, any, error) {
			return fn(requestFrom(ctx))
		})
	}

	tool("nas_status", "Status of this StrataOS NAS: name, version, uptime, and system information.", func(r *http.Request) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(r, "/api/system/info")
	})
	tool("list_volumes", "List the storage volumes of the NAS (filesystem, size, used space, health).", func(r *http.Request) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(r, "/api/storage/volumes")
	})
	tool("list_disks", "List the physical disks of the NAS with their SMART health.", func(r *http.Request) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(r, "/api/storage/disks")
	})
	tool("list_docker_containers", "List the Docker containers with their state and image.", func(r *http.Request) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(r, "/api/docker/containers")
	})
	tool("list_backups", "List the backup jobs of the NAS.", func(r *http.Request) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(r, "/api/backups")
	})
	tool("recent_events", "Recent events of the NAS (alerts, jobs, security). Administrator only.", func(r *http.Request) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(r, "/api/system/events")
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_files",
		Description: "List a folder of the NAS shares. Use path \"/Share/subfolder\" (share name first); empty path lists the shares.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Path string `json:"path,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(requestFrom(ctx), "/api/files?path="+url.QueryEscape(in.Path))
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a small text file from the NAS shares (source code, configuration, notes, logs).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Path string `json:"path"`
	}) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(requestFrom(ctx), "/api/files/content?path="+url.QueryEscape(in.Path))
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_files",
		Description: "Search files by name inside a share (at least 2 characters).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Query string `json:"query"`
		Path  string `json:"path,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		p := "/api/files/search?q=" + url.QueryEscape(in.Query)
		if in.Path != "" {
			p += "&path=" + url.QueryEscape(in.Path)
		}
		return s.mcpGet(requestFrom(ctx), p)
	})
}

// mcpWriteIn gathers the arguments of every write tool (only the fields a
// tool uses matter) plus the confirmation token.
type mcpWriteIn struct {
	Path         string `json:"path,omitempty"`
	ID           string `json:"id,omitempty"`
	Action       string `json:"action,omitempty"`
	ConfirmToken string `json:"confirmToken,omitempty"`
}

// mcpWriteTools are gated twice: the write scope, then a confirmation token
// the model must echo back after seeing what it is about to do.
func (s *Server) mcpAddWriteTools(srv *mcp.Server) {
	pending := &mcpConfirmations{m: map[string]mcpPending{}}

	gated := func(name, desc string, summarize func(mcpWriteIn) string, exec func(*http.Request, mcpWriteIn) (*mcp.CallToolResult, any, error)) {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: desc + " Two-step confirmation: call it once to preview, then again with the returned confirmToken."}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpWriteIn) (*mcp.CallToolResult, any, error) {
			r := requestFrom(ctx)
			if !hasScope(sessionScopes(r), "write") {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "this tool needs an API token with the write scope"}}}, nil, nil
			}
			if in.ConfirmToken == "" {
				tok, err := pending.arm(userOf(r), name, in)
				if err != nil {
					return nil, nil, err
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
					Text: fmt.Sprintf("CONFIRMATION REQUIRED\n%s\nCall this tool again with {\"confirmToken\": %q} within 5 minutes to execute.", summarize(in), tok),
				}}}, nil, nil
			}
			stored, err := pending.consume(userOf(r), name, in.ConfirmToken)
			if err != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
			}
			return exec(r, stored)
		})
	}

	gated("create_folder", "Create a folder in the NAS shares (path: /Share/folder/new).",
		func(in mcpWriteIn) string { return "Create the folder " + in.Path },
		func(r *http.Request, in mcpWriteIn) (*mcp.CallToolResult, any, error) {
			return s.mcpCall(r, http.MethodPost, "/api/files/mkdir", map[string]string{"path": in.Path})
		})
	gated("trash_file", "Move a file or folder of the NAS shares to the trash (path: /Share/folder/item).",
		func(in mcpWriteIn) string { return "Move " + in.Path + " to the trash" },
		func(r *http.Request, in mcpWriteIn) (*mcp.CallToolResult, any, error) {
			return s.mcpCall(r, http.MethodPost, "/api/files/trash", map[string]any{"paths": []string{in.Path}})
		})
	gated("docker_container_action", "Start, stop or restart a Docker container (id: container id or name, action: start, stop, restart).",
		func(in mcpWriteIn) string { return "Container " + in.ID + ": " + in.Action },
		func(r *http.Request, in mcpWriteIn) (*mcp.CallToolResult, any, error) {
			return s.mcpCall(r, http.MethodPost, "/api/docker/containers/"+url.PathEscape(in.ID)+"/"+url.PathEscape(in.Action), nil)
		})
}

// mcpPending is one armed confirmation with the arguments it was armed with.
type mcpPending struct {
	tool    string
	user    string
	args    mcpWriteIn
	expires time.Time
}

// mcpConfirmations holds the armed confirmations in memory (a restart
// cancels them).
type mcpConfirmations struct {
	mu sync.Mutex
	m  map[string]mcpPending
}

func (c *mcpConfirmations) arm(user, tool string, args mcpWriteIn) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, p := range c.m {
		if now.After(p.expires) {
			delete(c.m, k)
		}
	}
	if len(c.m) > 100 {
		return "", fmt.Errorf("too many pending confirmations")
	}
	tok := "cfm_" + token()[:24]
	c.m[tok] = mcpPending{tool: tool, user: user, args: args, expires: now.Add(5 * time.Minute)}
	return tok, nil
}

func (c *mcpConfirmations) consume(user, tool, tok string) (mcpWriteIn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.m[tok]
	if !ok || p.user != user || p.tool != tool || time.Now().After(p.expires) {
		delete(c.m, tok)
		return mcpWriteIn{}, fmt.Errorf("unknown or expired confirmation token: call the tool again to get a new one")
	}
	delete(c.m, tok)
	return p.args, nil
}
