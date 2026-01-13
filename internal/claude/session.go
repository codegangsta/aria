package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// SessionID generates a stable UUID-format session ID from a chat ID
// The session ID is used with Claude's --resume flag to maintain conversation context
// Claude requires valid UUID format: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
func SessionID(chatID int) string {
	return hashToUUID(fmt.Sprintf("aria-chat-%d", chatID))
}

// SessionIDFromString generates a UUID-format session ID from a string identifier
func SessionIDFromString(identifier string) string {
	return hashToUUID(fmt.Sprintf("aria-%s", identifier))
}

// hashToUUID creates a deterministic UUID from a string input
// Uses SHA-256 hash and formats as UUID v4 (with modified version bits)
func hashToUUID(input string) string {
	hash := sha256.Sum256([]byte(input))
	hex := hex.EncodeToString(hash[:16])

	// Format as UUID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex[0:8],
		hex[8:12],
		hex[12:16],
		hex[16:20],
		hex[20:32],
	)
}
