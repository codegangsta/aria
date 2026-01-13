package telegram

import (
	"strings"
	"testing"
)

func TestFormatHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:     "plain text",
			input:    "Hello! How are you?",
			contains: []string{"Hello! How are you?"},
		},
		{
			name:     "bold text",
			input:    "This is **bold** text",
			contains: []string{"<strong>bold</strong>"},
		},
		{
			name:     "italic text",
			input:    "This is *italic* text",
			contains: []string{"<em>italic</em>"},
		},
		{
			name:     "inline code",
			input:    "Run `go build` to compile",
			contains: []string{"<code>go build</code>"},
		},
		{
			name:  "code block",
			input: "Example:\n```go\nfunc main() {}\n```",
			contains: []string{
				"<pre><code class=\"language-go\">",
				"func main()",
			},
		},
		{
			name:     "link",
			input:    "See [link](https://example.com)",
			contains: []string{"<a href=\"https://example.com\">link</a>"},
		},
		{
			name:     "bullet list",
			input:    "Items:\n\n- one\n- two\n- three",
			contains: []string{"<li>one</li>", "<li>two</li>", "<li>three</li>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatHTML(tt.input)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatHTML(%q)\ngot:  %q\nmissing: %q", tt.input, got, want)
				}
			}
		})
	}
}
