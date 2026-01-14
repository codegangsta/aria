package commands

import (
	"context"
	"log/slog"

	"github.com/codegangsta/aria/internal/claude"
)

// ClearCommand handles /clear - resets the conversation
type ClearCommand struct {
	manager *claude.ProcessManager
}

// NewClearCommand creates a new clear command
func NewClearCommand(manager *claude.ProcessManager) *ClearCommand {
	return &ClearCommand{manager: manager}
}

func (c *ClearCommand) Name() string {
	return "clear"
}

func (c *ClearCommand) Execute(ctx context.Context, chatID int64, args string) (*Response, error) {
	slog.Info("clearing conversation", "chat_id", chatID)
	c.manager.Reset(chatID)
	return &Response{
		Text:   "Conversation cleared.",
		Silent: false,
	}, nil
}
