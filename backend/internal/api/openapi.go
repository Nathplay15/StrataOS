package api

import (
	"net/http"
	"sort"
	"strings"
)

// openapiRoutes serves a description of the whole API as an OpenAPI 3.1
// document, built from the routes registered through the helpers (server.go).
// It lists what exists and how to authenticate; request and response bodies
// stay at the level of their JSON field names when the route declares them.
func (s *Server) openapiRoutes() {
	s.user("GET /api/openapi.json", func(r *http.Request) (any, error) {
		return s.openapi(), nil
	})
}

// scopeNote explains the token scopes in the document.
const scopeNote = "Scoped API tokens (prefix stt_) authenticate with `Authorization: Bearer stt_…`. " +
	"Scopes: read (GET requests), write (state-changing requests), admin (everything, " +
	"including administrator routes), mcp (the MCP endpoint /api/mcp). " +
	"Tokens are managed on /api/me/tokens and can never reach the account, session " +
	"and credential endpoints."

func (s *Server) openapi() map[string]any {
	paths := map[string]any{}
	for _, rt := range s.recorded {
		p := openapiPath(rt.Path)
		item, _ := paths[p].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[p] = item
		}
		if rt.Method == "" {
			// A pattern without method accepts several: list the common ones.
			for _, m := range []string{"get", "post", "put", "delete", "patch"} {
				if _, ok := item[m]; !ok {
					item[m] = s.operation(rt, m)
				}
			}
			continue
		}
		item[strings.ToLower(rt.Method)] = s.operation(rt, strings.ToLower(rt.Method))
	}
	var tags []map[string]any
	seen := map[string]bool{}
	for _, rt := range s.recorded {
		tag := openapiTag(rt.Path)
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, map[string]any{"name": tag})
		}
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i]["name"].(string) < tags[j]["name"].(string) })
	security := []map[string]any{{"bearerToken": []string{}}}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "StrataOS API",
			"version":     s.version,
			"description": "Every route of this NAS. Web sessions use the strata_session cookie; API clients use scoped bearer tokens (" + scopeNote + ")",
		},
		"servers": []map[string]string{{"url": "/"}},
		"tags":    tags,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerToken": map[string]any{
					"type":         "http",
					"scheme":       "bearer",
					"description":  "Scoped API token created on POST /api/me/tokens",
					"bearerFormat": "stt_…",
				},
				"sessionCookie": map[string]any{
					"type": "apiKey",
					"in":   "cookie",
					"name": "strata_session",
				},
			},
		},
		"security": security,
		"paths":    paths,
	}
}

// operation describes one method of one route.
func (s *Server) operation(rt apiRoute, method string) map[string]any {
	op := map[string]any{
		"tags":        []string{openapiTag(rt.Path)},
		"operationId": openapiId(rt, method),
		"responses": map[string]any{
			"200": map[string]any{"description": "Success"},
			"401": map[string]any{"description": "Authentication required"},
			"403": map[string]any{"description": "Insufficient rights or token scope"},
		},
	}
	switch rt.Level {
	case "public":
		op["security"] = []map[string]any{}
	case "admin":
		op["description"] = "Administrator only."
	}
	return op
}

// openapiPath converts a Go ServeMux pattern to an OpenAPI path
// ("/api/files/{path...}" → "/api/files/{path}").
func openapiPath(p string) string {
	return strings.ReplaceAll(p, "...}", "}")
}

// openapiTag derives the tag (first segment after /api) of a route.
func openapiTag(p string) string {
	rest, ok := strings.CutPrefix(p, "/api/")
	if !ok {
		return "other"
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return "other"
	}
	return rest
}

// openapiId builds a stable operation id from the method and path.
func openapiId(rt apiRoute, method string) string {
	var b strings.Builder
	b.WriteString(method)
	for _, part := range strings.Split(openapiPath(rt.Path), "/") {
		if part == "" || part == "api" {
			continue
		}
		part = strings.Trim(part, "{}")
		part = strings.ReplaceAll(strings.ReplaceAll(part, ".", ""), "-", "_")
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}
