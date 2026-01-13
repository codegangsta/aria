.PHONY: build install uninstall test clean run logs

# Build the aria binary
build:
	go build -o aria ./cmd/aria

# Install aria as a launchd daemon
install:
	./scripts/install.sh

# Uninstall aria
uninstall:
	./scripts/uninstall.sh

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -f aria
	go clean

# Run aria locally (for development)
run: build
	./aria --config config.example.yaml

# Tail the logs
logs:
	tail -f /tmp/aria.log

# Check daemon status
status:
	@launchctl list | grep aria || echo "Aria is not running"

# Restart the daemon
restart:
	launchctl unload ~/Library/LaunchAgents/com.codegangsta.aria.plist 2>/dev/null || true
	launchctl load ~/Library/LaunchAgents/com.codegangsta.aria.plist
