package claude

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SessionInfo represents discovered session metadata
type SessionInfo struct {
	ID          string    // Full UUID of the session
	ShortID     string    // First 8 chars for callback data
	ProjectPath string    // Decoded project path (e.g., /Users/jeremy/code/aria)
	ProjectName string    // Short name (e.g., "aria")
	Summary     string    // Topic from summary entry
	LastActive  time.Time // Timestamp of last entry
}

// SessionDiscovery handles finding and parsing Claude sessions
type SessionDiscovery struct {
	claudeDir    string
	logger       *slog.Logger
	lastSessions []SessionInfo // Cache of last discovered sessions for lookup
}

// NewSessionDiscovery creates a new session discovery instance
func NewSessionDiscovery(claudeDir string, logger *slog.Logger) *SessionDiscovery {
	return &SessionDiscovery{
		claudeDir: claudeDir,
		logger:    logger,
	}
}

// DiscoverSessions finds recent sessions across all projects
func (d *SessionDiscovery) DiscoverSessions(limit int) ([]SessionInfo, error) {
	projectsDir := filepath.Join(d.claudeDir, "projects")

	// Find all project directories
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No sessions yet
		}
		return nil, err
	}

	var sessions []SessionInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		projectDir := filepath.Join(projectsDir, entry.Name())
		projectPath := decodeProjectPath(entry.Name())
		projectName := filepath.Base(projectPath)

		// Find session files in this project
		sessionFiles, err := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
		if err != nil {
			d.logger.Warn("error globbing session files", "dir", projectDir, "error", err)
			continue
		}

		for _, sessionFile := range sessionFiles {
			session, err := d.parseSessionFile(sessionFile, projectPath, projectName)
			if err != nil {
				d.logger.Debug("skipping session file", "file", sessionFile, "error", err)
				continue
			}
			sessions = append(sessions, *session)
		}
	}

	// Sort by last active (most recent first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActive.After(sessions[j].LastActive)
	})

	// Limit results
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}

	// Cache for lookup
	d.lastSessions = sessions

	return sessions, nil
}

// LookupSessionByShortID finds a session by its short ID prefix
func (d *SessionDiscovery) LookupSessionByShortID(shortID string) *SessionInfo {
	for i := range d.lastSessions {
		if d.lastSessions[i].ShortID == shortID {
			return &d.lastSessions[i]
		}
	}
	return nil
}

// GetLastAssistantMessage returns the last assistant message from a session
func (d *SessionDiscovery) GetLastAssistantMessage(sessionID string) string {
	// Find the session file
	projectsDir := filepath.Join(d.claudeDir, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}

	// Search all project directories for this session
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionPath := filepath.Join(projectsDir, entry.Name(), sessionID+".jsonl")
		if msg := d.parseLastAssistantMessage(sessionPath); msg != "" {
			return msg
		}
	}
	return ""
}

func (d *SessionDiscovery) parseLastAssistantMessage(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 4*1024*1024)

	var lastAssistantMsg string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}

		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		if entry.Type == "assistant" && entry.Message.Role == "assistant" {
			// Content can be string or array of content blocks
			var textContent string

			// Try as string first
			if err := json.Unmarshal(entry.Message.Content, &textContent); err == nil {
				if textContent != "" {
					lastAssistantMsg = textContent
				}
				continue
			}

			// Try as array of content blocks
			var contentBlocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(entry.Message.Content, &contentBlocks); err == nil {
				for _, block := range contentBlocks {
					if block.Type == "text" && block.Text != "" {
						lastAssistantMsg = block.Text
						break
					}
				}
			}
		}
	}

	return lastAssistantMsg
}

// parseSessionFile extracts metadata from a session JSONL file
func (d *SessionDiscovery) parseSessionFile(path, projectPath, projectName string) (*SessionInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Extract session ID from filename
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")

	session := &SessionInfo{
		ID:          sessionID,
		ShortID:     sessionID[:min(8, len(sessionID))],
		ProjectPath: projectPath,
		ProjectName: projectName,
	}

	scanner := bufio.NewScanner(file)
	// Increase buffer for large JSON lines (some can be very large)
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 4*1024*1024)

	var lastTimestamp time.Time
	var firstUserContent string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry struct {
			Type      string `json:"type"`
			Summary   string `json:"summary"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		}

		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		// Capture summary from summary entry (preferred)
		if entry.Type == "summary" && session.Summary == "" {
			session.Summary = entry.Summary
		}

		// Capture first user message as fallback for summary
		if entry.Type == "user" && entry.Message.Role == "user" && firstUserContent == "" {
			content := entry.Message.Content
			// Extract actual content - skip command tags
			if idx := strings.Index(content, "<command-args>"); idx != -1 {
				start := idx + len("<command-args>")
				if end := strings.Index(content[start:], "</command-args>"); end != -1 {
					content = content[start : start+end]
				}
			}
			// Clean up and truncate
			content = strings.TrimSpace(content)
			if len(content) > 60 {
				content = content[:57] + "..."
			}
			if content != "" {
				firstUserContent = content
			}
		}

		// Track latest timestamp
		if entry.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
				if t.After(lastTimestamp) {
					lastTimestamp = t
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	session.LastActive = lastTimestamp

	// Use first user message as fallback if no summary
	if session.Summary == "" && firstUserContent != "" {
		session.Summary = firstUserContent
	}

	// Skip sessions with no activity
	if session.LastActive.IsZero() {
		return nil, os.ErrNotExist
	}

	return session, nil
}

// decodeProjectPath converts encoded directory name to path
// e.g., "-Users-jeremy-code-aria" -> "/Users/jeremy/code/aria"
func decodeProjectPath(encoded string) string {
	// Replace leading dash with slash, then all dashes with slashes
	if strings.HasPrefix(encoded, "-") {
		encoded = "/" + encoded[1:]
	}
	return strings.ReplaceAll(encoded, "-", "/")
}

// FormatTimeAgo returns a human-readable relative time
func FormatTimeAgo(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		return formatNumber(mins) + "m"
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		return formatNumber(hours) + "h"
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		return formatNumber(days) + "d"
	default:
		weeks := int(diff.Hours() / 24 / 7)
		return formatNumber(weeks) + "w"
	}
}

func formatNumber(n int) string {
	return strconv.Itoa(n)
}

// TruncateWithEllipsis truncates a string and adds ellipsis if needed
func TruncateWithEllipsis(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
