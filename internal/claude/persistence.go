package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// SessionMapping holds the chat_id to session_id mapping for persistence
type SessionMapping struct {
	ChatID     int64     `yaml:"chat_id"`
	SessionID  string    `yaml:"session_id"`
	LastActive time.Time `yaml:"last_active"`
}

// PersistedSessions holds all persisted session mappings
type PersistedSessions struct {
	Sessions []SessionMapping `yaml:"sessions"`
}

// SessionPersistence handles saving and loading session mappings
type SessionPersistence struct {
	path     string
	sessions map[int64]SessionMapping // chat_id -> mapping
	mu       sync.RWMutex
}

// NewSessionPersistence creates a new persistence handler
// path should be ~/.config/aria/sessions.yaml
func NewSessionPersistence(path string) *SessionPersistence {
	return &SessionPersistence{
		path:     path,
		sessions: make(map[int64]SessionMapping),
	}
}

// Load reads the session mappings from disk
func (p *SessionPersistence) Load() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	data, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			// No file yet, that's fine
			return nil
		}
		return fmt.Errorf("reading sessions file: %w", err)
	}

	var persisted PersistedSessions
	if err := yaml.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("parsing sessions file: %w", err)
	}

	// Convert to map
	p.sessions = make(map[int64]SessionMapping)
	for _, s := range persisted.Sessions {
		p.sessions[s.ChatID] = s
	}

	return nil
}

// Save writes the session mappings to disk
func (p *SessionPersistence) Save() error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Convert map to slice
	persisted := PersistedSessions{
		Sessions: make([]SessionMapping, 0, len(p.sessions)),
	}
	for _, s := range p.sessions {
		persisted.Sessions = append(persisted.Sessions, s)
	}

	data, err := yaml.Marshal(&persisted)
	if err != nil {
		return fmt.Errorf("marshaling sessions: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating sessions directory: %w", err)
	}

	if err := os.WriteFile(p.path, data, 0644); err != nil {
		return fmt.Errorf("writing sessions file: %w", err)
	}

	return nil
}

// Set stores a session mapping for a chat
func (p *SessionPersistence) Set(chatID int64, sessionID string) {
	p.mu.Lock()
	p.sessions[chatID] = SessionMapping{
		ChatID:     chatID,
		SessionID:  sessionID,
		LastActive: time.Now(),
	}
	p.mu.Unlock()

	// Save in background (don't block)
	go p.Save()
}

// Get returns the session ID for a chat, or empty string if none
func (p *SessionPersistence) Get(chatID int64) string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if mapping, ok := p.sessions[chatID]; ok {
		return mapping.SessionID
	}
	return ""
}

// Delete removes the session mapping for a chat
func (p *SessionPersistence) Delete(chatID int64) {
	p.mu.Lock()
	delete(p.sessions, chatID)
	p.mu.Unlock()

	go p.Save()
}

// GetAll returns all session mappings (for debugging)
func (p *SessionPersistence) GetAll() map[int64]string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[int64]string)
	for chatID, mapping := range p.sessions {
		result[chatID] = mapping.SessionID
	}
	return result
}
