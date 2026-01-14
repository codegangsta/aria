# Aria Development

Go daemon bridging Telegram to Claude Code.

## Architecture

```
Telegram Bot API → Long polling (gotgbot) → Aria daemon
    ↓
Check allowlist (user IDs) → Get/create persistent Claude process for chat
    ↓
Write to stdin: {"type":"user","message":{"role":"user","content":"/aria <message>"}}
    ↓
Read stream-json responses → Send to Telegram with HTML formatting
```

## Directory Structure

```
aria/
├── cmd/aria/main.go        # Entry point, wires everything together
├── internal/
│   ├── config/             # YAML config loading, allowlist
│   ├── telegram/           # Telegram bot (gotgbot), message formatting
│   └── claude/             # Claude Code CLI streaming, process management
└── Makefile
```

## Key Commands

```bash
make build      # Build binary
make test       # Run tests
make run        # Run locally for development
```

## Dependencies

- `github.com/PaulSonOfLars/gotgbot/v2` - Telegram Bot API
- `gopkg.in/yaml.v3` - YAML config parsing
- `claude` - Claude Code CLI

## Configuration

Config lives at `~/.config/aria/config.yaml`:

```yaml
telegram:
  token: "bot-token-from-botfather"
allowlist:
  - 123456789    # Telegram user IDs
debug: false
log_file: "/tmp/aria.log"  # optional
```

## How It Works

1. **Telegram bot** uses gotgbot long-polling to receive messages
2. **Allowlist check** - ignores messages from non-allowed user IDs
3. **Process management** - each chat_id gets a persistent Claude process via ProcessManager
4. **Claude streaming** - sends messages via stdin (stream-json), reads responses from stdout
5. **Message sending** - formats responses as HTML, sends to Telegram with typing indicators

## The /aria Skill

Every prompt is prepended with `/aria` to load the skill from `~/.claude/skills/aria/SKILL.md`. This tells Claude to:
- Acknowledge before using tools ("Checking your tasks...")
- Keep responses brief and messaging-friendly
- Use casual, direct tone

## Features

- **Typing indicators** - Shows "typing..." while Claude processes
- **Tool notifications** - Brief messages when Claude uses tools
- **Dynamic commands** - Slash commands auto-registered from Claude's skills
- **Silent commands** - Commands like `/compact` send confirmations without Claude response
- **HTML formatting** - Markdown converted to Telegram-compatible HTML

## Testing Locally

```bash
# Build and run with config
./aria --config ~/.config/aria/config.yaml

# In another terminal, watch logs
tail -f /tmp/aria.log

# Message your bot on Telegram to test
```

## Troubleshooting

**Bot not responding:**
- Check bot token is correct
- Verify your Telegram user ID is in the allowlist
- Check logs for errors

**Claude errors:**
- Check Claude Code is authenticated: `claude --version`
- Test manually: `echo "hello" | claude -p`

## Self-Development Workflow

When working on Aria's own codebase (i.e., when Claude is modifying Aria):

### Making Changes

1. **Edit code** - Make changes to Go files as needed
2. **Build** - Run `make build` to compile
3. **Ask for restart** - Tell the user to run `make restart` (don't attempt to restart yourself)

### Session Persistence

Sessions survive restarts via `~/.config/aria/sessions.yaml`:
- Each chat_id maps to a Claude session_id
- On restart, Aria resumes the previous session with `--resume`
- Use `/sessions` command to view/manage active sessions
- Use `/reset` to clear session and start fresh

### Testing Changes

```bash
# Build and test locally
make build
./aria --config config.example.yaml

# Or rebuild the daemon
make build
make restart

# Watch logs
make logs
```

### After Rebuilding

When Aria rebuilds itself:
1. The build completes
2. Aria tells the user: "Rebuilt! Run `make restart` to pick up changes."
3. User restarts, session resumes automatically
4. Aria continues with full context from before the restart
