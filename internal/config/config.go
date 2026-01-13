package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// TelegramConfig holds Telegram-specific settings
type TelegramConfig struct {
	Token string `yaml:"token"` // Bot token from @BotFather
}

// ClaudeConfig holds Claude CLI settings
type ClaudeConfig struct {
	SkipPermissions bool `yaml:"skip_permissions"` // pass --dangerously-skip-permissions to Claude
}

// Config holds the Aria configuration
type Config struct {
	Telegram  TelegramConfig `yaml:"telegram"`
	Claude    ClaudeConfig   `yaml:"claude"`
	Allowlist []int64        `yaml:"allowlist"` // Telegram user IDs allowed to use the bot
	LogFile   string         `yaml:"log_file"`  // path to log file
	Debug     bool           `yaml:"debug"`     // enable debug logging
}

// Load reads and parses the config file from the given path
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if cfg.Telegram.Token == "" {
		return nil, fmt.Errorf("telegram.token is required")
	}

	if len(cfg.Allowlist) == 0 {
		return nil, fmt.Errorf("allowlist cannot be empty")
	}

	return &cfg, nil
}

// IsAllowed checks if the given Telegram user ID is in the allowlist
func (c *Config) IsAllowed(userID int64) bool {
	for _, allowed := range c.Allowlist {
		if allowed == userID {
			return true
		}
	}
	return false
}
