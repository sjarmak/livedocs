# PRD: Sourcegraph Deepsearch Integration for Semantic Enrichment

## Problem Statement

live*docs extracts structural claims from source code using tree-sitter — types, signatures, imports, exports, interface implementations. But it cannot capture \_why* code exists, _how_ it's used, or _how stable_ it is. The claims DB already defines four semantic predicates (`purpose`, `usage_pattern`, `complexity`, `stability`) in `extractor/types.go`, and the renderer already queries and renders them in `describe_package` output — but they are universally empty because no extractor populates them.

Meanwhile, Sourcegraph's MCP server provides `deepsearch` (agentic multi-step research), `find_references` (compiler-accurate usage sites), `nls_search` (semantic NLP search), `commit_search` (temporal analysis), and `compare_revisions`. These tools can answer the exact questions needed to populate semantic claims.

The existing `semantic/` package already implements the enrichment pipeline — `LLMClient` interface, prompt templates, JSON parser, adversarial verifier, and batch processor. The integration is implementing one interface and wiring it to Sourcegraph's MCP tools via mcp-go's built-in client library.

### Key Research Findings

Three independent research agents converged on:

1. **mcp-go v0.46.0 (already a dependency) ships a full MCP client** with stdio transport. `client.NewStdioMCPClient` spawns a subprocess and provides `CallTool(ctx, request)`. Zero new dependencies needed.
2. **The `semantic.LLMClient` interface** (`Complete(ctx, system, user string) (string, error)`) is the exact seam. A `SourcegraphClient` implementing this interface slots into the existing generator, parser, verifier, and batch processor with no changes to those components.
3. **Deepsearch is best used as a context-gathering stage**, not a direct claim generator. Its prose output feeds into the existing LLM prompt pipeline which extracts structured JSON claims. This makes deepsearch failures non-fatal — the generator falls back to structural-only context.
4. **Per-predicate tool routing** maximizes cost-efficiency:
   - `purpose` → `deepsearch` (needs architectural understanding)
   - `usage_pattern` → `find_references` + synthesis (needs usage sites)
   - `stability` → `commit_search` + `compare_revisions` (direct metric, no LLM needed)
   - `complexity` → `deepsearch` (needs cross-file analysis)

## Goals & Non-Goals

### Goals

- Populate the empty semantic claim tier (`purpose`, `usage_pattern`, `complexity`, `stability`) using Sourcegraph MCP tools
- Integrate via the existing `semantic/` package infrastructure — implement `LLMClient`, reuse generator/parser/verifier
- Provide explicit cost controls (budget cap, symbol filtering, content-hash caching)
- Enable richer `describe_package` output and semantic drift detection

### Non-Goals

- Real-time/on-demand enrichment during MCP queries (too expensive; batch only for v1)
- Replacing tree-sitter extraction (structural claims remain the foundation)
- Building a general-purpose Sourcegraph proxy (only the enrichment pipeline calls Sourcegraph)
- Supporting non-Sourcegraph code intelligence backends (design for it, but implement Sourcegraph only)

## Requirements

### Must-Have

- Requirement: `SourcegraphClient` that satisfies `semantic.LLMClient` interface, backed by mcp-go's stdio MCP client
  - Acceptance: A `sourcegraph/client.go` file exists with `type SourcegraphClient struct` implementing `Complete(ctx, system, user string) (string, error)`. It spawns the Sourcegraph MCP server process via `transport.NewStdio`, calls `deepsearch` with the combined system+user prompt, and returns the prose result. `go build ./...` succeeds. A test using a mock MCP server verifies the client lifecycle (spawn, initialize, call, shutdown).

- Requirement: `livedocs enrich` CLI command that runs batch semantic enrichment with cost controls
  - Acceptance: `livedocs enrich --data-dir data/claims/ --budget 100 --max-symbols 200` enriches up to 200 public symbols across repos, stopping at 100 deepsearch calls. Flags: `--data-dir` (required), `--budget` (max enrichment calls, default 100), `--max-symbols` (default 200), `--include-internal` (default false, public only), `--force` (re-enrich even if semantic claims exist), `--dry-run` (preview only). `go build ./...` succeeds.

- Requirement: Enrichment targets public symbols only by default, prioritized by reverse-dependency fan-in
  - Acceptance: The enrichment pipeline queries symbols with `visibility=public` and kinds `type`, `func`, `interface`, `method`. Packages are ranked by `ReverseDepCount` (already computed in `renderer/query.go`). A test verifies that private symbols and non-API kinds (const, var) are excluded by default.

- Requirement: Content-hash caching — skip enrichment for symbols whose structural claims are unchanged since last enrichment
  - Acceptance: Semantic claims are stored with the source file's content hash at enrichment time. If the content hash matches on the next run, the symbol is skipped. `--force` overrides this. A test verifies the skip path.

- Requirement: Semantic claims stored with `ClaimTier: "semantic"`, `Extractor: "sourcegraph-deepsearch"`, and configurable `Confidence`
  - Acceptance: After enrichment, `SELECT * FROM claims WHERE claim_tier = 'semantic' AND extractor = 'sourcegraph-deepsearch'` returns rows with `purpose`, `usage_pattern`, `complexity`, or `stability` predicates. The `describe_package` MCP tool renders these claims in its output (the renderer already handles this).

### Should-Have

- Requirement: Per-predicate tool routing — use cheaper Sourcegraph tools when deepsearch is overkill
  - Acceptance: `stability` claims are generated using `commit_search` (count commits touching the symbol in the last 6 months) instead of deepsearch. `usage_pattern` claims use `find_references` to gather usage sites before LLM synthesis. A configuration or strategy pattern allows overriding which tool handles which predicate.

- Requirement: Semantic drift detection — extend `drift.Detect()` with an optional deepsearch-powered semantic pass
  - Acceptance: `livedocs check --semantic` runs structural drift detection (existing) plus semantic validation for each README section describing package purpose/behavior. Findings of type `SemanticDrift` are reported when README descriptions don't match deepsearch's assessment of code intent. Gated behind `--semantic` flag (expensive).

- Requirement: Connection lifecycle management — long-lived Sourcegraph MCP client with health checking
  - Acceptance: The `SourcegraphClient` spawns the MCP server once and reuses the connection across all enrichment calls in a batch. If the subprocess dies, it is restarted automatically. A test verifies restart-on-failure behavior.

- Requirement: Enrichment telemetry — log per-symbol enrichment status, deepsearch call count, and elapsed time
  - Acceptance: `livedocs enrich` outputs a summary: symbols enriched, symbols skipped (cached), deepsearch calls made, total elapsed time. Per-symbol results are logged at INFO level.

### Nice-to-Have

- Requirement: `research_symbol` MCP tool — expose deepsearch as a user-initiated tool for one-off deep investigation of a symbol
  - Acceptance: `research_symbol(repo="kubernetes", symbol="Informer")` delegates to Sourcegraph deepsearch and returns the prose research summary. Registered in multi-repo mode alongside other tools.

- Requirement: Adversarial verification of semantic claims using the existing `semantic.Verifier`
  - Acceptance: When `--verify` flag is passed to `livedocs enrich`, each generated semantic claim is verified by a second LLM call (existing verifier). Claims that fail verification are discarded. Verification is off by default to reduce cost.

- Requirement: Cross-repo usage pattern aggregation — `usage_pattern` claims aggregate `find_references` results across all repos in the pool
  - Acceptance: For a type like `runtime.Object`, `find_references` is called across all repos that import the package. The synthesized `usage_pattern` claim reflects cross-repo usage, not single-repo.

## Design Considerations

**Two-pass architecture**: Deepsearch returns prose, not structured claims. The integration uses a two-pass approach: (1) call Sourcegraph tools to gather context, (2) feed context into the existing `semantic.Generator` LLM pipeline which extracts structured JSON claims. This reuses the tested parser and makes deepsearch failures non-fatal.

**Cost model**: Each deepsearch call is ~10-30 seconds and consumes Sourcegraph compute. At $0.05/call (estimated), enriching 200 symbols costs ~$10. The `--budget` flag caps total calls. Per-predicate routing reduces cost: `stability` via `commit_search` is free (no LLM), `usage_pattern` via `find_references` + local LLM is cheaper than deepsearch.

**Semantic claim staleness**: Structural claims invalidate when file content hashes change. Semantic claims depend on cross-file context — a function's `purpose` can change if its callers change, even if the function itself doesn't. For v1, semantic claims invalidate alongside structural claims (same content hash). A TTL-based re-enrichment schedule can be added later.

**`semantic/` package integration**: The existing `semantic.Generator`, `semantic.Parser`, `semantic.Verifier`, and `semantic.GenerateBatchFromDB` are designed for exactly this use case. The `LLMClient` interface is the single integration point. No new claim types, DB schema changes, or renderer modifications are needed.

**Sourcegraph MCP server configuration**: The Sourcegraph MCP server binary path and any required auth tokens must be configurable. Propose environment variables: `SOURCEGRAPH_MCP_COMMAND` (default: `npx -y @sourcegraph/mcp`), `SRC_ACCESS_TOKEN`, `SRC_ENDPOINT`.

## Open Questions

- What authentication does the Sourcegraph MCP server require? Is `SRC_ACCESS_TOKEN` sufficient, or does it need an active Sourcegraph instance URL?
- What is the actual latency and cost per deepsearch call at scale? Need empirical data from a 10-symbol prototype.
- Should semantic claims have a separate staleness/invalidation path from structural claims, or is content-hash coupling sufficient for v1?
- How should `Confidence` scores be assigned to deepsearch-derived claims? Options: fixed (0.8), evidence-count-based, or dual-query consistency check.
- Can the Sourcegraph MCP server run without an active Sourcegraph Cloud or self-hosted instance? (i.e., does it work against public repos only?)

## Risk Annotations (from Premortem — 5 independent failure agents)

### Top Risks (sorted by risk score)

| #   | Failure Lens      | Severity | Likelihood | Score | Root Cause                                                                                      | Top Mitigation                                                                 |
| --- | ----------------- | -------- | ---------- | ----- | ----------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| 1   | Technical Arch.   | Critical | High       | 12    | mcp-go stdio client not concurrent-safe; corrupted deepsearch prose poisons claims cache        | Serialize MCP calls; validate deepsearch output before passing to LLM          |
| 2   | Cost Explosion    | Critical | High       | 12    | Budget enforces call count not dollars; deepsearch token fan-out 3-6x higher than estimated     | Dollar-denominated budget; implement per-predicate cheap routing before launch |
| 3   | Output Quality    | Critical | High       | 12    | Adversarial verifier not independent from generator — both consume same deepsearch prose        | Structural verifier (git log, call graph); require null for unsupported fields |
| 4   | Dependency        | Critical | High       | 12    | Sourcegraph subprocess spawned unconditionally; crash takes down entire MCP server              | Lazy spawn; process isolation; capability gate when SRC\_\* env vars absent    |
| 5   | Staleness & Trust | High     | High       | 9     | Semantic claims encode cross-file context but invalidation only tracks direct file hash changes | Enrichment timestamps in rendered output; max claim age (30d) with degradation |

### Cross-Cutting Themes

**Theme 1: The two-pass architecture is fragile at both stages** (Technical, Quality)
Deepsearch returns variable-quality prose. The LLM faithfully extracts claims from bad prose. Neither pass validates the other. The adversarial verifier uses the same prose as ground truth. Solution: structural verification (git log, AST metrics, call graph) instead of LLM-vs-LLM.

**Theme 2: Cost is fundamentally unpredictable** (Cost, Dependency)
Deepsearch is an agentic tool — each call internally fans out to multiple sub-calls with unpredictable token consumption. The budget system counts calls, not cost. Rate limits from Sourcegraph compound latency. Solution: dollar-denominated budgets, per-predicate routing to cheaper tools, cold-cache warnings.

**Theme 3: The Sourcegraph dependency violates local-first** (Dependency, Staleness)
live_docs is designed as local-first with no external dependencies. Adding Sourcegraph as a subprocess makes the entire system fragile for users without Sourcegraph access. Solution: strict process isolation, lazy spawn, graceful degradation to structural-only claims.

**Theme 4: Semantic claims look authoritative but aren't** (Quality, Staleness)
Rendered output shows structural and semantic claims with equal weight. Agents can't distinguish a continuously-maintained structural fact from a months-old semantic snapshot. Solution: enrichment timestamps, confidence scores, visual differentiation in rendered output, max claim age.

### Mitigations Promoted to Requirements

**Must-Have additions:**

- **Serialize MCP client calls**: Do not call `SourcegraphClient` from multiple goroutines. Use a single-goroutine worker with a request channel. (Risk #1)
- **Deepsearch output validation gate**: If deepsearch prose does not contain the target symbol name or repo name, skip claim extraction and emit a low-confidence sentinel. (Risks #1, #3)
- **Process isolation**: Sourcegraph subprocess crash must not propagate to the livedocs MCP server. Lazy spawn on first enrichment call, not at server startup. (Risk #4)
- **Graceful degradation when Sourcegraph unavailable**: `livedocs enrich` without `SRC_ACCESS_TOKEN` prints a clear message and exits 0. `livedocs mcp` never spawns Sourcegraph subprocess. Enrichment is strictly opt-in. (Risk #4)
- **Per-predicate routing before launch**: `stability` via `commit_search` (free), `usage_pattern` via `find_references` + LLM (cheaper). Do not ship with everything routed through deepsearch. (Risk #2)
- **Enrichment timestamp in rendered output**: `describe_package` must show when semantic claims were last enriched, visually distinct from structural claims. (Risk #5)
- **LLM extraction prompt must allow null**: The extraction prompt instructs the LLM to return `null` for fields it cannot support with a direct quote from the deepsearch prose. Null claims are treated as absent. (Risk #3)

**Should-Have additions:**

- **Dollar-denominated budget**: Track estimated cost per call (response size as proxy). Abort batch when dollar estimate crosses threshold. (Risk #2)
- **Cold-cache warning**: On first run against a new corpus, estimate total cost and require `--confirm` to proceed. (Risk #2)
- **Max semantic claim age**: Claims older than 30 days render with a degraded-confidence marker. (Risk #5)
- **Structural verifier**: Compare `stability` claims against git churn rate, `complexity` against AST metrics, `usage_pattern` against static call graph. (Risk #3)
- **Contract test in CI**: Wiremock/recorded-response stub for Sourcegraph MCP wire protocol. Runs without live token. (Risk #4)
- **Version pin for Sourcegraph MCP**: `npx -y @sourcegraph/mcp@0.3.x`, not floating latest. (Risk #4)
- **Concurrency cap**: Default Sourcegraph concurrency to 5, expose `--concurrency` flag. (Risk #2)

### Resolved Open Questions

- **Semantic claim staleness**: Content-hash coupling is insufficient. Add enrichment timestamps + max claim age (30 days) + visual differentiation in rendered output. Cross-file invalidation deferred to v2.
- **Confidence scores**: Require LLM to output null for unsupported fields. Non-null claims get confidence based on deepsearch evidence quality (symbol name present in prose → 0.8, absent → 0.4).
- **Sourcegraph without active instance**: Enrichment is strictly opt-in. Missing env vars → clean skip, not error.

## Research Provenance

Three independent research agents contributed:

- **Prior Art & Integration Patterns**: Discovered mcp-go ships a full MCP client. Found that `deepsearch` returns prose (not structured data), making it a context-gathering stage. Identified `find_references` and `nls_search` as better tools for bulk enrichment. Confirmed industry pattern: structural extraction → context gathering → LLM enrichment.
- **Technical Architecture**: Found the `semantic.LLMClient` interface is the exact integration seam. Designed the `SourcegraphClient` implementation. Proposed `livedocs enrich` CLI with cost controls. Confirmed content-hash caching is reusable from the pipeline.
- **Use Cases & Value Analysis**: Mapped each semantic predicate to specific Sourcegraph tools. Proposed reverse-dependency fan-in prioritization. Designed semantic drift detection. Created concrete example workflows (CRD addition, cache layer modification, README drift).

Convergence: all three agents agreed on mcp-go client integration, batch enrichment, cost controls, and the two-pass architecture. The `semantic/` package's existing infrastructure makes this a surprisingly small integration surface.
