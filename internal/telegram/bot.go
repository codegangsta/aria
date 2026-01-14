package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
)

// RespondFunc sends a markdown message (will be converted to HTML)
type RespondFunc func(text string, silent bool)

// ReplyHTMLFunc sends pre-formatted HTML as a reply to a specific message
type ReplyHTMLFunc func(html string, replyToMsgID int64, silent bool)

// MessageHandler is called when a message is received from an allowed user
// msgID is the ID of the user's message (for replies)
type MessageHandler func(ctx context.Context, chatID int64, userID int64, msgID int64, text string, respond RespondFunc, replyHTML ReplyHTMLFunc)

// CallbackHandler is called when an inline keyboard button is pressed
// Returns the text to show the user after button press
type CallbackHandler func(ctx context.Context, chatID int64, userID int64, data string) string

// Bot wraps the Telegram bot functionality
type Bot struct {
	bot                *gotgbot.Bot
	updater            *ext.Updater
	allowlist          map[int64]bool
	handler            MessageHandler
	callbackHandler    CallbackHandler
	logger             *slog.Logger
	debug              bool
	commandsRegistered bool
}

// New creates a new Telegram bot
func New(token string, allowlist []int64, debug bool, logger *slog.Logger) (*Bot, error) {
	// Create HTTP client with longer timeout for long-polling
	httpClient := http.Client{
		Timeout: 60 * time.Second,
	}

	bot, err := gotgbot.NewBot(token, &gotgbot.BotOpts{
		BotClient: &gotgbot.BaseBotClient{
			Client: httpClient,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating bot: %w", err)
	}

	// Convert allowlist slice to map for O(1) lookup
	allowMap := make(map[int64]bool, len(allowlist))
	for _, id := range allowlist {
		allowMap[id] = true
	}

	b := &Bot{
		bot:       bot,
		allowlist: allowMap,
		logger:    logger,
		debug:     debug,
	}

	return b, nil
}

// SetHandler sets the message handler function
func (b *Bot) SetHandler(h MessageHandler) {
	b.handler = h
}

// SetCallbackHandler sets the callback query handler function
func (b *Bot) SetCallbackHandler(h CallbackHandler) {
	b.callbackHandler = h
}

// Start begins polling for updates and blocks until context is cancelled
func (b *Bot) Start(ctx context.Context) error {
	// Create updater and dispatcher
	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(bot *gotgbot.Bot, ctx *ext.Context, err error) ext.DispatcherAction {
			b.logger.Error("dispatcher error", "error", err)
			return ext.DispatcherActionNoop
		},
	})

	b.updater = ext.NewUpdater(dispatcher, nil)

	// Add message handler
	dispatcher.AddHandler(handlers.NewMessage(nil, b.handleMessage))

	// Add callback query handler for inline keyboard buttons
	dispatcher.AddHandler(handlers.NewCallback(nil, b.handleCallback))

	// Start polling
	err := b.updater.StartPolling(b.bot, &ext.PollingOpts{
		DropPendingUpdates: true,
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
			Timeout: 30,
			AllowedUpdates: []string{
				"message",
				"callback_query",
			},
			RequestOpts: &gotgbot.RequestOpts{
				Timeout: 60 * time.Second,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("starting polling: %w", err)
	}

	b.logger.Info("telegram bot started",
		"username", b.bot.Username,
		"allowlist_count", len(b.allowlist),
	)

	// Wait for context cancellation
	<-ctx.Done()

	// Stop polling gracefully
	b.updater.Stop()
	b.logger.Info("telegram bot stopped")

	return nil
}

// handleMessage processes incoming messages
func (b *Bot) handleMessage(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil || msg.Text == "" {
		return nil
	}

	userID := msg.From.Id
	chatID := msg.Chat.Id

	// Check allowlist
	if !b.allowlist[userID] {
		b.logger.Debug("ignoring message from non-allowed user",
			"user_id", userID,
			"chat_id", chatID,
			"username", msg.From.Username,
		)
		return nil
	}

	b.logger.Info("processing message",
		"user_id", userID,
		"chat_id", chatID,
		"username", msg.From.Username,
		"text_length", len(msg.Text),
	)

	// Call the handler if set
	if b.handler != nil {
		// Create a context for this message
		msgCtx := context.Background()

		// Start typing indicator
		b.startTyping(chatID)

		// respond converts markdown to MarkdownV2 before sending
		respond := func(text string, silent bool) {
			formatted := FormatMarkdownV2(text)
			opts := &gotgbot.SendMessageOpts{
				ParseMode:           "MarkdownV2",
				DisableNotification: silent,
			}
			if _, err := bot.SendMessage(chatID, formatted, opts); err != nil {
				// If MarkdownV2 parsing fails, fall back to plain text
				b.logger.Warn("MarkdownV2 send failed, retrying plain",
					"chat_id", chatID,
					"error", err,
					"formatted", formatted,
				)
				plainOpts := &gotgbot.SendMessageOpts{
					DisableNotification: silent,
				}
				if _, err := bot.SendMessage(chatID, text, plainOpts); err != nil {
					b.logger.Error("failed to send message",
						"chat_id", chatID,
						"error", err,
					)
				}
			}
		}

		// replyMarkdown sends pre-formatted MarkdownV2 as a reply to a specific message
		replyHTML := func(text string, replyToMsgID int64, silent bool) {
			opts := &gotgbot.SendMessageOpts{
				ParseMode:           "MarkdownV2",
				DisableNotification: silent,
				ReplyParameters: &gotgbot.ReplyParameters{
					MessageId: replyToMsgID,
				},
			}
			if _, err := bot.SendMessage(chatID, text, opts); err != nil {
				b.logger.Warn("MarkdownV2 reply failed, retrying plain",
					"chat_id", chatID,
					"reply_to", replyToMsgID,
					"error", err,
				)
				// Fall back to plain text
				plainOpts := &gotgbot.SendMessageOpts{
					DisableNotification: silent,
					ReplyParameters: &gotgbot.ReplyParameters{
						MessageId: replyToMsgID,
					},
				}
				if _, err := bot.SendMessage(chatID, text, plainOpts); err != nil {
					b.logger.Error("failed to send reply",
						"chat_id", chatID,
						"error", err,
					)
				}
			}
		}

		// Call handler (this blocks until Claude responds)
		b.handler(msgCtx, chatID, userID, msg.MessageId, msg.Text, respond, replyHTML)
	}

	return nil
}

// handleCallback processes callback queries from inline keyboard buttons
func (b *Bot) handleCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	cb := ctx.CallbackQuery
	if cb == nil {
		return nil
	}

	userID := cb.From.Id
	chatID := cb.Message.GetChat().Id

	// Check allowlist
	if !b.allowlist[userID] {
		b.logger.Debug("ignoring callback from non-allowed user",
			"user_id", userID,
			"chat_id", chatID,
		)
		return nil
	}

	b.logger.Info("processing callback",
		"user_id", userID,
		"chat_id", chatID,
		"data", cb.Data,
	)

	// Call the callback handler if set
	var answerText string
	if b.callbackHandler != nil {
		cbCtx := context.Background()
		answerText = b.callbackHandler(cbCtx, chatID, userID, cb.Data)
	}

	// Answer the callback to remove the loading state
	_, err := cb.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
		Text: answerText,
	})
	if err != nil {
		b.logger.Warn("failed to answer callback", "error", err)
	}

	return nil
}

// startTyping sends a typing indicator and refreshes it periodically
func (b *Bot) startTyping(chatID int64) {
	_, _ = b.bot.SendChatAction(chatID, "typing", nil)
}

// SendMessage sends a text message to a chat with MarkdownV2 formatting
// silent=true disables notification sound
func (b *Bot) SendMessage(chatID int64, text string, silent bool) error {
	formatted := FormatMarkdownV2(text)
	opts := &gotgbot.SendMessageOpts{
		ParseMode:           "MarkdownV2",
		DisableNotification: silent,
	}
	_, err := b.bot.SendMessage(chatID, formatted, opts)
	if err != nil {
		// Fall back to plain text if MarkdownV2 fails
		b.logger.Warn("MarkdownV2 send failed, retrying plain", "error", err, "formatted", formatted)
		plainOpts := &gotgbot.SendMessageOpts{
			DisableNotification: silent,
		}
		_, err = b.bot.SendMessage(chatID, text, plainOpts)
	}
	return err
}

// SendMessageMarkdownV2 sends a pre-formatted MarkdownV2 message (no additional escaping)
// Use this for text that's already escaped for MarkdownV2
func (b *Bot) SendMessageMarkdownV2(chatID int64, text string, silent bool) error {
	opts := &gotgbot.SendMessageOpts{
		ParseMode:           "MarkdownV2",
		DisableNotification: silent,
	}
	_, err := b.bot.SendMessage(chatID, text, opts)
	if err != nil {
		b.logger.Warn("MarkdownV2 send failed", "error", err, "text", text)
	}
	return err
}

// SendToolNotification sends a tool notification and returns the message ID
// Used for tool call notifications that may be edited later
func (b *Bot) SendToolNotification(chatID int64, text string) (int64, error) {
	opts := &gotgbot.SendMessageOpts{
		ParseMode:           "MarkdownV2",
		DisableNotification: true, // Always silent for tool notifications
	}
	msg, err := b.bot.SendMessage(chatID, text, opts)
	if err != nil {
		b.logger.Warn("failed to send tool notification", "error", err, "text", text)
		return 0, err
	}
	return msg.MessageId, nil
}

// EditMessageMarkdownV2 edits an existing message with new MarkdownV2 content
func (b *Bot) EditMessageMarkdownV2(chatID int64, msgID int64, text string) error {
	opts := &gotgbot.EditMessageTextOpts{
		ParseMode: "MarkdownV2",
	}
	_, _, err := b.bot.EditMessageText(text, &gotgbot.EditMessageTextOpts{
		ChatId:    chatID,
		MessageId: msgID,
		ParseMode: opts.ParseMode,
	})
	if err != nil {
		b.logger.Warn("failed to edit message", "error", err, "chat_id", chatID, "msg_id", msgID)
	}
	return err
}

// SendQuestionKeyboard sends a question with inline keyboard options
func (b *Bot) SendQuestionKeyboard(chatID int64, text string, keyboard gotgbot.InlineKeyboardMarkup) error {
	opts := &gotgbot.SendMessageOpts{
		ParseMode:   "MarkdownV2",
		ReplyMarkup: keyboard,
	}
	_, err := b.bot.SendMessage(chatID, text, opts)
	if err != nil {
		b.logger.Error("failed to send question keyboard",
			"chat_id", chatID,
			"error", err,
		)
	}
	return err
}

// TypingLoop starts a goroutine that sends typing indicators every 4 seconds
// Returns a cancel function to stop the loop
func (b *Bot) TypingLoop(chatID int64) func() {
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()

		// Send initial typing indicator
		b.startTyping(chatID)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.startTyping(chatID)
			}
		}
	}()

	return cancel
}

// builtinCommands are Claude Code commands not exposed in the init event's slash_commands
// Also includes ARIA-specific commands handled before Claude
var builtinCommands = []string{
	"clear",    // Clear conversation history
	"help",     // Show help
	"memory",   // Edit CLAUDE.md
	"sessions", // Switch between Claude sessions
	"rebuild",  // Rebuild and restart ARIA
}

// RegisterCommands registers slash commands with Telegram's command menu
// Only registers once per bot lifetime
func (b *Bot) RegisterCommands(commands []string) {
	if b.commandsRegistered || len(commands) == 0 {
		return
	}

	// Combine discovered commands with known built-in commands
	allCommands := append(commands, builtinCommands...)

	// Build bot commands with descriptions
	// Telegram commands must be lowercase, 1-32 chars, only a-z, 0-9, and underscores
	botCommands := make([]gotgbot.BotCommand, 0, len(allCommands))
	for _, cmd := range allCommands {
		// Skip internal commands
		if cmd == "aria" {
			continue
		}

		// Convert hyphens to underscores for Telegram compatibility
		telegramCmd := strings.ReplaceAll(cmd, "-", "_")

		// Skip if still invalid (contains other special chars)
		if !isValidTelegramCommand(telegramCmd) {
			continue
		}

		botCommands = append(botCommands, gotgbot.BotCommand{
			Command:     telegramCmd,
			Description: getCommandDescription(cmd),
		})
	}

	if len(botCommands) == 0 {
		return
	}

	// Log commands being registered
	cmdNames := make([]string, len(botCommands))
	for i, bc := range botCommands {
		cmdNames[i] = bc.Command
	}
	b.logger.Info("attempting to register commands", "commands", cmdNames)

	// Register with Telegram
	_, err := b.bot.SetMyCommands(botCommands, nil)
	if err != nil {
		b.logger.Error("failed to register commands", "error", err, "commands", cmdNames)
		return
	}

	b.commandsRegistered = true
	b.logger.Info("registered telegram commands", "count", len(botCommands))
}

// isValidTelegramCommand checks if a command name is valid for Telegram
func isValidTelegramCommand(cmd string) bool {
	if len(cmd) < 1 || len(cmd) > 32 {
		return false
	}
	for i, c := range cmd {
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c >= '0' && c <= '9' && i > 0 {
			continue
		}
		if c == '_' && i > 0 {
			continue
		}
		return false
	}
	return true
}

// getCommandDescription returns a human-readable description for a command
func getCommandDescription(cmd string) string {
	descriptions := map[string]string{
		// Built-in commands
		"clear":  "Clear conversation history",
		"help":   "Show available commands",
		"memory": "Edit CLAUDE.md memory file",
		// Skills
		"commit":            "Stage and commit changes",
		"calendar":          "View and create calendar events",
		"mail":              "Read and manage email",
		"gtd-daily-review":  "Morning GTD daily review",
		"gtd-weekly-review": "Weekly GTD review",
		"gtd-process-inbox": "Process Things 3 inbox",
		"gtd-next-action":   "Get next action from Things 3",
		"gtd-project":       "Work through a Things 3 project",
		"gtd-clarify":       "Clarify today's tasks",
		"things3":           "Things 3 task management",
		"plan-to-project":   "Convert plan to Things 3 project",
		"reflect":           "Reflect on session",
		"browser":           "Browser automation",
		"compact":           "Compact conversation context",
	}

	if desc, ok := descriptions[cmd]; ok {
		return desc
	}
	return "Claude skill"
}
