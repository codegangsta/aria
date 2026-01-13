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
