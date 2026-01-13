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
	Type      string `json:"t"`           // "q" for question answer, "o" for other
	ToolID    string `json:"id"`          // Tool use ID to respond to
	QuestionIdx int  `json:"qi"`          // Which question (0-indexed)
	OptionIdx int    `json:"oi,omitempty"` // Which option selected (for answer type)
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
