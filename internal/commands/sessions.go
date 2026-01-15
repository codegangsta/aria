package commands

import (
	"context"
	"log/slog"

	"github.com/codegangsta/aria/internal/claude"
	"github.com/codegangsta/aria/internal/telegram"
)

// SessionsCommand handles /sessions - shows session picker
type SessionsCommand struct {
	discovery *claude.SessionDiscovery
	bot       *telegram.Bot
}

// NewSessionsCommand creates a new sessions command
func NewSessionsCommand(discovery *claude.SessionDiscovery, bot *telegram.Bot) *SessionsCommand {
	return &SessionsCommand{
		discovery: discovery,
		bot:       bot,
	}
}

func (c *SessionsCommand) Name() string {
	return "sessions"
}

func (c *SessionsCommand) Execute(ctx context.Context, chatID int64, args string) (*Response, error) {
	slog.Info("showing sessions", "chat_id", chatID)

	sessions, err := c.discovery.DiscoverSessions(7)
	if err != nil {
		slog.Error("failed to discover sessions", "error", err)
		return &Response{
			Text:   "Failed to load sessions.",
			Silent: false,
		}, nil
	}

	if len(sessions) == 0 {
		return &Response{
			Text:   "No recent sessions found.",
			Silent: false,
		}, nil
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
	if _, err := c.bot.SendQuestionKeyboard(chatID, "*Sessions*", keyboard); err != nil {
		slog.Error("failed to send session keyboard", "error", err)
	}

	// Return nil response since we handle the keyboard ourselves
	return nil, nil
}
