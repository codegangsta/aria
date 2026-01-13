package telegram

import (
	"fmt"
	htmlpkg "html"
	"strings"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
)

// ToolUse represents a tool call from Claude for display purposes
type ToolUse struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

// toolDisplayConfig defines how to display a specific tool
type toolDisplayConfig struct {
	Emoji    string
	Format   func(input map[string]interface{}) string
	Verb     string // e.g., "Running", "Reading", "Editing"
}

// esc escapes HTML entities in a string
func esc(s string) string {
	return htmlpkg.EscapeString(s)
}

// toolDisplays maps tool names to their display configuration
var toolDisplays = map[string]toolDisplayConfig{
	"Bash": {
		Emoji: "🔧",
		Verb:  "Running",
		Format: func(input map[string]interface{}) string {
			if cmd, ok := input["command"].(string); ok {
				// Truncate long commands
				if len(cmd) > 60 {
					cmd = cmd[:57] + "..."
				}
				return fmt.Sprintf("<code>%s</code>", esc(cmd))
			}
			return ""
		},
	},
	"Read": {
		Emoji: "📄",
		Verb:  "Reading",
		Format: func(input map[string]interface{}) string {
			if path, ok := input["file_path"].(string); ok {
				return esc(shortPath(path))
			}
			return ""
		},
	},
	"Edit": {
		Emoji: "✏️",
		Verb:  "Editing",
		Format: func(input map[string]interface{}) string {
			if path, ok := input["file_path"].(string); ok {
				return esc(shortPath(path))
			}
			return ""
		},
	},
	"Write": {
		Emoji: "📝",
		Verb:  "Writing",
		Format: func(input map[string]interface{}) string {
			if path, ok := input["file_path"].(string); ok {
				return esc(shortPath(path))
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
				return fmt.Sprintf("<code>%s</code>", esc(pattern))
			}
			return ""
		},
	},
	"Glob": {
		Emoji: "📂",
		Verb:  "Finding",
		Format: func(input map[string]interface{}) string {
			if pattern, ok := input["pattern"].(string); ok {
				return fmt.Sprintf("<code>%s</code>", esc(pattern))
			}
			return ""
		},
	},
	"Task": {
		Emoji: "🤖",
		Verb:  "Spawning",
		Format: func(input map[string]interface{}) string {
			if desc, ok := input["description"].(string); ok {
				return esc(desc)
			}
			if agentType, ok := input["subagent_type"].(string); ok {
				return esc(agentType) + " agent"
			}
			return "agent"
		},
	},
	"WebFetch": {
		Emoji: "🌐",
		Verb:  "Fetching",
		Format: func(input map[string]interface{}) string {
			if url, ok := input["url"].(string); ok {
				// Show just domain
				url = strings.TrimPrefix(url, "https://")
				url = strings.TrimPrefix(url, "http://")
				if idx := strings.Index(url, "/"); idx > 0 {
					url = url[:idx]
				}
				return esc(url)
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
				return fmt.Sprintf(`"%s"`, esc(query))
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
			// Try to extract meaningful info from common Things operations
			if title, ok := input["title"].(string); ok {
				if len(title) > 30 {
					title = title[:27] + "..."
				}
				return esc(title)
			}
			if query, ok := input["query"].(string); ok {
				return fmt.Sprintf(`"%s"`, esc(query))
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
				return esc(url)
			}
			if action, ok := input["action"].(string); ok {
				return esc(action)
			}
			return ""
		},
	},
}

// FormatToolNotification creates a Telegram-friendly display of a tool call
func FormatToolNotification(tool ToolUse) string {
	// Check for exact tool match first
	if cfg, ok := toolDisplays[tool.Name]; ok {
		detail := ""
		if cfg.Format != nil {
			detail = cfg.Format(tool.Input)
		}
		if detail != "" {
			return fmt.Sprintf("%s %s %s", cfg.Emoji, cfg.Verb, detail)
		}
		return fmt.Sprintf("%s %s", cfg.Emoji, cfg.Verb)
	}

	// Check for MCP tool prefixes
	for prefix, cfg := range mcpToolDisplays {
		if strings.HasPrefix(tool.Name, prefix) {
			// Extract the operation name after the prefix
			operation := strings.TrimPrefix(tool.Name, prefix)
			operation = strings.ReplaceAll(operation, "_", " ")

			detail := ""
			if cfg.Format != nil {
				detail = cfg.Format(tool.Input)
			}
			if detail != "" {
				return fmt.Sprintf("%s %s: %s %s", cfg.Emoji, cfg.Verb, operation, detail)
			}
			return fmt.Sprintf("%s %s: %s", cfg.Emoji, cfg.Verb, operation)
		}
	}

	// Fallback for unknown tools
	return fmt.Sprintf("⚙️ %s", tool.Name)
}

// shortPath returns just the filename from a path
func shortPath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// FormatHTML converts markdown text to Telegram-compatible HTML.
// Telegram supports a subset of HTML: <b>, <i>, <code>, <pre>, <a>.
func FormatHTML(text string) string {
	// Create parser with common extensions
	extensions := parser.CommonExtensions | parser.AutoHeadingIDs
	p := parser.NewWithExtensions(extensions)

	// Create HTML renderer configured for Telegram's subset
	opts := html.RendererOptions{
		Flags: html.CommonFlags | html.SkipHTML,
	}
	renderer := html.NewRenderer(opts)

	// Convert markdown to HTML
	doc := p.Parse([]byte(text))
	output := markdown.Render(doc, renderer)

	return string(output)
}
