package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	// EnvCallbackPort is the environment variable for the callback server port
	EnvCallbackPort = "ARIA_CALLBACK_PORT"
	// EnvCallbackChatID is the environment variable for the chat ID
	EnvCallbackChatID = "ARIA_CHAT_ID"
)

// PermissionRequest is the request sent from MCP subprocess to parent
type PermissionRequest struct {
	ChatID   int64                  `json:"chat_id"`
	ToolName string                 `json:"tool_name"`
	Input    map[string]interface{} `json:"input"`
}

// CallbackServer runs in the parent Aria process and receives permission requests
type CallbackServer struct {
	listener net.Listener
	server   *http.Server
	port     int
	handler  func(ctx context.Context, req PermissionRequest) (*PermissionResponse, error)
	logger   *slog.Logger
	wg       sync.WaitGroup
}

// NewCallbackServer creates a callback server on a random port
func NewCallbackServer(logger *slog.Logger) (*CallbackServer, error) {
	// Listen on random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("creating listener: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	cs := &CallbackServer{
		listener: listener,
		port:     port,
		logger:   logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/permission", cs.handlePermission)

	cs.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second, // Long timeout for user interaction
	}

	return cs, nil
}

// Start begins serving requests
func (cs *CallbackServer) Start() {
	cs.wg.Add(1)
	go func() {
		defer cs.wg.Done()
		if err := cs.server.Serve(cs.listener); err != http.ErrServerClosed {
			cs.logger.Error("callback server error", "error", err)
		}
	}()
	cs.logger.Info("callback server started", "port", cs.port)
}

// Stop gracefully shuts down the server
func (cs *CallbackServer) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cs.server.Shutdown(ctx)
	cs.wg.Wait()
}

// Port returns the server's port
func (cs *CallbackServer) Port() int {
	return cs.port
}

// SetHandler sets the permission request handler
func (cs *CallbackServer) SetHandler(h func(ctx context.Context, req PermissionRequest) (*PermissionResponse, error)) {
	cs.handler = h
}

func (cs *CallbackServer) handlePermission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		cs.logger.Error("failed to read request body", "error", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req PermissionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		cs.logger.Error("failed to parse request", "error", err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	cs.logger.Info("permission request received",
		"chat_id", req.ChatID,
		"tool", req.ToolName,
	)

	if cs.handler == nil {
		// No handler, deny
		resp := &PermissionResponse{
			Behavior: "deny",
			Message:  "No handler configured",
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	ctx := r.Context()
	resp, err := cs.handler(ctx, req)
	if err != nil {
		cs.logger.Error("handler error", "error", err)
		resp = &PermissionResponse{
			Behavior: "deny",
			Message:  err.Error(),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// CallbackClient is used by the MCP subprocess to call the parent
type CallbackClient struct {
	port   int
	chatID int64
	client *http.Client
}

// NewCallbackClientFromEnv creates a client using environment variables
func NewCallbackClientFromEnv() (*CallbackClient, error) {
	portStr := os.Getenv(EnvCallbackPort)
	if portStr == "" {
		return nil, fmt.Errorf("%s not set", EnvCallbackPort)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port: %w", err)
	}

	chatIDStr := os.Getenv(EnvCallbackChatID)
	if chatIDStr == "" {
		return nil, fmt.Errorf("%s not set", EnvCallbackChatID)
	}
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid chat ID: %w", err)
	}

	return &CallbackClient{
		port:   port,
		chatID: chatID,
		client: &http.Client{
			Timeout: 120 * time.Second, // Long timeout for user interaction
		},
	}, nil
}

// RequestPermission sends a permission request to the parent and waits for response
func (cc *CallbackClient) RequestPermission(ctx context.Context, toolName string, input map[string]interface{}) (*PermissionResponse, error) {
	req := PermissionRequest{
		ChatID:   cc.chatID,
		ToolName: toolName,
		Input:    input,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/permission", cc.port)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := cc.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server error %d: %s", resp.StatusCode, string(body))
	}

	var permResp PermissionResponse
	if err := json.NewDecoder(resp.Body).Decode(&permResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &permResp, nil
}
