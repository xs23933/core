package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/xs23933/core/v3/middleware/mcp"
)

// mcp-stdio is a standalone binary that runs an MCP server over stdio.
// Use it with Claude Desktop or any MCP client that supports stdio transport.
//
// Usage:
//
//	mcp-stdio
//
// Then configure Claude Desktop's claude_desktop_config.json:
//
//	{
//	  "mcpServers": {
//	    "my-api": {
//	      "command": "mcp-stdio",
//	      "args": []
//	    }
//	  }
//	}

func main() {
	srv := mcp.NewServer("my-api-stdio", "1.0.0")
	registerTools(srv)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req mcp.JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			sendError(nil, -32700, "parse error: "+err.Error())
			continue
		}

		resp := srv.HandleRequest(context.Background(), req)

		// Notifications produce no output.
		if req.ID == nil {
			continue
		}

		out, _ := json.Marshal(resp)
		fmt.Println(string(out))
	}
}

func sendError(id json.RawMessage, code int, msg string) {
	resp := mcp.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &mcp.RPCError{Code: code, Message: msg},
	}
	out, _ := json.Marshal(resp)
	fmt.Println(string(out))
}

func registerTools(srv *mcp.MCPServer) {
	srv.Tool("get_time", "Get current server time", mcp.ToolSchema{
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
}
