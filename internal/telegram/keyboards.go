package telegram

import (
	"encoding/json"
	"fmt"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// QuestionOption represents a single option in an AskUserQuestion
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question represents a single question from AskUserQuestion tool
type Question struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multiSelect"`
}

// AskUserQuestionInput represents the input to the AskUserQuestion tool
type AskUserQuestionInput struct {
	Questions []Question `json:"questions"`
}

// CallbackData stores callback information for keyboard buttons
type CallbackData struct {
	Type        string `json:"t"`            // "q" for question, "o" for other, "s" for session
	ToolID      string `json:"id,omitempty"` // Tool use ID to respond to
	QuestionIdx int    `json:"qi,omitempty"` // Which question (0-indexed)
	OptionIdx   int    `json:"oi,omitempty"` // Which option selected (for answer type)
	SessionID   string `json:"s,omitempty"`  // Session ID (for session switching)
	Action      string `json:"a,omitempty"`  // Action: "r" resume, "f" fresh
}

// SessionDisplayInfo contains info needed to display a session in the keyboard
type SessionDisplayInfo struct {
	ID          string // Full session UUID
	ShortID     string // First 8 chars for callback
	ProjectName string // Short project name
	Summary     string // Session summary/topic
	TimeAgo     string // Formatted relative time
}

// ParseAskUserQuestion parses the input map from an AskUserQuestion tool call
func ParseAskUserQuestion(input map[string]interface{}) (*AskUserQuestionInput, error) {
	// Re-marshal and unmarshal to properly parse nested structures
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshaling input: %w", err)
	}

	var parsed AskUserQuestionInput
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshaling input: %w", err)
	}

	return &parsed, nil
}

// BuildQuestionKeyboard creates an inline keyboard for a question
// Returns the keyboard and a formatted question text
func BuildQuestionKeyboard(toolID string, questionIdx int, q Question) (gotgbot.InlineKeyboardMarkup, string) {
	var rows [][]gotgbot.InlineKeyboardButton

	// Add option buttons
	for i, opt := range q.Options {
		callbackData := CallbackData{
			Type:        "q",
			ToolID:      toolID,
			QuestionIdx: questionIdx,
			OptionIdx:   i,
		}

		data, _ := json.Marshal(callbackData)

		// Telegram callback_data has 64 byte limit, truncate tool ID if needed
		dataStr := string(data)
		if len(dataStr) > 64 {
			// Use shorter tool ID
			callbackData.ToolID = toolID[:8]
			data, _ = json.Marshal(callbackData)
			dataStr = string(data)
		}

		rows = append(rows, []gotgbot.InlineKeyboardButton{
			{
				Text:         opt.Label,
				CallbackData: dataStr,
			},
		})
	}

	// Add "Other" button for custom input
	otherData := CallbackData{
		Type:        "o",
		ToolID:      toolID,
		QuestionIdx: questionIdx,
	}
	otherDataBytes, _ := json.Marshal(otherData)
	otherDataStr := string(otherDataBytes)
	if len(otherDataStr) > 64 {
		otherData.ToolID = toolID[:8]
		otherDataBytes, _ = json.Marshal(otherData)
		otherDataStr = string(otherDataBytes)
	}

	rows = append(rows, []gotgbot.InlineKeyboardButton{
		{
			Text:         "Other...",
			CallbackData: otherDataStr,
		},
	})

	keyboard := gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: rows,
	}

	// Format question text with header using MarkdownV2
	// *text* is bold in MarkdownV2
	text := fmt.Sprintf("*%s*\n%s", escapeMarkdownV2(q.Header), escapeMarkdownV2(q.Question))

	return keyboard, text
}

// ParseCallbackData parses the callback_data from a button press
func ParseCallbackData(data string) (*CallbackData, error) {
	var cb CallbackData
	if err := json.Unmarshal([]byte(data), &cb); err != nil {
		return nil, err
	}
	return &cb, nil
}

// PermissionRequest represents a pending permission request
type PermissionRequest struct {
	ToolName string                 `json:"tool_name"`
	Input    map[string]interface{} `json:"input"`
}

// BuildPermissionKeyboard creates an inline keyboard for permission prompts
// Returns the keyboard and a formatted message describing the permission request
func BuildPermissionKeyboard(toolID string, toolName string, input map[string]interface{}) (gotgbot.InlineKeyboardMarkup, string) {
	// Format the permission request message
	var details string
	switch toolName {
	case "Write", "Edit":
		if path, ok := input["file_path"].(string); ok {
			details = fmt.Sprintf("File: %s", path)
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			// Truncate long commands
			if len(cmd) > 100 {
				cmd = cmd[:97] + "..."
			}
			details = fmt.Sprintf("Command: %s", cmd)
		}
	default:
		// Generic: show first few input keys
		keys := make([]string, 0)
		for k := range input {
			keys = append(keys, k)
			if len(keys) >= 3 {
				break
			}
		}
		if len(keys) > 0 {
			details = fmt.Sprintf("Params: %v", keys)
		}
	}

	text := fmt.Sprintf("*Permission Request*\nTool: %s", escapeMarkdownV2(toolName))
	if details != "" {
		text += fmt.Sprintf("\n%s", escapeMarkdownV2(details))
	}

	// Create buttons: Allow, Allow Always, Deny
	allowData := CallbackData{
		Type:   "p",
		ToolID: toolID,
		Action: "a", // allow
	}
	allowAlwaysData := CallbackData{
		Type:   "p",
		ToolID: toolID,
		Action: "aa", // allow-always
	}
	denyData := CallbackData{
		Type:   "p",
		ToolID: toolID,
		Action: "d", // deny
	}

	// Truncate tool ID if needed for 64 byte limit
	truncateCallback := func(cb *CallbackData) string {
		data, _ := json.Marshal(cb)
		if len(data) > 64 && len(cb.ToolID) > 8 {
			cb.ToolID = cb.ToolID[:8]
			data, _ = json.Marshal(cb)
		}
		return string(data)
	}

	keyboard := gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
			{
				{Text: "Allow", CallbackData: truncateCallback(&allowData)},
				{Text: "Always", CallbackData: truncateCallback(&allowAlwaysData)},
				{Text: "Deny", CallbackData: truncateCallback(&denyData)},
			},
		},
	}

	return keyboard, text
}

// BuildSessionKeyboard creates an inline keyboard for session selection
func BuildSessionKeyboard(sessions []SessionDisplayInfo) gotgbot.InlineKeyboardMarkup {
	var rows [][]gotgbot.InlineKeyboardButton

	for _, s := range sessions {
		// Format label: "project · summary · time"
		summary := s.Summary
		if len(summary) > 25 {
			summary = summary[:22] + "..."
		}
		label := fmt.Sprintf("%s · %s · %s", s.ProjectName, summary, s.TimeAgo)

		callbackData := CallbackData{
			Type:      "s",
			SessionID: s.ShortID,
			Action:    "r", // resume
		}
		data, _ := json.Marshal(callbackData)

		rows = append(rows, []gotgbot.InlineKeyboardButton{
			{
				Text:         label,
				CallbackData: string(data),
			},
		})
	}

	// Add "Start Fresh" button
	freshData := CallbackData{
		Type:   "s",
		Action: "f", // fresh
	}
	freshDataBytes, _ := json.Marshal(freshData)

	rows = append(rows, []gotgbot.InlineKeyboardButton{
		{
			Text:         "Start Fresh",
			CallbackData: string(freshDataBytes),
		},
	})

	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: rows,
	}
}
