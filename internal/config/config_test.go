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
telegram:
  token: "test-bot-token"
allowlist:
  - 123456789
  - 987654321
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

	if cfg.Allowlist[0] != 123456789 {
		t.Errorf("Allowlist[0] = %d, want %d", cfg.Allowlist[0], 123456789)
	}

	if cfg.Telegram.Token != "test-bot-token" {
		t.Errorf("Telegram.Token = %q, want %q", cfg.Telegram.Token, "test-bot-token")
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
telegram:
  token: "test-bot-token"
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

func TestLoadMissingToken(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `
allowlist:
  - 123456789
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Error("Load() should error on missing telegram token")
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
		Allowlist: []int64{123456789, 987654321},
	}

	tests := []struct {
		name   string
		userID int64
		want   bool
	}{
		{"allowed user 1", 123456789, true},
		{"allowed user 2", 987654321, true},
		{"not allowed", 111111111, false},
		{"zero", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfg.IsAllowed(tt.userID); got != tt.want {
				t.Errorf("IsAllowed(%d) = %v, want %v", tt.userID, got, tt.want)
			}
		})
	}
}
