// Package commands provides a router for handling Aria slash commands
package commands

import (
	"context"
	"strings"
)

// Response represents the result of executing a command
type Response struct {
	Text   string
	Silent bool // If true, don't play notification sound
}

// Command defines the interface for a slash command
type Command interface {
	// Name returns the command name without the slash (e.g., "clear")
	Name() string
	// Execute runs the command and returns a response
	Execute(ctx context.Context, chatID int64, args string) (*Response, error)
}

// Router dispatches commands to their handlers
type Router struct {
	commands map[string]Command
}

// NewRouter creates a new command router
func NewRouter() *Router {
	return &Router{
		commands: make(map[string]Command),
	}
}

// Register adds a command to the router
func (r *Router) Register(cmd Command) {
	r.commands[cmd.Name()] = cmd
}

// Lookup returns the command for a given name, or nil if not found
func (r *Router) Lookup(name string) Command {
	// Normalize: remove leading slash, convert underscores to hyphens
	name = strings.TrimPrefix(name, "/")
	name = strings.ReplaceAll(name, "_", "-")
	return r.commands[name]
}

// ParseCommand extracts the command name and args from a message
// Returns empty string if not a command
func ParseCommand(text string) (name string, args string) {
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	parts := strings.SplitN(text, " ", 2)
	name = strings.TrimPrefix(parts[0], "/")
	name = strings.ReplaceAll(name, "_", "-")
	if len(parts) > 1 {
		args = parts[1]
	}
	return name, args
}
