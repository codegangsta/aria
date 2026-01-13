package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	// Create a temporary config file
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `
allowlist:
  - "+15551234567"
  - "test@example.com"
log_file: "/tmp/test.log"
debug: true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.Allowlist) != 2 {
		t.Errorf("Allowlist length = %d, want 2", len(cfg.Allowlist))
	}

	if cfg.Allowlist[0] != "+15551234567" {
		t.Errorf("Allowlist[0] = %q, want %q", cfg.Allowlist[0], "+15551234567")
	}

	if cfg.LogFile != "/tmp/test.log" {
		t.Errorf("LogFile = %q, want %q", cfg.LogFile, "/tmp/test.log")
	}

	if !cfg.Debug {
		t.Error("Debug = false, want true")
	}
}

func TestLoadEmptyAllowlist(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `
allowlist: []
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Error("Load() should error on empty allowlist")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Error("Load() should error on missing file")
	}
}

func TestIsAllowed(t *testing.T) {
	cfg := &Config{
		Allowlist: []string{"+15551234567", "test@example.com"},
	}

	tests := []struct {
		sender string
		want   bool
	}{
		// Exact matches
		{"+15551234567", true},
		{"test@example.com", true},
		{"+15559999999", false},
		{"other@example.com", false},
		{"", false},

		// Phone number normalization
		{"+1 555 123 4567", true},      // spaces
		{"+1-555-123-4567", true},      // dashes
		{"+1 (555) 123-4567", true},    // parentheses
		{"+1.555.123.4567", true},      // dots
		{"15551234567", false},         // without + (different from +15551234567)

		// Email case-insensitivity
		{"TEST@example.com", true},
		{"Test@Example.COM", true},
		{"TEST@EXAMPLE.COM", true},
	}

	for _, tt := range tests {
		t.Run(tt.sender, func(t *testing.T) {
			if got := cfg.IsAllowed(tt.sender); got != tt.want {
				t.Errorf("IsAllowed(%q) = %v, want %v", tt.sender, got, tt.want)
			}
		})
	}
}
