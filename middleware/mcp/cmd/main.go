// Package main provides a stdio MCP transport adapter.
// Build and run this binary to expose your MCP server via stdin/stdout,
// which is the transport mode used by Claude Desktop and other local clients.
//
// Build:
//
//	go build -o my-mcp-server ./cmd/mcp-stdio
//
// Configure in Claude Desktop (claude_desktop_config.json):
//
//	{
//	  "mcpServers": {
//	    "my-api": {
//	      "command": "/path/to/my-mcp-server",
//	      "args": []
//	    }
//	  }
//	}
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/xs23933/core/v3/middleware/mcp"
)

func main() {
	srv := mcp.NewServer("core-mcp-stdio", "0.1.0")

	// ─── Register your tools here ──────────────────────────────────

	srv.Tool("hello", "Say hello", mcp.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"name": map[string]any{"type": "string", "description": "Name to greet"},
		},
		Required: []string{"name"},
	}, func(args map[string]any) (any, error) {
		name, _ := args["name"].(string)
		return fmt.Sprintf("Hello, %s!", name), nil
	})

	srv.Tool("echo", "Echo back the input", mcp.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"message": map[string]any{"type": "string", "description": "Message to echo"},
		},
		Required: []string{"message"},
	}, func(args map[string]any) (any, error) {
		msg, _ := args["message"].(string)
		return msg, nil
	})

	// ─── Stdio transport loop ──────────────────────────────────────

	scanner := bufio.NewScanner(os.Stdin)
	// Allow lines up to 1MB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req mcp.JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeResponse(mcp.JSONRPCResponse{
				JSONRPC: "2.0",
				Error:   &mcp.RPCError{Code: -32700, Message: "parse error"},
			})
			continue
		}

		resp := srv.HandleRequest(context.Background(), req)
		// Notifications get no response.
		if req.ID == nil {
			continue
		}
		writeResponse(resp)
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "stdin read error: %v\n", err)
		os.Exit(1)
	}
}

func writeResponse(resp mcp.JSONRPCResponse) {
	raw, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal error: %v\n", err)
		return
	}
	fmt.Println(string(raw))
}
