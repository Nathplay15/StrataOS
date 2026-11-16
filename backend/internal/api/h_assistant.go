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
	"time"

	"strataos/internal/store"
)

// This file adds the conversational assistant of the web interface: it talks
// to an OpenAI-compatible chat endpoint configured by the administrator and
// can call the same in-process API tools as the MCP server (apiCall), so
// every tool runs with the permissions and the audit trail of the signed-in
// user. Tools that modify the NAS never run on the model's word: the chat
// answers with a pending action that the interface shows, and the action only
// runs when the user clicks Confirm (POST /api/assistant/confirm).

// assistantPrompt tells the model who it is and how the tools behave.
const assistantPrompt = `You are the StrataOS assistant, embedded in a NAS web interface. Answer briefly and helpfully. Use the provided tools to inspect the NAS (status, storage, Docker, backups, files) before answering questions about it. Tools that modify the NAS require the user's explicit confirmation in the interface: call one and the interface asks the user; its result arrives in a later message. Never claim an action was done before a tool result confirms it.`

type assistantMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []llmToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type llmToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type llmTool struct {
	Type     string     `json:"type"`
	Function llmToolDef `json:"function"`
}

type llmToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type llmResponse struct {
	Choices []struct {
		Message assistantMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// assistantAction is a write tool the model asked for and that waits for the
// user's confirmation in the interface.
type assistantAction struct {
	Token   string `json:"token"`
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
}

func fnTool(name, desc string, props map[string]any, required ...string) llmTool {
	if props == nil {
		props = map[string]any{}
	}
	return llmTool{Type: "function", Function: llmToolDef{
		Name: name, Description: desc,
		Parameters: map[string]any{"type": "object", "properties": props, "required": required},
	}}
}

// strProp is one string argument of a tool.
func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// assistantTools mirrors the MCP tools: the read ones call the API as the
// signed-in user, the write ones arm a confirmation shown in the interface.
func assistantTools() []llmTool {
	return []llmTool{
		fnTool("nas_status", "Status of this StrataOS NAS: name, version, uptime, and system information.", nil),
		fnTool("list_volumes", "List the storage volumes of the NAS (filesystem, size, used space, health).", nil),
		fnTool("list_disks", "List the physical disks of the NAS with their SMART health.", nil),
		fnTool("list_docker_containers", "List the Docker containers with their state and image.", nil),
		fnTool("list_backups", "List the backup jobs of the NAS.", nil),
		fnTool("recent_events", "Recent events of the NAS (alerts, jobs, security). Administrator only.", nil),
		fnTool("list_vms", "List the virtual machines of the NAS with their state. Administrator only.", nil),
		fnTool("list_zfs_snapshots", "List the ZFS snapshots of the NAS. Administrator only.", nil),
		fnTool("list_notifications", "List the recent notifications of the NAS (alerts, job results).", nil),
		fnTool("list_files", `List a folder of the NAS shares. Use path "/Share/subfolder" (share name first); empty path lists the shares.`,
			map[string]any{"path": strProp("Folder to list, e.g. /Documents/photos; empty for the share list")}),
		fnTool("read_file", "Read a small text file from the NAS shares (source code, configuration, notes, logs).",
			map[string]any{"path": strProp("File path, e.g. /Documents/notes.txt")}, "path"),
		fnTool("search_files", "Search files by name inside a share (at least 2 characters).",
			map[string]any{"query": strProp("File name to search"), "path": strProp("Optional share or folder to search in")}, "query"),
		fnTool("search_file_content", "Search inside the contents of indexed documents (text, code, Office, PDF; at least 2 characters).",
			map[string]any{"query": strProp("Text to search inside the files")}, "query"),
		fnTool("create_folder", "Create a folder in the NAS shares. Needs the user's confirmation in the interface.",
			map[string]any{"path": strProp("Folder to create, e.g. /Documents/new")}, "path"),
		fnTool("trash_file", "Move a file or folder of the NAS shares to the trash. Needs the user's confirmation in the interface.",
			map[string]any{"path": strProp("Item to move to the trash, e.g. /Documents/old.txt")}, "path"),
		fnTool("docker_container_action", "Start, stop or restart a Docker container. Needs the user's confirmation in the interface.",
			map[string]any{"id": strProp("Container id or name"), "action": strProp("start, stop or restart")}, "id", "action"),
		fnTool("vm_action", "Start, stop, force-stop, reboot or shut down a virtual machine. Needs the user's confirmation in the interface.",
			map[string]any{"id": strProp("VM name"), "action": strProp("start, stop, force-stop, reboot or shutdown")}, "id", "action"),
		fnTool("create_zfs_snapshot", "Create a ZFS snapshot. Needs the user's confirmation in the interface.",
			map[string]any{"dataset": strProp("Dataset path such as tank/data"), "snapshotName": strProp("Snapshot name")}, "dataset", "snapshotName"),
	}
}

func (s *Server) assistantRoutes() {
	s.user("GET /api/assistant/conversations", func(r *http.Request) (any, error) {
		type meta struct {
			ID      string    `json:"id"`
			Title   string    `json:"title"`
			Updated time.Time `json:"updated"`
		}
		var out []meta
		s.store.Read(func(st *store.State) {
			for _, c := range st.AssistantChatsOf(userOf(r)) {
				out = append(out, meta{c.ID, c.Title, c.Updated})
			}
		})
		if out == nil {
			out = []meta{}
		}
		return out, nil
	})

	s.user("GET /api/assistant/conversations/{id}", func(r *http.Request) (any, error) {
		var chat *store.AssistantChat
		s.store.Read(func(st *store.State) { chat = st.AssistantChat(userOf(r), r.PathValue("id")) })
		if chat == nil {
			return nil, notFound("unknown conversation")
		}
		return chat, nil
	})

	s.user("DELETE /api/assistant/conversations/{id}", func(r *http.Request) (any, error) {
		err := s.store.Update(func(st *store.State) error {
			st.DeleteAssistantChat(userOf(r), r.PathValue("id"))
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "assistant", "Conversation deleted")
		return nil, nil
	})

	s.user("GET /api/assistant/config", func(r *http.Request) (any, error) {
		var set store.Settings
		s.store.Read(func(st *store.State) { set = st.Settings })
		enabled := set.AssistantURL != "" && set.AssistantKey != "" && set.AssistantModel != ""
		return struct {
			Enabled bool   `json:"enabled"`
			Model   string `json:"model"`
		}{enabled, set.AssistantModel}, nil
	})

	s.user("POST /api/assistant/chat", func(r *http.Request) (any, error) {
		var req struct {
			Messages       []assistantMessage `json:"messages"`
			ConversationID string             `json:"conversationId,omitempty"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Messages) == 0 || len(req.Messages) > 40 {
			return nil, badRequest("invalid conversation")
		}
		for _, m := range req.Messages {
			if (m.Role != "user" && m.Role != "assistant") || len(m.Content) > 8000 {
				return nil, badRequest("invalid conversation")
			}
		}
		var set store.Settings
		s.store.Read(func(st *store.State) { set = st.Settings })
		if set.AssistantURL == "" || set.AssistantKey == "" || set.AssistantModel == "" {
			return nil, badRequest("the assistant is not configured: set it up in Settings")
		}
		inner := s.assistantReq(r)
		conv := append([]assistantMessage{{Role: "system", Content: assistantPrompt}}, req.Messages...)
		reply := ""
		var actions []assistantAction
		for iter := 0; iter < 5; iter++ {
			resp, err := s.llmChat(r.Context(), set, conv)
			if err != nil {
				return nil, err
			}
			if len(resp.Choices) == 0 {
				return nil, badRequest("the assistant endpoint returned no answer")
			}
			msg := resp.Choices[0].Message
			if len(msg.ToolCalls) == 0 {
				reply = msg.Content
				break
			}
			reply = msg.Content
			conv = append(conv, assistantMessage{Role: "assistant", Content: msg.Content, ToolCalls: msg.ToolCalls})
			stop := false
			for _, tc := range msg.ToolCalls {
				var args map[string]string
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				out, armed := s.assistantTool(inner, tc.Function.Name, args)
				if armed != nil {
					actions = append(actions, *armed)
					conv = append(conv, assistantMessage{Role: "tool", ToolCallID: tc.ID, Content: "Waiting for the user's confirmation in the interface."})
					stop = true
					continue
				}
				conv = append(conv, assistantMessage{Role: "tool", ToolCallID: tc.ID, Content: out})
			}
			if stop {
				break
			}
		}
		// Save the conversation (user/assistant messages only): created on the
		// first exchange, or extended when the client follows one.
		var convID string
		if err := s.store.Update(func(st *store.State) error {
			var chat *store.AssistantChat
			if req.ConversationID != "" {
				chat = st.AssistantChat(userOf(r), req.ConversationID)
			}
			if chat == nil {
				title := ""
				for _, m := range req.Messages {
					if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
						title = strings.TrimSpace(m.Content)
						break
					}
				}
				if len(title) > 60 {
					title = title[:60]
				}
				chat = &store.AssistantChat{ID: token()[:16], User: userOf(r), Title: title}
				st.AssistantChats = append(st.AssistantChats, chat)
			}
			chat.Messages = append(chat.Messages, store.AssistantMsg{Role: "user", Content: req.Messages[len(req.Messages)-1].Content})
			if reply != "" {
				chat.Messages = append(chat.Messages, store.AssistantMsg{Role: "assistant", Content: reply})
			}
			if len(chat.Messages) > store.MaxAssistantMessages {
				chat.Messages = chat.Messages[len(chat.Messages)-store.MaxAssistantMessages:]
			}
			for i := range chat.Messages {
				if len(chat.Messages[i].Content) > store.MaxAssistantChars {
					chat.Messages[i].Content = chat.Messages[i].Content[:store.MaxAssistantChars]
				}
			}
			chat.Updated = time.Now()
			convID = chat.ID
			return nil
		}); err != nil {
			return nil, err
		}
		return struct {
			Reply          string            `json:"reply"`
			ConversationID string            `json:"conversationId"`
			Actions        []assistantAction `json:"actions,omitempty"`
		}{reply, convID, actions}, nil
	})

	s.user("POST /api/assistant/confirm", func(r *http.Request) (any, error) {
		var req struct {
			Token string `json:"token"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		p, err := s.assistantConf.consumeAny(userOf(r), req.Token)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		inner := s.assistantReq(r)
		status, out := s.assistantExec(inner, p.tool, p.args)
		text := fmt.Sprintf("API answered %d", status)
		if b, err := json.MarshalIndent(out, "", "  "); err == nil && len(b) > 0 {
			text = string(b)
		}
		return struct {
			Result string `json:"result"`
		}{text}, nil
	})
}

// assistantReq carries the caller's credential into apiCall, the same way the
// MCP transport does: the tools then run as the signed-in user.
func (s *Server) assistantReq(r *http.Request) *http.Request {
	cred := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if c, err := r.Cookie(cookieName); err == nil && cred == "" {
		cred = "cookie:" + c.Value
	}
	return r.WithContext(context.WithValue(r.Context(), mcpCredKey{}, cred))
}

// assistantTool runs one read tool through the API, or arms one write tool;
// armed = nil means the tool ran and the returned text is its result.
func (s *Server) assistantTool(r *http.Request, name string, args map[string]string) (string, *assistantAction) {
	arg := func(k string) string { return strings.TrimSpace(args[k]) }
	switch name {
	case "nas_status":
		return s.assistantCall(r, http.MethodGet, "/api/system/info", nil), nil
	case "list_volumes":
		return s.assistantCall(r, http.MethodGet, "/api/storage/volumes", nil), nil
	case "list_disks":
		return s.assistantCall(r, http.MethodGet, "/api/storage/disks", nil), nil
	case "list_docker_containers":
		return s.assistantCall(r, http.MethodGet, "/api/docker/containers", nil), nil
	case "list_backups":
		return s.assistantCall(r, http.MethodGet, "/api/backups", nil), nil
	case "recent_events":
		return s.assistantCall(r, http.MethodGet, "/api/system/events", nil), nil
	case "list_vms":
		return s.assistantCall(r, http.MethodGet, "/api/vms", nil), nil
	case "list_zfs_snapshots":
		return s.assistantCall(r, http.MethodGet, "/api/storage/zfs-snapshots", nil), nil
	case "list_notifications":
		return s.assistantCall(r, http.MethodGet, "/api/notifications", nil), nil
	case "list_files":
		return s.assistantCall(r, http.MethodGet, "/api/files?path="+url.QueryEscape(arg("path")), nil), nil
	case "read_file":
		return s.assistantCall(r, http.MethodGet, "/api/files/content?path="+url.QueryEscape(arg("path")), nil), nil
	case "search_files":
		p := "/api/files/search?q=" + url.QueryEscape(arg("query"))
		if arg("path") != "" {
			p += "&path=" + url.QueryEscape(arg("path"))
		}
		return s.assistantCall(r, http.MethodGet, p, nil), nil
	case "search_file_content":
		return s.assistantCall(r, http.MethodGet, "/api/files/fulltext?q="+url.QueryEscape(arg("query"))+"&limit=20", nil), nil
	case "create_folder":
		return s.assistantArm(r, name, mcpWriteIn{Path: arg("path")}, "Create the folder "+arg("path"))
	case "trash_file":
		return s.assistantArm(r, name, mcpWriteIn{Path: arg("path")}, "Move "+arg("path")+" to the trash")
	case "docker_container_action":
		return s.assistantArm(r, name, mcpWriteIn{ID: arg("id"), Action: arg("action")}, "Container "+arg("id")+": "+arg("action"))
	case "vm_action":
		return s.assistantArm(r, name, mcpWriteIn{ID: arg("id"), Action: arg("action")}, "Virtual machine "+arg("id")+": "+arg("action"))
	case "create_zfs_snapshot":
		return s.assistantArm(r, name, mcpWriteIn{Dataset: arg("dataset"), SnapshotName: arg("snapshotName")}, "Create the ZFS snapshot "+arg("dataset")+"@"+arg("snapshotName"))
	}
	return "unknown tool: " + name, nil
}

// assistantArm arms a write tool: the interface shows the summary and the
// action only runs after the user confirms.
func (s *Server) assistantArm(r *http.Request, tool string, in mcpWriteIn, summary string) (string, *assistantAction) {
	tok, err := s.assistantConf.arm(userOf(r), tool, in)
	if err != nil {
		return err.Error(), nil
	}
	return "", &assistantAction{Token: tok, Tool: tool, Summary: summary}
}

// assistantCall runs one API call as the signed-in user and returns the
// answer (or the failure) as text the model can read.
func (s *Server) assistantCall(r *http.Request, method, path string, body any) string {
	status, out := s.apiCall(r, method, path, body)
	if status == 0 {
		return "internal error calling " + path
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		b = []byte(fmt.Sprint(out))
	}
	text := string(b)
	if status/100 != 2 {
		return fmt.Sprintf("API %s answered %d: %s", path, status, text)
	}
	if len(text) > 20000 {
		text = text[:20000] + "…"
	}
	return text
}

// assistantExec runs a confirmed write tool through the API (the same
// endpoints and permission checks as the MCP write tools).
func (s *Server) assistantExec(r *http.Request, tool string, in mcpWriteIn) (int, any) {
	switch tool {
	case "create_folder":
		return s.apiCall(r, http.MethodPost, "/api/files/mkdir", map[string]string{"path": in.Path})
	case "trash_file":
		return s.apiCall(r, http.MethodPost, "/api/files/trash", map[string]any{"paths": []string{in.Path}})
	case "docker_container_action":
		return s.apiCall(r, http.MethodPost, "/api/docker/containers/"+url.PathEscape(in.ID)+"/"+url.PathEscape(in.Action), nil)
	case "vm_action":
		return s.apiCall(r, http.MethodPost, "/api/vms/"+url.PathEscape(in.ID)+"/"+url.PathEscape(in.Action), nil)
	case "create_zfs_snapshot":
		return s.apiCall(r, http.MethodPost, "/api/storage/zfs-snapshots", map[string]any{"dataset": in.Dataset, "name": in.SnapshotName})
	}
	return 0, map[string]string{"error": "unknown tool: " + tool}
}

// llmChat calls the configured OpenAI-compatible chat endpoint.
func (s *Server) llmChat(ctx context.Context, set store.Settings, messages []assistantMessage) (*llmResponse, error) {
	body, err := json.Marshal(map[string]any{"model": set.AssistantModel, "messages": messages, "tools": assistantTools()})
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, strings.TrimSuffix(set.AssistantURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, badRequest("invalid assistant URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+set.AssistantKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, badRequest("the assistant endpoint is unreachable")
	}
	defer resp.Body.Close()
	var out llmResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, badRequest(fmt.Sprintf("the assistant endpoint answered %d with an invalid body", resp.StatusCode))
	}
	if out.Error != nil {
		return nil, badRequest(fmt.Sprintf("the assistant endpoint answered %d: %s", resp.StatusCode, out.Error.Message))
	}
	if resp.StatusCode/100 != 2 {
		return nil, badRequest(fmt.Sprintf("the assistant endpoint answered %d", resp.StatusCode))
	}
	return &out, nil
}
