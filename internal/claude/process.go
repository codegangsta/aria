package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
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
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	chatID  int64
	debug   bool
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

// Send writes a user message to Claude's stdin in stream-json format
func (p *ClaudeProcess) Send(message string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Prepend /aria skill to load iMessage mode
	prompt := fmt.Sprintf("/aria %s", message)

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
