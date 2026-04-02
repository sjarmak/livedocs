.PHONY: build docker-build test test-race lint clean

BINARY   := livedocs
CMD_DIR  := ./cmd/livedocs
IMAGE    := livedocs

# Default: CGO build (tree-sitter requires CGO).
build:
	CGO_ENABLED=1 go build -o $(BINARY) $(CMD_DIR)

# Build inside Docker (no local gcc needed).
docker-build:
	docker build -t $(IMAGE) .

# Run unit tests.
test:
	CGO_ENABLED=1 go test ./...

# Run tests with race detector.
test-race:
	CGO_ENABLED=1 go test -race ./...

# Static analysis.
lint:
	go vet ./...

# Remove build artifacts.
clean:
	rm -f $(BINARY)
	go clean -cache -testcache
