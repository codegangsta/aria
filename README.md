# Aria

Personal AI assistant via iMessage, powered by Claude Code.

Text Aria from your phone and get AI assistance with your tasks, calendar, email, and more.

## How It Works

Aria is a daemon that watches for incoming iMessages and responds using Claude Code. Each conversation maintains context through Claude's session system, so you can have natural back-and-forth conversations.

```
You: What's on my calendar today?
Aria: Checking your calendar...
Aria: You've got 3 things:
      - 10am: Standup
      - 2pm: Dentist
      - 4pm: 1:1 with Sarah

You: Reschedule the dentist to tomorrow
Aria: On it...
Aria: Done! Moved dentist to tomorrow at 2pm.
```

## Prerequisites

- macOS 14+
- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and authenticated
- Full Disk Access permission (for reading Messages database)
- Automation permission for Messages.app (for sending)

## Quick Start

1. **Clone the repo:**
   ```bash
   git clone https://github.com/codegangsta/aria.git
   cd aria
   ```

2. **Install:**
   ```bash
   make install
   ```

3. **Configure your allowlist:**
   ```bash
   # Edit the config to add your phone number
   nano ~/.config/aria/config.yaml
   ```

   ```yaml
   allowlist:
     - "+15551234567"  # Your phone number
   ```

4. **Grant permissions:**
   - System Settings → Privacy & Security → Full Disk Access → Add `aria`
   - When prompted, allow Automation access for Messages.app

5. **Restart the daemon:**
   ```bash
   make restart
   ```

6. **Text yourself!**

## Configuration

Config file: `~/.config/aria/config.yaml`

```yaml
# Phone numbers and emails that can trigger responses
# Use E.164 format for phone numbers (+1 for US)
allowlist:
  - "+15551234567"
  - "friend@icloud.com"

# Enable debug logging
debug: false
```

## Commands

```bash
make install    # Install and start daemon
make uninstall  # Stop and remove daemon
make logs       # View daemon logs
make status     # Check if daemon is running
make restart    # Restart daemon
```

## Capabilities

Aria has access to:

- **Things 3** - View and manage tasks
- **Calendar** - Check schedule, create events
- **Mail** - Read and manage email
- **Web search** - Look up information

## Troubleshooting

### Not receiving messages

1. Check Full Disk Access is granted:
   - System Settings → Privacy & Security → Full Disk Access
   - Ensure `aria` (or Terminal if running manually) is listed and enabled

2. Verify imsg works:
   ```bash
   ~/.local/bin/imsg watch --json
   ```
   Send yourself a message - you should see JSON output.

### Not sending messages

1. Check Automation permission:
   - System Settings → Privacy & Security → Automation
   - Ensure Messages.app is enabled for aria/Terminal

2. Test manually:
   ```bash
   ~/.local/bin/imsg send --to "+15551234567" --text "test"
   ```

### Claude errors

1. Verify Claude Code is working:
   ```bash
   echo "hello" | claude -p
   ```

2. Check logs for details:
   ```bash
   make logs
   ```

### View logs

```bash
# Daemon output
tail -f /tmp/aria.log

# Daemon errors
tail -f /tmp/aria.err
```

## Uninstall

```bash
make uninstall
```

This removes the daemon and binary but preserves your config at `~/.config/aria/`.

To fully remove:
```bash
rm -rf ~/.config/aria
```

## License

MIT
