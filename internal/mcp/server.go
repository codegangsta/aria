// Package mcp implements an MCP server for Aria
// This allows Claude to call back into Aria for permission prompts
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// Server implements an MCP server over stdio
type Server struct {
	name    string
	version string
	tools   map[string]*Tool
	logger  *slog.Logger

	// Handler for permission prompts - set by Aria
	permissionHandler PermissionHandler
}

// PermissionHandler is called when Claude needs permission for a tool
type PermissionHandler func(ctx context.Context, chatID int64, toolName string, input map[string]interface{}) (*PermissionResponse, error)

// PermissionResponse is the response to a permission request
type PermissionResponse struct {
	Behavior     string                 `json:"behavior"` // "allow", "deny", "allow-always"
	UpdatedInput map[string]interface{} `json:"updatedInput,omitempty"`
	Message      string                 `json:"message,omitempty"` // For deny
}

// Tool represents an MCP tool
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// NewServer creates a new MCP server
func NewServer(name, version string, logger *slog.Logger) *Server {
	s := &Server{
		name:    name,
		version: version,
		tools:   make(map[string]*Tool),
		logger:  logger,
	}

	// Register the permission prompt tool
	s.tools["prompt_permission"] = &Tool{
		Name:        "prompt_permission",
		Description: "Request permission from the user for a tool operation",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"tool_name": map[string]interface{}{
					"type":        "string",
					"description": "The name of the tool being requested",
				},
				"input": map[string]interface{}{
					"type":        "object",
					"description": "The input parameters for the tool",
				},
			},
			"required": []string{"tool_name", "input"},
		},
	}

	return s
}

// SetPermissionHandler sets the handler for permission prompts
func (s *Server) SetPermissionHandler(h PermissionHandler) {
	s.permissionHandler = h
}

// JSON-RPC types
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs the MCP server on the given reader/writer (typically stdin/stdout)
// chatID is passed through to the permission handler
func (s *Server) Serve(ctx context.Context, chatID int64, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// Increase buffer for large messages
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var mu sync.Mutex
	write := func(resp jsonRPCResponse) error {
		mu.Lock()
		defer mu.Unlock()
		data, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", data)
		return err
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.logger.Debug("mcp: failed to parse request", "error", err, "line", line)
			continue
		}

		s.logger.Debug("mcp: received request", "method", req.Method, "id", req.ID)

		resp := s.handleRequest(ctx, chatID, req)
		if err := write(resp); err != nil {
			s.logger.Error("mcp: failed to write response", "error", err)
			return err
		}
	}

	return scanner.Err()
}

func (s *Server) handleRequest(ctx context.Context, chatID int64, req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, chatID, req)
	case "notifications/initialized":
		// Client notification, no response needed but we return empty success
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}
	default:
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "Method not found"},
		}
	}
}

func (s *Server) handleInitialize(req jsonRPCRequest) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    s.name,
				"version": s.version,
			},
		},
	}
}

func (s *Server) handleToolsList(req jsonRPCRequest) jsonRPCResponse {
	tools := make([]map[string]interface{}, 0, len(s.tools))
	for _, tool := range s.tools {
		tools = append(tools, map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		})
	}

	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"tools": tools,
		},
	}
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

func (s *Server) handleToolsCall(ctx context.Context, chatID int64, req jsonRPCRequest) jsonRPCResponse {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: "Invalid params"},
		}
	}

	s.logger.Info("mcp: tool call", "tool", params.Name, "chat_id", chatID)

	if params.Name != "prompt_permission" {
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: "Unknown tool"},
		}
	}

	if s.permissionHandler == nil {
		// No handler, deny by default
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": `{"behavior":"deny","message":"No permission handler configured"}`,
					},
				},
			},
		}
	}

	// Extract tool_name and input from arguments
	toolName, _ := params.Arguments["tool_name"].(string)
	toolInput, _ := params.Arguments["input"].(map[string]interface{})

	resp, err := s.permissionHandler(ctx, chatID, toolName, toolInput)
	if err != nil {
		s.logger.Error("mcp: permission handler error", "error", err)
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": fmt.Sprintf(`{"behavior":"deny","message":"Error: %s"}`, err.Error()),
					},
				},
			},
		}
	}

	// Marshal the response as JSON text content
	respJSON, _ := json.Marshal(resp)
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": string(respJSON),
				},
			},
		},
	}
}
