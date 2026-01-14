package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/codegangsta/aria/internal/claude"
)

// CdCommand handles /cd - changes the working directory
type CdCommand struct {
	manager *claude.ProcessManager
	homeDir string
}

// NewCdCommand creates a new cd command
func NewCdCommand(manager *claude.ProcessManager, homeDir string) *CdCommand {
	return &CdCommand{
		manager: manager,
		homeDir: homeDir,
	}
}

func (c *CdCommand) Name() string {
	return "cd"
}

func (c *CdCommand) Execute(ctx context.Context, chatID int64, args string) (*Response, error) {
	args = strings.TrimSpace(args)

	// No path provided - show current cwd
	if args == "" {
		currentCwd := c.manager.GetCwd(chatID)
		if currentCwd == "" {
			currentCwd = "(default)"
		}
		return &Response{
			Text:   fmt.Sprintf("Working directory: %s", currentCwd),
			Silent: true,
		}, nil
	}

	// Expand ~ to home directory
	newCwd := args
	if strings.HasPrefix(newCwd, "~") {
		newCwd = strings.Replace(newCwd, "~", c.homeDir, 1)
	}

	// Resolve to absolute path
	newCwd, err := filepath.Abs(newCwd)
	if err != nil {
		return &Response{
			Text:   fmt.Sprintf("Invalid path: %v", err),
			Silent: false,
		}, nil
	}

	// Validate path exists and is a directory
	info, err := os.Stat(newCwd)
	if err != nil {
		if os.IsNotExist(err) {
			return &Response{
				Text:   fmt.Sprintf("Directory not found: %s", newCwd),
				Silent: false,
			}, nil
		}
		return &Response{
			Text:   fmt.Sprintf("Error checking path: %v", err),
			Silent: false,
		}, nil
	}
	if !info.IsDir() {
		return &Response{
			Text:   fmt.Sprintf("Not a directory: %s", newCwd),
			Silent: false,
		}, nil
	}

	// Change the cwd (kills process, preserves session)
	slog.Info("changing cwd", "chat_id", chatID, "cwd", newCwd)
	c.manager.SetCwd(chatID, newCwd)

	// Format display path (collapse home dir back to ~)
	displayPath := newCwd
	if strings.HasPrefix(newCwd, c.homeDir) {
		displayPath = "~" + strings.TrimPrefix(newCwd, c.homeDir)
	}
	return &Response{
		Text:   fmt.Sprintf("Now working in %s", displayPath),
		Silent: false,
	}, nil
}
