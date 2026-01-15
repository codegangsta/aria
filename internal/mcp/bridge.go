package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Bridge manages an MCP server subprocess for a specific chat
type Bridge struct {
	chatID     int64
	configPath string // Path to generated MCP config file
	logger     *slog.Logger
}

// BridgeManager manages MCP bridges for all chats
type BridgeManager struct {
	bridges      map[int64]*Bridge
	mu           sync.RWMutex
	ariaPath     string // Path to aria binary for subprocess mode
	logger       *slog.Logger
	tmpDir       string // Temp directory for config files
	callbackPort int    // Port of the callback server (passed to subprocesses via env)
}

// NewBridgeManager creates a new bridge manager
// callbackPort is the port of the parent's callback server
func NewBridgeManager(ariaPath string, callbackPort int, logger *slog.Logger) (*BridgeManager, error) {
	// Create temp directory for MCP configs
	tmpDir, err := os.MkdirTemp("", "aria-mcp-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}

	return &BridgeManager{
		bridges:      make(map[int64]*Bridge),
		ariaPath:     ariaPath,
		callbackPort: callbackPort,
		logger:       logger,
		tmpDir:       tmpDir,
	}, nil
}


// GetConfigPath returns the MCP config path for a chat, creating the bridge if needed
func (m *BridgeManager) GetConfigPath(chatID int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if bridge, exists := m.bridges[chatID]; exists {
		return bridge.configPath, nil
	}

	// Create config file for this chat
	configPath := filepath.Join(m.tmpDir, fmt.Sprintf("mcp-%d.json", chatID))

	// Use the "command" transport which spawns aria --mcp-server
	// Pass callback port and chat ID via environment variables
	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"aria": map[string]interface{}{
				"command": m.ariaPath,
				"args":    []string{"--mcp-server"},
				"env": map[string]interface{}{
					EnvCallbackPort:   fmt.Sprintf("%d", m.callbackPort),
					EnvCallbackChatID: fmt.Sprintf("%d", chatID),
				},
			},
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return "", fmt.Errorf("writing config: %w", err)
	}

	m.bridges[chatID] = &Bridge{
		chatID:     chatID,
		configPath: configPath,
		logger:     m.logger,
	}

	m.logger.Info("created mcp config", "chat_id", chatID, "path", configPath)
	return configPath, nil
}

// GetToolName returns the full MCP tool name for the permission prompt
func (m *BridgeManager) GetToolName() string {
	return "mcp__aria__prompt_permission"
}

// Cleanup removes all temp files
func (m *BridgeManager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.tmpDir != "" {
		os.RemoveAll(m.tmpDir)
	}
}

// RunMCPServer runs the MCP server in stdio mode (called when aria is invoked with --mcp-server)
func RunMCPServer(chatID int64, handler PermissionHandler, logger *slog.Logger) error {
	server := NewServer("aria", "1.0.0", logger)
	server.SetPermissionHandler(handler)

	ctx := context.Background()
	return server.Serve(ctx, chatID, os.Stdin, os.Stdout)
}
