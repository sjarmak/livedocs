# Live Docs

## Purpose

Tools and workflows that keep repository documentation automatically up to date with every commit. Extracts structural claims (exports, types, signatures, dependencies, interface implementations) from source code into per-repo SQLite databases, then serves them to AI coding agents via MCP or static Markdown files.

## Architecture

- **Extraction pipeline** (`extractor/`, `extract/`, `pipeline/`) — tree-sitter-based symbol extraction producing per-repo `.claims.db` files; supports local and remote (Sourcegraph MCP) file sources
- **Claims database** (`db/`) — SQLite-backed storage for symbols, claims, and tribal knowledge with cross-repo xref index
- **Tribal knowledge** (`extractor/tribal/`, `db/tribal.go`) — provenance-tracked ownership, rationale, invariants, and quirks extracted from CODEOWNERS, git blame, commit messages, and inline markers (TODO/HACK/NOTE)
- **Renderer** (`renderer/`) — transforms claims into compact Markdown (interfaces, deps, function categories)
- **MCP server** (`mcpserver/`) — Model Context Protocol server exposing claims via 12 tools (4 single-DB, 4 multi-repo, 3 tribal knowledge, 1 extraction request); supports stdio and HTTP/SSE transports
- **CLI** (`cmd/livedocs/`) — `livedocs` binary with `init`, `extract`, `mcp`, `context`, `check`, `diff`, `export`, `verify`, `tribal`, `watch`, `extract-schedule` commands
- **Drift detection** (`drift/`, `anchor/`) — compares documentation against code exports to find stale references; tribal drift transitions facts to stale/quarantined (never deletes)
- **Remote extraction** (`pipeline/filesource.go`, `watch/gitops.go`) — extract claims from remote repos via Sourcegraph MCP without cloning
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
- Tribal facts use provenance envelopes: every fact must have `source_quote`, `evidence[]`, `confidence`, `status`
- Tribal extractors are deterministic (model=NULL); LLM-classified extraction is Phase 2 behind explicit opt-in
- Design documents live in `docs/design/`
