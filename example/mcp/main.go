package main

import (
	"github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/middleware/mcp"
)

func main() {
	app := core.New()

	// Register MCP middleware — one line.
	mcpSrv := mcp.New(app, mcp.WithServerInfo("mcp-demo", "1.0.0"))

	// Custom tool.
	mcpSrv.Tool("get_time", "Get current server time", mcp.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "IANA timezone (e.g. Asia/Shanghai)",
			},
		},
	}, func(args map[string]any) (any, error) {
		return "2026-05-09T13:00:00+08:00", nil
	})

	// Custom tool — returns structured data.
	mcpSrv.Tool("calculate", "Simple math", mcp.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"expression": map[string]any{"type": "string", "description": "Math expression"},
		},
		Required: []string{"expression"},
	}, func(args map[string]any) (any, error) {
		expr, _ := args["expression"].(string)
		return map[string]any{"expression": expr, "result": 42}, nil
	})

	// Route-based tool — auto-generates name and schema.
	mcpSrv.Route("GET", "/users/:id", "Get user by ID", func(args map[string]any) (any, error) {
		id := args["id"].(string)
		return map[string]any{"id": id, "name": "User " + id}, nil
	})

	app.Run()
}
