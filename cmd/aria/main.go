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
	"sync"
	"syscall"

	"github.com/codegangsta/aria/internal/claude"
	"github.com/codegangsta/aria/internal/config"
	"github.com/codegangsta/aria/internal/telegram"
)

// PendingQuestion stores context for an AskUserQuestion waiting for user input
type PendingQuestion struct {
	ToolID    string
	Questions []telegram.Question
}

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
		"skip_permissions", cfg.Claude.SkipPermissions,
	)

	// Create components
	manager := claude.NewManager(*claudePath, cfg.Debug, cfg.Claude.SkipPermissions, slog.Default())

	bot, err := telegram.New(cfg.Telegram.Token, cfg.Allowlist, cfg.Debug, slog.Default())
	if err != nil {
		slog.Error("failed to create telegram bot", "error", err)
		os.Exit(1)
	}

	// Pending questions waiting for user input (chatID -> PendingQuestion)
	pendingQuestions := make(map[int64]*PendingQuestion)
	var pendingMu sync.RWMutex

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

		// Check if this is a silent command that needs special handling
		isSilentCmd, confirmation := claude.IsSilentCommand(text)
		if isSilentCmd {
			slog.Debug("handling silent command", "command", text)
		}

		// Track if we got any response
		gotResponse := false

		// Send message via persistent process manager
		// isFinal=true means it's the last message, so we play a sound
		// isFinal=false means intermediate message, send silently
		err := manager.Send(msgCtx, chatID, text, claude.ResponseCallbacks{
			OnMessage: func(responseText string, isFinal bool) {
				gotResponse = true
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
				// Handle AskUserQuestion specially - send inline keyboard
				if tool.Name == "AskUserQuestion" {
					parsed, err := telegram.ParseAskUserQuestion(tool.Input)
					if err != nil {
						slog.Error("failed to parse AskUserQuestion", "error", err)
						return
					}

					// Store pending question for this chat
					pendingMu.Lock()
					pendingQuestions[chatID] = &PendingQuestion{
						ToolID:    tool.ID,
						Questions: parsed.Questions,
					}
					pendingMu.Unlock()

					// Send keyboard for first question only (one at a time for now)
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

				// Format and send tool notification as reply to user's message
				notification := telegram.FormatToolNotification(telegram.ToolUse{
					ID:    tool.ID,
					Name:  tool.Name,
					Input: tool.Input,
				})
				slog.Debug("tool use notification",
					"chat_id", chatID,
					"tool", tool.Name,
					"notification", notification,
				)
				replyHTML(notification, msgID, true) // Reply to user's message, silent
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

	// Set up callback handler for inline keyboard button presses
	bot.SetCallbackHandler(func(cbCtx context.Context, chatID int64, userID int64, data string) string {
		cb, err := telegram.ParseCallbackData(data)
		if err != nil {
			slog.Error("failed to parse callback data", "error", err, "data", data)
			return "Error processing selection"
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
		)

		// Clear pending question
		pendingMu.Lock()
		delete(pendingQuestions, chatID)
		pendingMu.Unlock()

		// Send the selection back to Claude as a user message
		// The selection is sent as plain text which Claude will interpret
		go func() {
			// Start typing indicator
			stopTyping := bot.TypingLoop(chatID)
			defer stopTyping()

			err := manager.Send(cbCtx, chatID, selectedOption.Label, claude.ResponseCallbacks{
				OnMessage: func(text string, isFinal bool) {
					silent := !isFinal
					bot.SendMessage(chatID, text, silent)
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
							ToolID:    tool.ID,
							Questions: parsed.Questions,
						}
						pendingMu.Unlock()
						// Send first question only (one at a time)
						if len(parsed.Questions) > 0 {
							q := parsed.Questions[0]
							keyboard, text := telegram.BuildQuestionKeyboard(tool.ID, 0, q)
							bot.SendQuestionKeyboard(chatID, text, keyboard)
						}
						return
					}
					notification := telegram.FormatToolNotification(telegram.ToolUse{
						ID:    tool.ID,
						Name:  tool.Name,
						Input: tool.Input,
					})
					bot.SendMessage(chatID, notification, true)
				},
			})
			if err != nil {
				slog.Error("error sending callback response to claude", "error", err)
				bot.SendMessage(chatID, "Sorry, something went wrong.", false)
			}
		}()

		return "Selected: " + selectedOption.Label
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
