# PRD: Evergreen Document Refresh

**Status:** Draft v2 — revised after audit of sourcegraph evergreen_deepsearch architecture (2026-04-22). Pre-research, pre-premortem. Review before `/prd-build`.
**Supersedes:** v1 (same file, commit 477bac1) — v1 duplicated upstream refresh infra; v2 separates OSS vs. sourcegraph-adapter responsibilities.
**Related:** `prd_deep_search_mcp.md`, `prd_change_triggered_enrichment.md`, `prd_staleness_hardening.md`

---

## Problem Statement

Users accumulate prose research output — deep-search answers, architecture explainers, "how does X work across these repos" writeups — that is valuable as durable documentation but goes stale silently as the underlying code changes. The user today has two unsatisfactory options: re-run the query periodically (paying full cost every time with no signal on whether it's needed), or trust a cached copy blindly.

live_docs already owns the claims database, per-symbol content hashes, drift detection, and the change-triggered enrichment pipeline that make precise staleness detection cheap. The missing piece is a **document-granularity evergreen layer**: store the saved answer with a dependency manifest (the symbols and repo revisions it was derived from) and diff that manifest against current claims to emit a drift signal.

### Two audiences — one capability, two deployments

This capability must serve two distinct audiences without dragging one into the other:

1. **OSS live_docs users** installing alongside any GitHub repo(s). For them, live_docs is the **whole system**: save query → store manifest → detect drift → optionally re-run. The `RefreshExecutor` backing the re-run can be a local deepsearch MCP, a simple "re-run this prompt" wrapper around Claude/Anthropic, or any other backend they configure.
2. **Sourcegraph's evergreen deepsearch product.** Sourcegraph already owns the durable-query model (`evergreen_deepsearch` + `evergreen_deepsearch_versions` tables), a 24h auto-refresh worker, per-user quota, and first-class citations (`sources[]` on each question). What it **lacks** is (a) a structured dependency manifest, (b) drift-tier staleness classification beyond worker state, (c) symbol-precise "what changed" diffs, (d) an MCP surface for agents. live_docs supplies those as an adapter layer.

**The live_docs OSS product ships only the generic capability.** The sourcegraph-specific adapter (ConnectRPC wrapper for `RefreshEvergreenDeepSearch`, `sources[]` → manifest lift, MCP-tool registration into sourcegraph's MCP server, optional upstream schema change) lives on branch **`sj/egds-livedocs`** of the sourcegraph repo, tracked as a separate roadmap but not merged into live_docs. This keeps the open-source product clean while the sourcegraph integration remains shareable with the Sourcegraph team.

### Minimal-footprint principle (binding constraint)

The sourcegraph-side addition on `sj/egds-livedocs` **must be as small as possible** — a thin bridge, not a reimplementation. Target: ~4 files, ~150 lines total. That constraint flows back into live_docs' interface design:

- **No concrete implementations cross the boundary.** live_docs exposes `DocumentStore`, `RefreshExecutor`, and an MCP-tool factory as *interfaces*. The adapter supplies its own `DocumentStore` impl backed by sourcegraph's existing `evergreen_deepsearch` / `evergreen_deepsearch_versions` tables — it does not maintain a parallel SQLite store.
- **The detector is a pure function.** No embedded store, no embedded executor — just `Detect(doc, claimsReader) []Finding`. The adapter passes documents it pulled from upstream tables straight in.
- **The MCP tool handlers are constructor functions.** `NewStatusTool(store, detector)` / `NewRefreshTool(store, executor, limiter)` return handler values the adapter registers into sourcegraph's MCP server via a single call.
- **Anything that would force the adapter to carry live_docs' OSS-specific state (SQLite files, CLI flag parsing, watch-loop integration) is out of the adapter's boundary by construction.**

### What the sourcegraph audit found (evidence for the split)

From audit of `/home/ds/projects/sourcegraph` (2026-04-22):

| Concern | Sourcegraph today | Implication |
|---|---|---|
| Saved/pinned query + revisions | ✅ `internal/database/evergreen_deepsearch.go`, `evergreen_deepsearch_version.go` (state machine: queued/processing/completed/errored/failed/canceled) | Adapter reuses; live_docs OSS builds its own. |
| Auto-refresh loop (24h) | ✅ `cmd/frontend/internal/deepsearchapi/evergreen_refresh_worker.go:241` `enqueueNextRefresh()` | Adapter **defers** to upstream loop; live_docs OSS owns its own. |
| Manual refresh trigger | ✅ ConnectRPC `RefreshEvergreenDeepSearch` | Adapter calls upstream; OSS calls its `RefreshExecutor`. |
| Never-delete on failure | ✅ `HasFailedVersion()` blocks refresh | Both paths respect this. |
| Call-count quota per user/day | ✅ `deepsearch_quota` table | Dollar-budget work (if ever needed) is per-backend; not a live_docs OSS responsibility. |
| Citations on questions (`sources[]` with file+line) | ✅ `DeepSearchQuestion.sources` | Adapter lifts into live_docs' manifest shape. |
| Structured dependency manifest | ❌ Not present | live_docs defines shape; adapter populates; optional upstream schema column. |
| Drift-tier staleness (hot/warm/cold/orphaned) | ❌ Only worker state exists | **Net-new** — built in live_docs generically. |
| MCP tools for evergreen | ❌ MCP server exposes deepsearch but not evergreen | **Net-new** — OSS MCP tools are generic; adapter optionally registers them into sourcegraph's MCP server too. |

## Goals

- Ship a **provider-agnostic** evergreen layer in live_docs: save an answer + manifest, detect drift, refresh on demand via a pluggable `RefreshExecutor`.
- Surface drift as an **alert-first** signal (CLI + MCP) with severity tiers: `hot` / `warm` / `cold` / `orphaned`.
- Keep the sourcegraph-specific integration **out of this repo** — it lives as a sourcegraph branch that depends on live_docs as a library.
- Reuse existing live_docs primitives: claims DB + content hashes, change-triggered enrichment pipeline, `KeyedLimiter`, tribal-style never-delete quarantine semantics, MCP adapter pattern.

## Non-Goals

- **Sourcegraph ConnectRPC adapter in this repo.** That code ships in the sourcegraph branch, not live_docs.
- **Upstream sourcegraph schema changes in this repo.** An optional `dependency_manifest` JSONB column on `evergreen_deepsearch_versions` is discussed in the adapter PRD, not here.
- **Auto-refresh on every drift event in v1.** Alert-first; auto-refresh is Phase 2 and only applies to OSS-path (sourcegraph path always defers to the upstream 24h loop).
- **Semantic-only drift detection in v1.** Caller-intent shifts that don't move the callee's content hash are a known gap, same as `prd_deep_search_mcp.md`. Disclosed in UI, not solved.
- **Rich editing / collaborative documents / multi-user ownership.** Out of scope.

---

## Architecture: the `RefreshExecutor` seam

The central abstraction that enables dual-purpose deployment:

```
┌─────────────────────────────────────────────────────────────────┐
│ live_docs OSS (this repo)                                       │
│                                                                  │
│  evergreen/                                                      │
│  ├── types.go            // Document, ManifestEntry, Finding    │
│  ├── detector.go         // Detect(doc, claimsReader) []Finding │
│  ├── interfaces.go       // DocumentStore, RefreshExecutor,     │
│  │                       //   ClaimsReader, RateLimiter         │
│  ├── mcptools.go         // NewStatusTool, NewRefreshTool       │
│  │                       //   (factory functions, no globals)   │
│  ├── sqlite_store.go     // default DocumentStore impl (OSS)    │
│  └── executors/                                                  │
│      └── deepsearch_mcp.go  // default RefreshExecutor (OSS)    │
│                                                                  │
│  cmd/livedocs evergreen ...  // CLI (OSS only, thin wrapper)    │
│  mcpserver/evergreen.go      // wires factories for OSS server  │
└─────────────────────────────────────────────────────────────────┘
                            ▲
                            │ go-get dependency
                            │ (live_docs as a library)
                            │
┌─────────────────────────────────────────────────────────────────┐
│ sourcegraph repo, branch sj/egds-livedocs                       │
│ (NOT shipped in live_docs — private integration)                │
│                                                                  │
│  egds/livedocs/                                                  │
│  ├── store.go        // DocumentStore impl over upstream tables │
│  ├── executor.go     // ConnectRPC RefreshEvergreenDeepSearch   │
│  ├── manifest.go     // sources[] → ManifestEntry lift          │
│  └── register.go     // registers factories into SG MCP server  │
│                                                                  │
│  Target size: ~150 lines total. No SQLite, no CLI, no watch.    │
└─────────────────────────────────────────────────────────────────┘
```

**The `RefreshExecutor` interface** (sketch, final signature in Phase 1):

```go
type RefreshExecutor interface {
    // Refresh re-executes the saved query (or its upstream equivalent)
    // and returns the new rendered answer plus an updated manifest.
    Refresh(ctx context.Context, doc *Document) (RefreshResult, error)
    // Name is used in telemetry/logs; e.g. "deepsearch-mcp", "sourcegraph-evergreen".
    Name() string
}

type RefreshResult struct {
    RenderedAnswer string
    Manifest       []ManifestEntry
    Backend        string           // stable identifier
    ExternalID     *string          // e.g. sourcegraph version ID, for audit
    Metadata       map[string]any   // backend-specific
}
```

The detector, store, CLI, and MCP surface are all executor-agnostic. Swapping from OSS-default to sourcegraph-adapter is a single wiring change.

---

## Requirements

### Must-Have (Phase 1 — alert-first, provider-agnostic)

- **`evergreen/` package: types + interfaces (adapter contract surface)**
  - Acceptance: `Document`, `ManifestEntry`, `Finding`, `RefreshResult` types in `evergreen/types.go`. `DocumentStore`, `RefreshExecutor`, `ClaimsReader`, `RateLimiter` interfaces in `evergreen/interfaces.go`. All types exported, JSON-stable, documented. This file is the public contract the sourcegraph adapter compiles against — **changes here are breaking changes and must be semver-signaled**.

- **Default `DocumentStore` impl (SQLite, OSS-only)**
  - Acceptance: `evergreen/sqlite_store.go` implements `DocumentStore` with the `deep_search_documents` table (indexed `id`, `last_refreshed_at`, `status`). Forward-only migration. Append-only revision history (cap 5). Used by OSS install; sourcegraph adapter supplies its own impl backed by upstream tables.

- **`RefreshExecutor` default impl (deepsearch-MCP-backed, OSS)**
  - Acceptance: `executors/deepsearch_mcp.go` implements `RefreshExecutor` by wrapping the existing `sourcegraph.Client` in this repo (the deepsearch MCP client, *not* the sourcegraph ConnectRPC evergreen API). Unit tests with a mock MCP. Note: we ship exactly one default executor; the second (`prompt_replay.go`) proposed in v1 is cut to minimize surface. Users who want LLM-replay can implement the interface themselves; it's ~20 lines.

- **Staleness detector (hot/warm/cold/orphaned)**
  - Acceptance: `evergreen.Detect(doc, claimsDB) ([]Finding, error)`. Severity rules:
    - `hot` — signature hash change on any manifest entry
    - `warm` — body-only content-hash drift without signature change
    - `cold` — no per-symbol drift but age > `max_age_days`, or adjacent-repo churn within N commits of any manifested commit
    - `orphaned` — cited symbol missing from current claims (rename/delete)
  - Unit tests for each transition. Pure function of (doc, claims) — no I/O beyond the claims read.

- **`livedocs evergreen` CLI**
  - Acceptance: Subcommands `list`, `save`, `check`, `refresh <id>`, `delete <id>`. `check` runs detector across all docs and prints findings grouped by severity. `refresh` invokes the configured `RefreshExecutor` and updates the store. `--dry-run` on `refresh`. `--executor=<name>` flag picks non-default executor. `--help` lists all.

- **MCP tool factories: `NewStatusTool`, `NewRefreshTool`**
  - Acceptance: `evergreen/mcptools.go` exports constructor functions that take a `DocumentStore`, a `RefreshExecutor`, and a `RateLimiter` and return registerable MCP tool handlers. No globals. `mcpserver/evergreen.go` wires the OSS-default triple (SQLite store + deepsearch-MCP executor + KeyedLimiter). The sourcegraph adapter calls the same factories with its own triple — one call per tool, zero duplicated handler logic.

- **Orphaned-document quarantine (never-delete)**
  - Acceptance: When detector returns `orphaned`, document status transitions to `orphaned` and `refresh` is blocked pending human review (CLI: `livedocs evergreen force-refresh <id>`; MCP: explicit `acknowledge_orphan=true` param). Manifest preserved for provenance. Tests.

- **MCP-session rate limit on `evergreen_refresh`**
  - Acceptance: Reuse `KeyedLimiter` from m7v.22/.24. Default: 1 refresh per doc per 10 min per session; N refreshes per session per hour (configurable). Returns `ErrRateLimited` sentinel (m7v.26). Note: this is the live_docs MCP-level gate. When a specific executor (e.g. sourcegraph adapter) has its own upstream rate limiting, this layer acts as a secondary cap, not the primary.

### Should-Have (Phase 1)

- **Freshness banner in rendered answer**
  - Acceptance: `Document.RenderedAnswer` can be rendered with a freshness banner showing `last_refreshed_at`, status, and "what changed" summary (drifted symbols since last refresh) when status != fresh. Visually distinct from body. Separate `Render()` function so raw answer remains addressable.

- **Signature-hash early cutoff**
  - Acceptance: Per-symbol signature hash computed separately from content hash. Body-only churn classifies as `warm`, not `hot`. Matches `prd_change_triggered_enrichment.md` Phase 2.

- **Optional webhook sink for alerts**
  - Acceptance: `livedocs evergreen check --webhook=<url>` POSTs findings JSON when severity ≥ configurable threshold. Idempotent retry on 5xx. Last-send timestamp per doc. Slack/PagerDuty-friendly.

- **Symbol-precise diff output**
  - Acceptance: Detector findings include per-entry `was_hash`, `current_hash`, `change_kind` (`signature`/`body`/`deleted`/`renamed`). MCP `evergreen_status` response surfaces these directly so agents can decide locally without invoking a refresh.

### Must-Have (Phase 2 — OSS-path auto-refresh only)

**Scope note:** Phase 2 auto-refresh **only applies to OSS-path executors**. When the sourcegraph adapter is configured, auto-refresh defers entirely to sourcegraph's upstream 24h worker — live_docs' auto-refresh is disabled by config.

- **Watch-loop integration**
  - Acceptance: `livedocs watch --evergreen` fans out structural-extraction cycles to the evergreen detector. Docs with `refresh_policy=auto` queue into the existing debounced enrichment pipeline. Never blocks the watch poll cycle.

- **Tiered auto-refresh policy**
  - Acceptance: `refresh_policy=auto` triggers on `hot` by default; `warm` batches into a daily refresh; `cold` waits for user action. Per-document override.

- **Dollar-denominated budget (OSS executors only)**
  - Acceptance: Per-document `budget_usd_per_month`. Auto-refresh honors budget; exceeding degrades to alert-only. N/A when adapter backend has its own cost controls.

- **Cold-cache first-run warning**
  - Acceptance: First bulk `check` estimates total refresh cost; requires `--confirm` to proceed.

### Should-Have (Phase 2)

- **Cross-document freshness dashboard** (CLI-rendered + MCP-addressable)
  - Acceptance: `livedocs evergreen dashboard` ranks all docs by drift severity × reverse-dep blast radius. MCP tool `evergreen_dashboard()` returns the same. Works across all configured executors (including sourcegraph-adapter-sourced docs).

- **Manifest trimming for precision**
  - Acceptance: When manifest >N entries (default 50), detector prefers higher reverse-dep entries to bound per-doc detection cost.

### Nice-to-Have

- **Diff-only refresh** — re-render only the sections citing drifted symbols (requires structured rendered output; defer).
- **Scheduled refresh** — cron-cadence `refresh_policy=scheduled` independent of drift detection (OSS only; sourcegraph has its own 24h cadence).

---

## Sourcegraph Adapter — Scoped Out (Roadmap Only)

The adapter is **not built in this repo**. It lives on branch **`sj/egds-livedocs`** of the sourcegraph repo. This PRD notes its scope only so the live_docs-side interfaces are shaped correctly for it to plug in; the adapter's own PRD is a sourcegraph-side artifact.

**Target footprint: ~4 files, ~150 lines total.** Anything larger means a live_docs-side interface is the wrong shape and should be reshaped before the adapter ships, not after.

**Adapter responsibilities (on `sj/egds-livedocs`, not this repo):**

1. **`ConnectRPCExecutor` implements `evergreen.RefreshExecutor`** — Calls sourcegraph's `RefreshEvergreenDeepSearch` ConnectRPC method; polls version state; returns the completed conversation's answer + lifted manifest. ~40 lines.
2. **`sources[] → []ManifestEntry` lift** — For each `DeepSearchQuestion.sources[i]`, parse the `link` (repo path + line ranges) into `ManifestEntry{repo, commit_sha, file_path, line_range}`; resolve to `symbol_id` via live_docs' claims DB when available (fuzzy otherwise). ~30 lines.
3. **`UpstreamStore` implements `evergreen.DocumentStore`** — Reads documents directly from `evergreen_deepsearch` + `evergreen_deepsearch_versions`; materializes `Document` shape on read. No writes — sourcegraph owns the source of truth. `Save`/`UpdateStatus` are no-ops or push to an optional sidecar. ~50 lines.
4. **Registration** — `Register(mcp *mcpserver.Server, db *database.DB)` calls live_docs' `NewStatusTool` and `NewRefreshTool` factories with the adapter's `UpstreamStore` + `ConnectRPCExecutor` and registers the returned handlers on sourcegraph's MCP server. ~30 lines.

**Optional, deferred:** JSONB `dependency_manifest` column on `evergreen_deepsearch_versions` for authoritative upstream manifest storage. Until/unless sourcegraph team approves the migration, the adapter either stores manifest in a sidecar keyed by `(evergreen_id, version_id)` or re-derives it from `sources[]` on each read.

**Coordination items with Sourcegraph team:**

- Upstream schema change appetite (JSONB `dependency_manifest` column vs. sidecar-only).
- Whether the adapter registers via sourcegraph's existing `getDeepSearchHandler` or a new handler.
- Integration test strategy (live sourcegraph instance vs. recorded ConnectRPC fixtures).

---

## Design Considerations

**Why `RefreshExecutor` is the right seam.** The detector is pure (manifest vs. claims diff). The store is generic (just rows). The CLI/MCP surface is executor-agnostic. Everything backend-specific — how to re-run a query, what the answer format is, how citations come back — collapses into one interface with one method. Adding a third backend later (Exa, web search, internal RAG) is one file.

**Manifest fidelity vs. cost.** Manifest entries can be precise (`symbol_id` resolved) or fuzzy (`repo + commit_sha` only). Fuzzy entries still drive coarse drift detection ("the repo advanced 200 commits; this answer may be stale") but don't produce per-symbol diffs. Each executor populates precision it has available.

**Staleness ≠ wrongness.** A stale doc might still be correct; the detector reports drift, not invalidity. Refresh is what determines whether the answer actually needs to change.

**Never-delete.** Orphaned status preserves the manifest for provenance and audit. Mirrors tribal stale/quarantined semantics. Both OSS and adapter paths honor this.

**ZFC boundary.** Severity classification is arithmetic over hash diffs, reverse-dep counts, and ages — mechanical, stays in code. Re-running the query is delegated to the executor (model or upstream service).

**Revision history caps.** Append-only revisions bounded by `max_revisions` (default 5) per doc keeps disk usage bounded while preserving "what changed between refreshes" visibility.

## Open Questions

- Does `sourcegraph/client.go` in live_docs today already expose enough citation metadata from deepsearch MCP responses to build a precise manifest, or is the two-pass extraction opaque? (Audit live_docs-side `sourcegraph/client.go` + `semantic.Generator` before Phase 1 manifest-builder implementation.)
- For the adapter: is the upstream schema change worth pushing (authoritative manifest), or is a sidecar sufficient? Depends on Sourcegraph team appetite.
- Should the detector optionally consult upstream evergreen state (when adapter is configured) to avoid emitting drift findings for a doc that has an active upstream refresh already queued? (Likely yes — prevents noise.)
- How should `refresh_policy=auto` behave with the sourcegraph adapter configured? Hard-disable, or degrade to "alert-only with upstream-refresh-pending note"?

## Risk Preview (pre-premortem)

1. **Manifest-fidelity trap** — fuzzy fallbacks dominating would reduce staleness detection to "repo moved" noise. Need telemetry on precise-vs-fuzzy ratios early.
2. **Orphan floods on refactors** — large renames orphan many docs at once. Batch-quarantine with aggregate alert, not per-doc paging.
3. **Refresh storms on signature changes** — a widely-cited exported type fanning out. Reverse-dep ranking + warm-batching mitigates.
4. **False trust in cold docs** — users assuming old = accurate. Prominent freshness banner + MCP response always includes staleness.
5. **Adapter / live_docs version drift** — the sourcegraph branch depends on live_docs as a library; the `RefreshExecutor` interface is the contract. Breaking changes to the interface must be semver-signaled.
6. **Double-refresh across OSS+adapter configurations** — a user running both would trigger live_docs' watch-loop refresh AND upstream's 24h worker. Config flag `--defer-auto-refresh-to-upstream` mitigates; docs must call it out.

---

## Provenance

Initial design 2026-04-22; revised same day after audit of sourcegraph evergreen architecture (`internal/database/evergreen_deepsearch*.go`, `cmd/frontend/internal/deepsearchapi/evergreen_refresh_worker.go`, `externalapi/deepsearch/v1/`, `internal/openapi/goapi/model_source_item.go`, `internal/database/deepsearch_quota.go`). Dual-purpose split (OSS live_docs + separate sourcegraph-branch adapter) established 2026-04-22. Integrates manifest model from `prd_change_triggered_enrichment.md`; never-delete transitions from tribal staleness; severity tiering inspired by `drift/` findings; alert-first rollout inherits cost-risk lessons from `prd_deep_search_mcp.md` premortem.
