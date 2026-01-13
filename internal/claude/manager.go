package claude

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// ProcessManager manages a pool of persistent Claude processes, one per chat
type ProcessManager struct {
	claudePath string
	debug      bool
	processes  map[int64]*ClaudeProcess
	mu         sync.RWMutex
	logger     *slog.Logger
}

// NewManager creates a new ProcessManager
func NewManager(claudePath string, debug bool, logger *slog.Logger) *ProcessManager {
	return &ProcessManager{
		claudePath: claudePath,
		debug:      debug,
		processes:  make(map[int64]*ClaudeProcess),
		logger:     logger,
	}
}

// GetOrCreate returns an existing process for the chat or creates a new one
func (m *ProcessManager) GetOrCreate(chatID int64) (*ClaudeProcess, error) {
	// Check if we have an existing process
	m.mu.RLock()
	proc, exists := m.processes[chatID]
	m.mu.RUnlock()

	if exists && proc.Alive() {
		return proc, nil
	}

	// Need to create a new process
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if proc, exists = m.processes[chatID]; exists && proc.Alive() {
		return proc, nil
	}

	// Clean up dead process if it exists
	if exists {
		proc.Close()
		delete(m.processes, chatID)
	}

	// Create new process
	m.logger.Info("creating new claude process", "chat_id", chatID)
	newProc, err := NewProcess(m.claudePath, chatID, m.debug, m.logger)
	if err != nil {
		return nil, fmt.Errorf("creating process for chat %d: %w", chatID, err)
	}

	m.processes[chatID] = newProc
	return newProc, nil
}

// Send sends a message to the Claude process for a chat and reads the responses
// The callbacks struct contains handlers for text messages and tool use events
func (m *ProcessManager) Send(ctx context.Context, chatID int64, message string, callbacks ResponseCallbacks) error {
	proc, err := m.GetOrCreate(chatID)
	if err != nil {
		return err
	}

	// Send the message
	if err := proc.Send(message); err != nil {
		// Process may have died, remove it and let next message create a new one
		m.mu.Lock()
		delete(m.processes, chatID)
		m.mu.Unlock()
		return fmt.Errorf("sending message: %w", err)
	}

	// Read responses
	if err := proc.ReadResponses(ctx, callbacks); err != nil {
		// Process may have died
		m.mu.Lock()
		delete(m.processes, chatID)
		m.mu.Unlock()
		return fmt.Errorf("reading responses: %w", err)
	}

	return nil
}

// Shutdown gracefully closes all Claude processes
func (m *ProcessManager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("shutting down all claude processes", "count", len(m.processes))

	for chatID, proc := range m.processes {
		if err := proc.Close(); err != nil {
			m.logger.Error("error closing process", "chat_id", chatID, "error", err)
		}
		delete(m.processes, chatID)
	}
}

// ProcessCount returns the number of active processes (useful for debugging)
func (m *ProcessManager) ProcessCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.processes)
}

// GetSlashCommands returns slash commands from any active process
// Returns nil if no processes exist yet
func (m *ProcessManager) GetSlashCommands() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, proc := range m.processes {
		if proc.Alive() {
			return proc.SlashCommands()
		}
	}
	return nil
}

// Reset kills the Claude process for a chat, forcing a fresh one on next message
func (m *ProcessManager) Reset(chatID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if proc, exists := m.processes[chatID]; exists {
		m.logger.Info("resetting claude process", "chat_id", chatID)
		proc.Close()
		delete(m.processes, chatID)
	}
}
