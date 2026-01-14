package telegram

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/codegangsta/aria/internal/types"
)

// toolDisplayConfig defines how to display a specific tool
type toolDisplayConfig struct {
	Emoji  string
	Format func(input map[string]interface{}) string
	Verb   string // e.g., "Running", "Reading", "Editing"
}

// toolDisplays maps tool names to their display configuration
var toolDisplays = map[string]toolDisplayConfig{
	"Bash": {
		Emoji: "🔧",
		Verb:  "Running",
		Format: func(input map[string]interface{}) string {
			if cmd, ok := input["command"].(string); ok {
				if len(cmd) > 60 {
					cmd = cmd[:57] + "..."
				}
				return fmt.Sprintf("`%s`", escapeInlineCode(cmd))
			}
			return ""
		},
	},
	"Read": {
		Emoji: "📄",
		Verb:  "Reading",
		Format: func(input map[string]interface{}) string {
			if path, ok := input["file_path"].(string); ok {
				return escapeMarkdownV2(shortPath(path))
			}
			return ""
		},
	},
	"Edit": {
		Emoji: "✏️",
		Verb:  "Editing",
		Format: func(input map[string]interface{}) string {
			if path, ok := input["file_path"].(string); ok {
				return escapeMarkdownV2(shortPath(path))
			}
			return ""
		},
	},
	"Write": {
		Emoji: "📝",
		Verb:  "Writing",
		Format: func(input map[string]interface{}) string {
			if path, ok := input["file_path"].(string); ok {
				return escapeMarkdownV2(shortPath(path))
			}
			return ""
		},
	},
	"Grep": {
		Emoji: "🔍",
		Verb:  "Searching",
		Format: func(input map[string]interface{}) string {
			if pattern, ok := input["pattern"].(string); ok {
				if len(pattern) > 40 {
					pattern = pattern[:37] + "..."
				}
				return fmt.Sprintf("`%s`", escapeInlineCode(pattern))
			}
			return ""
		},
	},
	"Glob": {
		Emoji: "📂",
		Verb:  "Finding",
		Format: func(input map[string]interface{}) string {
			if pattern, ok := input["pattern"].(string); ok {
				return fmt.Sprintf("`%s`", escapeInlineCode(pattern))
			}
			return ""
		},
	},
	"Task": {
		Emoji: "🤖",
		Verb:  "Spawning",
		Format: func(input map[string]interface{}) string {
			if desc, ok := input["description"].(string); ok {
				return escapeMarkdownV2(desc)
			}
			if agentType, ok := input["subagent_type"].(string); ok {
				return escapeMarkdownV2(agentType) + " agent"
			}
			return "agent"
		},
	},
	"WebFetch": {
		Emoji: "🌐",
		Verb:  "Fetching",
		Format: func(input map[string]interface{}) string {
			if url, ok := input["url"].(string); ok {
				url = strings.TrimPrefix(url, "https://")
				url = strings.TrimPrefix(url, "http://")
				if idx := strings.Index(url, "/"); idx > 0 {
					url = url[:idx]
				}
				return escapeMarkdownV2(url)
			}
			return ""
		},
	},
	"WebSearch": {
		Emoji: "🔎",
		Verb:  "Searching",
		Format: func(input map[string]interface{}) string {
			if query, ok := input["query"].(string); ok {
				if len(query) > 40 {
					query = query[:37] + "..."
				}
				return fmt.Sprintf(`"%s"`, escapeMarkdownV2(query))
			}
			return ""
		},
	},
}

// MCP tool prefixes and their display configs
var mcpToolDisplays = map[string]toolDisplayConfig{
	"mcp__things__": {
		Emoji: "✅",
		Verb:  "Things",
		Format: func(input map[string]interface{}) string {
			if title, ok := input["title"].(string); ok {
				if len(title) > 30 {
					title = title[:27] + "..."
				}
				return escapeMarkdownV2(title)
			}
			if query, ok := input["query"].(string); ok {
				return fmt.Sprintf(`"%s"`, escapeMarkdownV2(query))
			}
			return ""
		},
	},
	"mcp__claude-in-chrome__": {
		Emoji: "🌐",
		Verb:  "Browser",
		Format: func(input map[string]interface{}) string {
			if url, ok := input["url"].(string); ok {
				url = strings.TrimPrefix(url, "https://")
				url = strings.TrimPrefix(url, "http://")
				if idx := strings.Index(url, "/"); idx > 0 {
					url = url[:idx]
				}
				return escapeMarkdownV2(url)
			}
			if action, ok := input["action"].(string); ok {
				return escapeMarkdownV2(action)
			}
			return ""
		},
	},
}

// formatToolText creates the text content of a tool notification (no emoji, no italic wrapper)
func formatToolText(tool types.ToolUse) string {
	// Check for exact tool match first
	if cfg, ok := toolDisplays[tool.Name]; ok {
		detail := ""
		if cfg.Format != nil {
			detail = cfg.Format(tool.Input)
		}
		if detail != "" {
			return fmt.Sprintf("%s %s", escapeMarkdownV2(cfg.Verb), detail)
		}
		return escapeMarkdownV2(cfg.Verb)
	}

	// Check for MCP tool prefixes
	for prefix, cfg := range mcpToolDisplays {
		if strings.HasPrefix(tool.Name, prefix) {
			operation := strings.TrimPrefix(tool.Name, prefix)
			operation = strings.ReplaceAll(operation, "_", " ")

			detail := ""
			if cfg.Format != nil {
				detail = cfg.Format(tool.Input)
			}
			if detail != "" {
				return fmt.Sprintf("%s: %s %s", escapeMarkdownV2(cfg.Verb), escapeMarkdownV2(operation), detail)
			}
			return fmt.Sprintf("%s: %s", escapeMarkdownV2(cfg.Verb), escapeMarkdownV2(operation))
		}
	}

	// Fallback for unknown tools
	return escapeMarkdownV2(tool.Name)
}

// FormatToolNotification creates a Telegram-friendly display of a tool call in progress
// Returns italic text with ◌ prefix for subtle, compact appearance
func FormatToolNotification(tool types.ToolUse) string {
	content := formatToolText(tool)
	return "_◌ " + content + "_"
}

// FormatToolNotificationSuccess creates a completed tool notification with ✓
func FormatToolNotificationSuccess(tool types.ToolUse) string {
	content := formatToolText(tool)
	return "_✓ " + content + "_"
}

// FormatToolNotificationFailure creates a failed tool notification with ✗
func FormatToolNotificationFailure(tool types.ToolUse) string {
	content := formatToolText(tool)
	return "_✗ " + content + "_"
}

// shortPath returns just the filename from a path
func shortPath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// MarkdownV2 special characters that need escaping
const markdownV2SpecialChars = `_*[]()~` + "`" + `>#+-=|{}.!`

// escapeMarkdownV2 escapes special characters for Telegram MarkdownV2
func escapeMarkdownV2(text string) string {
	var result strings.Builder
	for _, r := range text {
		if strings.ContainsRune(markdownV2SpecialChars, r) {
			result.WriteRune('\\')
		}
		result.WriteRune(r)
	}
	return result.String()
}

// escapeInlineCode escapes characters inside inline code (only ` and \)
func escapeInlineCode(text string) string {
	text = strings.ReplaceAll(text, "\\", "\\\\")
	text = strings.ReplaceAll(text, "`", "\\`")
	return text
}

// escapeCodeBlock escapes characters inside code blocks
func escapeCodeBlock(text string) string {
	text = strings.ReplaceAll(text, "\\", "\\\\")
	text = strings.ReplaceAll(text, "`", "\\`")
	return text
}

// Regex patterns for markdown elements
var (
	codeBlockRegex  = regexp.MustCompile("(?s)```([a-zA-Z]*)\\n?(.*?)```")
	inlineCodeRegex = regexp.MustCompile("`([^`]+)`")
	linkRegex       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	boldRegex       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	italicRegex     = regexp.MustCompile(`(?:^|[^*])\*([^*]+)\*(?:[^*]|$)`)
	underscoreItalicRegex = regexp.MustCompile(`_(.+?)_`)
	strikethroughRegex    = regexp.MustCompile(`~~(.+?)~~`)
)

// placeholder represents a protected element
type placeholder struct {
	key     string
	content string
}

// FormatMarkdownV2 converts standard markdown to Telegram MarkdownV2 format
func FormatMarkdownV2(text string) string {
	placeholders := make(map[string]string)
	counter := 0

	// Helper to generate unique placeholder keys
	// Using hex digits only - no special chars that need escaping
	genKey := func(prefix string) string {
		key := fmt.Sprintf("XPLACEHOLDERX%sX%dX", prefix, counter)
		counter++
		return key
	}

	// Step 1: Extract and protect code blocks
	text = codeBlockRegex.ReplaceAllStringFunc(text, func(match string) string {
		key := genKey("CB")

		parts := codeBlockRegex.FindStringSubmatch(match)
		lang := ""
		code := match
		if len(parts) >= 3 {
			lang = parts[1]
			code = parts[2]
		}

		// Format as MarkdownV2 code block
		escaped := escapeCodeBlock(code)
		if lang != "" {
			placeholders[key] = fmt.Sprintf("```%s\n%s```", lang, escaped)
		} else {
			placeholders[key] = fmt.Sprintf("```\n%s```", escaped)
		}
		return key
	})

	// Step 2: Extract and protect inline code
	text = inlineCodeRegex.ReplaceAllStringFunc(text, func(match string) string {
		key := genKey("IC")

		parts := inlineCodeRegex.FindStringSubmatch(match)
		if len(parts) >= 2 {
			escaped := escapeInlineCode(parts[1])
			placeholders[key] = fmt.Sprintf("`%s`", escaped)
		} else {
			placeholders[key] = match
		}
		return key
	})

	// Step 3: Extract and protect links
	text = linkRegex.ReplaceAllStringFunc(text, func(match string) string {
		key := genKey("LK")

		parts := linkRegex.FindStringSubmatch(match)
		if len(parts) >= 3 {
			linkText := escapeMarkdownV2(parts[1])
			linkURL := parts[2]
			// URLs in links need special escaping: only ) and \
			linkURL = strings.ReplaceAll(linkURL, "\\", "\\\\")
			linkURL = strings.ReplaceAll(linkURL, ")", "\\)")
			placeholders[key] = fmt.Sprintf("[%s](%s)", linkText, linkURL)
		} else {
			placeholders[key] = match
		}
		return key
	})

	// Step 4: Convert bold **text** to *text*
	text = boldRegex.ReplaceAllStringFunc(text, func(match string) string {
		key := genKey("BD")

		parts := boldRegex.FindStringSubmatch(match)
		if len(parts) >= 2 {
			inner := escapeMarkdownV2(parts[1])
			placeholders[key] = fmt.Sprintf("*%s*", inner)
		} else {
			placeholders[key] = match
		}
		return key
	})

	// Step 5: Convert strikethrough ~~text~~ to ~text~
	text = strikethroughRegex.ReplaceAllStringFunc(text, func(match string) string {
		key := genKey("ST")

		parts := strikethroughRegex.FindStringSubmatch(match)
		if len(parts) >= 2 {
			inner := escapeMarkdownV2(parts[1])
			placeholders[key] = fmt.Sprintf("~%s~", inner)
		} else {
			placeholders[key] = match
		}
		return key
	})

	// Step 6: Escape remaining special characters in plain text
	// The placeholder keys use Unicode private use area chars that aren't in
	// markdownV2SpecialChars, so they pass through unchanged
	text = escapeMarkdownV2(text)

	// Step 7: Restore all placeholders (keys are unchanged after escaping)
	// We need multiple passes because placeholders can be nested
	// (e.g., inline code inside bold text)
	for i := 0; i < 3; i++ { // Max 3 levels of nesting should be plenty
		prevText := text
		for key, value := range placeholders {
			text = strings.ReplaceAll(text, key, value)
		}
		// Also restore placeholders that ended up inside other placeholder values
		for key, value := range placeholders {
			newValue := value
			for innerKey, innerValue := range placeholders {
				newValue = strings.ReplaceAll(newValue, innerKey, innerValue)
			}
			if newValue != value {
				placeholders[key] = newValue
			}
		}
		if text == prevText {
			break // No more changes
		}
	}

	return strings.TrimSpace(text)
}

// FormatHTML is kept for backward compatibility but now just escapes for plain text
// Deprecated: Use FormatMarkdownV2 instead
func FormatHTML(text string) string {
	return FormatMarkdownV2(text)
}
