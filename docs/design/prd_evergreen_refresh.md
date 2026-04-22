# PRD: Evergreen Deep-Search Document Refresh

**Status:** Draft — pre-research, pre-premortem. Review before `/prd-build`.
**Related:** `prd_deep_search_mcp.md` (Sourcegraph enrichment), `prd_change_triggered_enrichment.md` (watch→enrich bridge), `prd_staleness_hardening.md`, `prd_staleness_wiring.md`, `prd_realtime_extraction.md`

---

## Problem Statement

Deep-search queries against Sourcegraph (via the `SourcegraphClient` and `deepsearch` tool) produce prose answers that are useful to humans and agents as durable documentation — architecture explanations, "how does X work across these repos," integration playbooks. Today these answers are disposable: a user runs a query, reads the answer, and either re-runs the same query later (paying full deep-search cost every time) or trusts a stale copy with no signal that the underlying code has changed.

live_docs already owns the primitives needed to solve this at the document layer: per-symbol content hashes, change-triggered enrichment, drift detection, tombstones, reverse-dep fan-in ranking, and the `SourcegraphClient` that executes the deep-search itself. What's missing is a **document-granularity refresh loop** that treats a saved deep-search answer as a materialized view over the claims DB and fires a refresh — or an alert — when its dependencies change.

The central design move is to record a **dependency manifest** alongside each saved document (the set of `{symbol_id, content_hash_at_render, repo, commit_sha}` tuples cited during extraction) and reuse the existing change-triggered pipeline to diff that manifest against current claims. Refresh becomes a specific instance of change-triggered enrichment where the work unit is a document, not a symbol.

## Goals

- Let users save a deep-search query+answer as an **evergreen document** with a captured dependency manifest.
- Detect when a document's manifest has drifted from current claims, classified by severity (hot / warm / cold / orphaned).
- Expose staleness as an alert surface (MCP tool, CLI, optional webhook) without auto-spending on refresh.
- Allow opt-in auto-refresh with dollar-denominated budgets and tiered trigger policies once the alert path has produced real cost telemetry.
- Reuse the existing `Enricher` debounce/batch pipeline and `SourcegraphClient` as the refresh executor — no parallel refresh infrastructure.

## Non-Goals

- **Real-time (<1s) refresh** — deep-search calls are 10–30s; async is acceptable, same as `prd_deep_search_mcp.md`.
- **Generalized cached-query service** — this is scoped to deep-search / enrichment-derived documents, not arbitrary MCP tool output.
- **Rich document editing** — documents are immutable rendered outputs; human edits should be persisted in a separate overlay layer (deferred).
- **Semantic-only change detection in v1** — structural-hash invalidation misses semantic-only shifts (same limitation `prd_deep_search_mcp.md` already acknowledges). We surface this in the UI rather than try to solve it.
- **Distributed refresh coordination** — single-node orchestrator in v1. Multi-worker coordination deferred.

## Phased Delivery

### Phase 1 — Alert-First (Ship the Dependency Model)

The minimum system that lets users save documents, surfaces staleness clearly, and requires a manual button-press to refresh. Proves out the manifest model on real queries before spending agent $ autonomously.

### Phase 2 — Auto-Refresh with Dollar Budgets

Add opt-in auto-refresh policies per document, dollar-denominated budgets, and tiered triggers. Requires Phase 1 cost telemetry to set safe defaults.

### Phase 3 — Cross-File Semantic Drift (Deferred)

Extend staleness detection to cover caller-intent shifts that don't move a callee's content hash. Reuses the `--semantic` drift detection path from `prd_deep_search_mcp.md` should-haves.

---

## Requirements

### Must-Have (Phase 1)

- **`deep_search_documents` table + schema migration**
  - Acceptance: New table with columns `id`, `query`, `rendered_answer`, `dependency_manifest` (JSON), `created_at`, `last_refreshed_at`, `refresh_policy` (`alert`/`manual`/`auto`), `max_age_days` (default 30), `status` (`fresh`/`stale`/`orphaned`/`refreshing`). Migration is forward-only, reversible-by-drop. `go test ./db/...` passes with new fixtures.

- **Manifest builder at query time**
  - Acceptance: `SourcegraphClient.Complete()` (or a wrapper) records, for every deep-search call made through the evergreen path, the set of `{symbol_id, content_hash_at_render, repo, commit_sha, file_path}` tuples resolved during the two-pass extraction. When citations can't be attributed to specific symbols, fall back to `{repo, commit_sha}` granularity with an explicit `fuzzy: true` flag on the manifest entry. Unit test verifies both precise and fuzzy paths.

- **Evergreen staleness detector**
  - Acceptance: New `evergreen/` package with `Detect(doc *DeepSearchDocument, claims *db.Claims) ([]StalenessFinding, error)`. Diffs `dependency_manifest` against current claims. Emits findings with severity: `hot` (signature change on cited symbol), `warm` (body-only churn), `cold` (adjacent-repo churn or age > `max_age_days`), `orphaned` (cited symbol renamed/deleted). `go test ./evergreen/...` covers each severity transition.

- **`livedocs evergreen` CLI**
  - Acceptance: Subcommands `list`, `save`, `check`, `refresh`, `delete`. `livedocs evergreen check --data-dir data/claims/` runs the detector across all saved documents and prints a staleness report grouped by severity. `livedocs evergreen refresh <doc_id>` manually re-executes the saved query via `SourcegraphClient`, updates the rendered answer, and refreshes the manifest. `--dry-run` supported on `refresh`. `livedocs evergreen --help` lists all subcommands.

- **MCP tools: `evergreen_status` and `evergreen_refresh`**
  - Acceptance: Two new MCP tools registered in `mcpserver/`. `evergreen_status(doc_id?)` returns staleness findings for one or all documents. `evergreen_refresh(doc_id)` kicks off a manual refresh (subject to rate limits, same as `tribal_mine_on_demand`). Repo existence checks applied per the m7v.18 pattern. Tests cover happy path, missing-doc, and rate-limit denial.

- **Orphaned-document quarantine (never-delete semantics)**
  - Acceptance: When a cited symbol is renamed or deleted, the document transitions to `status=orphaned` and refresh is blocked pending human review. Documents are never auto-deleted. Mirrors the tribal `stale`/`quarantined` model. Test verifies: (1) orphaned status set on missing symbol, (2) `evergreen refresh` returns error with remediation message, (3) manifest entry preserved for provenance.

- **Rate limit on `evergreen_refresh`**
  - Acceptance: Reuse the `KeyedLimiter` from `tribal_mine_on_demand` (m7v.22, m7v.24). Default: 1 refresh per doc per 10 minutes, per-session cap N/hour (configurable). Exceeded requests return `ErrRateLimited` (m7v.26 sentinel). Test verifies denial path.

### Should-Have (Phase 1)

- **Freshness badges in rendered output**
  - Acceptance: `rendered_answer` output includes a freshness header showing `last_refreshed_at`, current `status`, and a "what changed" summary (list of symbols with hash drift since last refresh) when status is not `fresh`. Visually distinct from the content body.

- **Signature-hash early cutoff**
  - Acceptance: The detector computes a signature hash per cited symbol (exports, type signatures) separate from content hash. Body-only churn that doesn't change the signature hash classifies as `warm`, not `hot` — matching `prd_change_triggered_enrichment.md` Phase 2 goal. Reduces alert noise.

- **Optional webhook sink**
  - Acceptance: `livedocs evergreen check --webhook=<url>` POSTs staleness findings as JSON when severity ≥ configurable threshold. Idempotent retries on 5xx. No webhook state stored beyond last-send timestamp per doc. Useful for Slack/PagerDuty integration without coupling live_docs to those specific services.

### Must-Have (Phase 2 — gated on Phase 1 telemetry)

- **Dollar-denominated refresh budget**
  - Acceptance: Per-document `budget_usd_per_month` field. Auto-refresh honors the budget; exceeding it degrades to alert-only. Piggybacks on the dollar-budget work flagged in `prd_deep_search_mcp.md` premortem Risk #2.

- **Tiered auto-refresh policy**
  - Acceptance: `refresh_policy=auto` triggers refresh only on `hot` findings by default; `warm` findings batch into a daily refresh; `cold` findings wait for user action. Policy override per-document.

- **Watch-loop integration**
  - Acceptance: When `livedocs watch --evergreen` is enabled, structural extraction cycles fan out to evergreen staleness detection. Stale documents with `refresh_policy=auto` are queued into the same debounced refresh pipeline. Never blocks the watch poll cycle. Mirrors `prd_change_triggered_enrichment.md` Phase 1 watch→enrich bridge.

- **Cold-cache warning on initial batch**
  - Acceptance: First-time `livedocs evergreen check` on a new manifest set estimates total refresh cost and requires `--confirm` to proceed. Matches `prd_deep_search_mcp.md` cold-cache mitigation.

### Should-Have (Phase 2)

- **Per-manifest-entry drift breakdown in MCP output**
  - Acceptance: `evergreen_status` response includes per-symbol drift details (was-hash, current-hash, kind of change) so agents can decide locally whether a refresh is warranted without invoking deep-search.

- **Manifest trimming for precision**
  - Acceptance: When the manifest has >N entries (configurable, default 50), the detector prefers entries with higher reverse-dep fan-in to bound alert-evaluation cost.

### Nice-to-Have

- **Diff-only refresh** — re-render only the sections of the answer that cite drifted symbols (requires structured rendered output; deferred until answer format stabilizes).
- **Scheduled refresh** — cron-style `refresh_policy=scheduled` with explicit cadence, independent of drift detection.
- **Shared documents** — multi-user saved documents with refresh ownership. Out of scope for a single-node product; flag for future.

---

## Design Considerations

**Manifest fidelity vs. cost.** Deep-search prose doesn't always cite specific symbols. The manifest builder must be best-effort: precise attribution when Sourcegraph tools return symbol references, fuzzy `{repo, commit_sha}` fallback otherwise. Fuzzy entries still trigger coarse drift detection ("the repo advanced 200 commits; this answer may be stale") but can't produce per-symbol diffs.

**Refresh as a document-scoped change-triggered enrichment.** The existing `Enricher.Run(ctx, EnrichOpts{SymbolIDs: ...})` seam is almost the right shape — swap `SymbolIDs` for a `DocumentIDs` field and have the document path expand to its manifest's symbol set internally. Keeps one pipeline, not two.

**Staleness ≠ wrongness.** A stale document might still be correct. The detector reports drift, not invalidity. The refresh itself (via deep-search) is what determines whether the answer actually needs to change. This matters for alert-noise budgeting: `warm` findings should be batched, not paged.

**Structural-hash coupling is a known gap.** Semantic-only changes (a caller's intent shifts without the callee's hash moving) won't be caught in v1. This is the same limitation `prd_deep_search_mcp.md` already acknowledges for semantic claims. Disclose it in the freshness UI; don't pretend to solve it.

**Immutable rendered answers, append-only history.** Each refresh writes a new `rendered_answer` revision (old revisions preserved). This gives users "what changed?" visibility and preserves provenance. Disk cost is bounded by a configurable revision cap (default: last 5 refreshes per doc).

**ZFC boundary.** Severity classification is arithmetic over hash diffs, reverse-dep counts, and ages — mechanical, not semantic. Stays in code. Deep-search re-execution (the refresh itself) is delegated to the model, as it already is.

## Open Questions

- Does the `SourcegraphClient` currently expose enough citation metadata to build a precise manifest, or do we need to extend the two-pass extraction to record symbol resolutions? (Check `sourcegraph/client.go` + `semantic.Generator`.)
- Should orphaned documents be auto-archived after a configurable window, or require explicit user action forever?
- What's the right MCP surface for subscribing to staleness events vs. polling `evergreen_status`? (SSE transport is already supported per architecture notes.)
- Manifest storage cost: at N documents × M citations × revision history, is SQLite still the right substrate, or do we need a separate artifact store?
- How do we handle documents whose query references a private repo that later becomes inaccessible to the current token? (Treat as orphaned? Separate `status=unauthorized`?)

## Risk Preview (pre-premortem)

Risks likely to surface in a formal premortem, carried forward from related PRDs:

1. **Cost runaway on auto-refresh** — premortem Risk #2 from `prd_deep_search_mcp.md` applies directly. Phase 1 alert-first deployment is the primary mitigation.
2. **Manifest-fidelity trap** — if fuzzy fallbacks dominate, staleness detection becomes "the repo moved" noise. Need telemetry on precise-vs-fuzzy ratios early.
3. **Orphan-flood on refactors** — a large rename PR could orphan hundreds of documents at once. Batch-quarantine with a single aggregate alert, not N paging alerts.
4. **Refresh-storm on signature changes** — a widely-cited exported type changing signature could fan out to many documents. Reverse-dep ranking + daily-batch policy for warm findings mitigates.
5. **Confidence in stale answers** — users may trust a `cold`-aged document as fresh. Require prominent freshness header (Should-Have: badges) and surface staleness in every MCP response that returns a document.

---

## Provenance

Initial design synthesized from conversation on 2026-04-22. Integrates:
- Manifest model inspired by `prd_change_triggered_enrichment.md` (content-hash caching, tombstones).
- Never-delete transitions borrowed from tribal staleness semantics.
- Severity tiering inspired by drift detection findings in `drift/`.
- Phased alert-first rollout informed by `prd_deep_search_mcp.md` premortem cost analysis.
