# PRD: MCP Claims Server

> **Status:** Risk-annotated (v3). Updated 2026-04-03 after premortem (5 failure lenses). All 5 risks rated Critical/High. See `premortem_mcp_claims_server.md` for full failure narratives. Previous: convergence debate (3 advocates), divergent research (5 agents).

## Problem Statement

The live docs system has extracted 665k structural claims across 80 Kubernetes repos into per-repo SQLite databases. This structured knowledge graph contains the exact information AI coding agents need for codebase onboarding — exports, types, signatures, dependencies, interface implementations, and cross-package relationships. However, this data is inaccessible to agents during coding sessions.

The current MCP server scaffold (`mcpserver/`) connects to a single SQLite database and exposes 4 verification-oriented tools. It cannot serve the multi-repo corpus, returns raw claim tuples (5-10x more token-expensive than pre-rendered summaries), and has no orientation or navigation tools. Meanwhile, the `renderer` package already produces the ideal agent output — compact Markdown with interface maps, dependency graphs, and function categories — but is disconnected from the MCP server.

Agents today fall back to expensive grep/read cycles to build context. A claims-backed MCP server could reduce context acquisition from thousands of tokens of raw source to ~30-50 tokens per claim tuple, achieving the 50x context reduction demonstrated by similar systems (AiDex).

## Goals & Non-Goals

### Goals

- Expose the full 80-repo claims corpus through a single MCP server session
- Give agents structured answers to orientation ("what repos/packages exist?"), navigation ("what does this symbol do? who calls it?"), and verification ("is this doc still accurate?") questions
- Minimize context window pressure: return pre-rendered summaries, not raw tuples
- Support Claude Code (stdio), Cursor, and other MCP-compatible tools
- Provide a static generation fallback (`livedocs context`) for tools without MCP support

### Non-Goals

- Real-time code indexing (extraction happens separately via `livedocs extract`)
- Cross-repo JOIN queries in SQL (fan-out with goroutines instead)
- Supporting Streamable HTTP transport in v1 (stdio only, HTTP deferred)
- Replacing IDE language servers (LSP/SCIP) for go-to-definition or rename
- Exposing raw SQL query capability

## Premortem-Driven Design Constraints

> These constraints emerged from the 5-lens premortem analysis (see `premortem_mcp_claims_server.md`). They override or refine earlier convergence decisions.

1. **Semantic context in describe_package is must-have, not nice-to-have** _(premortem theme 2)_: Include LLM-generated purpose sentence and usage patterns in `describe_package` output. Without this, agents use the tool once and abandon it. Structural-only output duplicates `go doc` for most repos. Rationale: scope failure lens showed agents stop calling tools that answer "what exists" but not "how to use this."

2. **Symbol routing index before search_symbols ships** _(premortem themes 1, 3)_: Build in-memory inverted index (symbol prefix -> repo IDs) at startup. `search_symbols` consults this first, fans out only to matching repos. Rationale: fan-out across all repos is O(repos) I/O with LRU thrashing; 3/5 failure lenses flagged this as the highest-risk pattern.

3. **Startup validation and ready signal** _(premortem theme 3)_: Before entering stdio loop, verify DB exists and is readable. Emit ready signal to stderr. Register SIGTERM/SIGINT handlers. Resolve paths via `--project-root` or env var, never relative to cwd. Rationale: every external user hits the invisible-failure path resolution issue on first use.

4. **mcp-go adapter layer** _(premortem theme 4)_: Wrap all mcp-go types behind interfaces (`ToolRegistry`, `RequestParser`). Confine breakage from mcp-go API changes to one file. Add E2E subprocess integration test validating JSON-RPC handshake. Rationale: mcp-go is pre-1.0, has broken API 3 times in 8 versions.

5. **Staleness metadata in every response** _(premortem theme 3)_: Include `extracted_at` timestamp. Warn if extraction >7 days old. Rationale: stale claims producing incorrect output is worse than no tool.

6. **Validate against non-Kubernetes repos before launch** _(premortem theme 2)_: Run full tool suite against 5 diverse repos. If `describe_package` output is not meaningfully better than built-in doc tools for 3+, redesign. Rationale: Kubernetes is atypically interface-heavy; generalizing from it is the scope failure's root cause.

## Architecture

### Multi-Repo DB Pool

```
data/claims/
  api.claims.db          (9 MB)
  client-go.claims.db    (42 MB)
  kubernetes.claims.db   (18 MB)
  ... (80 total, 262 MB)
```

A `DBPool` lazily opens per-repo SQLite files on demand with LRU eviction (max 20 concurrent open DBs). Repo manifest built at startup by scanning the directory (no DB opens needed). Each `*sql.DB` uses `SetMaxOpenConns(2)` to prevent SQLite busy errors.

### Tool Design (3 tiers)

**Orientation** (discovery):

- `list_repos` — repo names + symbol/claim counts
- `list_packages` — import paths for a repo, optionally filtered by prefix

**Navigation** (understanding):

- `describe_package` — pre-rendered Markdown via renderer (interfaces, deps, reverse deps, function categories). The highest-value tool.
- `search_symbols` — cross-repo symbol search with fan-out. Returns symbol metadata, not full claims.

**Verification** (drift detection):

- `check_drift` — compare claims against current source
- `verify_freshness` — compare extraction timestamp against git HEAD

### Response Strategy

- Default output: Markdown (pre-rendered via existing renderer). JSON available via `format` parameter.
- Hard cap: 50 results per tool call with `total_count` metadata. No cursor pagination.
- Filter-and-cap pattern: agents narrow queries via `repo`, `import_path`, `predicate` parameters rather than paginating.

### Static Generation Fallback

`livedocs context <repo> [package]` generates `.livedocs/CONTEXT.md` files per package using the renderer. Works with every tool that can read files.

## Requirements

### Must-Have

- **Multi-repo DB pool**: Lazy-loading connection pool scanning `data/claims/*.claims.db`.
  - Acceptance: `go test ./mcpserver/... -run TestDBPool` passes. Pool opens DBs on demand and evicts LRU when exceeding 20 concurrent. `SetMaxOpenConns(2)` set on each DB.

- **`list_repos` tool**: Returns all repos with symbol count and claim count.
  - Acceptance: MCP tool call `list_repos` returns JSON array with 80 entries. Each entry has `repo`, `symbols`, `claims` fields. Response is <2000 tokens.

- **`list_packages` tool**: Returns import paths for a repo.
  - Acceptance: `list_packages(repo="client-go")` returns >100 import paths. Supports `prefix` filter. Response capped at 200 entries with `total_count`.

- **`describe_package` tool**: Returns pre-rendered Markdown for a package using the existing renderer.
  - Acceptance: `describe_package(repo="client-go", import_path="tools/cache")` returns Markdown with "Interfaces", "Dependencies", "Reverse Dependencies", "Functions" sections. Uses `renderer.LoadPackageData` + `renderer.RenderMarkdown`. Response is <1500 tokens for a typical package.

- **`search_symbols` tool**: Cross-repo symbol search with fan-out.
  - Acceptance: `search_symbols(query="NewInformer")` returns matches from multiple repos. Fan-out uses `errgroup` with concurrency limit of 10. Results capped at 50. Response includes `repo`, `import_path`, `symbol_name`, `kind`, `visibility` per match. Completes in <500ms.

- **Result size limits**: All tools enforce hard caps with `total_count`.
  - Acceptance: No tool response ever exceeds 50,000 tokens. A wildcard query returns capped results with accurate `total_count`.

- **Static context generation**: `livedocs context <repo> [package]` CLI command.
  - Acceptance: `livedocs context client-go tools/cache` outputs Markdown to stdout matching renderer output. `livedocs context client-go` generates `.livedocs/CONTEXT.md` per package.

- **Server integration**: Wire into `livedocs mcp` command with `--data-dir` flag.
  - Acceptance: `livedocs mcp --data-dir data/claims/` starts MCP server on stdio. `go test ./mcpserver/...` passes. `go test ./cmd/livedocs/... -run TestMCP` passes.

- **Semantic context in describe_package** _(promoted from nice-to-have per premortem)_: Include purpose sentence and usage patterns from Tier 2 semantic claims when available.
  - Acceptance: `describe_package` output includes "Purpose" and "Usage Patterns" sections when semantic claims exist for the package. Falls back gracefully to structural-only when no semantic claims are available.

- **Startup validation and operational hardening** _(added per premortem)_: DB existence check, stderr ready signal, signal handlers, absolute path resolution.
  - Acceptance: `livedocs mcp --data-dir /nonexistent` exits with clear error before entering stdio loop. Server emits `{"status":"ready"}` to stderr on successful init. SIGTERM triggers clean shutdown with telemetry flush.

- **mcp-go adapter layer** _(added per premortem)_: All mcp-go types wrapped behind interfaces.
  - Acceptance: `grep -r "mcp\." mcpserver/` shows mcp-go imports only in `adapter.go` (or equivalent), not in tool handlers.

- **Symbol routing index** _(added per premortem)_: In-memory inverted index for search_symbols routing.
  - Acceptance: `search_symbols` consults routing index before fan-out. With 80 repos, a typical query opens <5 DBs instead of 80. `go test ./mcpserver/... -run TestRoutingIndex` passes.

- **E2E subprocess integration test** _(added per premortem)_: Spawn `livedocs mcp` as real subprocess, validate JSON-RPC handshake.
  - Acceptance: `go test -tags integration ./integration/... -run TestMCPSubprocess` passes. Test sends `initialize` request over stdin, asserts valid response on stdout.

### Should-Have

- **`check_drift` tool**: Verify claims against current source.
  - Acceptance: `check_drift(repo="client-go", file="tools/cache/store.go")` returns drift findings with severity levels. Uses existing anchor/drift packages.

- **`verify_freshness` tool**: Compare extraction age against git HEAD.
  - Acceptance: `verify_freshness(repo="client-go")` returns `extracted_at`, `repo_head`, `staleness_hours`. Returns "stale" if extraction is >24h behind HEAD.

- **XRefDB population**: CLI command to build cross-repo symbol index.
  - Acceptance: `livedocs xref build --data-dir data/claims/` creates `data/claims/_xref.db`. `search_symbols` uses XRefDB when available to reduce fan-out from 80 to targeted repos.

- **MCP Resources**: Expose repos and packages as browsable resources.
  - Acceptance: `resources/list` returns resource URIs. `claims://client-go/packages` returns package list. `claims://repos` returns repo manifest.

### Nice-to-Have

- **MCP Prompts**: Pre-composed onboarding workflows.
  - Acceptance: `prompts/list` includes `onboard_repo` and `explain_symbol`. Running `onboard_repo(repo="client-go")` returns a structured prompt with top packages, key types, and entry points.

- **Sensitive content filter review**: Audit the keyword blocklist for over-deletion of legitimate k8s API terms (Secret, Token, ServiceAccountToken).
  - Acceptance: Review and document false positive rate. Adjust filter to use context-aware matching (e.g., don't filter "Secret" when it's a Kubernetes resource type name).

- **Telemetry dashboard**: Use existing `mcpserver/telemetry.go` to log which tools agents actually call.
  - Acceptance: Tool call counts logged to a file. After 100 agent interactions, produce a report of tool usage frequency.

## Design Considerations (Resolved via Debate)

### Static docs vs. dynamic MCP _(resolved)_

Build both in Phase 1. Static generation (`livedocs context`) and MCP server ship together. The renderer is the shared core — both paths use `LoadPackageData` + `RenderMarkdown`. Static files validate content quality with a 10-second feedback loop; MCP enables interactive cross-repo queries that static files cannot serve. Decisive argument: cross-repo `search_symbols` is the unique MCP value-add, but `describe_package` (the #1 tool) works through both channels.

### DB pool timing _(resolved)_

DB pool ships in Phase 1 alongside core tools, with `--db` fallback for single-repo use. MCP-max argued that a single-DB server "doesn't solve the problem MCP is supposed to solve" — agents don't know which repo to connect to. Minimal-MCP argued the pool is the riskiest abstraction. Resolution: both modes coexist (`--data-dir` for multi-repo pool, `--db` for single-repo bypass). This de-risks Phase 1 while enabling orientation tools.

### Response format _(resolved)_

Markdown by default, JSON via `format=json` parameter. Agents consume Markdown natively and it compresses 5-10x better than raw claim tuples in context windows. The existing MCP server's raw JSON tuple output is retired in favor of renderer-backed Markdown.

### Multi-repo query strategy _(resolved)_

Fan-out with `errgroup` deferred to Phase 2 (`search_symbols`). Phase 1 tools operate on single repos at a time (routed via DB pool). XRefDB as a Phase 2 performance optimization. No ATTACH DATABASE.

### Result limiting _(resolved)_

Filter-and-cap with `total_count`. No cursor pagination. Agents are better at narrowing queries than paginating. Hard cap of 50 for search results, 200 for list results.

### Connection pooling _(resolved)_

Lazy-load with LRU eviction at 20 concurrent open DBs. `SetMaxOpenConns(2)` per DB to prevent SQLite busy errors. Repo manifest built at startup from filesystem scan (no DB opens).

### Tool count _(resolved)_

Phase 1: 3 tools (`list_repos`, `list_packages`, `describe_package`) — complete orientation-to-navigation workflow. Phase 2: +1 tool (`search_symbols` with cross-repo fan-out) — the unique MCP differentiator. Phase 3: +2 tools (`check_drift`, `verify_freshness`) — verification tier. Decisive argument: a partial server with only `describe_package` is strictly better than the current 4 verification-only tools; but `list_repos` requires the pool and is necessary for agent discovery.

## Open Questions

1. Should the MCP server support Streamable HTTP transport for browser-based or remote IDE use cases? (Deferred to v2)
2. What is the maximum MCP message size that Claude Code and Cursor can handle? (Needs empirical testing)
3. Should the server expose per-session state (e.g., "focus on these 3 repos")? (Deferred — adds complexity)
4. Should all 80 per-repo DBs be merged into a single unified DB for simpler querying? (Rejected — per-repo is more maintainable, aligns with extraction pipeline)
5. How should the sensitive content filter be tuned for Kubernetes API terminology? (Open — needs audit)

## Phased Implementation (Post-Convergence)

**Phase 1: DB Pool + Core Tools + Static CLI** (single PR)

- `DBPool` with lazy-load + LRU eviction (max 20 concurrent, `SetMaxOpenConns(2)`)
- `--data-dir` flag for multi-repo, `--db` fallback for single-repo bypass
- `list_repos` tool — filesystem manifest + lazy DB open for counts
- `list_packages(repo, prefix?)` tool — single DB open, distinct import paths
- `describe_package(repo, import_path)` tool — `LoadPackageData` + `RenderMarkdown` through MCP
- `livedocs context <repo> [package]` CLI command — same renderer, file output
- Result caps on all tools with `total_count`
- Verification:
  - `go test ./mcpserver/... -run TestDBPool` passes
  - `go test ./mcpserver/... -run TestDescribePackage` passes
  - `livedocs mcp --data-dir data/claims/` starts and responds to tool calls
  - `livedocs context client-go tools/cache` outputs Markdown to stdout

**Phase 2: Cross-Repo Search** (next PR)

- `search_symbols(query, repo?)` — errgroup fan-out across pool, concurrency limit 10, cap 50 results
- XRefDB population command (`livedocs xref build --data-dir`)
- Verification:
  - `search_symbols(query="NewInformer")` returns matches from multiple repos in <500ms
  - `go test ./mcpserver/... -run TestSearchSymbols` passes

**Phase 3: Verification Tools** (should-have)

- `check_drift(repo, file)` — wire existing drift/anchor packages
- `verify_freshness(repo)` — compare extraction timestamp against git HEAD
- MCP Resources for browsable repo/package URIs

## Research Provenance

### Divergent Research (5 independent agents)

| Lens                   | Key Contribution                                                                                                                        |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| Prior Art              | Found 5+ competing MCP code intelligence servers, identified ResourceLink spec for large datasets, confirmed claims model is novel      |
| Technical Architecture | Designed DBPool with lazy-load/LRU, discovered XRefDB is coded but unpopulated, identified SetMaxOpenConns concurrency bug              |
| Agent Experience       | Mapped three-tier question model (orientation/navigation/verification), identified renderer as highest-value unexposed asset            |
| Failure Modes          | Proved context window bombs are trivially achievable, sensitive filter likely over-deletes k8s API terms, 7ms cold start is a non-issue |
| Contrarian             | Argued static generation delivers 80% of value at 10% effort, MCP server's raw tuples bypass the renderer (strictly worse)              |

### Key Convergence Points

- All 5 agents: single-DB architecture is the critical blocker
- 4/5 agents: renderer is the highest-value unexposed asset
- 4/5 agents: lazy DB pooling with LRU eviction is the right strategy
- 3/5 agents: 5-7 tools max with progressive detail levels

### Key Divergence Points

- Static docs vs. dynamic server → Resolved: build both
- Result limiting strategy → Resolved: filter-and-cap with total_count
- XRefDB timing → Resolved: fan-out first, XRefDB as optimization

### Convergent Debate (3 advocates, 2 rounds)

| Position       | Key Contribution                                                                              | What They Conceded                                                                        |
| -------------- | --------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| Static-First   | Renderer is the shared core; validate content before building infrastructure                  | MCP should be Phase 2-3, not a separate version. Cross-repo search is genuinely valuable. |
| MCP-Maximalist | Single-DB server "doesn't solve the problem MCP is supposed to solve" — agents need discovery | `search_symbols` fan-out can be Phase 2; `livedocs context` CLI should ship in Phase 1    |
| Minimal-MCP    | DB pool is the riskiest piece; ship tools before infrastructure                               | Accepted pool on architectural merit for Phase 2→Phase 1 (shifted to include pool)        |

**Final architecture**: DB pool + 3 core MCP tools + static CLI in Phase 1. Cross-repo search in Phase 2. Verification tools in Phase 3. All three positions integrated.
