// Package types contains shared types used across packages
package types

// ToolUse represents a tool call from Claude
type ToolUse struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

// Todo represents a single todo item from Claude's TodoWrite
type Todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"` // "pending", "in_progress", "completed"
	ActiveForm string `json:"activeForm"`
}

// ToolResult represents the result of a tool execution
type ToolResult struct {
	ToolID  string
	IsError bool
}
