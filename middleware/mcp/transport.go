package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xs23933/core/v3"
)

const (
	// HeaderSessionID is the MCP session header per spec.
	HeaderSessionID = "Mcp-Session-Id"
)

// Option configures the MCP middleware.
type Option func(*config)

type config struct {
	info    ServerInfo
	path    string
	origins []string
}

// WithServerInfo sets the server name and version.
func WithServerInfo(name, version string) Option {
	return func(c *config) { c.info = ServerInfo{Name: name, Version: version} }
}

// WithPath sets the MCP endpoint path (default "/mcp").
func WithPath(path string) Option {
	return func(c *config) { c.path = path }
}

// WithOrigins sets allowed CORS origins (default ["*"]).
func WithOrigins(origins ...string) Option {
	return func(c *config) { c.origins = origins }
}

// New creates an MCP middleware and attaches it to the core app.
// Returns an *MCPServer for registering tools/resources/prompts.
//
// Usage:
//
//	app := core.New()
//	mcpSrv := mcp.New(app, mcp.WithServerInfo("my-api", "1.0.0"))
//	mcpSrv.Tool("hello", "Say hello", mcp.ToolSchema{Type: "object"}, func(args map[string]any) (any, error) {
//	    return "Hello!", nil
//	})
//	app.Run()
func New(app *core.Core, opts ...Option) *MCPServer {
	cfg := config{
		info:    ServerInfo{Name: "core-mcp", Version: "0.1.0"},
		path:    "/mcp",
		origins: []string{"*"},
	}
	for _, o := range opts {
		o(&cfg)
	}

	srv := NewServer(cfg.info.Name, cfg.info.Version)

	path := strings.TrimRight(cfg.path, "/")

	// CORS middleware scoped to MCP path.
	app.Use(path, mcpCORS(cfg.origins))

	// Session tracking middleware scoped to MCP path.
	app.Use(path, sessionMiddleware(srv))

	// POST — JSON-RPC request.
	app.POST(path, handlePost(srv))
	// GET — SSE stream for server-initiated messages.
	app.GET(path, handleGet(srv))
	// DELETE — session teardown.
	app.DELETE(path, handleDelete(srv))

	return srv
}

// ─── POST Handler ────────────────────────────────────────────────────

func handlePost(srv *MCPServer) core.HandlerFunc {
	return func(c core.Ctx) error {
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return c.Status(http.StatusBadRequest).JSON(jsonRPCResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: errParseError, Message: "failed to read body"},
			})
		}

		// Batch: array of requests.
		if len(body) > 0 && body[0] == '[' {
			var reqs []jsonRPCRequest
			if err := json.Unmarshal(body, &reqs); err != nil {
				return c.Status(http.StatusBadRequest).JSON(jsonRPCResponse{
					JSONRPC: "2.0",
					Error:   &rpcError{Code: errParseError, Message: "invalid batch request"},
				})
			}
			var results []jsonRPCResponse
			for _, req := range reqs {
				resp := srv.HandleRequest(c.Request().Context(), req)
				// Skip notifications (no response needed).
				if req.ID != nil {
					results = append(results, resp)
				}
			}
			return c.JSON(results)
		}

		// Single request.
		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return c.Status(http.StatusBadRequest).JSON(jsonRPCResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: errParseError, Message: "invalid request"},
			})
		}

		resp := srv.HandleRequest(c.Request().Context(), req)

		// Notifications get 202 Accepted with no body.
		if req.ID == nil {
			return c.Status(http.StatusAccepted).SendString("")
		}
		return c.JSON(resp)
	}
}

// ─── GET Handler (SSE) ───────────────────────────────────────────────

func handleGet(srv *MCPServer) core.HandlerFunc {
	return func(c core.Ctx) error {
		flusher, ok := c.Response().(http.Flusher)
		if !ok {
			return c.Status(http.StatusInternalServerError).SendString("streaming not supported")
		}

		c.SetHeader("Content-Type", "text/event-stream")
		c.SetHeader("Cache-Control", "no-cache")
		c.SetHeader("Connection", "keep-alive")
		c.SetHeader("Access-Control-Allow-Origin", "*")

		// Keep the connection open until client disconnects.
		ctx := c.Request().Context()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				// Heartbeat comment to keep connection alive.
				fmt.Fprintf(c.Response(), ": heartbeat\n\n")
				flusher.Flush()
			}
		}
	}
}

// ─── DELETE Handler ──────────────────────────────────────────────────

func handleDelete(srv *MCPServer) core.HandlerFunc {
	return func(c core.Ctx) error {
		sessID := c.GetHeader(HeaderSessionID)
		if sessID != "" {
			srv.sessions.Delete(sessID)
		}
		return c.Status(http.StatusNoContent).SendString("")
	}
}

// ─── Session Middleware ──────────────────────────────────────────────

func sessionMiddleware(srv *MCPServer) core.HandlerFunc {
	return func(c core.Ctx) error {
		sessID := c.GetHeader(HeaderSessionID)
		if sessID != "" {
			if _, ok := srv.sessions.Get(sessID); ok {
				c.Set(HeaderSessionID, sessID)
				c.SetHeader(HeaderSessionID, sessID)
			}
		}
		return c.Next()
	}
}

// ─── CORS Middleware ─────────────────────────────────────────────────

func mcpCORS(origins []string) core.HandlerFunc {
	allowOrigin := "*"
	if len(origins) == 1 {
		allowOrigin = origins[0]
	}

	return func(c core.Ctx) error {
		c.SetHeader("Access-Control-Allow-Origin", allowOrigin)
		c.SetHeader("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.SetHeader("Access-Control-Allow-Headers", "Content-Type, Mcp-Session-Id, Authorization")
		c.SetHeader("Access-Control-Expose-Headers", "Mcp-Session-Id")

		if c.Method() == "OPTIONS" {
			return c.Status(http.StatusNoContent).SendString("")
		}
		return c.Next()
	}
}
