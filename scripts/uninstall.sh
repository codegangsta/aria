#!/bin/bash
set -e

echo "Uninstalling Aria..."

PLIST="$HOME/Library/LaunchAgents/com.codegangsta.aria.plist"

# Stop the daemon
if [ -f "$PLIST" ]; then
    echo "Stopping daemon..."
    launchctl unload "$PLIST" 2>/dev/null || true
    rm "$PLIST"
fi

# Remove binary
if [ -f "$HOME/.local/bin/aria" ]; then
    echo "Removing binary..."
    rm "$HOME/.local/bin/aria"
fi

echo ""
echo "Aria uninstalled."
echo ""
echo "Config preserved at: ~/.config/aria/config.yaml"
echo "To remove config: rm -rf ~/.config/aria"
