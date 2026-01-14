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
	cmd              *exec.Cmd
	stdin            io.WriteCloser
	stdout           io.ReadCloser
	scanner          *bufio.Scanner
	mu               sync.Mutex
	chatID           int64
	debug            bool
	logger           *slog.Logger
	slashCommands    []string // Commands discovered from init event
	sessionID        string   // Session ID from init event
	done             chan struct{} // Closed when process exits
	sessionNotFound  bool     // True if resume failed due to missing session
}

// InitEvent represents the system init event from Claude
type InitEvent struct {
	Type          string   `json:"type"`
	Subtype       string   `json:"subtype"`
	SessionID     string   `json:"session_id"`
	SlashCommands []string `json:"slash_commands"`
}

// NewProcess creates and starts a new persistent Claude process
// If resumeSessionID is provided, the process will resume that session
// If cwd is provided, the process will start in that directory
func NewProcess(claudePath string, chatID int64, debug bool, skipPermissions bool, resumeSessionID string, cwd string, logger *slog.Logger) (*ClaudeProcess, error) {
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

	// Set working directory if specified
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Capture stderr to detect session resume failures
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	// Increase buffer size for potentially large JSON responses
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	done := make(chan struct{})
	proc := &ClaudeProcess{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		scanner: scanner,
		chatID:  chatID,
		debug:   debug,
		logger:  logger,
		done:    done,
	}

	// Monitor stderr for session not found warning and process exit
	go func() {
		stderrScanner := bufio.NewScanner(stderr)
		for stderrScanner.Scan() {
			line := stderrScanner.Text()
			if strings.Contains(line, "No conversation found with session ID") {
				proc.mu.Lock()
				proc.sessionNotFound = true
				proc.mu.Unlock()
				logger.Warn("session not found, will use new session",
					"chat_id", chatID,
					"stderr", line,
				)
			} else if line != "" {
				logger.Debug("claude stderr", "chat_id", chatID, "line", line)
			}
		}
	}()

	// Monitor process exit and close done channel
	go func() {
		cmd.Wait()
		close(done)
	}()

	return proc, nil
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

// Todo represents a single todo item from Claude's TodoWrite
type Todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"` // "pending", "in_progress", "completed"
	ActiveForm string `json:"activeForm"`
}

// TodoEvent represents a todo update event from Claude
type TodoEvent struct {
	Type  string `json:"type"`
	Todos []Todo `json:"todos"`
}

// ResponseCallbacks holds callbacks for different response types
type ResponseCallbacks struct {
	OnMessage           func(text string, isFinal bool)
	OnToolUse           func(tool ToolUse)
	OnToolResult        func(result ToolResult) // Called when a tool completes (success or failure)
	OnInputRequest      func(toolID string)     // Called when Claude needs user input (e.g., AskUserQuestion)
	OnTodoUpdate        func(todos []Todo)      // Called when Claude updates todos via TodoWrite
	OnToolError         func(toolName string, errorMsg string) // Called when a tool returns an error
	OnPermissionDenial  func(denials []string)  // Called when permissions are denied
}

// ToolResultEvent represents an event containing tool result information
type ToolResultEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// UserEvent represents a user event that may contain tool results
type UserEvent struct {
	Type          string       `json:"type"`
	Message       UserEventMsg `json:"message,omitempty"`
	ToolUseResult string       `json:"tool_use_result,omitempty"` // Error message if tool failed
}

// UserEventMsg represents the message content in a user event
type UserEventMsg struct {
	Role    string            `json:"role"`
	Content []UserEventContent `json:"content,omitempty"`
}

// UserEventContent represents content in a user event message
type UserEventContent struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// ResultEvent represents the final result event from Claude
type ResultEvent struct {
	Type              string   `json:"type"`
	Subtype           string   `json:"subtype,omitempty"`
	IsError           bool     `json:"is_error,omitempty"`
	PermissionDenials []string `json:"permission_denials,omitempty"`
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

		// Capture slash commands and session ID from init event (only once)
		if event.Type == "system" && p.slashCommands == nil {
			var initEvent InitEvent
			if json.Unmarshal([]byte(line), &initEvent) == nil && initEvent.Subtype == "init" {
				p.slashCommands = initEvent.SlashCommands
				p.sessionID = initEvent.SessionID
				p.logger.Debug("captured init data",
					"session_id", p.sessionID,
					"commands_count", len(p.slashCommands),
				)
			}
		}

		// Check for tool errors in user events (tool_result with is_error: true)
		if event.Type == "user" {
			var userEvent UserEvent
			if json.Unmarshal([]byte(line), &userEvent) == nil {
				for _, content := range userEvent.Message.Content {
					if content.Type == "tool_result" && content.IsError {
						// Mark tool as failed in tracker
						completeTool(content.ToolUseID, true)

						// Extract error message - prefer content field, fall back to top-level
						errorMsg := content.Content
						if errorMsg == "" && userEvent.ToolUseResult != "" {
							errorMsg = userEvent.ToolUseResult
						}
						if errorMsg != "" && callbacks.OnToolError != nil {
							p.logger.Debug("tool error detected",
								"tool_id", content.ToolUseID,
								"error", errorMsg,
								"chat_id", p.chatID,
							)
							callbacks.OnToolError(content.ToolUseID, errorMsg)
						}
					}
				}
			}
		}

		// Process assistant messages
		if event.Type == "assistant" {
			// Collect all text and tool_use from this event first
			// so we can emit them in the correct order (text before tools)
			var textBlocks []string
			var toolBlocks []ContentBlock

			for _, content := range event.Message.Content {
				if content.Type == "text" && content.Text != "" {
					textBlocks = append(textBlocks, content.Text)
				}
				if content.Type == "tool_use" && content.Name != "" {
					toolBlocks = append(toolBlocks, content)
				}
			}

			// Process text blocks first (emit messages before tool notifications)
			for _, text := range textBlocks {
				// Text content means any pending tools have completed
				completeAllPending()
				// Flush previous message (it wasn't final)
				flushBuffer()
				// Buffer this message (might be final)
				lastMessage = text
				hasMessage = true
			}

			// Then process tool_use blocks
			for _, content := range toolBlocks {
				// New tool_use means previous tools have completed
				completeAllPending()
				// Flush any pending text BEFORE emitting tool use
				// This ensures text appears before tool notifications/keyboards
				flushBuffer()
				// Track this tool as pending
				pendingTools[content.ID] = true

				// Special handling for TodoWrite - extract and emit todos
				if content.Name == "TodoWrite" && callbacks.OnTodoUpdate != nil {
					if todosRaw, ok := content.Input["todos"]; ok {
						if todosSlice, ok := todosRaw.([]interface{}); ok {
							todos := make([]Todo, 0, len(todosSlice))
							for _, t := range todosSlice {
								if todoMap, ok := t.(map[string]interface{}); ok {
									todo := Todo{}
									if c, ok := todoMap["content"].(string); ok {
										todo.Content = c
									}
									if s, ok := todoMap["status"].(string); ok {
										todo.Status = s
									}
									if a, ok := todoMap["activeForm"].(string); ok {
										todo.ActiveForm = a
									}
									todos = append(todos, todo)
								}
							}
							callbacks.OnTodoUpdate(todos)
						}
					}
				}

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

		// Result event indicates end of response
		if event.Type == "result" {
			// Complete any remaining pending tools
			completeAllPending()

			// Check for permission denials
			var resultEvent ResultEvent
			if json.Unmarshal([]byte(line), &resultEvent) == nil {
				if len(resultEvent.PermissionDenials) > 0 && callbacks.OnPermissionDenial != nil {
					p.logger.Info("permission denials in result",
						"chat_id", p.chatID,
						"denials", resultEvent.PermissionDenials,
					)
					callbacks.OnPermissionDenial(resultEvent.PermissionDenials)
				}
			}

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

	// Scanner finished without result event - process likely died
	select {
	case <-p.done:
		// Process exited - check if it was due to session not found
		if p.SessionNotFound() {
			return fmt.Errorf("session not found, needs fresh start")
		}
		return fmt.Errorf("claude process exited unexpectedly")
	default:
		// Process still running but no more output - unusual
		return fmt.Errorf("claude output ended without result event")
	}
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

// SessionID returns the session ID from the init event
func (p *ClaudeProcess) SessionID() string {
	return p.sessionID
}

// SessionNotFound returns true if the session resume failed because the session doesn't exist
func (p *ClaudeProcess) SessionNotFound() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessionNotFound
}

// Done returns a channel that's closed when the process exits
func (p *ClaudeProcess) Done() <-chan struct{} {
	return p.done
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
