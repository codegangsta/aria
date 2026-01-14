package commands

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/codegangsta/aria/internal/claude"
	"github.com/codegangsta/aria/internal/telegram"
)

// RebuildCommand handles /rebuild - recompiles and restarts ARIA
type RebuildCommand struct {
	manager        *claude.ProcessManager
	bot            *telegram.Bot
	sourceDir      string
	executablePath string
}

// NewRebuildCommand creates a new rebuild command
func NewRebuildCommand(manager *claude.ProcessManager, bot *telegram.Bot, sourceDir, executablePath string) *RebuildCommand {
	return &RebuildCommand{
		manager:        manager,
		bot:            bot,
		sourceDir:      sourceDir,
		executablePath: executablePath,
	}
}

func (c *RebuildCommand) Name() string {
	return "rebuild"
}

func (c *RebuildCommand) Execute(ctx context.Context, chatID int64, args string) (*Response, error) {
	slog.Info("rebuild requested", "chat_id", chatID)

	// Run rebuild in background - it will restart the process
	go func() {
		if err := c.rebuildAndRestart(); err != nil {
			slog.Error("rebuild failed", "error", err)
			c.bot.SendMessage(chatID, fmt.Sprintf("Rebuild failed: %v", err), false)
		}
		// If we get here, exec failed or wasn't called
	}()

	return &Response{
		Text:   "Rebuilding ARIA...",
		Silent: true,
	}, nil
}

func (c *RebuildCommand) rebuildAndRestart() error {
	slog.Info("starting rebuild",
		"source_dir", c.sourceDir,
		"executable", c.executablePath,
	)

	// Check that source directory has go.mod
	goModPath := filepath.Join(c.sourceDir, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		return fmt.Errorf("no go.mod found in %s - set --source flag to aria source directory", c.sourceDir)
	}

	// Build the new binary to a temp location first
	tempBinary := c.executablePath + ".new"
	buildCmd := exec.Command("go", "build", "-o", tempBinary, "./cmd/aria")
	buildCmd.Dir = c.sourceDir
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	slog.Info("running go build", "dir", c.sourceDir, "output", tempBinary)
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("go build failed: %w", err)
	}

	// Replace the old binary with the new one
	if err := os.Rename(tempBinary, c.executablePath); err != nil {
		// Try copy if rename fails (cross-device)
		if copyErr := copyFile(tempBinary, c.executablePath); copyErr != nil {
			os.Remove(tempBinary)
			return fmt.Errorf("failed to replace binary: %w", err)
		}
		os.Remove(tempBinary)
	}

	slog.Info("build successful, restarting...")

	// Gracefully shutdown Claude processes
	c.manager.Shutdown()

	// Get current args to pass to new process
	args := os.Args

	// Exec the new binary (replaces current process)
	slog.Info("exec-ing new binary", "path", c.executablePath, "args", args)
	if err := syscall.Exec(c.executablePath, args, os.Environ()); err != nil {
		return fmt.Errorf("exec failed: %w", err)
	}

	return nil
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	// Copy file permissions
	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, sourceInfo.Mode())
}
