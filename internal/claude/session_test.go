package claude

import (
	"regexp"
	"testing"
)

// uuidRegex matches valid UUID format
var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestSessionID(t *testing.T) {
	tests := []struct {
		chatID int
	}{
		{1},
		{12345},
		{999999999},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := SessionID(tt.chatID)

			// Should be valid UUID format
			if !uuidRegex.MatchString(got) {
				t.Errorf("SessionID(%d) = %q, should be valid UUID format", tt.chatID, got)
			}

			// Should be deterministic
			got2 := SessionID(tt.chatID)
			if got != got2 {
				t.Errorf("SessionID(%d) not deterministic: %q != %q", tt.chatID, got, got2)
			}
		})
	}
}

func TestSessionIDFromString(t *testing.T) {
	tests := []struct {
		identifier string
	}{
		{"simple"},
		{"with spaces and special!@#$%"},
		{"verylongidentifierthatexceedstwentycharacters"},
		{"+15551234567"},
		{"test@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.identifier, func(t *testing.T) {
			got := SessionIDFromString(tt.identifier)

			// Should be valid UUID format
			if !uuidRegex.MatchString(got) {
				t.Errorf("SessionIDFromString(%q) = %q, should be valid UUID format", tt.identifier, got)
			}

			// Should be deterministic
			got2 := SessionIDFromString(tt.identifier)
			if got != got2 {
				t.Errorf("SessionIDFromString(%q) not deterministic: %q != %q", tt.identifier, got, got2)
			}
		})
	}
}

func TestDifferentChatIDsGetDifferentSessions(t *testing.T) {
	session1 := SessionID(1)
	session2 := SessionID(2)

	if session1 == session2 {
		t.Errorf("Different chat IDs should get different sessions: %q == %q", session1, session2)
	}
}
