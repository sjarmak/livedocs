# Live Docs

## Purpose

Tools and workflows that keep repository documentation automatically up to date with every commit. Extracts structural claims (exports, types, signatures, dependencies, interface implementations) from source code into per-repo SQLite databases, then serves them to AI coding agents via MCP or static Markdown files.

## Architecture

- **Extraction pipeline** (`extractor/`, `extract/`, `pipeline/`) — tree-sitter-based symbol extraction producing per-repo `.claims.db` files
- **Claims database** (`db/`) — SQLite-backed storage for symbols and claims with cross-repo xref index
- **Renderer** (`renderer/`) — transforms claims into compact Markdown (interfaces, deps, function categories)
- **MCP server** (`mcpserver/`) — Model Context Protocol server exposing claims via 8 tools (single-DB and multi-repo modes)
- **CLI** (`cmd/livedocs/`) — `livedocs` binary with `init`, `extract`, `mcp`, `context`, `check`, `diff`, `export`, `verify` commands
- **Drift detection** (`drift/`, `anchor/`) — compares documentation against code exports to find stale references
- **GitHub Action** (`action.yml`) — CI integration for drift checks

## Build & Test

```bash
go build ./...          # Build all packages
go test ./...           # Run unit tests
go test -race ./...     # Run with race detector
make build              # Build livedocs binary (CGO required for tree-sitter)
```

## Conventions

- Go module: `github.com/live-docs/live_docs`
- Claims DBs stored as `<repo-name>.claims.db` in data directories
- MCP adapter pattern: all mcp-go imports confined to `mcpserver/adapter.go`
- Design documents live in `docs/design/`
