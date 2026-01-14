.PHONY: build test clean run logs

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
