package handlers

import (
	"log/slog"

	"github.com/codegangsta/aria/internal/claude"
	"github.com/codegangsta/aria/internal/telegram"
	"github.com/codegangsta/aria/internal/trackers"
	"github.com/codegangsta/aria/internal/types"
)

// CallbackBuilder creates ResponseCallbacks for Claude message handling.
// This consolidates the duplicate callback logic from main message handler
// and callback handler into a single reusable builder.
type CallbackBuilder struct {
	ChatID     int64
	TrackerMgr *trackers.Manager
	Bot        *telegram.Bot
	SendFn     func(text string, silent bool)
	Logger     *slog.Logger
}

// Build creates ResponseCallbacks that route events to the appropriate handlers.
func (b *CallbackBuilder) Build() claude.ResponseCallbacks {
	logger := b.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return claude.ResponseCallbacks{
		OnMessage: func(text string, isFinal bool) {
			// Flush and clear tool tracker before sending text to start new tool group
			b.TrackerMgr.ToolTracker(b.ChatID).FlushAndClear()

			silent := !isFinal // Silent for intermediate messages, sound for final
			b.SendFn(text, silent)

			logger.Debug("sent response",
				"chat_id", b.ChatID,
				"text_length", len(text),
				"is_final", isFinal,
			)
		},

		OnTodoUpdate: func(todos []types.Todo) {
			b.TrackerMgr.ProgressTracker(b.ChatID).Update(todos)
			logger.Debug("todo update",
				"chat_id", b.ChatID,
				"count", len(todos),
			)
		},

		OnToolUse: func(tool types.ToolUse) {
			// Handle AskUserQuestion specially - send inline keyboard
			if tool.Name == "AskUserQuestion" {
				b.handleAskUserQuestion(tool, logger)
				return
			}

			// Add other tools to consolidated tracker
			b.TrackerMgr.ToolTracker(b.ChatID).AddTool(tool)
			logger.Debug("tool added to tracker",
				"chat_id", b.ChatID,
				"tool", tool.Name,
			)
		},

		OnToolResult: func(result types.ToolResult) {
			b.TrackerMgr.ToolTracker(b.ChatID).CompleteTool(result.ToolID, result.IsError)
		},

		OnToolError: func(toolID string, errorMsg string) {
			// Just log - the indicator in tool tracker is enough visual indication
			logger.Debug("tool error",
				"chat_id", b.ChatID,
				"tool_id", toolID,
				"error", errorMsg,
			)
		},

		OnPermissionDenial: func(denials []string) {
			// Just log for now - Phase 10 will add interactive permission handling
			logger.Warn("permission denials",
				"chat_id", b.ChatID,
				"denials", denials,
			)
		},
	}
}

// handleAskUserQuestion parses the tool input and sends an inline keyboard
func (b *CallbackBuilder) handleAskUserQuestion(tool types.ToolUse, logger *slog.Logger) {
	parsed, err := telegram.ParseAskUserQuestion(tool.Input)
	if err != nil {
		logger.Error("failed to parse AskUserQuestion", "error", err)
		return
	}

	// Store pending question for this chat
	b.TrackerMgr.SetQuestion(b.ChatID, &trackers.PendingQuestion{
		ToolID:     tool.ID,
		Questions:  parsed.Questions,
		CurrentIdx: 0,
		Answers:    make([]string, 0, len(parsed.Questions)),
	})

	// Send keyboard for first question
	if len(parsed.Questions) > 0 {
		q := parsed.Questions[0]
		keyboard, text := telegram.BuildQuestionKeyboard(tool.ID, 0, q)
		if err := b.Bot.SendQuestionKeyboard(b.ChatID, text, keyboard); err != nil {
			logger.Error("failed to send question keyboard",
				"chat_id", b.ChatID,
				"error", err,
			)
		}
	}

	logger.Debug("sent AskUserQuestion keyboard",
		"chat_id", b.ChatID,
		"tool_id", tool.ID,
		"question_count", len(parsed.Questions),
	)
}

// ClearTrackers clears both tool and progress trackers for a chat.
// Should be called after a response is complete.
func (b *CallbackBuilder) ClearTrackers() {
	b.TrackerMgr.ClearToolTracker(b.ChatID)
	b.TrackerMgr.ClearProgressTracker(b.ChatID)
}
