package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/codegangsta/aria/internal/claude"
	"github.com/codegangsta/aria/internal/commands"
	"github.com/codegangsta/aria/internal/config"
	"github.com/codegangsta/aria/internal/handlers"
	"github.com/codegangsta/aria/internal/mcp"
	"github.com/codegangsta/aria/internal/telegram"
	"github.com/codegangsta/aria/internal/trackers"
)

// Global vars for rebuild functionality
var (
	executablePath string // Path to current binary
	sourceDir      string // Path to source directory for rebuilding
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	claudePath := flag.String("claude", "claude", "path to claude binary")
	sourceDirFlag := flag.String("source", "", "path to source directory (for /rebuild)")
	mcpServer := flag.Bool("mcp-server", false, "run as MCP server (for Claude permission prompts)")
	flag.Parse()

	// If running as MCP server, handle that and exit
	if *mcpServer {
		runMCPServer()
		return
	}

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

	// Set up MCP callback server and bridge for permission prompts (if not skipping permissions)
	var mcpBridge *mcp.BridgeManager
	var callbackServer *mcp.CallbackServer
	if !cfg.Claude.SkipPermissions {
		// Start callback server first to get the port
		var err error
		callbackServer, err = mcp.NewCallbackServer(slog.Default())
		if err != nil {
			slog.Error("failed to create callback server", "error", err)
			os.Exit(1)
		}
		callbackServer.Start()
		defer callbackServer.Stop()

		// Create bridge manager with callback port
		mcpBridge, err = mcp.NewBridgeManager(executablePath, callbackServer.Port(), slog.Default())
		if err != nil {
			slog.Error("failed to create MCP bridge manager", "error", err)
			os.Exit(1)
		}
		defer mcpBridge.Cleanup()

		// We'll set the handler after trackerMgr is created (below)
		slog.Info("MCP permission prompts enabled", "callback_port", callbackServer.Port())
	}

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

	// Set up command router
	cmdRouter := commands.NewRouter()
	cmdRouter.Register(commands.NewClearCommand(manager))
	cmdRouter.Register(commands.NewCdCommand(manager, homeDir))
	cmdRouter.Register(commands.NewSessionsCommand(sessionDiscovery, bot))
	cmdRouter.Register(commands.NewRebuildCommand(manager, bot, sourceDir, executablePath))
	cmdRouter.Register(commands.NewExitCommand())

	// Unified tracker manager for all chat-scoped state
	trackerMgr := trackers.NewManager(bot)

	// Set up MCP callback handler now that we have trackerMgr and bot
	if callbackServer != nil && mcpBridge != nil {
		callbackServer.SetHandler(func(ctx context.Context, req mcp.PermissionRequest) (*mcp.PermissionResponse, error) {
			chatID := req.ChatID
			slog.Info("permission callback received",
				"chat_id", chatID,
				"tool", req.ToolName,
			)

			// Create response channel
			respChan := make(chan *trackers.PermissionResult, 1)

			// Build and send permission keyboard
			keyboard, text := telegram.BuildPermissionKeyboard("perm", req.ToolName, req.Input)
			msgID, err := bot.SendPermissionKeyboard(chatID, text, keyboard)
			if err != nil {
				return &mcp.PermissionResponse{
					Behavior: "deny",
					Message:  fmt.Sprintf("Failed to send keyboard: %v", err),
				}, nil
			}

			// Store pending permission
			trackerMgr.SetPermission(chatID, &trackers.PendingPermission{
				ToolID:    "perm",
				ToolName:  req.ToolName,
				Input:     req.Input,
				MessageID: msgID,
				Response:  respChan,
			})

			// Wait for user response (with timeout)
			select {
			case result := <-respChan:
				return &mcp.PermissionResponse{
					Behavior:     result.Behavior,
					UpdatedInput: result.UpdatedInput,
					Message:      result.Message,
				}, nil
			case <-ctx.Done():
				trackerMgr.ClearPermission(chatID)
				bot.DeleteMessage(chatID, msgID)
				return &mcp.PermissionResponse{
					Behavior: "deny",
					Message:  "Request cancelled",
				}, nil
			case <-time.After(2 * time.Minute):
				trackerMgr.ClearPermission(chatID)
				bot.DeleteMessage(chatID, msgID)
				return &mcp.PermissionResponse{
					Behavior: "deny",
					Message:  "Permission request timed out",
				}, nil
			}
		})

		// Set up MCP config with per-chat config function
		manager.SetMCPConfig(&claude.MCPConfig{
			ToolName:   mcpBridge.GetToolName(),
			ConfigFunc: mcpBridge.GetConfigPath,
		})
		slog.Info("MCP permission callback handler configured")
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

		// Check if this is a routed command (clear, rebuild, cd, sessions)
		if cmdName, cmdArgs := commands.ParseCommand(text); cmdName != "" {
			if cmd := cmdRouter.Lookup(cmdName); cmd != nil {
				resp, err := cmd.Execute(msgCtx, chatID, cmdArgs)
				if err != nil {
					slog.Error("command error", "cmd", cmdName, "error", err)
					respond(fmt.Sprintf("Error: %v", err), false)
					return
				}
				if resp != nil {
					respond(resp.Text, resp.Silent)
				}
				return
			}
		}

		// Check if this is a silent command that needs special handling
		isSilentCmd, confirmation := claude.IsSilentCommand(text)
		if isSilentCmd {
			slog.Debug("handling silent command", "command", text)
		}

		// Track if we got any response
		gotResponse := false

		// Build response callbacks using shared handler
		cb := &handlers.CallbackBuilder{
			ChatID:     chatID,
			TrackerMgr: trackerMgr,
			Bot:        bot,
			SendFn: func(text string, silent bool) {
				gotResponse = true
				respond(text, silent)
			},
			Logger: slog.Default(),
		}

		// Send message via persistent process manager
		err := manager.Send(msgCtx, chatID, text, cb.Build())

		// Clear the trackers after response is complete
		cb.ClearTrackers()

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

		// Handle permission callbacks
		if cb.Type == "p" {
			pending := trackerMgr.GetPermission(chatID)
			if pending == nil {
				slog.Warn("no pending permission for chat", "chat_id", chatID)
				return "Permission request expired"
			}

			var result *trackers.PermissionResult
			switch cb.Action {
			case "a": // allow
				result = &trackers.PermissionResult{
					Behavior:     "allow",
					UpdatedInput: pending.Input,
				}
				slog.Info("permission allowed", "chat_id", chatID, "tool", pending.ToolName)
			case "aa": // allow-always
				result = &trackers.PermissionResult{
					Behavior:     "allow-always",
					UpdatedInput: pending.Input,
				}
				slog.Info("permission allowed always", "chat_id", chatID, "tool", pending.ToolName)
			case "d": // deny
				result = &trackers.PermissionResult{
					Behavior: "deny",
					Message:  "User denied permission",
				}
				slog.Info("permission denied", "chat_id", chatID, "tool", pending.ToolName)
			default:
				return "Invalid permission action"
			}

			// Send result back through the channel
			select {
			case pending.Response <- result:
				// Delete the keyboard message
				if pending.MessageID > 0 {
					bot.DeleteMessage(chatID, pending.MessageID)
				}
				trackerMgr.ClearPermission(chatID)
			default:
				slog.Warn("permission response channel not ready", "chat_id", chatID)
			}

			return "Permission: " + result.Behavior
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
		pending := trackerMgr.GetQuestion(chatID)
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
			trackerMgr.ClearQuestion(chatID)
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

		// Delete the current keyboard message
		if pending.MessageID > 0 {
			bot.DeleteMessage(chatID, pending.MessageID)
		}

		// Store this answer
		pending.Answers = append(pending.Answers, selectedOption.Label)
		pending.CurrentIdx++
		nextIdx := pending.CurrentIdx
		totalQuestions := len(pending.Questions)
		allAnswers := make([]string, len(pending.Answers))
		copy(allAnswers, pending.Answers)

		// Check if more questions remain
		if nextIdx < totalQuestions {
			// Send next question keyboard
			nextQ := pending.Questions[nextIdx]
			keyboard, text := telegram.BuildQuestionKeyboard(pending.ToolID, nextIdx, nextQ)
			msgID, err := bot.SendQuestionKeyboard(chatID, text, keyboard)
			if err != nil {
				slog.Error("failed to send next question keyboard", "error", err)
			}
			// Update message ID for next question
			pending.MessageID = msgID
			return "Selected: " + selectedOption.Label
		}

		// All questions answered - clear pending and send all answers to Claude
		trackerMgr.ClearQuestion(chatID)

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

			// Build response callbacks using shared handler
			cb := &handlers.CallbackBuilder{
				ChatID:     chatID,
				TrackerMgr: trackerMgr,
				Bot:        bot,
				SendFn: func(text string, silent bool) {
					bot.SendMessage(chatID, text, silent)
				},
				Logger: slog.Default(),
			}

			err := manager.Send(cbCtx, chatID, combinedAnswer, cb.Build())
			cb.ClearTrackers()

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

// runMCPServer runs Aria as an MCP server for permission prompts
// This is invoked by Claude when it needs to ask for permission
func runMCPServer() {
	// Set up minimal logging to stderr (stdout is for MCP protocol)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Create callback client from environment
	client, err := mcp.NewCallbackClientFromEnv()
	if err != nil {
		logger.Error("failed to create callback client", "error", err)
		os.Exit(1)
	}

	logger.Info("aria mcp server starting", "callback_port", os.Getenv(mcp.EnvCallbackPort))

	// Handler calls back to parent Aria via HTTP
	handler := func(ctx context.Context, chatID int64, toolName string, input map[string]interface{}) (*mcp.PermissionResponse, error) {
		logger.Info("permission requested, calling parent", "tool", toolName)
		return client.RequestPermission(ctx, toolName, input)
	}

	// Chat ID comes from env, passed to handler via closure
	if err := mcp.RunMCPServer(0, handler, logger); err != nil {
		logger.Error("mcp server error", "error", err)
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

