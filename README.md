# Aria

Go daemon that bridges Telegram to Claude Code.

Message your Telegram bot from anywhere and get AI assistance powered by Claude Code's full capabilities - file editing, web search, task management, and more.

## How It Works

```
You: What's on my calendar today?
Aria: Checking your calendar...
Aria: You've got 3 things:
      - 10am: Standup
      - 2pm: Dentist
      - 4pm: 1:1 with Sarah

You: Add "buy groceries" to my tasks
Aria: ✅ Things: add todo "buy groceries"
Aria: Done! Added to your inbox.
```

## Features

- **Persistent sessions** - Each chat maintains Claude context across restarts
- **Session resumption** - Restart Aria without losing conversation history
- **Typing indicators** - Shows "typing..." while Claude works
- **Tool notifications** - See what Claude is doing (reading files, searching, etc.)
- **Todo progress display** - Pinned messages show multi-step task progress (○ → ◐ → ●)
- **Inline keyboards** - Interactive buttons for Claude's questions
- **Self-rebuild** - `/rebuild` compiles and restarts Aria from Telegram
- **Slash commands** - All your Claude skills available as `/commands`
- **MarkdownV2 formatting** - Rich text responses

## Prerequisites

- Go 1.21+
- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and authenticated
- Telegram bot token from [@BotFather](https://t.me/botfather)

## Quick Start

1. **Create a Telegram bot:**
   - Message [@BotFather](https://t.me/botfather) on Telegram
   - Send `/newbot` and follow the prompts
   - Save the bot token

2. **Get your Telegram user ID:**
   - Message [@userinfobot](https://t.me/userinfobot) on Telegram
   - It will reply with your user ID

3. **Clone and build:**
   ```bash
   git clone https://github.com/codegangsta/aria.git
   cd aria
   make build
   ```

4. **Configure:**
   ```bash
   mkdir -p ~/.config/aria
   cat > ~/.config/aria/config.yaml << EOF
   telegram:
     token: "YOUR_BOT_TOKEN"
   allowlist:
     - YOUR_USER_ID  # e.g., 123456789
   debug: false
   log_file: "/tmp/aria.log"
   EOF
   ```

5. **Run:**
   ```bash
   ./aria
   ```

6. **Message your bot on Telegram!**

## Configuration

Config file: `~/.config/aria/config.yaml`

```yaml
telegram:
  token: "bot-token-from-botfather"

# Telegram user IDs allowed to use the bot
allowlist:
  - 123456789
  - 987654321

# Enable debug logging
debug: false

# Log file path (optional)
log_file: "/tmp/aria.log"
```

## Architecture

```
Telegram Bot API → Long polling (gotgbot) → Aria daemon
    ↓
Check allowlist (user IDs) → Get/create persistent Claude process
    ↓
Write to stdin: {"type":"user","message":{...}}
    ↓
Read stream-json responses → Format HTML → Send to Telegram
```

## Development

```bash
make build      # Build binary
make test       # Run tests
make run        # Run locally
```

## Session Management

Sessions persist across restarts in `~/.config/aria/sessions.yaml`.

**Commands:**
- `/sessions` - List active sessions with inline keyboard to switch
- `/reset` - Clear current session and start fresh
- `/rebuild` - Recompile Aria and restart (for self-development)

When Aria restarts, it automatically resumes your previous conversation using Claude's `--resume` flag. No context is lost.

## Troubleshooting

**Bot not responding:**
- Check bot token is correct
- Verify your Telegram user ID is in the allowlist
- Check logs: `tail -f /tmp/aria.log`

**Claude errors:**
- Verify Claude Code is authenticated: `claude --version`
- Test manually: `echo "hello" | claude -p`

**Session issues:**
- Check `~/.config/aria/sessions.yaml` for stored sessions
- Use `/reset` to clear a corrupted session
- Delete `sessions.yaml` to reset all sessions

## License

MIT
