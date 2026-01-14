package telegram

import (
	"fmt"
	"sync"
	"time"
)

// Todo represents a single todo item from Claude's TodoWrite
type Todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"` // "pending", "in_progress", "completed"
	ActiveForm string `json:"activeForm"`
}

// ProgressTracker manages a pinned progress message for todo items
type ProgressTracker struct {
	bot       *Bot
	chatID    int64
	messageID int64 // pinned message ID (0 if none)
	todos     []Todo
	mu        sync.Mutex

	// Debouncing
	pendingUpdate bool
	debounceTimer *time.Timer
	debounceDur   time.Duration
}

// NewProgressTracker creates a new progress tracker for a chat
func NewProgressTracker(bot *Bot, chatID int64) *ProgressTracker {
	return &ProgressTracker{
		bot:         bot,
		chatID:      chatID,
		debounceDur: 150 * time.Millisecond, // Debounce rapid updates
	}
}

// Update updates the todo list and refreshes the pinned message
func (p *ProgressTracker) Update(todos []Todo) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.todos = todos

	// Check if all todos are completed
	allDone := len(todos) > 0
	for _, t := range todos {
		if t.Status != "completed" {
			allDone = false
			break
		}
	}

	if allDone {
		// All done - update immediately and unpin
		p.flushLocked()
		p.completeLocked()
		return
	}

	// Debounce the update
	p.pendingUpdate = true
	if p.debounceTimer != nil {
		p.debounceTimer.Stop()
	}
	p.debounceTimer = time.AfterFunc(p.debounceDur, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.pendingUpdate {
			p.flushLocked()
		}
	})
}

// flushLocked sends/updates the progress message (must hold lock)
func (p *ProgressTracker) flushLocked() {
	p.pendingUpdate = false

	if len(p.todos) == 0 {
		return
	}

	text := p.formatProgress()

	if p.messageID == 0 {
		// First update - send and pin
		msgID, err := p.bot.SendAndPinMessage(p.chatID, text)
		if err != nil {
			// Fallback: just send without pinning
			msgID, _ = p.bot.SendToolNotification(p.chatID, FormatMarkdownV2(text))
		}
		p.messageID = msgID
	} else {
		// Update existing message
		p.bot.EditMessageMarkdownV2(p.chatID, p.messageID, FormatMarkdownV2(text))
	}
}

// completeLocked marks progress as done and unpins (must hold lock)
func (p *ProgressTracker) completeLocked() {
	if p.messageID == 0 {
		return
	}

	// Update to show completion
	total := len(p.todos)
	text := fmt.Sprintf("✅ Done (%d/%d)", total, total)
	p.bot.EditMessageMarkdownV2(p.chatID, p.messageID, FormatMarkdownV2(text))

	// Unpin
	p.bot.UnpinMessage(p.chatID, p.messageID)
	p.messageID = 0
}

// Clear cancels any pending updates and resets state
func (p *ProgressTracker) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.debounceTimer != nil {
		p.debounceTimer.Stop()
		p.debounceTimer = nil
	}
	p.pendingUpdate = false
	p.todos = nil

	// Unpin if we have a message
	if p.messageID != 0 {
		p.bot.UnpinMessage(p.chatID, p.messageID)
		p.messageID = 0
	}
}

// Cancel marks the progress as stopped with a reason
func (p *ProgressTracker) Cancel(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.messageID == 0 {
		return
	}

	text := "❌ Stopped"
	if reason != "" {
		text += ": " + reason
	}
	p.bot.EditMessageMarkdownV2(p.chatID, p.messageID, FormatMarkdownV2(text))
	p.bot.UnpinMessage(p.chatID, p.messageID)
	p.messageID = 0
}

// formatProgress creates the progress message text
func (p *ProgressTracker) formatProgress() string {
	if len(p.todos) == 0 {
		return ""
	}

	// Count stats
	completed := 0
	var inProgressItem *Todo
	for i := range p.todos {
		if p.todos[i].Status == "completed" {
			completed++
		}
		if p.todos[i].Status == "in_progress" && inProgressItem == nil {
			inProgressItem = &p.todos[i]
		}
	}
	total := len(p.todos)

	// Build message
	lines := []string{fmt.Sprintf("📋 Progress (%d/%d)", completed, total)}

	for _, t := range p.todos {
		var icon string
		var text string
		switch t.Status {
		case "completed":
			icon = "✅"
			text = t.Content
		case "in_progress":
			icon = "⏳"
			// Use activeForm if available for in-progress items
			if t.ActiveForm != "" {
				text = t.ActiveForm
			} else {
				text = t.Content
			}
		default: // pending
			icon = "⬜"
			text = t.Content
		}
		lines = append(lines, fmt.Sprintf("%s %s", icon, text))
	}

	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}

// GetInlineStatus returns a short status line for inline display in messages
// e.g., "[2/5] Running tests..."
func (p *ProgressTracker) GetInlineStatus() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.todos) == 0 {
		return ""
	}

	completed := 0
	var currentTask string
	for _, t := range p.todos {
		if t.Status == "completed" {
			completed++
		}
		if t.Status == "in_progress" && currentTask == "" {
			if t.ActiveForm != "" {
				currentTask = t.ActiveForm
			} else {
				currentTask = t.Content
			}
		}
	}

	total := len(p.todos)
	if currentTask != "" {
		return fmt.Sprintf("[%d/%d] %s", completed, total, currentTask)
	}
	return fmt.Sprintf("[%d/%d]", completed, total)
}

// HasTodos returns true if there are any todos being tracked
func (p *ProgressTracker) HasTodos() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.todos) > 0
}
