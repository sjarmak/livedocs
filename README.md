# Livedocs

**Keep your docs in sync with your code.** Livedocs extracts structural claims from source code — exports, types, signatures, dependencies, interface implementations — into per-repo SQLite databases. AI coding agents query these claims via MCP to understand codebases without expensive grep/read cycles.

## What It Does

1. **Extract** symbols and relationships from source code using tree-sitter
2. **Mine** tribal knowledge — ownership, rationale, invariants, and quirks from git blame, CODEOWNERS, commit messages, and inline markers
3. **Store** them as structured claims in per-repo SQLite databases
4. **Serve** them to AI agents via the [Model Context Protocol](https://modelcontextprotocol.io/) (MCP)
5. **Detect** documentation drift — find stale references, undocumented exports, and semantically inaccurate sections
6. **Watch** repositories for changes and incrementally update claims
7. **Enrich** claims with semantic context via Sourcegraph MCP

A claims-backed MCP server reduces context acquisition from thousands of tokens of raw source to ~30-50 tokens per claim, achieving roughly 50x context reduction for codebase onboarding.

## Quick Start

### Local Repository

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

### Remote Repository via Sourcegraph

Extract claims from any public or private repo without cloning:

```bash
export SRC_ACCESS_TOKEN=<your-sourcegraph-token>

# Extract from a remote repo
livedocs extract --source sourcegraph --repo github.com/org/repo -o repo.claims.db

# Watch multiple remote repos for changes
livedocs watch --source sourcegraph --repos 'org/*' --data-dir ./claims/

# Enrich claims with semantic context (purpose, complexity, stability)
livedocs enrich --data-dir ./claims/ --repo org/repo
```

See [SETUP.md](SETUP.md) for detailed setup guides, IDE configuration, and Sourcegraph workflows.

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

| Tool                        | Description                                                  |
| --------------------------- | ------------------------------------------------------------ |
| `list_repos`                | List all repositories with symbol/claim counts               |
| `list_packages`             | List import paths for a repo with prefix filter              |
| `describe_package`          | Render Markdown docs for a package (interfaces, deps, types) |
| `search_symbols`            | Cross-repo symbol search with routing index                  |
| `tribal_context_for_symbol` | All tribal facts for a symbol with full provenance envelope  |
| `tribal_owners`             | Ownership facts (CODEOWNERS + git blame) for a symbol        |
| `tribal_why_this_way`       | Rationale and invariant facts explaining why code exists     |

### Static Context Generation

For tools without MCP support:

```bash
# Render a single package to stdout
livedocs context client-go tools/cache

# Generate CONTEXT.md for all packages in a repo
livedocs context client-go --data-dir data/claims/
```

## CLI Commands

| Command                                 | Description                                                |
| --------------------------------------- | ---------------------------------------------------------- |
| `livedocs init`                         | Initialize a `.livedocs/` directory in a repo              |
| `livedocs extract`                      | Extract symbols and claims from source code                |
| `livedocs extract --source sourcegraph` | Extract from remote repo via Sourcegraph MCP               |
| `livedocs extract --source clone`       | Shallow-clone a remote repo, extract, clean up             |
| `livedocs extract --tribal`             | Also extract tribal knowledge (ownership, rationale, etc.) |
| `livedocs mcp`                          | Start the MCP server (stdio transport)                     |
| `livedocs context`                      | Generate static Markdown context files                     |
| `livedocs check`                        | Run drift detection on documentation                       |
| `livedocs check --cross-repo`           | Cross-repo semantic drift detection using doc-map          |
| `livedocs diff`                         | Show documentation changes since last commit               |
| `livedocs export`                       | Export claims as Markdown                                  |
| `livedocs verify`                       | Verify AI context files against current source             |
| `livedocs verify-claims`                | Verify claim anchors against current source                |
| `livedocs watch`                        | Watch repos for changes and incrementally extract claims   |
| `livedocs watch --source sourcegraph`   | Watch remote repos via Sourcegraph MCP                     |
| `livedocs extract-schedule`             | Run scheduled extractions based on cron expressions        |
| `livedocs enrich`                       | Enrich claims with semantic context from Sourcegraph       |
| `livedocs tribal status`                | Show tribal fact counts by kind                            |
| `livedocs prbot`                        | Analyze PR diff for documentation impact                   |
| `livedocs version`                      | Print version information                                  |

## Sourcegraph Integration

Livedocs integrates with [Sourcegraph](https://sourcegraph.com) via its MCP server for remote extraction, code search, and semantic enrichment — no local cloning needed.

### What You Can Do

- **Extract claims from any repo** without cloning it locally
- **Watch remote repos** for changes and incrementally update claims
- **Enrich claims** with semantic properties (purpose, complexity, stability) using Sourcegraph's code intelligence
- **Cross-repo semantic drift detection** — validate documentation against code in other repositories using keyword search + LLM verification

### Setup

1. Get a Sourcegraph access token from your Sourcegraph instance settings
2. Set it in your environment:
   ```bash
   export SRC_ACCESS_TOKEN=<your-token>
   ```
3. The Sourcegraph MCP server is spawned automatically when needed (requires `npx` on PATH)

See [SETUP.md](SETUP.md) for detailed Sourcegraph workflow tutorials.

## GitHub Action

Add drift detection to your CI pipeline:

```yaml
- uses: live-docs/live_docs@v1
  with:
    fail-threshold: 0 # Fail if any drift detected
```

See [action.yml](action.yml) for all inputs and outputs.

## PR Context Pack (AI Review Assistant)

Drop a single workflow file into any repo to get documentation-impact analysis on every PR. Assists **both human and agent reviewers**.

### Install

Copy [`workflows/livedocs-prbot.yml`](https://github.com/live-docs/live_docs/blob/main/examples/workflows/livedocs-prbot.yml) into your repo as `.github/workflows/livedocs-context.yml`. That's it.

### What it does

On every PR, the workflow:
1. Extracts structural claims from your codebase (cached — subsequent runs are <30s)
2. Computes documentation impact from the PR diff
3. Posts an idempotent PR comment with findings
4. Uploads `claims.db` as an artifact for agent consumption

### Two surfaces

- **Rendered PR comment** — what humans and `gh pr view` agents see
- **Claims database artifact** — agents download with `gh run download` and query via `livedocs mcp --db claims.db` for the full structured claim surface

### Optional: Sourcegraph enrichment

Add `SRC_ACCESS_TOKEN` as a repo secret to enable cross-repo drift detection and semantic claim properties. Without it, the baseline docs-impact comment still works.

### Agent consumption

```bash
# During an agentic coding session on a PR
gh run download <run-id> --repo owner/repo --name livedocs-claims-pr-<number>
livedocs mcp --db claims.db   # exposes MCP tools to your agent
```

The download command is pre-filled in the PR comment.

## Architecture

```
cmd/livedocs/       CLI entry point (cobra commands)
mcpserver/          MCP server with adapter layer, DB pool, routing index
renderer/           Claims-to-Markdown renderer (interfaces, deps, functions)
db/                 SQLite-backed claims storage, tribal knowledge, cross-repo xref index
extractor/          Tree-sitter symbol extraction (Go, Python, TypeScript, Shell)
extractor/tribal/   Tribal knowledge extractors (CODEOWNERS, blame, commit rationale, inline markers)
pipeline/           Extraction pipeline with caching and remote file sources
drift/              Documentation drift detection (symbol-level, semantic, cross-repo)
semantic/           LLM-backed semantic claim generation and verification
sourcegraph/        Sourcegraph MCP client, predicate router, enrichment pipeline
anchor/             Claim-to-source anchoring and verification
aicontext/          AI context file (CLAUDE.md, .cursorrules) validation
watch/              Repository watcher with state persistence
gascity_livedocs/   Example: automated doc freshness monitoring for Gas City ecosystem
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
