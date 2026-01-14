package telegram

import (
	"strings"
	"sync"
	"time"

	"github.com/codegangsta/aria/internal/types"
)

// ToolStatus represents the status of a tracked tool
type ToolStatus int

const (
	ToolStatusPending ToolStatus = iota
	ToolStatusSuccess
	ToolStatusFailure
)

// TrackedTool represents a tool being tracked in the consolidated view
type TrackedTool struct {
	ID     string
	Tool   types.ToolUse
	Status ToolStatus
}

// ToolStatusTracker manages a consolidated tool status message
// that updates in-place as tools start and complete
type ToolStatusTracker struct {
	chatID   int64
	msgID    int64 // 0 if no message sent yet
	tools    []TrackedTool
	mu       sync.Mutex
	bot      *Bot
	dirty    bool
	updateCh chan struct{}
	doneCh   chan struct{}
	started  bool
}

// NewToolStatusTracker creates a new tracker for a chat
func NewToolStatusTracker(bot *Bot, chatID int64) *ToolStatusTracker {
	t := &ToolStatusTracker{
		chatID:   chatID,
		bot:      bot,
		tools:    make([]TrackedTool, 0),
		updateCh: make(chan struct{}, 1),
		doneCh:   make(chan struct{}),
	}
	return t
}

// Start begins the debounced render loop
func (t *ToolStatusTracker) Start() {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return
	}
	t.started = true
	t.mu.Unlock()

	go t.renderLoop()
}

// Stop stops the render loop and cleans up
func (t *ToolStatusTracker) Stop() {
	t.mu.Lock()
	if !t.started {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	close(t.doneCh)
}

// AddTool adds a new tool to the tracker as pending
func (t *ToolStatusTracker) AddTool(tool types.ToolUse) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.tools = append(t.tools, TrackedTool{
		ID:     tool.ID,
		Tool:   tool,
		Status: ToolStatusPending,
	})
	t.dirty = true
	t.triggerUpdate()
}

// CompleteTool marks a tool as complete (success or failure)
func (t *ToolStatusTracker) CompleteTool(toolID string, isError bool) {
	t.mu.Lock()

	for i := range t.tools {
		if t.tools[i].ID == toolID {
			if isError {
				t.tools[i].Status = ToolStatusFailure
			} else {
				t.tools[i].Status = ToolStatusSuccess
			}
			t.dirty = true
			t.mu.Unlock()
			// Render immediately for completions (no debounce)
			t.render()
			return
		}
	}
	t.mu.Unlock()
}

// Flush forces an immediate render, bypassing debounce
// Call this before Clear() to ensure final state is shown
func (t *ToolStatusTracker) Flush() {
	t.render()
}

// Clear removes all tools and resets the tracker
// Call this when a response is complete
func (t *ToolStatusTracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.tools = make([]TrackedTool, 0)
	t.msgID = 0
	t.dirty = false
}

// FlushAndClear flushes pending updates and clears the tracker
// Call this when an agent message is sent to start a new tool group
func (t *ToolStatusTracker) FlushAndClear() {
	t.Flush()
	t.Clear()
}

// HasPendingTools returns true if there are any tools being tracked
func (t *ToolStatusTracker) HasPendingTools() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.tools) > 0
}

// triggerUpdate signals the render loop to update (non-blocking)
func (t *ToolStatusTracker) triggerUpdate() {
	select {
	case t.updateCh <- struct{}{}:
	default:
		// Already has pending update
	}
}

// renderLoop handles debounced rendering
func (t *ToolStatusTracker) renderLoop() {
	debounceTimer := time.NewTimer(0)
	if !debounceTimer.Stop() {
		<-debounceTimer.C
	}

	pendingRender := false

	for {
		select {
		case <-t.doneCh:
			debounceTimer.Stop()
			return

		case <-t.updateCh:
			// Debounce: wait 150ms before rendering to batch rapid changes
			if !pendingRender {
				debounceTimer.Reset(150 * time.Millisecond)
				pendingRender = true
			}

		case <-debounceTimer.C:
			pendingRender = false
			t.render()
		}
	}
}

// render sends or edits the consolidated status message
func (t *ToolStatusTracker) render() {
	t.mu.Lock()
	if !t.dirty || len(t.tools) == 0 {
		t.mu.Unlock()
		return
	}
	t.dirty = false

	// Build the message content
	content := t.buildContent()
	msgID := t.msgID

	if msgID == 0 {
		// Send new message - keep lock to prevent race where two renders
		// both see msgID=0 and both send new messages
		newMsgID, err := t.bot.SendToolNotification(t.chatID, content)
		if err == nil {
			t.msgID = newMsgID
		}
		t.mu.Unlock()
	} else {
		// Edit existing message - safe to release lock first
		t.mu.Unlock()
		t.bot.EditMessageMarkdownV2(t.chatID, msgID, content)
	}
}

// buildContent creates the consolidated message content
func (t *ToolStatusTracker) buildContent() string {
	var lines []string

	for _, tracked := range t.tools {
		var prefix string
		switch tracked.Status {
		case ToolStatusPending:
			prefix = "◌"
		case ToolStatusSuccess:
			prefix = "✓"
		case ToolStatusFailure:
			prefix = "✗"
		}

		text := formatToolText(tracked.Tool)
		lines = append(lines, prefix+" "+text)
	}

	// Wrap entire block in italic
	return "_" + strings.Join(lines, "\n") + "_"
}
