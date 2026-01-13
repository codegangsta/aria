package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/codegangsta/aria/internal/claude"
	"github.com/codegangsta/aria/internal/config"
	"github.com/codegangsta/aria/internal/telegram"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	claudePath := flag.String("claude", "claude", "path to claude binary")
	flag.Parse()

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
	)

	// Create components
	manager := claude.NewManager(*claudePath, cfg.Debug, slog.Default())

	bot, err := telegram.New(cfg.Telegram.Token, cfg.Allowlist, cfg.Debug, slog.Default())
	if err != nil {
		slog.Error("failed to create telegram bot", "error", err)
		os.Exit(1)
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
	bot.SetHandler(func(msgCtx context.Context, chatID int64, userID int64, text string, respond func(string, bool)) {
		slog.Info("processing message",
			"chat_id", chatID,
			"user_id", userID,
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

		// Check if this is a silent command that needs special handling
		isSilentCmd, confirmation := claude.IsSilentCommand(text)
		if isSilentCmd {
			slog.Debug("handling silent command", "command", text)
		}

		// Track if we got any response
		gotResponse := false

		// Collect tool calls to send as grouped summary
		var toolCalls []telegram.ToolUse

		// Send message via persistent process manager
		// isFinal=true means it's the last message, so we play a sound
		// isFinal=false means intermediate message, send silently
		err := manager.Send(msgCtx, chatID, text, claude.ResponseCallbacks{
			OnMessage: func(responseText string, isFinal bool) {
				gotResponse = true

				// Send tool summary before final response
				if isFinal && len(toolCalls) > 0 {
					summary := telegram.FormatToolSummary(toolCalls)
					slog.Debug("sending tool summary",
						"chat_id", chatID,
						"tool_count", len(toolCalls),
					)
					respond(summary, true) // Silent for tool summary
				}

				silent := !isFinal // Silent for intermediate messages, sound for final
				slog.Debug("sending response to telegram",
					"chat_id", chatID,
					"text_length", len(responseText),
					"is_final", isFinal,
					"silent", silent,
				)
				respond(responseText, silent)
				slog.Debug("response sent")
			},
			OnToolUse: func(tool claude.ToolUse) {
				// Collect tool calls for grouped summary
				toolCalls = append(toolCalls, telegram.ToolUse{
					ID:    tool.ID,
					Name:  tool.Name,
					Input: tool.Input,
				})
				slog.Debug("tool use collected",
					"chat_id", chatID,
					"tool", tool.Name,
					"total_tools", len(toolCalls),
				)
			},
		})

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

	slog.Info("aria started, connecting to telegram")

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
