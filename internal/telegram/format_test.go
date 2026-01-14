package telegram

import (
	"strings"
	"testing"
)

func TestFormatMarkdownV2(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		notContains []string
	}{
		{
			name:     "plain text with special chars",
			input:    "Hello! How are you?",
			contains: []string{"Hello\\!", "How are you?"}, // ? is not a special char in MarkdownV2
		},
		{
			name:     "bold text",
			input:    "This is **bold** text",
			contains: []string{"*bold*"},
			notContains: []string{"**"},
		},
		{
			name:     "inline code",
			input:    "Run `go build` to compile",
			contains: []string{"`go build`"},
		},
		{
			name:  "code block",
			input: "Example:\n```go\nfunc main() {}\n```",
			contains: []string{
				"```go",
				"func main",
				"```",
			},
		},
		{
			name:     "link",
			input:    "See [docs](https://example.com)",
			contains: []string{"[docs]", "(https://example.com)"},
		},
		{
			name:     "special chars escaped",
			input:    "Use foo.bar and test-case",
			contains: []string{"foo\\.bar", "test\\-case"},
		},
		{
			name:     "strikethrough",
			input:    "This is ~~deleted~~ text",
			contains: []string{"~deleted~"},
			notContains: []string{"~~"},
		},
		{
			name:     "inline code with func keyword",
			input:    "Found `func main` in the code",
			contains: []string{"`func main`"},
			notContains: []string{"PLACEHOLDER", "XPLACEHOLDER"},
		},
		{
			name:     "multiple inline code blocks",
			input:    "Use `foo` and `bar` together",
			contains: []string{"`foo`", "`bar`"},
			notContains: []string{"PLACEHOLDER"},
		},
		{
			name:     "bold inside numbered list",
			input:    "1. **func main** - entry point",
			contains: []string{"*func main*"},
			notContains: []string{"PLACEHOLDER", "**"},
		},
		{
			name:     "mixed formatting no placeholder leak",
			input:    "Check `error` in **bold** with [link](http://x.com)",
			contains: []string{"`error`", "*bold*", "["},
			notContains: []string{"PLACEHOLDER"},
		},
		{
			name:  "numbered list with inline code",
			input: "**1. `func main`** - Entry point in cmd/aria/main.go:26",
			contains: []string{"`func main`"},
			notContains: []string{"PLACEHOLDER"},
		},
		{
			name:  "exact failing case from production",
			input: "Done! Here's what I found:\n\n**1. `func main`** - Entry point in `cmd/aria/main.go:26`, plus test examples\n\n**2. `TODO`** - Just one",
			contains: []string{"`func main`", "`TODO`", "`cmd/aria/main.go:26`"},
			notContains: []string{"PLACEHOLDER"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatMarkdownV2(tt.input)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatMarkdownV2(%q)\ngot:  %q\nmissing: %q", tt.input, got, want)
				}
			}
			for _, notWant := range tt.notContains {
				if strings.Contains(got, notWant) {
					t.Errorf("FormatMarkdownV2(%q)\ngot:  %q\nshould not contain: %q", tt.input, got, notWant)
				}
			}
		})
	}
}

func TestEscapeMarkdownV2(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"hello.world", "hello\\.world"},
		{"test!", "test\\!"},
		{"foo-bar", "foo\\-bar"},
		{"(parens)", "\\(parens\\)"},
		{"[brackets]", "\\[brackets\\]"},
		{"a_b*c", "a\\_b\\*c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeMarkdownV2(tt.input)
			if got != tt.expected {
				t.Errorf("escapeMarkdownV2(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
