package claude

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// ProcessManager manages a pool of persistent Claude processes, one per chat
type ProcessManager struct {
	claudePath      string
	debug           bool
	skipPermissions bool
	processes       map[int64]*ClaudeProcess
	mu              sync.RWMutex
	logger          *slog.Logger
	persistence     *SessionPersistence
}

// NewManager creates a new ProcessManager
func NewManager(claudePath string, debug bool, skipPermissions bool, logger *slog.Logger) *ProcessManager {
	return &ProcessManager{
		claudePath:      claudePath,
		debug:           debug,
		skipPermissions: skipPermissions,
		processes:       make(map[int64]*ClaudeProcess),
		logger:          logger,
	}
}

// SetPersistence sets the session persistence handler
func (m *ProcessManager) SetPersistence(p *SessionPersistence) {
	m.persistence = p
}

// GetOrCreate returns an existing process for the chat or creates a new one
// If a persisted session ID exists, it will resume that session
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

	// Check for persisted session ID and cwd to resume
	var resumeSessionID string
	var cwd string
	if m.persistence != nil {
		resumeSessionID = m.persistence.Get(chatID)
		cwd = m.persistence.GetCwd(chatID)
		if resumeSessionID != "" {
			m.logger.Info("resuming persisted session", "chat_id", chatID, "session_id", resumeSessionID, "cwd", cwd)
		}
	}

	// Create new process (with resume if we have a persisted session)
	m.logger.Info("creating new claude process", "chat_id", chatID, "resume", resumeSessionID != "", "cwd", cwd)
	newProc, err := NewProcess(m.claudePath, chatID, m.debug, m.skipPermissions, resumeSessionID, cwd, m.logger)
	if err != nil {
		return nil, fmt.Errorf("creating process for chat %d: %w", chatID, err)
	}

	m.processes[chatID] = newProc
	return newProc, nil
}

// GetOrCreateWithSession returns an existing process or creates one that resumes a specific session
// If sessionID is empty, behaves like GetOrCreate (starts fresh)
// If sessionID is provided, kills any existing process and starts a new one with --resume
func (m *ProcessManager) GetOrCreateWithSession(chatID int64, sessionID string) (*ClaudeProcess, error) {
	// If no session specified, use normal behavior
	if sessionID == "" {
		return m.GetOrCreate(chatID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Kill existing process if any
	if proc, exists := m.processes[chatID]; exists {
		m.logger.Info("killing existing process for session switch", "chat_id", chatID)
		proc.Close()
		delete(m.processes, chatID)
	}

	// Get cwd from persistence (preserve across session switches)
	var cwd string
	if m.persistence != nil {
		cwd = m.persistence.GetCwd(chatID)
	}

	// Create new process with resume flag
	m.logger.Info("creating claude process with session", "chat_id", chatID, "session_id", sessionID, "cwd", cwd)
	newProc, err := NewProcess(m.claudePath, chatID, m.debug, m.skipPermissions, sessionID, cwd, m.logger)
	if err != nil {
		return nil, fmt.Errorf("creating process with session %s: %w", sessionID, err)
	}

	m.processes[chatID] = newProc

	// Persist this session ID so it survives restarts
	if m.persistence != nil {
		m.persistence.Set(chatID, sessionID)
	}

	return newProc, nil
}

// Send sends a message to the Claude process for a chat and reads the responses
// The callbacks struct contains handlers for text messages and tool use events
// If the process dies mid-conversation, it will automatically retry by resuming the session
func (m *ProcessManager) Send(ctx context.Context, chatID int64, message string, callbacks ResponseCallbacks) error {
	return m.sendWithRetry(ctx, chatID, message, callbacks, 1)
}

// sendWithRetry attempts to send a message, retrying once if the process dies
func (m *ProcessManager) sendWithRetry(ctx context.Context, chatID int64, message string, callbacks ResponseCallbacks, retriesLeft int) error {
	proc, err := m.GetOrCreate(chatID)
	if err != nil {
		return err
	}

	// Send the message
	if err := proc.Send(message); err != nil {
		// Process may have died, remove it
		m.mu.Lock()
		delete(m.processes, chatID)
		m.mu.Unlock()

		// Retry if we have retries left
		if retriesLeft > 0 {
			m.logger.Info("send failed, retrying",
				"chat_id", chatID,
				"error", err,
			)
			return m.sendWithRetry(ctx, chatID, message, callbacks, retriesLeft-1)
		}
		return fmt.Errorf("sending message: %w", err)
	}

	// Read responses
	if err := proc.ReadResponses(ctx, callbacks); err != nil {
		// Process may have died
		m.mu.Lock()
		delete(m.processes, chatID)
		m.mu.Unlock()

		// Check if session was not found - clear it from persistence
		if proc.SessionNotFound() && m.persistence != nil {
			m.logger.Info("clearing stale session",
				"chat_id", chatID,
			)
			m.persistence.Delete(chatID)
		}

		// Retry if we have retries left
		if retriesLeft > 0 {
			m.logger.Info("read failed, retrying",
				"chat_id", chatID,
				"error", err,
			)
			return m.sendWithRetry(ctx, chatID, message, callbacks, retriesLeft-1)
		}
		return fmt.Errorf("reading responses: %w", err)
	}

	// Persist session ID if we got one from init event
	if m.persistence != nil {
		if newSessionID := proc.SessionID(); newSessionID != "" {
			m.persistence.Set(chatID, newSessionID)
		}
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
// Also clears any persisted session so the next message starts fresh
func (m *ProcessManager) Reset(chatID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if proc, exists := m.processes[chatID]; exists {
		m.logger.Info("resetting claude process", "chat_id", chatID)
		proc.Close()
		delete(m.processes, chatID)
	}

	// Clear persisted session so next message starts fresh
	if m.persistence != nil {
		m.persistence.Delete(chatID)
	}
}

// SetCwd changes the working directory for a chat
// This kills the current process but preserves the session for resume
func (m *ProcessManager) SetCwd(chatID int64, cwd string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Kill existing process
	if proc, exists := m.processes[chatID]; exists {
		m.logger.Info("killing process for cwd change", "chat_id", chatID, "new_cwd", cwd)
		proc.Close()
		delete(m.processes, chatID)
	}

	// Set new cwd while preserving session for resume
	if m.persistence != nil {
		m.persistence.SetCwdPreserveSession(chatID, cwd)
	}
}

// GetCwd returns the current working directory for a chat
func (m *ProcessManager) GetCwd(chatID int64) string {
	if m.persistence != nil {
		return m.persistence.GetCwd(chatID)
	}
	return ""
}
