package mcp

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
)

// Route registers an MCP tool derived from an API route pattern.
// It auto-generates the tool name and input schema from the method and path.
//
// Name:  "GET /users/:id" → "get_users_id"
// Schema: path params become required string properties;
//
//	POST/PUT/PATCH adds an optional "body" object property.
//
// You must provide a ToolHandler (not a core.HandlerFunc), because MCP
// calls bypass HTTP — there is no Ctx available.
//
// Usage:
//
//	mcpSrv.Route("GET", "/users/:id", "Get user by ID", func(args map[string]any) (any, error) {
//	    id := args["id"].(string)
//	    return getUser(id)
//	})
//	mcpSrv.Route("POST", "/users", "Create user", func(args map[string]any) (any, error) {
//	    body := args["body"].(map[string]any)
//	    return createUser(body)
//	})
func (s *MCPServer) Route(method, path, description string, handler ToolHandler) {
	name := routeToToolName(method, path)
	schema := routeToSchema(method, path)
	s.Tool(name, description, schema, handler)
}

// routeToToolName converts "GET /users/:id" → "get_users_id".
func routeToToolName(method, path string) string {
	name := strings.ToLower(method) + "_" + strings.Trim(path, "/")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, ":", "")
	name = strings.ReplaceAll(name, "*", "all")
	for strings.Contains(name, "__") {
		name = strings.ReplaceAll(name, "__", "_")
	}
	return strings.Trim(name, "_")
}

// routeToSchema generates a JSON Schema from a route path.
// Path params (:id, :name) become required string properties.
// POST/PUT/PATCH adds an optional "body" object property.
func routeToSchema(method, path string) ToolSchema {
	props := make(map[string]any)
	required := make([]string, 0)

	parts := strings.SplitSeq(strings.Trim(path, "/"), "/")
	for part := range parts {
		if after, ok := strings.CutPrefix(part, ":"); ok {
			props[after] = map[string]any{
				"type":        "string",
				"description": fmt.Sprintf("Path parameter: %s", after),
			}
			required = append(required, after)
		}
	}

	if method == "POST" || method == "PUT" || method == "PATCH" {
		props["body"] = map[string]any{
			"type":        "object",
			"description": "Request body (JSON)",
		}
	}

	return ToolSchema{
		Type:       "object",
		Properties: props,
		Required:   required,
	}
}

// MarshalJSON ensures ToolSchema serializes correctly via sonic.
func (s ToolSchema) MarshalJSON() ([]byte, error) {
	type Alias ToolSchema
	return sonic.Marshal(&struct{ Alias }{Alias(s)})
}
