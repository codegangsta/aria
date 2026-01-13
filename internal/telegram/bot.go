package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
)

// MessageHandler is called when a message is received from an allowed user
type MessageHandler func(ctx context.Context, chatID int64, userID int64, text string, respond func(string))

// Bot wraps the Telegram bot functionality
type Bot struct {
	bot       *gotgbot.Bot
	updater   *ext.Updater
	allowlist map[int64]bool
	handler   MessageHandler
	logger    *slog.Logger
	debug     bool
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

		// Create respond function that sends messages back
		respond := func(text string) {
			if _, err := bot.SendMessage(chatID, text, nil); err != nil {
				b.logger.Error("failed to send message",
					"chat_id", chatID,
					"error", err,
				)
			}
		}

		// Call handler (this blocks until Claude responds)
		b.handler(msgCtx, chatID, userID, msg.Text, respond)
	}

	return nil
}

// startTyping sends a typing indicator and refreshes it periodically
func (b *Bot) startTyping(chatID int64) {
	_, _ = b.bot.SendChatAction(chatID, "typing", nil)
}

// SendMessage sends a text message to a chat
func (b *Bot) SendMessage(chatID int64, text string) error {
	_, err := b.bot.SendMessage(chatID, text, nil)
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
