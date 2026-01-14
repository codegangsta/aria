package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	logger        *slog.Logger
	slashCommands []string // Commands discovered from init event
}

// InitEvent represents the system init event from Claude
type InitEvent struct {
	Type          string   `json:"type"`
	Subtype       string   `json:"subtype"`
	SlashCommands []string `json:"slash_commands"`
}

// NewProcess creates and starts a new persistent Claude process
// If resumeSessionID is provided, the process will resume that session
func NewProcess(claudePath string, chatID int64, debug bool, skipPermissions bool, resumeSessionID string, logger *slog.Logger) (*ClaudeProcess, error) {
	args := []string{
		"-p",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
	}

	if skipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}

	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
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
		logger:  logger,
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
		p.logger.Debug("sending command",
			"prompt", prompt,
			"original", message,
			"chat_id", p.chatID,
		)
	} else {
		// Prepend /aria skill to load iMessage mode for regular messages
		prompt = fmt.Sprintf("/aria %s", message)
		p.logger.Debug("sending message with /aria prefix",
			"chat_id", p.chatID,
		)
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

// ToolUse represents a tool call from Claude
type ToolUse struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

// InputRequestEvent represents an input_request event from Claude
type InputRequestEvent struct {
	Type   string `json:"type"`
	ToolID string `json:"tool_use_id"`
}

// ToolResult represents the result of a tool execution
type ToolResult struct {
	ToolID  string
	IsError bool
}

// ResponseCallbacks holds callbacks for different response types
type ResponseCallbacks struct {
	OnMessage      func(text string, isFinal bool)
	OnToolUse      func(tool ToolUse)
	OnToolResult   func(result ToolResult) // Called when a tool completes (success or failure)
	OnInputRequest func(toolID string)     // Called when Claude needs user input (e.g., AskUserQuestion)
}

// ToolResultEvent represents an event containing tool result information
type ToolResultEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// ReadResponses reads stream-json responses and calls callbacks for assistant text and tool use
// This blocks until the current response is complete (receives result event)
// Also captures slash commands from the init event if not already captured
// The isFinal parameter indicates whether this is the last message before the result
func (p *ClaudeProcess) ReadResponses(ctx context.Context, callbacks ResponseCallbacks) error {
	// Buffer to hold the last message so we can mark it as final
	var lastMessage string
	var hasMessage bool

	// Track pending tool IDs to detect completion
	pendingTools := make(map[string]bool)

	// Helper to flush the buffered message (not final)
	flushBuffer := func() {
		if hasMessage && callbacks.OnMessage != nil {
			callbacks.OnMessage(lastMessage, false)
			hasMessage = false
			lastMessage = ""
		}
	}

	// Helper to complete a specific tool with success/failure
	completeTool := func(toolID string, isError bool) {
		if pendingTools[toolID] && callbacks.OnToolResult != nil {
			callbacks.OnToolResult(ToolResult{
				ToolID:  toolID,
				IsError: isError,
			})
			delete(pendingTools, toolID)
		}
	}

	// Helper to complete all pending tools as success
	completeAllPending := func() {
		for toolID := range pendingTools {
			if callbacks.OnToolResult != nil {
				callbacks.OnToolResult(ToolResult{
					ToolID:  toolID,
					IsError: false,
				})
			}
		}
		pendingTools = make(map[string]bool)
	}

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

		// Log all JSON events from Claude for debugging and future feature development
		p.logger.Debug("claude event",
			"type", event.Type,
			"chat_id", p.chatID,
			"json", line,
		)

		// Check for tool result events (success or error)
		var toolResultEvent ToolResultEvent
		if json.Unmarshal([]byte(line), &toolResultEvent) == nil {
			if toolResultEvent.ToolUseID != "" {
				// This event references a tool - check if it indicates error
				completeTool(toolResultEvent.ToolUseID, toolResultEvent.IsError)
			}
		}

		// Capture slash commands from init event (only once)
		if event.Type == "system" && p.slashCommands == nil {
			var initEvent InitEvent
			if json.Unmarshal([]byte(line), &initEvent) == nil && initEvent.Subtype == "init" {
				p.slashCommands = initEvent.SlashCommands
				p.logger.Debug("captured slash commands",
					"count", len(p.slashCommands),
					"commands", p.slashCommands,
				)
			}
		}

		// Process assistant messages
		if event.Type == "assistant" {
			for _, content := range event.Message.Content {
				if content.Type == "text" && content.Text != "" {
					// Text content means any pending tools have completed
					completeAllPending()
					// Flush previous message (it wasn't final)
					flushBuffer()
					// Buffer this message (might be final)
					lastMessage = content.Text
					hasMessage = true
				}
				if content.Type == "tool_use" && content.Name != "" {
					// New tool_use means previous tools have completed
					completeAllPending()
					// Flush any pending text BEFORE emitting tool use
					// This ensures text appears before tool notifications/keyboards
					flushBuffer()
					// Track this tool as pending
					pendingTools[content.ID] = true
					// Emit tool use event
					if callbacks.OnToolUse != nil {
						callbacks.OnToolUse(ToolUse{
							ID:    content.ID,
							Name:  content.Name,
							Input: content.Input,
						})
					}
					p.logger.Debug("tool use",
						"tool", content.Name,
						"id", content.ID,
						"chat_id", p.chatID,
					)
				}
			}
		}

		// Result event indicates end of response
		if event.Type == "result" {
			// Complete any remaining pending tools
			completeAllPending()
			p.logger.Debug("result received, response complete",
				"chat_id", p.chatID,
				"has_final_message", hasMessage,
			)
			// Send the last buffered message as final
			if hasMessage && callbacks.OnMessage != nil {
				callbacks.OnMessage(lastMessage, true)
			}
			return nil
		}

		// Input request event indicates Claude is waiting for user input (e.g., AskUserQuestion)
		if event.Type == "input_request" {
			var inputReq InputRequestEvent
			if err := json.Unmarshal([]byte(line), &inputReq); err == nil {
				p.logger.Debug("input_request received, waiting for user input",
					"chat_id", p.chatID,
					"tool_id", inputReq.ToolID,
				)
				// Complete any pending tools (except the one waiting for input)
				for toolID := range pendingTools {
					if toolID != inputReq.ToolID && callbacks.OnToolResult != nil {
						callbacks.OnToolResult(ToolResult{
							ToolID:  toolID,
							IsError: false,
						})
						delete(pendingTools, toolID)
					}
				}
				// Flush any pending message (not final, since we're waiting for input)
				flushBuffer()
				if callbacks.OnInputRequest != nil {
					callbacks.OnInputRequest(inputReq.ToolID)
				}
				return nil
			}
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
