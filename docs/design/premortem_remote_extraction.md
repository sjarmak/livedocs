# Premortem: Remote Extraction via Sourcegraph MCP

## Risk Registry

| #   | Failure Lens             | Severity | Likelihood | Score | Root Cause                                                                                            | Top Mitigation                                                          |
| --- | ------------------------ | -------- | ---------- | ----- | ----------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| 1   | Technical Architecture   | Critical | High       | 12    | Per-file extraction model can't maintain cross-file relationships (imports, implements) incrementally | Reverse-dependency index: re-extract dependents of changed files        |
| 2   | Integration & Dependency | Critical | High       | 12    | Pre-1.0 Sourcegraph MCP pinned without contract tests or tool discovery                               | Contract test suite + tool discovery via listTools                      |
| 3   | Operational              | Critical | High       | 12    | Single-process architecture with no resource budgeting at 500+ repos                                  | Global resource budget: cap watchers, shared cache pool, per-repo state |
| 4   | Scale & Cost             | Critical | High       | 12    | DBPool cap, single-goroutine MCP client, invisible incremental costs                                  | Multiplexed MCP client, dynamic pool sizing, cumulative cost tracking   |
| 5   | Scope & Requirements     | High     | Medium     | 6     | Phase 1 ships manual CLI for persona that needs agent-autonomous operation                            | Ship request_extraction MCP tool first, validate demand                 |

## Cross-Cutting Themes

### Theme 1: Single-goroutine SourcegraphClient is the universal bottleneck

**Surfaced by:** Technical Architecture, Operational, Scale & Cost (3/5 lenses)

The `SourcegraphClient` serializes all MCP calls through one worker goroutine processing `reqCh` sequentially. At 500+ repos with concurrent extraction + enrichment, this becomes the binding constraint. Pooling multiple subprocesses trades memory (3-5GB at 10x) for throughput. The correct fix is request-ID multiplexing over a single MCP subprocess — the JSON-RPC protocol supports concurrent requests, the client doesn't implement it.

### Theme 2: Per-file extraction model breaks cross-file correctness

**Surfaced by:** Technical Architecture, Scale & Cost (2/5 lenses)

File-level diffs (`compare_revisions` returns changed file paths) can't maintain cross-file claims. When package A adds a new export and package B imports it, only package A appears in the diff. Package B's import claims become stale. Over months of incremental-only extraction, the claims DB drifts until dependency data is visibly wrong. Needs either reverse-dependency re-extraction or periodic full extraction to bound drift.

### Theme 3: No cost visibility for incremental operations at scale

**Surfaced by:** Scale & Cost, Operational (2/5 lenses)

The cost estimation gate (`--confirm`) only applies to full extraction. Incremental runs accumulate silently. At 800 repos averaging 3 commits/day and 30 files/commit: 74,400 MCP calls/day at $0.003 = $223/day = $6,700/month. Users discover this cost on their bill, not in the tool. Needs cumulative tracking, budget caps, and per-repo cost attribution.

### Theme 4: The user persona may be wrong

**Surfaced by:** Scope & Requirements (1/5 lenses, but high impact)

Sourcegraph users already have go-to-definition, find-references, and symbol search natively. The manual `livedocs extract --source sourcegraph` CLI targets a workflow these users don't perform. The actual unmet need is agent-autonomous context maintenance: agents that detect missing or stale claims and trigger their own extraction. This is Phase 2's `request_extraction` — which may be the real product.

## Mitigations Promoted to Requirements

### Must-Have (Phase 1)

1. **Reverse-dependency re-extraction** — When `DiffBetween` reports changed files, identify files that import the changed files' exports (using existing `imports` claims), and include them in the extraction batch. This bounds cross-file drift without requiring periodic full re-extraction. (Addresses Risks #1, #4)

2. **MCP tool discovery via `listTools`** — During `spawnMCP()` initialization, enumerate available tools via the MCP `listTools` response. Map tools by capability rather than hardcoded name strings. Fail loudly at startup if expected tools (`read_file`, `list_files`, `compare_revisions`) are missing. This survives tool renames across MCP versions. (Addresses Risk #2)

3. **Contract test against real Sourcegraph** — Weekly CI job exercising `read_file`, `list_files`, and `compare_revisions` against a real Sourcegraph instance with response shape assertions. Catches breaking changes within days. (Addresses Risk #2)

4. **Zero-change staleness alert** — If `DiffBetween` returns zero `FileChange` entries for a repo whose HEAD SHA has changed since last extraction, log an explicit warning. Never silently return zero changes. (Addresses Risk #2)

5. **Cumulative cost tracking** — Track total MCP calls per day/week in `.livedocs-status.json`. Add `--daily-budget` flag (default: unlimited) that pauses incremental extraction when exceeded, with operator notification. Model steady-state cost in extraction output: `repos * commits/day * files/commit * $0.003`. (Addresses Risk #4)

### Should-Have (Phase 1)

6. **Multiplexed MCP client** — Refactor `SourcegraphClient` to support N concurrent in-flight requests via JSON-RPC request IDs over a single subprocess, replacing the sequential `reqCh` worker. One subprocess at 10 concurrent requests uses ~300MB vs 3GB for 10 pooled subprocesses. (Addresses Risks #1, #3, #4)

7. **Global resource budget for multi-repo operation** — Cap concurrent watcher goroutines (default 50), shared in-memory cache pool (default 4GB total, not 2GB per repo), per-repo state files replacing the single shared JSON. Add `--max-repos` flag with default 50, requiring explicit opt-in for larger deployments. (Addresses Risk #3)

8. **Periodic full re-extraction** — Weekly full extraction for remote repos to bound incremental drift. Add staleness metric to claims DB tracking cycles since last full extraction. (Addresses Risk #1)

9. **`request_extraction` MCP tool promoted to Phase 1** — Validate the agent-operator persona early. Wire shallow-clone extraction behind an MCP tool that agents can call when they discover a missing or stale repo. This is the actual product for Sourcegraph users. (Addresses Risk #5)

## Full Failure Narratives

### 1. Technical Architecture Failure

The remote extraction project shipped its FileSource abstraction and tree-sitter-over-MCP incremental path on schedule. The SCIP verification spike concluded with "requires Enterprise only," so the team committed to shallow-clone-for-initial, tree-sitter-over-MCP-for-incremental. For small repos this worked. Then monorepos arrived: 15,000-80,000 files, polyglot stacks. Incremental extraction via `compare_revisions` appeared to work — commits touching 5-50 files processed fine. But after two months, accumulated drift between the shallow-clone initial snapshot and the incrementally-patched claims DB became severe. `compare_revisions` returns file-level diffs, not AST-level diffs. When package A added a new export and package B imported it, only package A appeared in the diff. Package B's import claims went stale. Over months, `describe_package` returned visibly wrong dependency lists. Users lost trust.

The second failure was concurrency. The team chose subprocess pooling (10 Node.js MCP processes). On modest VMs (2-4 cores, 4-8GB RAM), this consumed 3-5GB before extraction started. OOM kills forced reduction to 3x concurrency, bringing extraction time back to "hours."

**Root cause:** Per-file extraction model with cross-file claims creates inherent consistency gap.
**Severity:** Critical | **Likelihood:** High

### 2. Integration & Dependency Failure

Sourcegraph 6.0 restructured SCIP export endpoints and changed the MCP tool naming convention (`read_file` → `file.read`). The pinned `@sourcegraph/mcp@0.3` package was deprecated, printing warnings to stderr that corrupted JSON-RPC protocol parsing. The `compare_revisions` output format changed from structured JSON to markdown-formatted diff summary, causing `DiffBetween` to silently return zero changes — a staleness bug undetected for weeks.

**Root cause:** Pre-1.0 external dependency pinned without contract tests or tool discovery protocol.
**Severity:** Critical | **Likelihood:** High

### 3. Operational Failure

500+ repos deployed: shallow clone cleanup failed (SQLite WAL files held open), accumulating 80GB of temp directories. 500 concurrent watcher goroutines each opened a `ClaimsDB`; `DBPool` thrashed at its cap of 20. Enrichment queues (500 goroutines) all funneled into one `SourcegraphClient`, causing permanent enrichment backlog. Shared state file corrupted under concurrent writes. OOM kill triggered full re-extraction of entire corpus.

**Root cause:** Single-process architecture with no resource budgeting deployed at regime-change scale.
**Severity:** Critical | **Likelihood:** High

### 4. Scale & Cost Failure

800 repos × 3 commits/day × 30 files = 74,400 MCP calls/day at $0.003 = $6,700/month. Cost estimation only gated full extraction, not incremental. Customer discovered cost on monthly bill. DBPool thrashed at cap of 20. Single-goroutine MCP client serialized all 800 repos' requests. Enrichment in-flight guard caused cross-repo blocking.

**Root cause:** Three independent bottlenecks (DBPool cap, single-goroutine client, invisible costs) collapsed simultaneously.
**Severity:** Critical | **Likelihood:** High

### 5. Scope & Requirements Failure

Sourcegraph users already have native code intelligence. Nobody runs `livedocs extract --source sourcegraph` manually. The actual unmet need was agent-autonomous context maintenance (`request_extraction`), deferred to Phase 2. Phase 1 shipped a manual CLI workflow for a persona that doesn't exist.

**Root cause:** PRD assumed "eliminating local clones" was the value prop; actual need is "agents autonomously maintaining their own context."
**Severity:** High | **Likelihood:** Medium
