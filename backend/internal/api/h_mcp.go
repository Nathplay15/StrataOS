package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
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
	s.mcpApprovalRoutes()
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

// mcpSessionTimeout closes the MCP sessions left idle (clients rarely close
// theirs).
const mcpSessionTimeout = 30 * time.Minute

// mcpApprovalRoutes let a person approve or decline what MCP clients asked
// for. Only a person signed in to the web interface or the mobile app may
// decide: never an API token, which is what MCP clients use.
func (s *Server) mcpApprovalRoutes() {
	s.user("GET /api/assistant/approvals", func(r *http.Request) (any, error) {
		return s.mcpConf.list(userOf(r)), nil
	})
	s.user("POST /api/assistant/approvals/{id}", func(r *http.Request) (any, error) {
		var req struct {
			Approve bool `json:"approve"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if sessionScopes(r) != nil || r.Context().Value(mcpInternalKey{}) != nil {
			return nil, &Error{Status: http.StatusForbidden, Msg: "approve AI client requests from the web interface"}
		}
		if !s.mcpConf.decide(userOf(r), r.PathValue("id"), req.Approve) {
			return nil, notFound("unknown or expired request")
		}
		verb := "declined"
		if req.Approve {
			verb = "approved"
		}
		s.audit(r, "assistant", "AI client request "+verb)
		return nil, nil
	})
}

// mcpHandler builds the MCP server and its tools once.
func (s *Server) mcpHandler() http.Handler {
	s.mcpOnce.Do(func() {
		srv := mcp.NewServer(&mcp.Implementation{Name: "strataos", Title: "StrataOS NAS", Version: s.version}, nil)
		s.mcpAddReadTools(srv)
		s.mcpAddWriteTools(srv)
		s.mcpHTTP = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return srv },
			&mcp.StreamableHTTPOptions{SessionTimeout: mcpSessionTimeout})
	})
	return s.mcpHTTP
}

// mcpVMActions are the VM actions an assistant may ask for (vms.Action);
// "stop" and "force-stop", which models like to use, are mapped to them.
var mcpVMActions = map[string]string{
	"start": "start", "shutdown": "shutdown", "stop": "shutdown", "reboot": "reboot",
	"destroy": "destroy", "force-stop": "destroy", "suspend": "suspend", "resume": "resume",
}

// mcpDockerActions are the container actions an assistant may ask for.
var mcpDockerActions = map[string]bool{"start": true, "stop": true, "restart": true}

// mcpCheckWrite validates the arguments of a write tool before it is armed:
// only the listed actions, on names that cannot reach another route.
func mcpCheckWrite(tool string, in mcpWriteIn) (mcpWriteIn, error) {
	in.ConfirmToken = ""
	switch tool {
	case "create_folder", "trash_file":
		if strings.TrimSpace(in.Path) == "" {
			return in, fmt.Errorf("a path is required")
		}
	case "docker_container_action":
		if !mcpNameRe.MatchString(in.ID) {
			return in, fmt.Errorf("invalid container id")
		}
		if !mcpDockerActions[in.Action] {
			return in, fmt.Errorf("unknown action: use start, stop or restart")
		}
	case "vm_action":
		if !mcpNameRe.MatchString(in.ID) {
			return in, fmt.Errorf("invalid VM name")
		}
		a, ok := mcpVMActions[in.Action]
		if !ok {
			return in, fmt.Errorf("unknown action: use start, shutdown, reboot, destroy, suspend or resume")
		}
		in.Action = a
	case "create_zfs_snapshot":
		if in.Dataset == "" || in.SnapshotName == "" {
			return in, fmt.Errorf("a dataset and a snapshot name are required")
		}
	default:
		return in, fmt.Errorf("unknown tool: %s", tool)
	}
	return in, nil
}

var mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

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
	tool("list_vms", "List the virtual machines of the NAS with their state. Administrator only.", func(r *http.Request) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(r, "/api/vms")
	})
	tool("list_zfs_snapshots", "List the ZFS snapshots of the NAS. Administrator only.", func(r *http.Request) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(r, "/api/storage/zfs-snapshots")
	})
	tool("list_notifications", "List the recent notifications of the NAS (alerts, job results).", func(r *http.Request) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(r, "/api/notifications")
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

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_file_content",
		Description: "Search inside the contents of indexed documents (text, code, Office, PDF; at least 2 characters).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Query string `json:"query"`
	}) (*mcp.CallToolResult, any, error) {
		return s.mcpGet(requestFrom(ctx), "/api/files/fulltext?q="+url.QueryEscape(in.Query)+"&limit=20")
	})
}

// mcpWriteIn gathers the arguments of every write tool (only the fields a
// tool uses matter) plus the confirmation token.
type mcpWriteIn struct {
	Path         string `json:"path,omitempty"`
	ID           string `json:"id,omitempty"`
	Action       string `json:"action,omitempty"`
	Dataset      string `json:"dataset,omitempty"`
	SnapshotName string `json:"snapshotName,omitempty"`
	ConfirmToken string `json:"confirmToken,omitempty"`
}

// mcpAddWriteTools adds the tools that change the NAS. They are gated three
// times: the write scope of the token, the arguments checked against the
// actions an assistant may take (mcpCheckWrite), then a person: the first call
// only files a request that the user must approve in the web interface
// (GET/POST /api/assistant/approvals); the model gets a token it can only
// use, by calling the tool again, once that approval was given. A model can
// therefore never confirm its own action.
func (s *Server) mcpAddWriteTools(srv *mcp.Server) {
	pending := &s.mcpConf

	gated := func(name, desc string, summarize func(mcpWriteIn) string, exec func(*http.Request, mcpWriteIn) (*mcp.CallToolResult, any, error)) {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: desc + " Needs the user's approval: the first call files a request that the user approves in the StrataOS web interface (Assistant page); call the tool again with the returned confirmToken once they did."}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpWriteIn) (*mcp.CallToolResult, any, error) {
			r := requestFrom(ctx)
			fail := func(msg string) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil, nil
			}
			if !hasScope(sessionScopes(r), "write") {
				return fail("this tool needs an API token with the write scope")
			}
			if in.ConfirmToken == "" {
				checked, err := mcpCheckWrite(name, in)
				if err != nil {
					return fail(err.Error())
				}
				summary := summarize(checked)
				tok, err := pending.armPending(mcpPending{tool: name, user: userOf(r), args: checked, summary: summary}, 10*time.Minute)
				if err != nil {
					return fail(err.Error())
				}
				s.audit(r, "assistant", "AI client request waiting for approval: "+summary)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
					Text: fmt.Sprintf("APPROVAL REQUIRED\n%s\nThe user must approve this request in the StrataOS web interface (Assistant page) within 10 minutes. Once they did, call this tool again with {\"confirmToken\": %q}.", summary, tok),
				}}}, nil, nil
			}
			stored, err := pending.consumeApproved(userOf(r), name, in.ConfirmToken)
			if err != nil {
				return fail(err.Error())
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
	gated("vm_action", "Start, shut down, force off, reboot, suspend or resume a virtual machine (id: VM name, action: start, shutdown, destroy, reboot, suspend, resume).",
		func(in mcpWriteIn) string { return "Virtual machine " + in.ID + ": " + in.Action },
		func(r *http.Request, in mcpWriteIn) (*mcp.CallToolResult, any, error) {
			return s.mcpCall(r, http.MethodPost, "/api/vms/"+url.PathEscape(in.ID)+"/"+url.PathEscape(in.Action), nil)
		})
	gated("create_zfs_snapshot", "Create a ZFS snapshot (dataset: dataset path such as tank/data, snapshotName: snapshot name).",
		func(in mcpWriteIn) string { return "Create the ZFS snapshot " + in.Dataset + "@" + in.SnapshotName },
		func(r *http.Request, in mcpWriteIn) (*mcp.CallToolResult, any, error) {
			return s.mcpCall(r, http.MethodPost, "/api/storage/zfs-snapshots", map[string]any{"dataset": in.Dataset, "name": in.SnapshotName})
		})
}

// mcpPending is one armed confirmation with the arguments it was armed with.
type mcpPending struct {
	tool    string
	user    string
	summary string
	args    mcpWriteIn
	expires time.Time
	// approved: a person approved this MCP request in the web interface.
	approved bool
	// resume: the assistant conversation to continue once confirmed (web
	// tools of the assistant, h_assistant.go).
	resume *assistantResume
}

const (
	// Pending confirmations kept per user, and in all.
	maxPendingPerUser = 20
	maxPendingTotal   = 1000
)

// mcpConfirmations holds the armed confirmations in memory (a restart
// cancels them).
type mcpConfirmations struct {
	mu sync.Mutex
	m  map[string]mcpPending
}

func (c *mcpConfirmations) arm(user, tool string, args mcpWriteIn) (string, error) {
	return c.armPending(mcpPending{tool: tool, user: user, args: args}, 5*time.Minute)
}

// armPending stores p under a new token valid for ttl.
func (c *mcpConfirmations) armPending(p mcpPending, ttl time.Duration) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]mcpPending{}
	}
	now := time.Now()
	mine := 0
	for k, x := range c.m {
		if now.After(x.expires) {
			delete(c.m, k)
		} else if x.user == p.user {
			mine++
		}
	}
	if mine >= maxPendingPerUser || len(c.m) >= maxPendingTotal {
		return "", fmt.Errorf("too many pending confirmations: confirm or decline the waiting ones first")
	}
	tok := "cfm_" + token()[:24]
	p.expires = now.Add(ttl)
	c.m[tok] = p
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

// consumeApproved consumes an MCP request once a person approved it; until
// then it stays pending.
func (c *mcpConfirmations) consumeApproved(user, tool, tok string) (mcpWriteIn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.m[tok]
	if !ok || p.user != user || p.tool != tool || time.Now().After(p.expires) {
		delete(c.m, tok)
		return mcpWriteIn{}, fmt.Errorf("unknown, declined or expired request: call the tool again to ask for a new approval")
	}
	if !p.approved {
		return mcpWriteIn{}, fmt.Errorf("not approved yet: the user must approve this request in the StrataOS web interface (Assistant page), then call the tool again with the same confirmToken")
	}
	delete(c.m, tok)
	return p.args, nil
}

// consumeAny consumes a token whatever tool it was armed for (the tool is
// stored in the pending confirmation); used by the assistant, whose confirm
// endpoint only receives the token.
func (c *mcpConfirmations) consumeAny(user, tok string) (mcpPending, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.m[tok]
	if !ok || p.user != user || time.Now().After(p.expires) {
		delete(c.m, tok)
		return mcpPending{}, fmt.Errorf("unknown or expired confirmation token: ask for the action again")
	}
	delete(c.m, tok)
	return p, nil
}

// setConversation records the saved conversation an armed assistant step
// belongs to (known only once the exchange is saved).
func (c *mcpConfirmations) setConversation(tok, convID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.m[tok]; ok && p.resume != nil {
		p.resume.convID = convID
	}
}

// attach stores the conversation an armed assistant web tool continues.
func (c *mcpConfirmations) attach(tok string, r *assistantResume) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.m[tok]; ok {
		p.resume = r
		c.m[tok] = p
	}
}

// mcpRequest is a pending MCP write request as the web interface lists it.
type mcpRequest struct {
	ID       string    `json:"id"`
	Tool     string    `json:"tool"`
	Summary  string    `json:"summary"`
	Approved bool      `json:"approved"`
	Expires  time.Time `json:"expires"`
}

// list returns the pending requests of a user, oldest first.
func (c *mcpConfirmations) list(user string) []mcpRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []mcpRequest{}
	now := time.Now()
	for k, p := range c.m {
		if p.user == user && now.Before(p.expires) {
			out = append(out, mcpRequest{ID: k, Tool: p.tool, Summary: p.summary, Approved: p.approved, Expires: p.expires})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Expires.Before(out[j].Expires) })
	return out
}

// decide approves or declines a pending request of a user.
func (c *mcpConfirmations) decide(user, tok string, approve bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.m[tok]
	if !ok || p.user != user || time.Now().After(p.expires) {
		return false
	}
	if approve {
		p.approved = true
		c.m[tok] = p
	} else {
		delete(c.m, tok)
	}
	return true
}
