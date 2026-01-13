# Aria Development

Go daemon bridging iMessage to Claude Code.

## Architecture

```
iMessage → SQLite watch (fsnotify) → Aria daemon
    ↓
Check allowlist → Get/create persistent Claude process for chat
    ↓
Write to stdin: {"type":"user","message":{"role":"user","content":"/aria <message>"}}
    ↓
Read stream-json responses → Send via AppleScript → iMessage
```

## Directory Structure

```
aria/
├── cmd/aria/main.go        # Entry point, wires everything together
├── internal/
│   ├── config/             # YAML config loading, allowlist
│   ├── watcher/            # SQLite + fsnotify message watching
│   ├── sender/             # AppleScript iMessage sending
│   ├── claude/             # Claude Code CLI streaming
│   └── notify/             # macOS notifications
├── scripts/
│   ├── install.sh          # Install daemon
│   └── uninstall.sh        # Remove daemon
├── launchd/                # launchd plist template
├── config.example.yaml     # Example configuration
└── Makefile
```

## Key Commands

```bash
make build      # Build binary
make test       # Run tests
make install    # Install as daemon
make uninstall  # Remove daemon
make run        # Run locally for development
make logs       # Tail daemon logs
make status     # Check if daemon is running
make restart    # Restart the daemon
```

## Dependencies

- `github.com/mattn/go-sqlite3` - SQLite for reading Messages database
- `github.com/fsnotify/fsnotify` - File watching for database changes
- `claude` - Claude Code CLI

## Configuration

Config lives at `~/.config/aria/config.yaml`:

```yaml
allowlist:
  - "+15551234567"    # Phone numbers in E.164 format
  - "friend@icloud.com"
debug: false
```

## How It Works

1. **Watcher** uses fsnotify to detect chat.db changes, queries SQLite for new messages
2. **Allowlist check** - ignores messages from non-allowed senders
3. **Process management** - each chat_id gets a persistent Claude process via ProcessManager
4. **Claude streaming** - sends messages via stdin (stream-json), reads responses from stdout
5. **Message sending** - uses AppleScript to send via Messages.app

## The /aria Skill

Every prompt is prepended with `/aria` to load the skill from `~/.claude/skills/aria/SKILL.md`. This tells Claude to:
- Acknowledge before using tools ("Checking your tasks...")
- Keep responses brief and iMessage-friendly
- Use casual, direct tone

## Testing Locally

```bash
# Build and run with config
./aria --config ~/.config/aria/config.yaml

# In another terminal, watch logs
tail -f /tmp/aria.log

# Send yourself an iMessage to test
```

## Permissions Required

- **Full Disk Access** for aria binary (to read Messages database directly)
- **Automation** permission for Messages.app (for sending via AppleScript)

## Troubleshooting

**Messages not being received:**
- Check Full Disk Access in System Settings for the aria binary
- Test SQLite access: `sqlite3 ~/Library/Messages/chat.db "SELECT COUNT(*) FROM message"`

**Messages not being sent:**
- Check Automation permission for Messages.app
- Test AppleScript manually: `osascript -e 'tell application "Messages" to get name'`

**Claude errors:**
- Check Claude Code is authenticated: `claude --version`
- Test manually: `echo "hello" | claude -p`
