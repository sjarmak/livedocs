# PRD: Change-Triggered Semantic Enrichment

## Problem Statement

live_docs extracts structural claims from source code and can enrich them with semantic context (purpose, usage patterns, complexity, stability) via Sourcegraph tools. But enrichment is currently a manual batch operation — `livedocs enrich` must be run explicitly, with no connection to the file-watch loop that keeps structural claims fresh. Semantic claims go stale silently when code changes, and there is no first-run experience that guides users from structural extraction to semantic enrichment.

The system needs two modes: an initial full-corpus enrichment for repo onboarding, and change-triggered incremental enrichment that keeps semantic claims fresh automatically as code evolves. Both modes must serve human CLI workflows and AI agents querying `describe_package`.

## Goals & Non-Goals

### Goals

- Provide an `--initial` enrichment mode that enriches all public symbols with no budget cap
- Wire enrichment into the `livedocs watch` loop as an opt-in async pass triggered by file changes
- Accumulate changes in a batch window (60s default) to coalesce rapid edits before enriching
- Record enrichment failures as retryable tombstones so failed symbols aren't silently skipped
- Surface enrichment freshness and coverage in describe_package output

### Non-Goals

- On-demand enrichment triggered by MCP queries (too expensive; deferred to v2)
- Recursive blast radius beyond depth 1 for incremental mode (deferred to Phase 2)
- Export-signature early cutoff (deferred to Phase 2 — design data model now, implement later)
- Supporting non-Sourcegraph enrichment backends (design for it, implement Sourcegraph only)
- Real-time sub-second enrichment latency (Sourcegraph calls are 10-30s; async is acceptable)

## Phased Delivery

### Phase 1 — Ship the Bridge

The minimum system that connects watch to enrich with correct incremental behavior. Focus on: the batch-window queue, the CLI flags, the pipeline plumbing, and tombstones.

### Phase 2 — Optimize for Scale

Add signature-hash early cutoff (avoid unnecessary Sourcegraph calls when only function bodies change) and reverse-dep blast radius expansion (re-enrich dependents of changed exports). Requires empirical cost data from Phase 1 to validate ROI.

## Requirements

### Must-Have (Phase 1)

- Requirement: `--initial` flag on `livedocs enrich` that removes budget and max-symbols caps
  - Acceptance: `livedocs enrich --data-dir data/claims/ --initial` enriches all public symbols across all repos with no budget or symbol count limit. `--initial` sets Budget=0 and MaxSymbols=0 internally. `go build ./...` succeeds. `livedocs enrich --help` shows the flag.

- Requirement: Pipeline carries changed file paths through to callers
  - Acceptance: `pipeline.Result` struct has a `ChangedPaths []string` field populated after `Run()`. The watch loop receives the list of changed files from each extraction cycle. A test verifies `ChangedPaths` is populated when files change.

- Requirement: `EnrichOpts.SymbolIDs` filter for incremental enrichment
  - Acceptance: When `SymbolIDs` is non-empty, `Enricher.Run()` enriches only those symbol IDs (skipping `selectSymbols` and `rankByReverseDeps`). When empty, behavior is unchanged. A test verifies that passing specific symbol IDs enriches exactly those symbols.

- Requirement: Enrichment queue with debounce
  - Acceptance: The enrichment goroutine uses a buffered Go channel. After receiving symbol IDs from the watch loop, it waits for a configurable debounce period (default 5s, `--enrich-debounce` flag) after the last event before processing. Rapid edits coalesce into a single enrichment batch. Content-hash caching provides execution-level deduplication. If the channel is full, new batches are dropped with a log warning. A test verifies: debounce coalesces burst events, drop-on-full does not block the watch loop.

- Requirement: `--enrich` flag on `livedocs watch` that enables async incremental enrichment
  - Acceptance: `livedocs watch --data-dir data/claims/ --enrich` runs the watch loop normally and, after each structural extraction cycle, sends changed file paths to the enrichment goroutine via channel. The enrichment goroutine resolves file paths to symbol IDs and calls `Enricher.Run()` with `SymbolIDs` set. Without `--enrich`, behavior is unchanged. Enrichment never blocks the watch poll cycle. `go build ./...` succeeds.

- Requirement: Enrichment failure tombstones for retry
  - Acceptance: When a symbol fails enrichment (Sourcegraph error, bad response), a claim is inserted with `predicate: "enrichment_failed"`, `claim_tier: "meta"`, `extractor_version` set to the content hash at failure time. `isCacheHit` treats tombstones as cache misses, ensuring the next enrichment run retries failed symbols. A test verifies: (1) tombstone is created on failure, (2) subsequent run retries the symbol, (3) successful enrichment replaces the tombstone.

### Should-Have (Phase 1)

- Requirement: `livedocs init` prints enrichment next-step guidance
  - Acceptance: After `livedocs init` completes, stdout includes a message like "To add semantic context: export SRC_ACCESS_TOKEN=... && livedocs enrich --data-dir .livedocs/ --initial". Only printed when SRC_ACCESS_TOKEN is not already set.

- Requirement: Enrichment status in `describe_package` output
  - Acceptance: `describe_package` output includes one of: "Enriched at <date> (<age>)" when semantic claims exist, "Not yet enriched" when no semantic claims exist for the package, or "Enrichment stale: source changed since <date>" when content hash diverged. A test verifies each state renders correctly.

### Must-Have (Phase 2)

- Requirement: Export-signature early cutoff — skip enrichment when exported interfaces are unchanged
  - Acceptance: After structural extraction, the enrichment bridge compares the new structural claims for exported symbols against the previous extraction. If signatures/types are identical, the symbol is not queued for enrichment. Only symbols with actually-changed exports are enriched. A test verifies that a body-only change (no signature change) does not trigger re-enrichment.

- Requirement: Blast radius computation via reverse-dependency expansion
  - Acceptance: A `GetReverseDepSymbols(importPath string) ([]db.Symbol, error)` method on `ClaimsDB` returns all symbols in packages that import the given path. When a symbol's exported signature changes, its direct importers are also queued for re-enrichment. A test verifies that changing an exported type triggers re-enrichment of symbols in dependent packages.

- Requirement: `GetReverseDepSymbols` uses targeted SQL instead of loading all import claims
  - Acceptance: The query is `SELECT DISTINCT s.* FROM symbols s JOIN claims c ON c.subject_id = s.id WHERE c.predicate = 'imports' AND c.object_text = ?` (or equivalent), not a full-table scan. A benchmark shows it completes in <10ms for repos with 10,000+ claims.

### Should-Have (Phase 2)

- Requirement: Enrichment coverage in `list_packages` output
  - Acceptance: `list_packages` response includes `enriched_count` and `total_count` fields so agents can assess corpus-wide enrichment coverage.

### Nice-to-Have

- Requirement: TTL-based re-enrichment for long-lived semantic claims
  - Acceptance: Claims older than 30 days are treated as stale by `isCacheHit` even if content hash matches. Configurable via `--max-claim-age` flag.

- Requirement: Enrichment progress reporting during long initial runs
  - Acceptance: During `livedocs enrich --initial`, stdout shows progress updated every 10 symbols.

- Requirement: `enrich_package` MCP tool for on-demand agent-triggered enrichment
  - Acceptance: `enrich_package(repo, import_path)` triggers enrichment for a single package. Registered in multi-repo mode.

## Design Considerations

**Channel + debounce architecture**: The watch loop sends changed file paths to a buffered Go channel. The enrichment goroutine applies a 5-second debounce (waits 5s after last event before processing), coalescing burst edits into one batch. Content-hash caching provides execution-level deduplication — even if the same file is sent multiple times, unchanged symbols are skipped. Channel drop-on-full ensures the watch loop never blocks. Process restart loses the channel contents, but the next watch poll re-detects changes and re-queues naturally.

**Why debounce over batch-window**: The convergence debate considered a 60-second SQLite accumulation table but rejected it. The 60s window creates latency uncertainty for the single-file case (developer saves one file, waits a minute). A 5-second debounce captures burst-edit coalescing with much lower latency. The content-hash cache already handles execution-level deduplication, making queue-level deduplication redundant. The channel is simpler than a SQLite table and avoids adding a write path to the watch loop's fast path.

**Two-tier async**: Structural extraction runs synchronously in the watch loop (fast, tree-sitter, <1s per file). Enrichment runs asynchronously in a separate goroutine on the debounce ticker. The watch loop never blocks on enrichment.

**Tombstone claims**: Enrichment failures are recorded as meta-tier claims. The content hash in the tombstone's `ExtractorVersion` ensures tombstones auto-invalidate when the source changes. This prevents both silent gaps (failed looks like never-tried) and permanent retry loops (source changed, failure may be resolved).

**Concurrency model**: SQLite WAL mode supports concurrent reads during writes. The `txMu` mutex serializes Go-level transactions, which is fine given enrichment's slow cadence. Structural extraction and enrichment share the same `ClaimsDB` instance.

**Phase 2 early cutoff**: When implemented, the enrichment bridge will hash exported symbols' structural claims before and after extraction. If the hash matches, no enrichment is queued. This avoids the $0.05/call cost of re-enriching after body-only edits. Deferred to Phase 2 because it requires empirical data on what fraction of edits actually change exported signatures.

## Risk Annotations (from Premortem — 5 independent failure agents)

### Top Risks (sorted by risk score)

| #   | Failure Lens       | Severity | Likelihood | Score | Root Cause                                                                                                     | Top Mitigation                                                                |
| --- | ------------------ | -------- | ---------- | ----- | -------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| 1   | Scale & Cost       | Critical | High       | 12    | Single-process serialization (2-6 calls/min) can't keep pace; file-level hash invalidates all symbols per edit | Symbol-level hashing; parallel workers; dollar budget with cold-cache warning |
| 2   | Technical Arch.    | Critical | High       | 12    | Unbounded enrichment queue shares write lock with extraction; no admission control                             | Separate write paths; batch size cap; in-flight guard                         |
| 3   | Integration/Dep.   | Critical | High       | 12    | Unpinned Sourcegraph subprocess; pre-1.0 mcp-go; no schema validation                                          | Version pin; response validation; startup probe                               |
| 4   | Operational        | Critical | High       | 12    | Long-running daemon with no resource bounding, WAL management, or health checks                                | WAL checkpoint timer; flock; tombstone escalation; status file                |
| 5   | Scope/Requirements | Critical | High       | 12    | Enriches what just changed (developer has context) but not what agents need (stable untouched files)           | On-demand describe_symbol path; source-code fallback                          |

### Cross-Cutting Themes

**Theme 1: File-level granularity is wrong** (Technical, Scale)
pending_enrichment keys on (file_path, repo). Content-hash cache is per-file. But enrichment operates per-symbol. Any edit to a file with 180 exports triggers 720 calls. Solution: symbol-level content hashing — hash the symbol signature/body, not the whole file.

**Theme 2: Single serialized subprocess is an architectural ceiling** (Technical, Scale, Operational)
At 2-6 calls/minute, initial enrichment of kubernetes/kubernetes (14,000 symbols) takes 13 days. Not an optimization — a structural limit. Solution: configurable worker pool of N SourcegraphClient instances.

**Theme 3: No observability for a long-running daemon** (Operational, Scale)
No cost tracking, no queue depth alerting, no WAL monitoring, no health checks. Solution: status file, budget tracking, queue depth cap, WAL checkpoint management.

**Theme 4: Producer-push model misses the demand-pull use case** (Scope)
The system enriches recently-changed code. Agents need enrichment for stable, unfamiliar code they're about to modify. Solution: on-demand single-symbol enrichment path, even if limited.

### Mitigations Promoted to Requirements

**Must-Have additions (Phase 1):**

- **Symbol-level content hashing**: Hash exported symbol signatures via tree-sitter, not file content. Cache invalidation should be per-symbol, not per-file. (Risks #1, #2)
- **Cold-cache cost warning**: Before `--initial`, count symbols, estimate calls/cost/time, require `--confirm` to proceed. (Risk #1)
- **Version pin for Sourcegraph MCP**: `npx -y @sourcegraph/mcp@0.3.x`, not floating latest. (Risk #3)
- **Response schema validation**: Reject deepsearch responses missing expected fields rather than silently accepting empty results. (Risk #3)
- **Separate write paths**: Use independent mutexes (or separate DB connections) for structural extraction vs. enrichment writes. Extraction must never block on enrichment. (Risk #2)
- **Batch size cap**: Limit each enrichment batch to N symbols (e.g., 50). If pending queue exceeds threshold, process highest-priority symbols only. (Risk #2)
- **In-flight guard**: Don't dispatch a new enrichment batch if the previous one is still running. (Risk #2)

**Should-Have additions (Phase 1):**

- **Tombstone escalation**: After N consecutive failures for the same symbol, mark permanently failed until source changes. (Risk #4)
- **WAL checkpoint management**: Periodic forced WAL checkpoints on a separate timer, independent of enrichment ticker. (Risk #4)
- **Status file**: Write `.livedocs-status.json` with queue depth, cost spent, last enrichment time, Sourcegraph restart count. (Risk #4)

**Should-Have additions (Phase 2):**

- **Parallel enrichment workers**: Configurable pool of N SourcegraphClient instances (default 4). (Risks #1, #2)
- **Dollar-denominated budget**: Track estimated cost per call. `--max-cost` flag. (Risk #1)
- **On-demand symbol enrichment**: `describe_symbol` MCP tool that triggers single-symbol enrichment on query. (Risk #5)

### Resolved Open Questions (from premortem)

- **Symbol vs. file granularity**: Symbol-level hashing is mandatory for cost control. File-level is insufficient.
- **Daemon deployment model**: The design must account for long-running operation. Add WAL management, flock coordination, and health monitoring.
- **Sourcegraph version stability**: Pin the MCP server version. Add response validation. Add startup probe.

## Open Questions

### Resolved by Convergence

- **Queue persistence (channel vs. SQLite)**: SQLite table. Durability across restarts is worth the small complexity cost, and the table doubles as the deduplication mechanism.
- **Per-event vs. batch**: Batch with 60s window. Developers edit in bursts; per-event is wasteful.
- **Early cutoff timing**: Phase 2. Design the data model in Phase 1 (ChangedPaths, SymbolIDs) so Phase 2 is additive, not a rewrite.

### Still Open

- What is the right default debounce interval? 60s is a guess — needs empirical data from real watch sessions.
- Should `--enrich` on watch require SRC_ACCESS_TOKEN at startup, or lazily check on first enrichment attempt?
- For Phase 2 early cutoff, what constitutes an "exported signature change"? Hash of all structural claims for exported symbols, or just signature/type predicates?
- Should the `pending_enrichment` table live in each per-repo `.claims.db` or in a shared coordination DB?

## Research Provenance

Three independent research agents contributed:

- **Prior Art & Industry Patterns**: Found that Salsa/red-green (rust-analyzer), Bazel's early cutoff, and LSP two-tier architecture all validate the proposed design. Key insight: early cutoff on output equality (not input) is what makes incrementality practical. Semantic code indexing (SCIP, Kythe) is still fully batch — live_docs' per-symbol incrementality is novel.

- **First-Principles Technical Architecture**: Read the codebase and found that `pipeline.Result` discards `ChangedPaths` (one-line fix needed), `imports` claims provide blast radius data, WAL mode handles concurrent access, and `DeepExtractFn` callback is the pattern template. Confirmed initial and incremental share `Enricher.Run()` with different `EnrichOpts`.

- **User Experience & Workflow Design**: Identified the onboarding gap between init and enrich, the silent failure gap (failed enrichment looks like cache hit), and missing freshness metadata. Proposed tombstones, enrichment status in describe_package, and `--initial` flag.

### Convergence Debate

Three positions debated: minimal-bridge (ship simple), full-incremental (build complete), batch-window (accumulate and process). The debate converged on a phased approach using the batch-window architecture:

- **Phase 1**: Batch-window queue (SQLite table + 60s debounce), CLI flags, SymbolIDs filter, tombstones
- **Phase 2**: Signature-hash early cutoff, reverse-dep blast radius, enrichment coverage metrics
- **Key resolution**: Batch-window beats per-event because developers edit in bursts. Early cutoff deferred because it's highest complexity and needs cost data to validate ROI.
