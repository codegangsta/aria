package commands

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// ExitCommand handles /exit - gracefully exits so launchd can restart
type ExitCommand struct{}

// NewExitCommand creates a new exit command
func NewExitCommand() *ExitCommand {
	return &ExitCommand{}
}

func (c *ExitCommand) Name() string {
	return "exit"
}

func (c *ExitCommand) Execute(ctx context.Context, chatID int64, args string) (*Response, error) {
	slog.Info("exit command received, shutting down for launchd restart", "chat_id", chatID)

	// Schedule exit after response is sent
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()

	return &Response{
		Text:   "Restarting...",
		Silent: false,
	}, nil
}
