#!/bin/bash
set -e

echo "Installing Aria..."

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Build
echo "Building..."
cd "$PROJECT_DIR"
go build -o aria ./cmd/aria

# Create config directory
CONFIG_DIR="$HOME/.config/aria"
mkdir -p "$CONFIG_DIR"

# Copy example config if no config exists
if [ ! -f "$CONFIG_DIR/config.yaml" ]; then
    echo "Creating config file..."
    cp "$PROJECT_DIR/config.example.yaml" "$CONFIG_DIR/config.yaml"
    echo "Please edit $CONFIG_DIR/config.yaml to add your allowlist"
fi

# Install binary
echo "Installing binary to ~/.local/bin..."
mkdir -p "$HOME/.local/bin"
cp aria "$HOME/.local/bin/aria"
chmod +x "$HOME/.local/bin/aria"

# Install launchd plist
echo "Installing launchd plist..."
PLIST_SRC="$PROJECT_DIR/launchd/com.codegangsta.aria.plist"
PLIST_DST="$HOME/Library/LaunchAgents/com.codegangsta.aria.plist"

# Replace $HOME in plist with actual path
sed "s|\$HOME|$HOME|g" "$PLIST_SRC" > "$PLIST_DST"

# Also update the binary path to use ~/.local/bin
sed -i '' "s|/usr/local/bin/aria|$HOME/.local/bin/aria|g" "$PLIST_DST"

# Unload if already loaded
launchctl unload "$PLIST_DST" 2>/dev/null || true

# Load the daemon
echo "Starting daemon..."
launchctl load "$PLIST_DST"

echo ""
echo "Aria installed successfully!"
echo ""
echo "Next steps:"
echo "1. Edit ~/.config/aria/config.yaml to add allowed contacts"
echo "2. Grant Full Disk Access to aria in System Settings"
echo "3. View logs: tail -f /tmp/aria.log"
echo ""
echo "To uninstall: make uninstall"
