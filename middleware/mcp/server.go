package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MCPServer is the core MCP protocol handler. Transport-agnostic.
type MCPServer struct {
	mu        sync.RWMutex
	info      ServerInfo
	tools     map[string]*registeredTool
	toolOrder []string // preserves registration order
	resources map[string]*registeredResource
	prompts   map[string]*registeredPrompt

	sessions *sessionStore

	// Middleware for tool calls.
	middlewares []ToolMiddleware
}

type registeredTool struct {
	Tool
	handler ToolHandler
}

type registeredResource struct {
	Resource
	handler ResourceHandler
}

type registeredPrompt struct {
	Prompt
	handler PromptHandler
}

// ToolMiddleware wraps a ToolHandler for cross-cutting concerns (auth, logging, rate-limit).
type ToolMiddleware func(name string, args map[string]any, next ToolHandler) (any, error)

// NewServer creates a new MCP server core.
func NewServer(name, version string) *MCPServer {
	return &MCPServer{
		info:      ServerInfo{Name: name, Version: version},
		tools:     make(map[string]*registeredTool),
		toolOrder: make([]string, 0),
		resources: make(map[string]*registeredResource),
		prompts:   make(map[string]*registeredPrompt),
		sessions:  newSessionStore(),
	}
}

// ─── Registration ────────────────────────────────────────────────────

// Tool registers an MCP tool.
func (s *MCPServer) Tool(name, description string, schema ToolSchema, handler ToolHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tools[name]; !exists {
		s.toolOrder = append(s.toolOrder, name)
	}
	s.tools[name] = &registeredTool{
		Tool: Tool{
			Name:        name,
			Description: description,
			InputSchema: schema,
		},
		handler: handler,
	}
}

// Resource registers an MCP resource.
func (s *MCPServer) Resource(uri, name, description, mimeType string, handler ResourceHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[uri] = &registeredResource{
		Resource: Resource{
			URI:         uri,
			Name:        name,
			Description: description,
			MimeType:    mimeType,
		},
		handler: handler,
	}
}

// Prompt registers an MCP prompt template.
func (s *MCPServer) Prompt(name, description string, args []PromptArgument, handler PromptHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts[name] = &registeredPrompt{
		Prompt: Prompt{
			Name:        name,
			Description: description,
			Arguments:   args,
		},
		handler: handler,
	}
}

// Use adds middleware that wraps every tool call.
func (s *MCPServer) Use(mw ToolMiddleware) {
	s.middlewares = append(s.middlewares, mw)
}

// ─── Request Dispatch ────────────────────────────────────────────────

// HandleRequest processes a single JSON-RPC request and returns a response.
// This is the transport-agnostic core.
func (s *MCPServer) HandleRequest(ctx context.Context, req jsonRPCRequest) jsonRPCResponse {
	// Notifications (no id) get no response.
	if req.ID == nil {
		s.handleNotification(ctx, req)
		return jsonRPCResponse{}
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "ping":
		return s.handlePing(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "resources/list":
		return s.handleResourcesList(req)
	case "resources/read":
		return s.handleResourcesRead(req)
	case "prompts/list":
		return s.handlePromptsList(req)
	case "prompts/get":
		return s.handlePromptsGet(req)
	default:
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: errMethodNotFound, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

// ─── Handlers ────────────────────────────────────────────────────────

func (s *MCPServer) handleInitialize(req jsonRPCRequest) jsonRPCResponse {
	var params struct {
		ProtocolVersion string      `json:"protocolVersion"`
		ClientInfo      *ServerInfo `json:"clientInfo,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcErr(req.ID, errInvalidParams, "invalid params: "+err.Error())
	}

	capabilities := ServerCapabilities{}
	if len(s.tools) > 0 {
		capabilities.Tools = &toolCapability{}
	}
	if len(s.resources) > 0 {
		capabilities.Resources = &resourceCapability{}
	}
	if len(s.prompts) > 0 {
		capabilities.Prompts = &promptCapability{}
	}

	sessID := uuid.New().String()
	sess := &Session{
		ID:          sessID,
		ClientInfo:  params.ClientInfo,
		Initialized: true,
		CreatedAt:   time.Now(),
	}
	s.sessions.Put(sess)

	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: InitializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities:    capabilities,
			ServerInfo:      s.info,
		},
	}
}

func (s *MCPServer) handlePing(req jsonRPCRequest) jsonRPCResponse {
	return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
}

func (s *MCPServer) handleToolsList(req jsonRPCRequest) jsonRPCResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tools := make([]Tool, 0, len(s.toolOrder))
	for _, name := range s.toolOrder {
		if t, ok := s.tools[name]; ok {
			tools = append(tools, t.Tool)
		}
	}

	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": tools},
	}
}

func (s *MCPServer) handleToolsCall(ctx context.Context, req jsonRPCRequest) jsonRPCResponse {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcErr(req.ID, errInvalidParams, "invalid params: "+err.Error())
	}

	s.mu.RLock()
	t, ok := s.tools[params.Name]
	s.mu.RUnlock()

	if !ok {
		return rpcErr(req.ID, errInvalidParams, fmt.Sprintf("tool not found: %s", params.Name))
	}

	// Build the handler chain with middleware.
	handler := t.handler
	for i := len(s.middlewares) - 1; i >= 0; i-- {
		mw := s.middlewares[i]
		next := handler
		handler = func(args map[string]any) (any, error) {
			return mw(params.Name, args, next)
		}
	}

	result, err := handler(params.Arguments)
	if err != nil {
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ToolCallResult{
				Content: []ContentBlock{{Type: "text", Text: err.Error()}},
				IsError: true,
			},
		}
	}

	// Auto-wrap scalar results.
	content := toContentBlocks(result)
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  ToolCallResult{Content: content},
	}
}

func (s *MCPServer) handleResourcesList(req jsonRPCRequest) jsonRPCResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	resources := make([]Resource, 0, len(s.resources))
	for _, r := range s.resources {
		resources = append(resources, r.Resource)
	}

	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"resources": resources},
	}
}

func (s *MCPServer) handleResourcesRead(req jsonRPCRequest) jsonRPCResponse {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcErr(req.ID, errInvalidParams, "invalid params: "+err.Error())
	}

	s.mu.RLock()
	r, ok := s.resources[params.URI]
	s.mu.RUnlock()

	if !ok {
		return rpcErr(req.ID, errInvalidParams, fmt.Sprintf("resource not found: %s", params.URI))
	}

	blocks, err := r.handler(params.URI)
	if err != nil {
		return rpcErr(req.ID, errInternalError, err.Error())
	}

	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"contents": blocks},
	}
}

func (s *MCPServer) handlePromptsList(req jsonRPCRequest) jsonRPCResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prompts := make([]Prompt, 0, len(s.prompts))
	for _, p := range s.prompts {
		prompts = append(prompts, p.Prompt)
	}

	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"prompts": prompts},
	}
}

func (s *MCPServer) handlePromptsGet(req jsonRPCRequest) jsonRPCResponse {
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcErr(req.ID, errInvalidParams, "invalid params: "+err.Error())
	}

	s.mu.RLock()
	p, ok := s.prompts[params.Name]
	s.mu.RUnlock()

	if !ok {
		return rpcErr(req.ID, errInvalidParams, fmt.Sprintf("prompt not found: %s", params.Name))
	}

	blocks, err := p.handler(params.Arguments)
	if err != nil {
		return rpcErr(req.ID, errInternalError, err.Error())
	}

	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"messages": blocks},
	}
}

func (s *MCPServer) handleNotification(ctx context.Context, req jsonRPCRequest) {
	// Currently no server-side action needed for notifications.
	// Future: handle "notifications/cancelled", etc.
}

// ─── Helpers ─────────────────────────────────────────────────────────

func rpcErr(id json.RawMessage, code int, msg string) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
}

// toContentBlocks converts an arbitrary result into MCP content blocks.
func toContentBlocks(result any) []ContentBlock {
	switch v := result.(type) {
	case string:
		return []ContentBlock{{Type: "text", Text: v}}
	case []ContentBlock:
		return v
	case *ContentBlock:
		return []ContentBlock{*v}
	default:
		// JSON-serialize anything else.
		raw, err := json.Marshal(v)
		if err != nil {
			return []ContentBlock{{Type: "text", Text: fmt.Sprintf("%v", v)}}
		}
		return []ContentBlock{{Type: "text", Text: string(raw)}}
	}
}
