package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// UserMessage represents the stream-json input format for Claude
type UserMessage struct {
	Type    string      `json:"type"`
	Message UserContent `json:"message"`
}

// UserContent represents the content of a user message
type UserContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ClaudeProcess represents a persistent Claude CLI process
type ClaudeProcess struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	scanner       *bufio.Scanner
	mu            sync.Mutex
	chatID        int64
	debug         bool
	slashCommands []string // Commands discovered from init event
}

// InitEvent represents the system init event from Claude
type InitEvent struct {
	Type          string   `json:"type"`
	Subtype       string   `json:"subtype"`
	SlashCommands []string `json:"slash_commands"`
}

// NewProcess creates and starts a new persistent Claude process
func NewProcess(claudePath string, chatID int64, debug bool) (*ClaudeProcess, error) {
	args := []string{
		"-p",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
	}

	cmd := exec.Command(claudePath, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	// Increase buffer size for potentially large JSON responses
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	return &ClaudeProcess{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		scanner: scanner,
		chatID:  chatID,
		debug:   debug,
	}, nil
}

// SilentCommand represents a command that doesn't produce assistant output
type SilentCommand struct {
	Name         string
	Confirmation string
}

// silentCommands are commands that don't produce assistant messages
// Note: /clear is handled specially in main.go (kills process), not here
var silentCommands = map[string]SilentCommand{
	"/compact": {Name: "compact", Confirmation: "Context compacted."},
	"/help":    {Name: "help", Confirmation: ""}, // help might produce output, check
}

// IsSilentCommand checks if a message is a silent command and returns its confirmation
func IsSilentCommand(message string) (bool, string) {
	// Normalize: convert underscores to hyphens and get just the command part
	cmd := strings.SplitN(message, " ", 2)[0]
	cmd = strings.ReplaceAll(cmd, "_", "-")

	if sc, ok := silentCommands[cmd]; ok {
		return true, sc.Confirmation
	}
	return false, ""
}

// Send writes a user message to Claude's stdin in stream-json format
func (p *ClaudeProcess) Send(message string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Determine the prompt to send
	var prompt string
	if strings.HasPrefix(message, "/") && !strings.HasPrefix(message, "/aria") {
		// Forward slash commands directly to Claude (e.g., /commit, /calendar)
		// Convert underscores to hyphens (Telegram uses underscores, Claude uses hyphens)
		prompt = convertTelegramCommand(message)
		if p.debug {
			fmt.Printf("[DEBUG] Sending command: %s (original: %s)\n", prompt, message)
		}
	} else {
		// Prepend /aria skill to load iMessage mode for regular messages
		prompt = fmt.Sprintf("/aria %s", message)
		if p.debug {
			fmt.Printf("[DEBUG] Sending message with /aria prefix\n")
		}
	}

	msg := UserMessage{
		Type: "user",
		Message: UserContent{
			Role:    "user",
			Content: prompt,
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	// Write JSON followed by newline
	if _, err := p.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing to stdin: %w", err)
	}

	return nil
}

// ReadResponses reads stream-json responses and calls onMessage for each assistant text
// This blocks until the current response is complete (receives result event)
// Also captures slash commands from the init event if not already captured
func (p *ClaudeProcess) ReadResponses(ctx context.Context, onMessage func(string)) error {
	for p.scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := p.scanner.Text()

		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Skip non-JSON lines
			continue
		}

		// Debug logging for all events
		if p.debug {
			fmt.Printf("[DEBUG] Event type=%s\n", event.Type)
			if len(line) > 200 {
				fmt.Printf("[DEBUG] Line (truncated): %s...\n", line[:200])
			} else {
				fmt.Printf("[DEBUG] Line: %s\n", line)
			}
		}

		// Capture slash commands from init event (only once)
		if event.Type == "system" && p.slashCommands == nil {
			var initEvent InitEvent
			if json.Unmarshal([]byte(line), &initEvent) == nil && initEvent.Subtype == "init" {
				p.slashCommands = initEvent.SlashCommands
				if p.debug {
					fmt.Printf("[DEBUG] Captured %d slash commands\n", len(p.slashCommands))
				}
			}
		}

		// Process assistant messages
		if event.Type == "assistant" {
			for _, content := range event.Message.Content {
				if content.Type == "text" && content.Text != "" {
					onMessage(content.Text)
				}
			}
		}

		// Result event indicates end of response
		if event.Type == "result" {
			if p.debug {
				fmt.Printf("[DEBUG] Result received, response complete\n")
			}
			return nil
		}
	}

	if err := p.scanner.Err(); err != nil {
		return fmt.Errorf("reading claude output: %w", err)
	}

	return nil
}

// Alive checks if the process is still running
func (p *ClaudeProcess) Alive() bool {
	if p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	// ProcessState is nil if process hasn't exited
	return p.cmd.ProcessState == nil
}

// SlashCommands returns the slash commands discovered from the init event
func (p *ClaudeProcess) SlashCommands() []string {
	return p.slashCommands
}

// convertTelegramCommand converts a Telegram command (underscores) to Claude format (hyphens)
// e.g., "/gtd_daily_review args" -> "/gtd-daily-review args"
func convertTelegramCommand(message string) string {
	// Split into command and args
	parts := strings.SplitN(message, " ", 2)
	cmd := parts[0]

	// Convert underscores to hyphens in the command part only
	cmd = strings.ReplaceAll(cmd, "_", "-")

	// Rejoin with args if present
	if len(parts) > 1 {
		return cmd + " " + parts[1]
	}
	return cmd
}

// Close gracefully closes the Claude process
func (p *ClaudeProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error

	// Close stdin to signal EOF
	if p.stdin != nil {
		if err := p.stdin.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing stdin: %w", err))
		}
	}

	// Wait for process to exit
	if p.cmd != nil && p.cmd.Process != nil {
		if err := p.cmd.Wait(); err != nil {
			// Don't report error if process was already killed
			if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != -1 {
				errs = append(errs, fmt.Errorf("waiting for process: %w", err))
			}
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
