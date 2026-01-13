package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Default timeout for Claude commands
const DefaultTimeout = 5 * time.Minute

// ContentBlock represents a content block in a Claude message
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// MessageContent represents the message in an assistant event
type MessageContent struct {
	Content []ContentBlock `json:"content"`
}

// Event represents a stream-json event from Claude
type Event struct {
	Type    string         `json:"type"`
	Message MessageContent `json:"message,omitempty"`
}

// Client handles communication with Claude Code CLI
type Client struct {
	claudePath string
	timeout    time.Duration
	debug      bool
}

// New creates a new Claude client
func New(claudePath string, debug bool) *Client {
	return &Client{
		claudePath: claudePath,
		timeout:    DefaultTimeout,
		debug:      debug,
	}
}

// StreamRun executes a Claude command with streaming output
// It prepends /aria to every prompt and calls onMessage for each assistant text response
func (c *Client) StreamRun(ctx context.Context, sessionID, userMessage string, onMessage func(string)) error {
	// Prepend /aria skill to load iMessage mode
	prompt := fmt.Sprintf("/aria %s", userMessage)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Note: --resume requires existing session, so we don't use it for now
	// Each message is a fresh conversation (stateless)
	// TODO: Add session persistence by storing session IDs after first message
	args := []string{
		"-p",
		"--verbose",
		"--output-format", "stream-json",
	}

	cmd := exec.CommandContext(ctx, c.claudePath, args...)
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting claude: %w", err)
	}

	// Capture stderr in background
	var stderrOutput strings.Builder
	go func() {
		stderrScanner := bufio.NewScanner(stderr)
		for stderrScanner.Scan() {
			stderrOutput.WriteString(stderrScanner.Text())
			stderrOutput.WriteString("\n")
		}
	}()

	scanner := bufio.NewScanner(stdout)
	// Increase buffer size for potentially large JSON responses
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Skip non-JSON lines
			continue
		}

		// Only process assistant messages
		if event.Type == "assistant" {
			for _, content := range event.Message.Content {
				if content.Type == "text" && content.Text != "" {
					onMessage(content.Text)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading claude output: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		errMsg := stderrOutput.String()
		if errMsg != "" {
			return fmt.Errorf("%w: %s", err, errMsg)
		}
		return err
	}

	return nil
}
