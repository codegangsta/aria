package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/codegangsta/aria/internal/claude"
	"github.com/codegangsta/aria/internal/config"
	"github.com/codegangsta/aria/internal/telegram"
)

// PendingQuestion stores context for an AskUserQuestion waiting for user input
type PendingQuestion struct {
	ToolID       string
	Questions    []telegram.Question
	CurrentIdx   int               // Which question we're on (0-indexed)
	Answers      []string          // Collected answers so far
}

// Global vars for rebuild functionality
var (
	executablePath string // Path to current binary
	sourceDir      string // Path to source directory for rebuilding
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	claudePath := flag.String("claude", "claude", "path to claude binary")
	sourceDirFlag := flag.String("source", "", "path to source directory (for /rebuild)")
	flag.Parse()

	// Get the path to the current executable
	var err error
	executablePath, err = os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get executable path: %v\n", err)
		os.Exit(1)
	}
	executablePath, _ = filepath.EvalSymlinks(executablePath) // Resolve symlinks

	// Source directory for rebuilds - use flag, or try to infer from executable location
	if *sourceDirFlag != "" {
		sourceDir = *sourceDirFlag
	} else {
		// Default: assume source is in same directory as binary or one level up
		sourceDir = filepath.Dir(executablePath)
		// Check if go.mod exists here or parent
		if _, err := os.Stat(filepath.Join(sourceDir, "go.mod")); os.IsNotExist(err) {
			sourceDir = filepath.Dir(sourceDir)
		}
	}

	// Find default paths
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get home directory: %v\n", err)
		os.Exit(1)
	}

	if *configPath == "" {
		*configPath = homeDir + "/.config/aria/config.yaml"
	}

	fmt.Println("Aria starting...")
	fmt.Printf("Config: %s\n", *configPath)
	fmt.Printf("Claude: %s\n", *claudePath)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Set up structured logging
	setupLogger(cfg)

	slog.Info("config loaded",
		"allowlist_count", len(cfg.Allowlist),
		"debug", cfg.Debug,
		"skip_permissions", cfg.Claude.SkipPermissions,
	)

	// Create components
	manager := claude.NewManager(*claudePath, cfg.Debug, cfg.Claude.SkipPermissions, slog.Default())
	sessionDiscovery := claude.NewSessionDiscovery(homeDir+"/.claude", slog.Default())

	// Set up session persistence
	sessionsPath := homeDir + "/.config/aria/sessions.yaml"
	persistence := claude.NewSessionPersistence(sessionsPath)
	if err := persistence.Load(); err != nil {
		slog.Warn("failed to load persisted sessions", "error", err)
	} else {
		slog.Info("loaded persisted sessions", "count", len(persistence.GetAll()))
	}
	manager.SetPersistence(persistence)

	bot, err := telegram.New(cfg.Telegram.Token, cfg.Allowlist, cfg.Debug, slog.Default())
	if err != nil {
		slog.Error("failed to create telegram bot", "error", err)
		os.Exit(1)
	}

	// Pending questions waiting for user input (chatID -> PendingQuestion)
	pendingQuestions := make(map[int64]*PendingQuestion)
	var pendingMu sync.RWMutex

	// Tool status trackers for consolidated tool notifications (chatID -> tracker)
	toolTrackers := make(map[int64]*telegram.ToolStatusTracker)
	var trackersMu sync.Mutex

	// Progress trackers for todo display (chatID -> tracker)
	progressTrackers := make(map[int64]*telegram.ProgressTracker)
	var progressMu sync.Mutex

	// getOrCreateTracker gets or creates a tool status tracker for a chat
	getOrCreateTracker := func(chatID int64) *telegram.ToolStatusTracker {
		trackersMu.Lock()
		defer trackersMu.Unlock()

		if tracker, ok := toolTrackers[chatID]; ok {
			return tracker
		}

		tracker := telegram.NewToolStatusTracker(bot, chatID)
		tracker.Start()
		toolTrackers[chatID] = tracker
		return tracker
	}

	// clearTracker flushes and clears a tracker for a chat
	clearTracker := func(chatID int64) {
		trackersMu.Lock()
		tracker := toolTrackers[chatID]
		trackersMu.Unlock()

		if tracker != nil {
			tracker.Flush() // Force final render before clearing
			tracker.Clear()
		}
	}

	// getOrCreateProgressTracker gets or creates a progress tracker for a chat
	getOrCreateProgressTracker := func(chatID int64) *telegram.ProgressTracker {
		progressMu.Lock()
		defer progressMu.Unlock()

		if tracker, ok := progressTrackers[chatID]; ok {
			return tracker
		}

		tracker := telegram.NewProgressTracker(bot, chatID)
		progressTrackers[chatID] = tracker
		return tracker
	}

	// clearProgressTracker clears the progress tracker for a chat
	clearProgressTracker := func(chatID int64) {
		progressMu.Lock()
		tracker := progressTrackers[chatID]
		progressMu.Unlock()

		if tracker != nil {
			tracker.Clear()
		}
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		slog.Info("shutdown signal received", "signal", sig.String())
		manager.Shutdown()
		cancel()
	}()

	// Set up message handler
	bot.SetHandler(func(msgCtx context.Context, chatID int64, userID int64, msgID int64, text string, respond telegram.RespondFunc, replyHTML telegram.ReplyHTMLFunc) {
		slog.Info("processing message",
			"chat_id", chatID,
			"user_id", userID,
			"msg_id", msgID,
			"text_length", len(text),
		)

		// Start typing indicator loop
		stopTyping := bot.TypingLoop(chatID)
		defer stopTyping()

		// Handle /clear specially - kill the process instead of forwarding to Claude
		// (Claude's /clear is a CLI command, not a user message)
		cmd := strings.SplitN(text, " ", 2)[0]
		cmd = strings.ReplaceAll(cmd, "_", "-")
		if cmd == "/clear" {
			slog.Info("clearing conversation", "chat_id", chatID)
			manager.Reset(chatID)
			respond("Conversation cleared.", false) // Play sound for clear confirmation
			return
		}

		// Handle /rebuild - recompile and restart ARIA
		if cmd == "/rebuild" {
			slog.Info("rebuild requested", "chat_id", chatID)
			respond("Rebuilding ARIA...", true)

			// Run go build in background, then exec the new binary
			go func() {
				if err := rebuildAndRestart(manager); err != nil {
					slog.Error("rebuild failed", "error", err)
					bot.SendMessage(chatID, fmt.Sprintf("Rebuild failed: %v", err), false)
				}
				// If we get here, exec failed or wasn't called
			}()
			return
		}

		// Handle /cd - change working directory
		if cmd == "/cd" {
			parts := strings.SplitN(text, " ", 2)
			if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
				// No path provided - show current cwd
				currentCwd := manager.GetCwd(chatID)
				if currentCwd == "" {
					currentCwd = "(default)"
				}
				respond(fmt.Sprintf("Working directory: %s", currentCwd), true)
				return
			}

			// Expand ~ to home directory
			newCwd := strings.TrimSpace(parts[1])
			if strings.HasPrefix(newCwd, "~") {
				newCwd = strings.Replace(newCwd, "~", homeDir, 1)
			}

			// Resolve to absolute path
			newCwd, err := filepath.Abs(newCwd)
			if err != nil {
				respond(fmt.Sprintf("Invalid path: %v", err), false)
				return
			}

			// Validate path exists and is a directory
			info, err := os.Stat(newCwd)
			if err != nil {
				if os.IsNotExist(err) {
					respond(fmt.Sprintf("Directory not found: %s", newCwd), false)
				} else {
					respond(fmt.Sprintf("Error checking path: %v", err), false)
				}
				return
			}
			if !info.IsDir() {
				respond(fmt.Sprintf("Not a directory: %s", newCwd), false)
				return
			}

			// Change the cwd (kills process, preserves session)
			slog.Info("changing cwd", "chat_id", chatID, "cwd", newCwd)
			manager.SetCwd(chatID, newCwd)

			// Format display path (collapse home dir back to ~)
			displayPath := newCwd
			if strings.HasPrefix(newCwd, homeDir) {
				displayPath = "~" + strings.TrimPrefix(newCwd, homeDir)
			}
			respond(fmt.Sprintf("Now working in %s", displayPath), false)
			return
		}

		// Handle /sessions - show session picker keyboard
		if cmd == "/sessions" {
			slog.Info("showing sessions", "chat_id", chatID)
			sessions, err := sessionDiscovery.DiscoverSessions(7)
			if err != nil {
				slog.Error("failed to discover sessions", "error", err)
				respond("Failed to load sessions.", false)
				return
			}
			if len(sessions) == 0 {
				respond("No recent sessions found.", false)
				return
			}
			// Convert to display info
			var displaySessions []telegram.SessionDisplayInfo
			for _, s := range sessions {
				displaySessions = append(displaySessions, telegram.SessionDisplayInfo{
					ID:          s.ID,
					ShortID:     s.ShortID,
					ProjectName: s.ProjectName,
					Summary:     s.Summary,
					TimeAgo:     claude.FormatTimeAgo(s.LastActive),
				})
			}
			keyboard := telegram.BuildSessionKeyboard(displaySessions)
			if err := bot.SendQuestionKeyboard(chatID, "*Sessions*", keyboard); err != nil {
				slog.Error("failed to send session keyboard", "error", err)
			}
			return
		}

		// Check if this is a silent command that needs special handling
		isSilentCmd, confirmation := claude.IsSilentCommand(text)
		if isSilentCmd {
			slog.Debug("handling silent command", "command", text)
		}

		// Track if we got any response
		gotResponse := false

		// Get progress tracker for this chat
		progressTracker := getOrCreateProgressTracker(chatID)

		// Send message via persistent process manager
		// isFinal=true means it's the last message, so we play a sound
		// isFinal=false means intermediate message, send silently
		err := manager.Send(msgCtx, chatID, text, claude.ResponseCallbacks{
			OnMessage: func(responseText string, isFinal bool) {
				gotResponse = true
				silent := !isFinal // Silent for intermediate messages, sound for final

				// Flush and clear tracker before sending text to start new tool group
				tracker := getOrCreateTracker(chatID)
				tracker.FlushAndClear()

				slog.Debug("sending response to telegram",
					"chat_id", chatID,
					"text_length", len(responseText),
					"is_final", isFinal,
					"silent", silent,
				)
				respond(responseText, silent)
				slog.Debug("response sent")
			},
			OnTodoUpdate: func(todos []claude.Todo) {
				// Convert claude.Todo to telegram.Todo
				telegramTodos := make([]telegram.Todo, len(todos))
				for i, t := range todos {
					telegramTodos[i] = telegram.Todo{
						Content:    t.Content,
						Status:     t.Status,
						ActiveForm: t.ActiveForm,
					}
				}
				progressTracker.Update(telegramTodos)
				slog.Debug("todo update",
					"chat_id", chatID,
					"count", len(todos),
				)
			},
			OnToolUse: func(tool claude.ToolUse) {
				// Handle AskUserQuestion specially - send inline keyboard (no notification)
				if tool.Name == "AskUserQuestion" {
					parsed, err := telegram.ParseAskUserQuestion(tool.Input)
					if err != nil {
						slog.Error("failed to parse AskUserQuestion", "error", err)
						return
					}

					// Store pending question for this chat
					pendingMu.Lock()
					pendingQuestions[chatID] = &PendingQuestion{
						ToolID:     tool.ID,
						Questions:  parsed.Questions,
						CurrentIdx: 0,
						Answers:    make([]string, 0, len(parsed.Questions)),
					}
					pendingMu.Unlock()

					// Send keyboard for first question
					if len(parsed.Questions) > 0 {
						q := parsed.Questions[0]
						keyboard, text := telegram.BuildQuestionKeyboard(tool.ID, 0, q)
						if err := bot.SendQuestionKeyboard(chatID, text, keyboard); err != nil {
							slog.Error("failed to send question keyboard",
								"chat_id", chatID,
								"error", err,
							)
						}
					}

					slog.Debug("sent AskUserQuestion keyboard",
						"chat_id", chatID,
						"tool_id", tool.ID,
						"question_count", len(parsed.Questions),
					)
					return
				}

				// Add tool to consolidated tracker
				tracker := getOrCreateTracker(chatID)
				tracker.AddTool(telegram.ToolUse{
					ID:    tool.ID,
					Name:  tool.Name,
					Input: tool.Input,
				})
				slog.Debug("tool added to tracker",
					"chat_id", chatID,
					"tool", tool.Name,
				)
			},
			OnToolResult: func(result claude.ToolResult) {
				tracker := getOrCreateTracker(chatID)
				tracker.CompleteTool(result.ToolID, result.IsError)
			},
			OnToolError: func(toolID string, errorMsg string) {
				// Just log - the ✗ in tool tracker is enough visual indication
				slog.Debug("tool error",
					"chat_id", chatID,
					"tool_id", toolID,
					"error", errorMsg,
				)
			},
			OnPermissionDenial: func(denials []string) {
				// Just log for now - Phase 10 will add interactive permission handling
				slog.Warn("permission denials",
					"chat_id", chatID,
					"denials", denials,
				)
			},
		})

		// Clear the trackers after response is complete
		clearTracker(chatID)
		clearProgressTracker(chatID)

		if err != nil {
			slog.Error("claude error",
				"chat_id", chatID,
				"error", err,
			)
			respond("Sorry, something went wrong. Please try again.", false) // Play sound for errors
		}

		// For silent commands that didn't produce a response, send confirmation
		if isSilentCmd && !gotResponse && confirmation != "" {
			slog.Debug("sending silent command confirmation", "confirmation", confirmation)
			respond(confirmation, false) // Play sound for confirmations
		}

		// Register slash commands with Telegram after first successful message
		// (commands are discovered when Claude process starts)
		if commands := manager.GetSlashCommands(); commands != nil {
			bot.RegisterCommands(commands)
		}
	})

	// Set up callback handler for inline keyboard button presses
	bot.SetCallbackHandler(func(cbCtx context.Context, chatID int64, userID int64, data string) string {
		cb, err := telegram.ParseCallbackData(data)
		if err != nil {
			slog.Error("failed to parse callback data", "error", err, "data", data)
			return "Error processing selection"
		}

		// Handle session selection callbacks
		if cb.Type == "s" {
			if cb.Action == "f" {
				// Start fresh
				slog.Info("starting fresh session", "chat_id", chatID)
				manager.Reset(chatID)
				return "Starting fresh conversation"
			}
			if cb.Action == "r" && cb.SessionID != "" {
				// Resume session
				session := sessionDiscovery.LookupSessionByShortID(cb.SessionID)
				if session == nil {
					slog.Warn("session not found", "short_id", cb.SessionID)
					return "Session not found"
				}
				slog.Info("resuming session", "chat_id", chatID, "session_id", session.ID)

				// Get last assistant message before switching
				lastMsg := sessionDiscovery.GetLastAssistantMessage(session.ID)

				_, err := manager.GetOrCreateWithSession(chatID, session.ID)
				if err != nil {
					slog.Error("failed to resume session", "error", err)
					return "Failed to resume session"
				}

				// Send last assistant message as context
				if lastMsg != "" {
					// Truncate if too long for Telegram
					if len(lastMsg) > 500 {
						lastMsg = lastMsg[:497] + "..."
					}
					go bot.SendMessage(chatID, "Last response:\n\n"+lastMsg, true)
				}

				summary := session.Summary
				if len(summary) > 40 {
					summary = summary[:37] + "..."
				}
				return "Resuming: " + summary
			}
			return "Invalid session action"
		}

		// Get the pending question for this chat
		pendingMu.RLock()
		pending := pendingQuestions[chatID]
		pendingMu.RUnlock()

		if pending == nil {
			slog.Warn("no pending question for chat", "chat_id", chatID)
			return "Question expired"
		}

		// Handle "Other" selection - prompt for custom text input
		if cb.Type == "o" {
			slog.Debug("user selected Other option",
				"chat_id", chatID,
				"question_idx", cb.QuestionIdx,
			)
			// Clear pending question - they'll type a custom response
			pendingMu.Lock()
			delete(pendingQuestions, chatID)
			pendingMu.Unlock()
			return "Type your answer and send it"
		}

		// Get the selected option
		if cb.QuestionIdx >= len(pending.Questions) {
			return "Invalid question"
		}
		q := pending.Questions[cb.QuestionIdx]
		if cb.OptionIdx >= len(q.Options) {
			return "Invalid option"
		}
		selectedOption := q.Options[cb.OptionIdx]

		slog.Info("user selected option",
			"chat_id", chatID,
			"option", selectedOption.Label,
			"question_idx", cb.QuestionIdx,
			"total_questions", len(pending.Questions),
		)

		// Store this answer
		pendingMu.Lock()
		pending.Answers = append(pending.Answers, selectedOption.Label)
		pending.CurrentIdx++
		nextIdx := pending.CurrentIdx
		totalQuestions := len(pending.Questions)
		allAnswers := make([]string, len(pending.Answers))
		copy(allAnswers, pending.Answers)
		pendingMu.Unlock()

		// Check if more questions remain
		if nextIdx < totalQuestions {
			// Send next question keyboard
			nextQ := pending.Questions[nextIdx]
			keyboard, text := telegram.BuildQuestionKeyboard(pending.ToolID, nextIdx, nextQ)
			if err := bot.SendQuestionKeyboard(chatID, text, keyboard); err != nil {
				slog.Error("failed to send next question keyboard", "error", err)
			}
			return "Selected: " + selectedOption.Label
		}

		// All questions answered - clear pending and send all answers to Claude
		pendingMu.Lock()
		delete(pendingQuestions, chatID)
		pendingMu.Unlock()

		// Format answers as a combined response
		combinedAnswer := ""
		for i, ans := range allAnswers {
			if i > 0 {
				combinedAnswer += ", "
			}
			combinedAnswer += ans
		}

		// Send the combined answers back to Claude
		go func() {
			// Start typing indicator
			stopTyping := bot.TypingLoop(chatID)
			defer stopTyping()

			// Get progress tracker for this chat
			progressTracker := getOrCreateProgressTracker(chatID)

			err := manager.Send(cbCtx, chatID, combinedAnswer, claude.ResponseCallbacks{
				OnMessage: func(text string, isFinal bool) {
					silent := !isFinal

					// Flush and clear tracker before sending text to start new tool group
					tracker := getOrCreateTracker(chatID)
					tracker.FlushAndClear()

					bot.SendMessage(chatID, text, silent)
				},
				OnTodoUpdate: func(todos []claude.Todo) {
					// Convert claude.Todo to telegram.Todo
					telegramTodos := make([]telegram.Todo, len(todos))
					for i, t := range todos {
						telegramTodos[i] = telegram.Todo{
							Content:    t.Content,
							Status:     t.Status,
							ActiveForm: t.ActiveForm,
						}
					}
					progressTracker.Update(telegramTodos)
				},
				OnToolUse: func(tool claude.ToolUse) {
					// Handle nested AskUserQuestion or other tools
					if tool.Name == "AskUserQuestion" {
						parsed, err := telegram.ParseAskUserQuestion(tool.Input)
						if err != nil {
							slog.Error("failed to parse AskUserQuestion in callback", "error", err)
							return
						}
						pendingMu.Lock()
						pendingQuestions[chatID] = &PendingQuestion{
							ToolID:     tool.ID,
							Questions:  parsed.Questions,
							CurrentIdx: 0,
							Answers:    make([]string, 0, len(parsed.Questions)),
						}
						pendingMu.Unlock()
						// Send first question
						if len(parsed.Questions) > 0 {
							q := parsed.Questions[0]
							keyboard, text := telegram.BuildQuestionKeyboard(tool.ID, 0, q)
							bot.SendQuestionKeyboard(chatID, text, keyboard)
						}
						return
					}

					// Add tool to consolidated tracker
					tracker := getOrCreateTracker(chatID)
					tracker.AddTool(telegram.ToolUse{
						ID:    tool.ID,
						Name:  tool.Name,
						Input: tool.Input,
					})
				},
				OnToolResult: func(result claude.ToolResult) {
					tracker := getOrCreateTracker(chatID)
					tracker.CompleteTool(result.ToolID, result.IsError)
				},
				OnToolError: func(toolID string, errorMsg string) {
					// Just log - the ✗ in tool tracker is enough visual indication
					slog.Debug("tool error in callback",
						"chat_id", chatID,
						"tool_id", toolID,
						"error", errorMsg,
					)
				},
				OnPermissionDenial: func(denials []string) {
					// Just log for now - Phase 10 will add interactive permission handling
					slog.Warn("permission denials in callback",
						"chat_id", chatID,
						"denials", denials,
					)
				},
			})
			// Clear the trackers after response
			clearTracker(chatID)
			clearProgressTracker(chatID)
			if err != nil {
				slog.Error("error sending callback response to claude", "error", err)
				bot.SendMessage(chatID, "Sorry, something went wrong.", false)
			}
		}()

		return "Selected: " + selectedOption.Label
	})

	slog.Info("aria started, connecting to telegram")

	// Notify users with persisted sessions that ARIA has restarted
	// This runs in background after bot starts polling
	go func() {
		// Small delay to let bot initialize
		time.Sleep(500 * time.Millisecond)

		sessions := persistence.GetAll()
		if len(sessions) > 0 {
			for chatID := range sessions {
				slog.Info("notifying chat of restart", "chat_id", chatID)
				bot.SendMessage(chatID, "ARIA restarted. Session will resume on next message.", true)
			}
		}
	}()

	// Start the bot (blocks until context is cancelled)
	if err := bot.Start(ctx); err != nil {
		if ctx.Err() == context.Canceled {
			slog.Info("aria stopped")
			return
		}
		slog.Error("telegram bot error", "error", err)
		os.Exit(1)
	}
}

// setupLogger configures slog based on config settings
func setupLogger(cfg *config.Config) {
	var level slog.Level
	if cfg.Debug {
		level = slog.LevelDebug
	} else {
		level = slog.LevelInfo
	}

	// Determine output destination
	var w io.Writer = os.Stdout
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open log file: %v\n", err)
			os.Exit(1)
		}
		// Write to both stdout and file
		w = io.MultiWriter(os.Stdout, f)
	}

	opts := &slog.HandlerOptions{Level: level}
	handler := slog.NewTextHandler(w, opts)
	slog.SetDefault(slog.New(handler))
}

// rebuildAndRestart compiles the current source and exec's the new binary
func rebuildAndRestart(manager *claude.ProcessManager) error {
	slog.Info("starting rebuild",
		"source_dir", sourceDir,
		"executable", executablePath,
	)

	// Check that source directory has go.mod
	goModPath := filepath.Join(sourceDir, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		return fmt.Errorf("no go.mod found in %s - set --source flag to aria source directory", sourceDir)
	}

	// Build the new binary to a temp location first
	tempBinary := executablePath + ".new"
	buildCmd := exec.Command("go", "build", "-o", tempBinary, "./cmd/aria")
	buildCmd.Dir = sourceDir
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	slog.Info("running go build", "dir", sourceDir, "output", tempBinary)
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("go build failed: %w", err)
	}

	// Replace the old binary with the new one
	if err := os.Rename(tempBinary, executablePath); err != nil {
		// Try copy if rename fails (cross-device)
		if copyErr := copyFile(tempBinary, executablePath); copyErr != nil {
			os.Remove(tempBinary)
			return fmt.Errorf("failed to replace binary: %w", err)
		}
		os.Remove(tempBinary)
	}

	slog.Info("build successful, restarting...")

	// Gracefully shutdown Claude processes
	manager.Shutdown()

	// Get current args to pass to new process
	args := os.Args

	// Exec the new binary - this replaces the current process
	slog.Info("exec'ing new binary", "path", executablePath, "args", args)
	if err := syscall.Exec(executablePath, args, os.Environ()); err != nil {
		return fmt.Errorf("exec failed: %w", err)
	}

	// This line is never reached if exec succeeds
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

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}

	// Copy permissions
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode())
}
