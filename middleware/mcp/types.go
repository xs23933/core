// Package mcp provides a Model Context Protocol (MCP) middleware for the core web framework.
// It supports the Streamable HTTP transport mode and allows registering tools, resources,
// and prompts that can be discovered and called by MCP clients.
//
// MCP Protocol Version: 2025-03-26
//
// Usage:
//
//	mcpSrv := mcp.NewServer("my-server", "1.0.0")
//	mcpSrv.Tool("get_user", "Get user by ID", mcp.ToolSchema{
//		Type: "object",
//		Properties: map[string]any{
//			"id": map[string]any{"type": "string", "description": "User ID"},
//		},
//		Required: []string{"id"},
//	}, func(args map[string]any) (any, error) {
//		return getUser(args["id"].(string))
//	})
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ProtocolVersion is the MCP protocol version this middleware supports.
const ProtocolVersion = "2025-03-26"

// ─── JSON-RPC 2.0 Types ─────────────────────────────────────────────

// JSONRPCRequest is a JSON-RPC 2.0 request. Exported for stdio transport.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response. Exported for stdio transport.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error. Exported for stdio transport.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Internal aliases — used within the package.
type jsonRPCRequest = JSONRPCRequest
type jsonRPCResponse = JSONRPCResponse
type rpcError = RPCError

// JSON-RPC error codes (per spec).
const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternalError  = -32603
)

// ─── MCP Protocol Types ─────────────────────────────────────────────

// ServerInfo identifies the MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCapabilities declares what the server supports.
type ServerCapabilities struct {
	Tools     *toolCapability     `json:"tools,omitempty"`
	Resources *resourceCapability `json:"resources,omitempty"`
	Prompts   *promptCapability   `json:"prompts,omitempty"`
}

type toolCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type resourceCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

type promptCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeResult is the response to initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ─── Tool Types ──────────────────────────────────────────────────────

// ToolSchema defines the JSON Schema for a tool's input.
type ToolSchema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
	Required   []string       `json:"required,omitempty"`
}

// Tool represents an MCP tool.
type Tool struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema ToolSchema `json:"inputSchema"`
	Annotations Map        `json:"annotations,omitempty"`
}

// ToolHandler processes a tool call and returns the result.
// The context.Context carries lifecycle (cancel/timeout) and, when called
// via the HTTP transport, the core.Ctx via mcp.CtxFrom().
type ToolHandler func(ctx context.Context, args map[string]any) (any, error)

// ToolCallParams is the params for tools/call.
type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ContentBlock represents a content block in a tool result.
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// ToolCallResult is the result of tools/call.
type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ─── Resource Types ──────────────────────────────────────────────────

// Resource represents an MCP resource.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceHandler reads a resource and returns its content.
type ResourceHandler func(uri string) ([]ContentBlock, error)

// ─── Prompt Types ────────────────────────────────────────────────────

// PromptArgument defines a prompt template argument.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Prompt represents an MCP prompt template.
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptHandler resolves a prompt with given arguments.
type PromptHandler func(args map[string]string) ([]ContentBlock, error)

// ─── Map ─────────────────────────────────────────────────────────────

// Map is a convenience alias for map[string]any, used in tool schemas
// and annotations.
type Map = map[string]any

// ─── Session ─────────────────────────────────────────────────────────

// Session tracks an MCP client session.
type Session struct {
	ID           string
	ClientInfo   *ServerInfo
	Initialized  bool
	CreatedAt    time.Time
	lastActivity time.Time
}

// sessionStore manages active sessions.
type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*Session)}
}

func (s *sessionStore) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if ok {
		sess.lastActivity = time.Now()
	}
	return sess, ok
}

func (s *sessionStore) Put(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
}

func (s *sessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// ─── Errors ──────────────────────────────────────────────────────────

var (
	ErrToolNotFound     = errors.New("tool not found")
	ErrResourceNotFound = errors.New("resource not found")
	ErrPromptNotFound   = errors.New("prompt not found")
	ErrNotInitialized   = errors.New("server not initialized")
)
