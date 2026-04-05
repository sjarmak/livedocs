# PRD: Remote Extraction via Sourcegraph MCP

## Problem Statement

live_docs extracts structural claims from source code using tree-sitter and stores them in per-repo SQLite databases, which are then served to AI coding agents via MCP. But the entire extraction pipeline requires local filesystem access — cloned repositories, `os.ReadFile`, `git diff`, and `go/packages`. This forces users to clone every repo they want to index, making live_docs impractical for teams that use Sourcegraph as their primary code navigation layer.

Sourcegraph users already have every repository indexed and accessible via MCP tools (`read_file`, `list_files`, `compare_revisions`). The enrichment pipeline already calls Sourcegraph remotely. But extraction — the foundational step — remains stubbornly local. A Sourcegraph user should be able to enrich their entire codebase and serve pre-computed claims to agents without ever cloning a repository.

## Goals & Non-Goals

### Goals

- Enable extraction from remote repositories via Sourcegraph MCP without local clones
- Support two extraction backends: local (existing tree-sitter on disk) and remote (Sourcegraph MCP as file source)
- Provide a practical initial extraction path for remote repos (SCIP import or shallow clone)
- Support incremental remote extraction triggered by commit detection, not filesystem watching
- Maintain the existing local pipeline as a first-class option — remote is additive, not a replacement

### Non-Goals

- Supporting non-Sourcegraph remote backends (GitHub MCP, GitLab API) — design for extensibility, implement Sourcegraph only
- Real-time extraction latency for remote mode — async batch processing is acceptable
- Replacing tree-sitter with SCIP entirely — tree-sitter remains the universal fallback
- Multi-tenant hosted service — focus on single-operator deployment (one machine or container)
- Agent-triggered on-demand extraction (deferred to Phase 2)

## Phased Delivery

### Phase 1 — Gate-then-Branch: Verify SCIP, Ship Remote Extraction

Phase 1 begins with a 2-3 day SCIP verification spike that determines the primary initial extraction path. FileSource abstraction and incremental tree-sitter-over-MCP ship regardless.

**If SCIP export verified:** SCIP import (initial, indexed languages) + shallow clone (fallback, Go deep) + tree-sitter-over-MCP (incremental).

**If SCIP export not verified:** Shallow clone (initial, all languages) + tree-sitter-over-MCP (incremental). SCIP deferred to Phase 2.

### Phase 2 — Remote Watch + Agent Integration + SCIP Optimization

Add remote change detection (polling via `compare_revisions`), scheduled extraction, and the `request_extraction` MCP tool for agent-triggered indexing. If SCIP was deferred from Phase 1, add it here once API is available.

## Requirements

### Must-Have (Phase 1) — Ships Regardless of SCIP Verification

- Requirement: SCIP API verification spike (Phase 1 gate)
  - Acceptance: A 2-3 day time-boxed investigation determines whether Sourcegraph exposes SCIP index data via API (GraphQL, `src-cli`, direct download). Results documented in `docs/design/scip_api_spike.md` with one of three outcomes: "verified + instructions", "requires Enterprise only", or "not available". Also verifies whether `compare_revisions` returns structured file-level diffs and measures actual `read_file` latency distribution.

- Requirement: `FileSource` interface abstracting file access for the extraction pipeline
  - Acceptance: A `FileSource` interface exists with `ReadFile(ctx, repo, revision, path) ([]byte, error)`, `ListFiles(ctx, repo, revision, pattern) ([]string, error)`, and `DiffBetween(ctx, repo, fromRev, toRev) ([]FileChange, error)` methods. A `LocalFileSource` implementation wraps existing `os.ReadFile` and `gitdiff.DiffBetween`. A `SourcegraphFileSource` implementation wraps Sourcegraph MCP `read_file`, `list_files`, and `compare_revisions`. `go build ./...` succeeds. Tests verify both implementations satisfy the interface.

- Requirement: Tree-sitter extractor accepts byte content instead of only file paths
  - Acceptance: The extractor interface has an `ExtractBytes(ctx, src []byte, relPath, lang string) ([]Claim, error)` method alongside the existing `Extract(ctx, path, lang)`. The tree-sitter extractor implements `ExtractBytes` by passing bytes directly to `sitter.ParseCtx`. The Go deep extractor returns `ErrRequiresLocalFS` from `ExtractBytes`. A test verifies `ExtractBytes` produces identical claims to `Extract` for the same file content.

- Requirement: Pipeline accepts a `FileSource` instead of assuming local filesystem
  - Acceptance: `pipeline.Config` has a `FileSource` field. When set, `Run()` uses `FileSource.DiffBetween` for change detection and `FileSource.ReadFile` for content instead of local git and `os.ReadFile`. When nil, behavior is unchanged (local filesystem). A test verifies the pipeline produces claims when given a mock `FileSource`.

- Requirement: Shallow-clone bootstrap for initial extraction
  - Acceptance: `livedocs extract --source clone --repo github.com/org/repo` performs `git clone --depth=1` into a temp directory, runs local extraction, stores the claims DB, then cleans up the clone. Enables ALL extractors including Go deep. Peak disk usage is one repo at a time. `--source clone` is the recommended initial extraction path in CLI help text.

- Requirement: `livedocs extract --source sourcegraph --repo <repo>` CLI mode for incremental extraction
  - Acceptance: `livedocs extract --source sourcegraph --repo github.com/org/repo --data-dir ./data/` extracts claims from a remote repo via Sourcegraph MCP without a local clone. Requires `SRC_ACCESS_TOKEN`. Produces a `.claims.db` file in `--data-dir`. Also supports `--from-rev` and `--to-rev` for incremental extraction via `compare_revisions`. `livedocs extract --help` shows the `--source` and `--repo` flags. Without `--source`, behavior is unchanged (local extraction). `go build ./...` succeeds.

- Requirement: Cost estimation before full remote extraction
  - Acceptance: Before extracting a remote repo without `--from-rev`/`--to-rev`, the tool counts files via `list_files`, estimates the number of MCP calls, estimated cost, and estimated time, then prints the estimate and requires `--confirm` to proceed (same pattern as `livedocs enrich --initial`). A test verifies the cost estimate output.

### Must-Have (Phase 1) — Conditional on SCIP Verification

- Requirement: SCIP import as primary initial extraction for Sourcegraph-indexed repos
  - Acceptance: `livedocs extract --source scip --repo github.com/org/repo` queries Sourcegraph for SCIP index data and imports it via the existing `scip/importer.go` path (`ImportReader` accepting `io.Reader`). If SCIP data is available, extraction completes without per-file MCP calls. If SCIP data is unavailable, falls back to `--source clone` with a log message. A test verifies SCIP import produces claims including `implements` and `has_kind` predicates. **Only implemented if SCIP verification spike succeeds.**

### Should-Have (Phase 1)

- Requirement: Concurrent Sourcegraph MCP calls for remote extraction
  - Acceptance: `SourcegraphFileSource` makes up to N concurrent `read_file` calls (default 10, configurable via `--concurrency`). Uses a semaphore or worker pool pattern. A test verifies concurrent calls complete faster than serial and do not cause errors. The existing single-goroutine `SourcegraphClient` is either pooled (multiple instances) or refactored to support concurrent in-flight requests.

- Requirement: Language-aware extraction routing
  - Acceptance: When both SCIP and tree-sitter are available for a repo, the system uses SCIP for languages with SCIP indexers (richer predicates) and tree-sitter for languages without (universal coverage). A configuration or auto-detection determines which languages have SCIP indices.

### Must-Have (Phase 2)

- Requirement: Remote change detection via commit polling
  - Acceptance: `livedocs watch --source sourcegraph --repos org/*` polls Sourcegraph for new commits on configured repos (via `commit_search` or equivalent) at a configurable interval. When new commits are detected, triggers incremental extraction. No local git repo needed.

- Requirement: Scheduled extraction via cron or webhook
  - Acceptance: A `livedocs extract-schedule` command (or config file entry) configures periodic extraction for a list of repos. Supports cron syntax for scheduling. Alternatively, a webhook endpoint accepts GitHub/GitLab push events and triggers extraction for the pushed repo.

- Requirement: `request_extraction` MCP tool for agent-triggered indexing
  - Acceptance: An MCP tool `request_extraction(repo, import_path?)` queues extraction for a repo (or specific package). If the repo has no claims DB, triggers full extraction. If it has one, triggers incremental from the last indexed commit. Returns a status message ("queued", "already fresh", "in progress"). Registered in multi-repo mode.

### Should-Have (Phase 2)

- Requirement: Shared MCP server with HTTP/SSE transport
  - Acceptance: `livedocs mcp --transport http --port 8080` serves the MCP protocol over HTTP with Server-Sent Events, allowing multiple agents to connect simultaneously. The existing stdio transport remains the default.

### Nice-to-Have

- Requirement: Auto-discovery of repos from Sourcegraph
  - Acceptance: `livedocs extract --source sourcegraph --discover org/*` queries Sourcegraph for all repos matching a pattern and extracts each one. Progress reporting shows per-repo status.

- Requirement: Claims DB distribution via object storage
  - Acceptance: `livedocs export --format db --output s3://bucket/claims/` uploads `.claims.db` files to S3/GCS. `livedocs mcp --data-dir s3://bucket/claims/` reads from object storage. Enables shared access without a dedicated server.

## Design Considerations

**FileSource abstraction placement**: The interface lives at the pipeline level, not the extractor level. The pipeline orchestrates file discovery and change detection; extractors receive bytes. This keeps extractor implementations simple (tree-sitter already works with `[]byte`) and confines the local-vs-remote decision to one place.

**SCIP-first strategy for Sourcegraph users**: Sourcegraph already indexes repos with SCIP, producing the same structural claims live_docs extracts (defines, has_kind, implements, has_doc). The existing `scip/importer.go` maps SCIP data to the claims DB. If SCIP data is available, it eliminates per-file API calls entirely — one bulk operation replaces thousands of `read_file` calls. Tree-sitter-over-MCP is the fallback for repos without SCIP indices.

**Concurrency model**: The current `SourcegraphClient` serializes all MCP calls through a single goroutine (one in-flight request at a time). Remote extraction of 1000 files at serial latency is impractical. Two options: (a) pool of `SourcegraphClient` instances sharing a single Sourcegraph MCP subprocess, or (b) refactor the client to support concurrent in-flight requests via request IDs. Option (a) is simpler but spawns multiple subprocesses; option (b) is cleaner but a larger change.

**Why shallow clone for initial extraction**: Every production code intelligence system (Sourcegraph/SCIP, Google/Kythe, GitHub/stack-graphs) uses clone-then-index for initial analysis. Per-file API fetching for 1000+ files takes hours and costs ~$3-150 depending on repo size. A `git clone --depth=1` into a temp directory takes seconds and enables full local extraction including the Go deep extractor. The clone is deleted after extraction. For incremental updates (5-50 files per commit), API-based extraction is viable and avoids maintaining a persistent clone.

**Go deep extractor limitation**: `goextractor.GoDeepExtractor` uses `go/packages.Load` which requires a real Go module on disk. Remote extraction cannot run the deep extractor without a local filesystem. For remote mode, Go extraction falls back to tree-sitter only (partial claims) or SCIP (complete claims if available). This is an acceptable tradeoff — tree-sitter captures exports, types, and signatures; SCIP adds implements and cross-references.

**Incremental change detection without git**: For remote repos, `compare_revisions` replaces `git diff`. The pipeline stores the last-indexed commit SHA in the claims DB (already done via `ExtractionMeta`). On each run, it compares the latest remote HEAD against the stored SHA, fetches only changed files, and updates incrementally. Content-hash caching provides execution-level deduplication — files fetched but unchanged are skipped.

## Open Questions

### Resolved by Convergence

- **SCIP vs tree-sitter for remote:** Gate-then-branch. SCIP verification spike (days 1-3) determines the primary initial extraction path. FileSource + tree-sitter-over-MCP ships regardless for incremental extraction.
- **Shallow clone vs pure API for initial extraction:** Shallow clone is the universal bootstrap. It's ephemeral (clone-extract-delete), enables all extractors, costs nothing, and takes seconds. API-based full extraction is too expensive for initial use (hours, $3-150 per repo).
- **Go deep extractor on remote repos:** Shallow clone provides it for free. For SCIP path, SCIP may provide equivalent data (`has_kind`, `implements`). For incremental-only mode, accept the tree-sitter subset (6 predicates).
- **Data quality tradeoff:** Tree-sitter produces 6 predicates; SCIP produces 10 (adding `implements`, `has_kind`, `has_signature`, `encloses`). 6 is sufficient for core MCP use case (discovery, exports, imports, docs). 10 is better for interface navigation and type resolution. Enrichment pipeline partially compensates.

### Critical (must resolve in SCIP verification spike, days 1-3)

- Can Sourcegraph export SCIP index data via API or download? Three paths to check: GraphQL API (`lsif/scip` endpoints), `src-cli` tooling, direct index download. If the API returns protobuf compatible with `ImportReader(ctx, io.Reader)`, SCIP becomes primary initial path.
- Does `compare_revisions` return structured file-level diffs (path + status), or prose/patch format? The pipeline needs `[]FileChange{Path, Status}` to replace `git diff --name-status`.
- What is the actual `read_file` latency distribution? Median vs worst case determines concurrency requirements.

### Important (should resolve during Phase 1)

- What is the actual concurrency limit on Sourcegraph MCP calls? Rate limits, quotas, and server-side constraints determine whether 10x parallelism is realistic.
- Should remote extraction use a distinct extractor name (e.g., `tree-sitter-remote`) to avoid cache collisions with local extraction of the same repo?
- If SCIP export requires Sourcegraph Enterprise tier, how does this affect the user base?

### Deferred

- Should the claims DB be the unit of sharing (ship SQLite files), or should there be an API layer for real-time shared access?
- How should multi-tenant access control work if the MCP server serves multiple teams?

## Research Provenance

Three independent research agents contributed:

- **Prior Art & Industry Patterns**: Found that every production code intelligence system (Sourcegraph, Kythe, GitHub) uses clone-then-index. GitHub's stack-graphs (closest prior art) was archived in 2025. The hybrid approach (shallow clone for initial, API for incremental) is genuinely novel. Tree-sitter needs zero adaptation for remote content — `sitter.ParseCtx` takes `[]byte`.

- **First-Principles Technical Architecture**: Read the codebase and identified that the SCIP importer is the most important piece (already maps SCIP to claims DB). The Sourcegraph client's single-goroutine serialization is a hidden concurrency bottleneck. The cache layer is already content-hash based and remote-compatible. The Go deep extractor cannot work remotely.

- **User Experience & Deployment Model**: Found that the enrichment pipeline is "almost a complete remote extraction pipeline aimed at the wrong target" — existing cost estimation, batching, and budget infrastructure can be reused. Identified the agent-as-operator persona (agent triggers its own extraction) as the most natural Sourcegraph user workflow.

### Convergence Points

All three agents agreed on: FileSource abstraction is small (one `os.ReadFile` to replace), incremental via `compare_revisions` is viable, full initial extraction via API is impractical (use clone or SCIP).

### Key Divergence

SCIP-first vs tree-sitter-first for remote. Architecture strongly favored SCIP; UX favored reusing existing tree-sitter infrastructure. Resolution: SCIP is the fast path when available, tree-sitter is the universal fallback.

### Convergence Debate

Three positions debated: SCIP-first pragmatist, tree-sitter universalist, hybrid minimalist. The debate converged on a gate-then-branch architecture:

- **Resolved:** FileSource interface ships regardless (all agree). Shallow clone is the universal bootstrap (all agree). Tree-sitter-over-MCP for incremental (all agree). SCIP must be verified before committing to it (all agree).
- **Key resolution:** SCIP verification spike (2-3 days) gates the Phase 1 architecture. If verified, SCIP is primary initial path for indexed languages. If not, shallow clone is primary and SCIP defers to Phase 2. This eliminates the risk of building on unverified assumptions while preserving the SCIP path's data quality advantage.
- **Strongest arguments:** SCIP advocate showed tree-sitter produces 6/10 predicates (missing `implements`, `has_kind`, `has_signature`, `encloses`). Tree-sitter advocate showed SCIP export is the PRD's own unresolved critical question. Hybrid advocate proposed the gate-then-branch synthesis that resolved the tension.

## Risk Annotations (from Premortem — 5 independent failure agents)

### Top Risks (sorted by risk score)

| #   | Failure Lens       | Severity | Likelihood | Score | Root Cause                                                                                      | Top Mitigation                                                          |
| --- | ------------------ | -------- | ---------- | ----- | ----------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| 1   | Technical Arch.    | Critical | High       | 12    | Per-file extraction can't maintain cross-file relationships (imports, implements) incrementally | Reverse-dependency index: re-extract dependents of changed files        |
| 2   | Integration/Dep.   | Critical | High       | 12    | Pre-1.0 Sourcegraph MCP pinned without contract tests or tool discovery                         | Contract test suite + tool discovery via listTools                      |
| 3   | Operational        | Critical | High       | 12    | Single-process architecture with no resource budgeting at 500+ repos                            | Global resource budget: cap watchers, shared cache pool, per-repo state |
| 4   | Scale & Cost       | Critical | High       | 12    | DBPool cap, single-goroutine MCP client, invisible incremental costs                            | Multiplexed MCP client, dynamic pool sizing, cumulative cost tracking   |
| 5   | Scope/Requirements | High     | Medium     | 6     | Phase 1 ships manual CLI for persona that needs agent-autonomous operation                      | Ship request_extraction MCP tool first, validate demand                 |

### Cross-Cutting Themes

**Theme 1: Single-goroutine SourcegraphClient is the universal bottleneck** (Technical, Operational, Scale)
The `SourcegraphClient` serializes all MCP calls through one worker goroutine. At 500+ repos, this becomes the binding constraint. Fix: request-ID multiplexing over a single MCP subprocess (JSON-RPC supports it, the client doesn't implement it). One subprocess at 10 concurrent requests uses ~300MB vs 3GB for 10 pooled subprocesses.

**Theme 2: Per-file extraction breaks cross-file correctness** (Technical, Scale)
File-level diffs can't maintain import/implements claims. When package A adds a new export, package B (which imports it) doesn't appear in the diff. Over months of incremental-only extraction, dependency data drifts. Fix: reverse-dependency re-extraction or periodic full extraction.

**Theme 3: No cost visibility for incremental operations** (Scale, Operational)
Cost estimation gates full extraction but not incremental. At 800 repos × 3 commits/day × 30 files: $6,700/month accumulated silently. Fix: cumulative tracking, budget caps, per-repo cost attribution.

**Theme 4: The user persona may be wrong** (Scope)
Sourcegraph users already have code intelligence. The manual CLI may have no audience. The actual product may be `request_extraction` — agent-autonomous context maintenance. Fix: validate demand before full FileSource refactor.

### Mitigations Promoted to Requirements

**Must-Have additions (Phase 1):**

- **Reverse-dependency re-extraction**: When `DiffBetween` reports changed files, also re-extract files that import changed exports. Uses existing `imports` claims to build the reverse index. (Risks #1, #4)
- **MCP tool discovery via `listTools`**: During client initialization, enumerate available tools by capability rather than hardcoded names. Fail loudly if expected tools are missing. (Risk #2)
- **Contract test against real Sourcegraph**: Weekly CI job exercising `read_file`, `list_files`, `compare_revisions` with response shape assertions. (Risk #2)
- **Zero-change staleness alert**: If `DiffBetween` returns zero changes for a repo whose HEAD SHA changed, log warning rather than silently skipping. (Risk #2)
- **Cumulative cost tracking**: Track MCP calls per day/week in status file. `--daily-budget` flag pauses extraction when exceeded. Model steady-state cost: `repos * commits/day * files/commit * $0.003`. (Risk #4)

**Should-Have additions (Phase 1):**

- **Multiplexed MCP client**: Refactor `SourcegraphClient` for concurrent in-flight requests via JSON-RPC request IDs over a single subprocess. (Risks #1, #3, #4)
- **Global resource budget for multi-repo**: Cap concurrent watchers (default 50), shared cache pool (default 4GB), per-repo state files. `--max-repos` flag with default 50. (Risk #3)
- **Periodic full re-extraction**: Weekly full extraction for remote repos to bound incremental drift. Staleness metric tracking cycles since last full extraction. (Risk #1)
- **`request_extraction` MCP tool promoted to Phase 1**: Validate agent-operator persona early. Wire shallow-clone behind an MCP tool agents can call for missing/stale repos. (Risk #5)

Full failure narratives: see `docs/design/premortem_remote_extraction.md`
