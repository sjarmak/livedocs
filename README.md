# Livedocs

**Keep your docs in sync with your code.** Livedocs extracts structural claims from source code — exports, types, signatures, dependencies, interface implementations — into per-repo SQLite databases. AI coding agents query these claims via MCP to understand codebases without expensive grep/read cycles.

## What It Does

1. **Extract** symbols and relationships from source code using tree-sitter
2. **Store** them as structured claims in per-repo SQLite databases
3. **Serve** them to AI agents via the [Model Context Protocol](https://modelcontextprotocol.io/) (MCP)
4. **Detect** documentation drift — find stale references and undocumented exports

A claims-backed MCP server reduces context acquisition from thousands of tokens of raw source to ~30-50 tokens per claim, achieving roughly 50x context reduction for codebase onboarding.

## Quick Start

```bash
# Install
go install github.com/live-docs/live_docs/cmd/livedocs@latest

# Extract claims from your repo
cd /path/to/your/repo
livedocs init
livedocs extract

# Connect to Claude Code
claude mcp add livedocs -- livedocs mcp
```

See [SETUP.md](SETUP.md) for Cursor, Windsurf, and multi-repo configuration.

## MCP Tools

### Single-Repo Mode

```bash
livedocs mcp --db .livedocs/claims.db
```

| Tool               | Description                                            |
| ------------------ | ------------------------------------------------------ |
| `query_claims`     | Search claims by symbol name (supports LIKE wildcards) |
| `check_drift`      | Detect stale symbol references in documentation        |
| `verify_section`   | Check if claims for a file/line range are still valid  |
| `check_ai_context` | Verify AI context files for broken references          |

### Multi-Repo Mode

```bash
livedocs mcp --data-dir /path/to/claims/
```

| Tool               | Description                                                  |
| ------------------ | ------------------------------------------------------------ |
| `list_repos`       | List all repositories with symbol/claim counts               |
| `list_packages`    | List import paths for a repo with prefix filter              |
| `describe_package` | Render Markdown docs for a package (interfaces, deps, types) |
| `search_symbols`   | Cross-repo symbol search with routing index                  |

### Static Context Generation

For tools without MCP support:

```bash
# Render a single package to stdout
livedocs context client-go tools/cache

# Generate CONTEXT.md for all packages in a repo
livedocs context client-go --data-dir data/claims/
```

## CLI Commands

| Command            | Description                                   |
| ------------------ | --------------------------------------------- |
| `livedocs init`    | Initialize a `.livedocs/` directory in a repo |
| `livedocs extract` | Extract symbols and claims from source code   |
| `livedocs mcp`     | Start the MCP server (stdio transport)        |
| `livedocs context` | Generate static Markdown context files        |
| `livedocs check`   | Run drift detection on documentation          |
| `livedocs diff`    | Show documentation changes since last commit  |
| `livedocs export`  | Export claims as Markdown                     |
| `livedocs verify`  | Verify claim anchors against current source   |

## GitHub Action

Add drift detection to your CI pipeline:

```yaml
- uses: live-docs/live_docs@v1
  with:
    fail-threshold: 0 # Fail if any drift detected
```

See [action.yml](action.yml) for all inputs and outputs.

## Architecture

```
cmd/livedocs/     CLI entry point (cobra commands)
mcpserver/        MCP server with adapter layer, DB pool, routing index
renderer/         Claims-to-Markdown renderer (interfaces, deps, functions)
db/               SQLite-backed claims storage + cross-repo xref index
extractor/        Tree-sitter symbol extraction (Go, Python, TypeScript, Shell)
extract/          Extraction pipeline orchestration
drift/            Documentation drift detection
anchor/           Claim-to-source anchoring and verification
aicontext/        AI context file (CLAUDE.md, .cursorrules) validation
pipeline/         Full extraction pipeline with caching
```

## Supported Languages

| Language   | Extractor              | Symbols Extracted                                         |
| ---------- | ---------------------- | --------------------------------------------------------- |
| Go         | tree-sitter-go         | functions, methods, types, interfaces, constants, imports |
| Python     | tree-sitter-python     | functions, classes, methods, imports                      |
| TypeScript | tree-sitter-typescript | functions, classes, interfaces, types, exports            |
| Shell      | tree-sitter-bash       | functions                                                 |

## Development

```bash
# Build (requires CGO for tree-sitter)
make build

# Run tests
go test ./...

# Run with race detector
go test -race ./...

# Run integration tests
go test -tags integration ./integration/...
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

## License

[Apache License 2.0](LICENSE)
