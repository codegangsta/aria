// Package trackers provides unified management for chat-scoped trackers
package trackers

import (
	"sync"

	"github.com/codegangsta/aria/internal/telegram"
)

// PendingQuestion stores context for an AskUserQuestion waiting for user input
type PendingQuestion struct {
	ToolID     string
	Questions  []telegram.Question
	CurrentIdx int      // Which question we're on (0-indexed)
	Answers    []string // Collected answers so far
}

// ChatTrackers holds all trackers for a single chat
type ChatTrackers struct {
	Tool     *telegram.ToolStatusTracker
	Progress *telegram.ProgressTracker
	Question *PendingQuestion
}

// Manager manages all tracker types for all chats
type Manager struct {
	bot      *telegram.Bot
	chats    map[int64]*ChatTrackers
	mu       sync.RWMutex
}

// NewManager creates a new tracker manager
func NewManager(bot *telegram.Bot) *Manager {
	return &Manager{
		bot:   bot,
		chats: make(map[int64]*ChatTrackers),
	}
}

// getOrCreate gets or creates the ChatTrackers for a chat (must hold write lock)
func (m *Manager) getOrCreate(chatID int64) *ChatTrackers {
	if ct, ok := m.chats[chatID]; ok {
		return ct
	}
	ct := &ChatTrackers{}
	m.chats[chatID] = ct
	return ct
}

// ToolTracker gets or creates the tool status tracker for a chat
func (m *Manager) ToolTracker(chatID int64) *telegram.ToolStatusTracker {
	m.mu.Lock()
	defer m.mu.Unlock()

	ct := m.getOrCreate(chatID)
	if ct.Tool == nil {
		ct.Tool = telegram.NewToolStatusTracker(m.bot, chatID)
		ct.Tool.Start()
	}
	return ct.Tool
}

// ProgressTracker gets or creates the progress tracker for a chat
func (m *Manager) ProgressTracker(chatID int64) *telegram.ProgressTracker {
	m.mu.Lock()
	defer m.mu.Unlock()

	ct := m.getOrCreate(chatID)
	if ct.Progress == nil {
		ct.Progress = telegram.NewProgressTracker(m.bot, chatID)
	}
	return ct.Progress
}

// GetQuestion gets the pending question for a chat (nil if none)
func (m *Manager) GetQuestion(chatID int64) *PendingQuestion {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if ct, ok := m.chats[chatID]; ok {
		return ct.Question
	}
	return nil
}

// SetQuestion sets the pending question for a chat
func (m *Manager) SetQuestion(chatID int64, q *PendingQuestion) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ct := m.getOrCreate(chatID)
	ct.Question = q
}

// ClearQuestion clears the pending question for a chat
func (m *Manager) ClearQuestion(chatID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ct, ok := m.chats[chatID]; ok {
		ct.Question = nil
	}
}

// ClearToolTracker flushes and clears the tool tracker for a chat
func (m *Manager) ClearToolTracker(chatID int64) {
	m.mu.RLock()
	ct := m.chats[chatID]
	m.mu.RUnlock()

	if ct != nil && ct.Tool != nil {
		ct.Tool.Flush()
		ct.Tool.Clear()
	}
}

// ClearProgressTracker clears the progress tracker for a chat
func (m *Manager) ClearProgressTracker(chatID int64) {
	m.mu.RLock()
	ct := m.chats[chatID]
	m.mu.RUnlock()

	if ct != nil && ct.Progress != nil {
		ct.Progress.Clear()
	}
}

// ClearAll clears all trackers for a chat
func (m *Manager) ClearAll(chatID int64) {
	m.ClearToolTracker(chatID)
	m.ClearProgressTracker(chatID)
	m.ClearQuestion(chatID)
}
