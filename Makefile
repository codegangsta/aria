.PHONY: build test clean run logs install uninstall restart

# Build the aria binary
build:
	go build -o aria ./cmd/aria

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

# Install launchd service
install:
	cp com.codegangsta.aria.plist ~/Library/LaunchAgents/
	launchctl load ~/Library/LaunchAgents/com.codegangsta.aria.plist

# Uninstall launchd service
uninstall:
	launchctl unload ~/Library/LaunchAgents/com.codegangsta.aria.plist 2>/dev/null || true
	rm -f ~/Library/LaunchAgents/com.codegangsta.aria.plist

# Restart service (for rebuilds)
restart:
	launchctl stop com.codegangsta.aria 2>/dev/null || true
	@sleep 1
	@echo "Service restarting via launchd..."
