# PRD: Tribal Knowledge Mapping for live_docs

**Status:** Draft (risk-annotated, produced via diverge → converge → premortem)
**Bead:** live_docs-8dn
**Supersedes:** `prd_tribal_knowledge_mapping.single-pass-draft.md` (rejected) and a prior single-lens draft at this path
**Date:** 2026-04-08

---

## Problem Statement

Code-only documentation — even live, claims-backed documentation — captures *what* the code does but not *why* it exists, *who* owns it, *which invariants it silently depends on*, or *what the oncall lore says about it*. This "tribal knowledge" lives in git history, PR comments, CODEOWNERS, commit messages, in-file TODO/HACK markers, and (eventually) incident notes and chat. AI coding agents using live_docs today see an accurate but rationale-free picture, which makes them confidently wrong about intent — the hardest class of bug to catch in review.

Meta's 2026-04 engineering blog describes layering an LLM over *already-present* monorepo signals (tasks, diffs, runbooks) with strict provenance linkage and a feedback loop. We want the live_docs equivalent: a minimal, provenance-first layer that **extends** the existing claims DB rather than forking a parallel store, and that fails safely when evidence decays.

## Goals

- Give MCP-consuming agents structured access to ownership, rationale, invariants, and quirks, each with verifiable provenance (commit, file, lines, author, timestamp, source-ref URL).
- Reuse the existing claims DB schema (`claim_tier='meta'` already exists at `db/claims.go:219`), extractor registry, drift gate, and MCP adapter — zero new databases, zero new subsystems.
- Ship a valuable Phase 1 that uses **no LLM at all** (blame, CODEOWNERS, TODO/HACK extraction, commit-message mining). LLM-classified sources are strictly opt-in in Phase 2.
- Preserve uncertainty through the full pipeline: verbatim source quote, confidence, corroboration count, and staleness hash are required fields on every tribal fact returned to agents.
- Provide a correction loop so agents and humans can dispute, delete, or supersede facts; corrections are authoritative overlays.

## Non-Goals

- A universal knowledge graph unifying code + Slack + Jira + incidents. (Every OSS project that tried this rewrote it within 18 months — DataHub, Databook v1, Lexikon.)
- Human-authored wiki pages or docs-as-code portals (Backstage TechDocs is a cautionary tale: median 90-day staleness).
- Persistent mining of chat, incident systems, or ticket trackers in v1. These are Phase 3 behind a pluggable `AnnotationSource` interface.
- Replacing just-in-time retrieval. The persistent store is a **cache of high-confidence, corroborated facts**; low-confidence or uncached queries fall back to on-demand `gh search` / `git log -S` via MCP tools.
- Semantic drift detection via LLM re-analysis on every commit (cost-prohibitive; see Risk R2).

## Design Overview

### Storage: extend, do not fork

All tribal facts live in the existing per-repo `.claims.db`. Three additive tables join to `symbols.id`:

```sql
CREATE TABLE IF NOT EXISTS tribal_facts (
    id              INTEGER PRIMARY KEY,
    subject_id      INTEGER NOT NULL REFERENCES symbols(id),
    kind            TEXT NOT NULL
                    CHECK(kind IN ('ownership','rationale','invariant','quirk','todo','deprecation')),
    body            TEXT NOT NULL,          -- extractor output (may be summarized in Phase 2)
    source_quote    TEXT NOT NULL,          -- verbatim excerpt; authority-laundering defense
    confidence      REAL NOT NULL,
    corroboration   INTEGER NOT NULL DEFAULT 1,
    extractor       TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    model           TEXT,                   -- NULL for deterministic extractors
    staleness_hash  TEXT NOT NULL,          -- hash of evidence content; drift gate compares
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK(status IN ('active','stale','quarantined','superseded','deleted')),
    created_at      TEXT NOT NULL,
    last_verified   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tribal_evidence (
    id              INTEGER PRIMARY KEY,
    fact_id         INTEGER NOT NULL REFERENCES tribal_facts(id) ON DELETE CASCADE,
    source_type     TEXT NOT NULL
                    CHECK(source_type IN ('blame','commit_msg','pr_comment','codeowners','inline_marker','runbook','correction')),
    source_ref      TEXT NOT NULL,          -- URL or SHA:path:line; unique per fact
    author          TEXT,
    authored_at     TEXT,
    content_hash    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tribal_facts_subject ON tribal_facts(subject_id);
CREATE INDEX IF NOT EXISTS idx_tribal_evidence_fact ON tribal_evidence(fact_id);

CREATE TABLE IF NOT EXISTS tribal_corrections (
    id              INTEGER PRIMARY KEY,
    fact_id         INTEGER NOT NULL REFERENCES tribal_facts(id),
    action          TEXT NOT NULL CHECK(action IN ('correct','delete','supersede')),
    new_body        TEXT,
    reason          TEXT NOT NULL,
    actor           TEXT NOT NULL,          -- human id or signed agent id
    created_at      TEXT NOT NULL
);
```

**Write-time invariant:** every `tribal_facts` row MUST have ≥1 `tribal_evidence` row in the same transaction. Enforced in the Go insert helper (`db/tribal.go`), not via SQL `CHECK` (cross-row).

### Extractors — Phase 1 (zero LLM)

Added to the existing extractor registry (`extractor/registry.go`), each producing `tribal_facts` with `model = NULL`:

1. **`codeowners`** — parses `CODEOWNERS` once per repo; emits `ownership` facts keyed by glob → affected symbols. Deterministic.
2. **`blame_ownership`** — `git blame` aggregated per symbol range; emits top-author and commit-recency `ownership` facts. Deterministic. Pagerank-style weighting is Phase 1.5.
3. **`commit_rationale`** — for each symbol's hunk history, stores the most recent non-trivial commit message as a `rationale` fact, quote-verbatim. Filters trivial commits via length + conventional-commit type allowlist.
4. **`inline_marker`** — tree-sitter pass that captures `TODO`/`FIXME`/`XXX`/`HACK`/`NOTE`/`WHY` comments adjacent to a symbol; emits as `quirk` or `todo`. Deterministic.

### Extractors — Phase 2 (LLM-classified, opt-in)

Gated behind `livedocs extract --tribal=llm` **and** an explicit config flag. Every Phase-2 fact carries `model` + `confidence < 1.0` and REQUIRES corroboration ≥ 2 from independent `source_ref`s before it is served at default retrieval tier.

5. **`pr_comment_miner`** — fetches review comments scoped to files touching a symbol (via `gh api` or equivalent), routes them through the existing semantic tier (`drift/semantic.go` pattern) for classification into `rationale`/`invariant`/`quirk`. PII redaction via Presidio (or equivalent) on input is mandatory — see R3.

### Drift integration

`tribal_facts.staleness_hash` ties each fact to `tribal_evidence.content_hash`. The drift walker gains a `checkTribal` pass:

- Evidence file/PR unchanged → fact stays `active`.
- Evidence file `content_hash` changed but symbol fingerprint still matches → `stale` (NOT deleted — decayed confidence is itself a signal).
- Underlying symbol disappeared → `quarantined`.
- Only a correction via `tribal_propose_fact` with `action=supersede` moves a fact from `stale` back to `active`.

Deliberately different from structural drift (which deletes stale claims). See R2.

### MCP tool surface

Five new tools in `mcpserver/tools.go`, following the existing adapter pattern. Hard cap — no "list all facts" tool.

1. **`tribal_context_for_symbol(symbol, kinds?, min_confidence?)`** — all active facts for a symbol, each with full provenance envelope.
2. **`tribal_owners(symbol)`** — specialized, deterministic, fast path for the most common query.
3. **`tribal_search(query, kind?, limit?)`** — BM25 over `body` + `source_quote`, scoped to one repo.
4. **`tribal_why_this_way(symbol)`** — `rationale` + `invariant` kinds only, explicitly labeled with confidence and corroboration.
5. **`tribal_propose_fact(subject, kind, body, evidence, action?)`** — agent/human write-back. Pending review by default unless caller has a signed writer identity.

**Response envelope (non-negotiable for every fact returned):**

```json
{
  "body": "...",
  "source_quote": "...",
  "kind": "rationale",
  "confidence": 0.72,
  "corroboration": 2,
  "status": "active",
  "evidence": [
    {"source_type":"pr_comment","source_ref":"https://...","author":"...","authored_at":"..."}
  ],
  "extractor": "pr_comment_miner@v0.1",
  "model": "claude-haiku-4-5-20251001",
  "last_verified": "2026-04-08T..."
}
```

### CLI

`livedocs extract --tribal[=deterministic|llm]` — runs tribal extractors against the current repo's claims DB. Default is `deterministic` (Phase 1 only) and runs as part of the normal extract pipeline. `llm` mode is gated behind an explicit flag AND a config-level opt-in.

## Requirements

### Must-Have (Phase 1 — ships with v1)

- **M1: Schema migration.** Three new tables added to `db/claims.go` `CreateSchema`, idempotent (`IF NOT EXISTS`), backward-compatible with existing `.claims.db` files.
  - **Acceptance:** `go test ./db/... -run TribalSchema` passes; a fixture test loads a pre-M1 DB and runs the new schema creation without error.
- **M2: Deterministic tribal extractors** (`codeowners`, `blame_ownership`, `commit_rationale`, `inline_marker`) registered in `extractor/registry.go`.
  - **Acceptance:** On the live_docs repo itself, `livedocs extract --tribal=deterministic` produces ≥1 `tribal_facts` row of each kind, each with ≥1 `tribal_evidence` row, and all `model` fields are NULL. Verified by `go test ./extractor/... -run Tribal`.
- **M3: MCP tools 1, 2, 4 (context_for_symbol, owners, why_this_way)** wired through `mcpserver/adapter.go`.
  - **Acceptance:** Integration test calls each tool against a seeded DB and receives the full provenance envelope (all required fields present). Missing `source_quote` returns an error — verified by a negative test.
- **M4: Write-time evidence invariant.** The Go insert helper rejects any `tribal_facts` row without ≥1 evidence row in the same transaction.
  - **Acceptance:** `go test ./db/... -run TribalEvidenceRequired` asserts an error on insert-without-evidence; rollback leaves the DB unchanged.
- **M5: Drift integration.** `drift` package gains a `tribal` pass that transitions facts to `stale` or `quarantined` based on evidence `content_hash` / symbol fingerprint changes. Transitions never delete rows.
  - **Acceptance:** Fixture test mutates an evidence file, re-runs drift, observes `stale`; deletes the underlying symbol, observes `quarantined`. Row counts unchanged.
- **M6: Provenance envelope enforced at the MCP boundary.** The adapter layer refuses to emit any fact missing `source_quote`, `evidence`, `confidence`, or `status`.
  - **Acceptance:** Schema-level JSON test in `mcpserver/` validates all tribal-tool responses against the envelope spec.

### Should-Have (Phase 2 — follow-on PRD, same architecture)

- **S1: `tribal_search` MCP tool** (BM25 over `body` + `source_quote`).
  - **Acceptance:** Seeded DB returns expected top-k hits for three example queries; irrelevant symbols not in top 5.
- **S2: `tribal_propose_fact` write-back** with pending-review state for unsigned callers.
  - **Acceptance:** Unsigned proposal creates `status='quarantined'`; signed writer identity creates `active`; missing-evidence proposal rejected.
- **S3: PR comment miner (LLM-classified)** gated behind `--tribal=llm` + config opt-in, mandatory Presidio-style PII redaction, corroboration ≥ 2 before default-tier serving.
  - **Acceptance:** Pilot on a small repo produces ≥10 facts; PII scan on `body`/`source_quote` returns zero matches for redacted categories; uncorroborated facts are not returned by `tribal_context_for_symbol` at default `min_confidence`.

### Nice-to-Have (Phase 3)

- **N1: Cross-repo tribal facts via the existing `xref` index.**
  - **Acceptance:** A fact written in repo A is discoverable via `tribal_context_for_symbol` from repo B when the symbol is an xref target.
- **N2: Runbook/incident source** behind a pluggable `AnnotationSource` interface with ≥1 reference implementation and a contract test suite.
- **N3: Correction workflow UX** — `livedocs tribal correct <fact-id> --reason=...` writes to `tribal_corrections`; subsequent reads reflect the overlay.

## Design Considerations & Key Tensions

**1. Persistent DB vs just-in-time retrieval.** The failure-modes lens argued JIT retrieval (`gh search`, `git log -S`) strictly dominates for a small project and inherits none of DataHub's decay problems. The first-principles and prior-art lenses argued the thin annotations table keyed by the stable claim id is the convergent answer across ~8 production systems. **Resolution:** both. Phase 1 is deterministic-only, essentially a cache over already-derivable signals — if the cache decays, the source is still authoritative. LLM-classified mining is opt-in and isolated. JIT retrieval remains a first-class fallback via a passthrough MCP tool.

**2. Extend claims.db vs parallel tribal.db.** Strictly dominant to extend: `claim_tier='meta'` slot already exists, `source_files.content_hash` is a free staleness primitive, `mcpserver/adapter.go` accommodates new tools with no routing changes, no second database means no pool / migrations / backup / cross-DB join cost. This is the main reversal vs the rejected single-pass draft.

**3. Non-destructive tribal drift.** Structural claims get deleted when stale; tribal facts decay to `stale`/`quarantined`. Deletion would destroy the signal ("this is no longer true") agents most need.

**4. Authority laundering prevention.** Every fact stores the verbatim `source_quote`. Agents cannot receive an assertion without being able to see what a human actually said. `corroboration` counts only *independent* `source_ref`s (dedup enforced on insert), so a single fact re-extracted from its own PR description next cycle does not inflate it.

**5. ZFC compliance.** Extractors mine signals (IO, structural parsing, hash computation). Phase 2 classification into `rationale`/`invariant`/`quirk` is delegated to the existing semantic tier — no hardcoded regex heuristics for meaning detection. Phase 1 extractors record literally-present structural data (blame author, commit message, CODEOWNERS match, inline marker).

## Risks & Mitigations (premortem)

| ID | Risk | Severity | Mitigation | Residual |
|----|------|----------|------------|----------|
| **R1** | **Hallucinated invariants poison agent decisions.** LLM extracts a plausible-but-wrong "must run after X" fact; agent acts on it invisibly. | Critical | Phase 1 ships zero LLM extractors. Phase 2 requires corroboration ≥ 2 from independent source_refs before default-tier serving. Every fact carries `source_quote` so agents can verify. `tribal_why_this_way` explicitly labels confidence and corroboration. | Low (P1); Medium (P2) until pilot data informs thresholds. |
| **R2** | **Semantic drift undetected.** Retry count changes 3→5; the fact "we retry 3 times because upstream is flaky" is now a lie, but the structural drift gate passes. | High | `content_hash` tracks the text of the justification, not the code it describes. Any change to the quoted source flips the fact to `stale`. True semantic drift (upstream API fixed, retries vestigial) is Q1 and does not block v1. | Medium — accepted for v1; Phase 3 adds sampling-based LLM re-verification. |
| **R3** | **PII/secrets in PR comments + incident notes become exfiltration vector via MCP.** | Critical (P2+) | P1 sources exclude chat/incident data entirely. P2 PR comment miner requires Presidio-style redaction *before* LLM extraction, an allowlist of mineable repos, a kill-switch, and an audit log of every fact served. Samsung/ChatGPT incident is the reference case. | Medium — security review required before P2 flag-flip. |
| **R4** | **Feedback-loop collapse / manufactured consensus.** Agent-written PR descriptions get re-mined next cycle and inflate corroboration on a speculative fact. | High | Evidence table deduplicates on `source_ref`; corroboration counts only *independent* sources. Agent-authored PR descriptions are tagged/excluded via a heuristic on commit author or PR label, configurable. | Medium — monitor once P2 ships. |
| **R5** | **Maintenance tax.** Each new extractor/source is a new auth, rate-limit, schema-change, prompt-injection surface. | High | Hard cap: 4 P1 extractors (all deterministic), 1 P2 extractor. `AnnotationSource` interface is the only path for Phase 3 additions. Contract tests per source. CODEOWNERS entry for `extractor/tribal/`. | Medium — revisit at 6 months. |
| **R6** | **Meta survivorship bias.** Meta has thousands of engineers and a metadata team; cargo-culting their approach at small scale inverts cost/benefit. | Medium | We ship the smallest viable shape (deterministic only), no UI, no source unification, reusing existing infra. If Phase 2 pilot metrics are poor, the architecture permits removing the LLM layer without touching the Phase 1 store. | Low — architecturally hedged. |
| **R7** | **Contributor privacy.** Blame-derived ownership facts can name people who have left or requested removal. | Medium | `tribal_corrections` supports `delete` and `supersede` scoped to an author. Extractors honor a `.tribal-exclude` file. | Low. |
| **R8** | **Backstage-style decay.** Persistent store becomes untrusted within 12–18 months. | High | Phase 1 is fully derivable from git state — "decay" just means re-running the extractor; no human effort wasted. Phase 2 facts that go `stale` are never silently reactivated. | Low (P1); Medium (P2). |

## Open Questions

- **Q1:** How do we detect semantic drift (evidence text unchanged but its claim no longer matches code behavior) without LLM-per-commit? Candidate: sampling-based re-verification on `last_verified` age. Deferred to Phase 3.
- **Q2:** How does `tribal_propose_fact` authenticate a signed agent identity? MCP passes a session id — extend it with a signing key, or rely on an external gateway? Blocks S2.
- **Q3:** Does the persistent cache beat pure JIT retrieval on agent task-success rate? Instrument both and compare after 4 weeks of dogfood.
- **Q4:** Cross-repo tribal facts — live in source repo or consumer repo's `.claims.db`? Mirror the existing xref pattern (both sides).
- **Q5:** Default `min_confidence` for Phase 2 `tribal_context_for_symbol`? Best guess 0.6; calibrate from pilot.

## Research Provenance

Produced via the `/research-project` pipeline with three independent lenses:

- **Prior art lens** (a24be91598b01d6ef) — Meta, DataHub, Amundsen, Backstage, Metaflow, Sourcegraph Cody, Copilot Workspace. Key contribution: convergent pattern across ~8 systems is a thin annotations table keyed by stable entity id, with `derive-don't-ask` extraction; curated wiki approaches all decay.
- **First-principles lens** (ad417e5a76aa04f79) — grounded in `db/claims.go`, `mcpserver/tools.go`, `extractor/registry.go`. Key contribution: `claim_tier='meta'` already exists; extending claims.db strictly dominates a parallel tribal.db on every axis; five-tool MCP cap.
- **Failure-modes / contrarian lens** (a61d1e14ffc36422c) — RAG hallucination rates (Vectara HHEM, Stanford 2024), DataHub decay post-mortems, model collapse (Shumailov et al., *Nature* 2024), Samsung/ChatGPT PII incident. Key contribution: JIT retrieval as a serious alternative; mandatory `source_quote` to prevent authority laundering; R1–R8 risk frame.

### Convergence points (high confidence)
- Extend existing claims DB; do not fork. (All three lenses.)
- Provenance envelope with verbatim `source_quote` is mandatory. (Prior art + failure modes.)
- Start with deterministic extractors before any LLM. (Prior art + failure modes.)
- Small MCP tool surface (≤5) beats a kitchen sink. (First-principles + prior art.)

### Divergence points (resolved in Design Considerations §1–2)
- **Persistent DB vs JIT retrieval.** Both. P1 persistent store is a cache over deterministic signals; JIT fallback tool always available.
- **Aggressive LLM mining in v1 vs deterministic-only.** Deterministic-only for v1; LLM layer is P2 behind explicit flag, config opt-in, and corroboration threshold.

## Recommended Next Step

`/prd-build docs/design/prd_tribal_knowledge_mapping.md` to decompose M1–M6 into parallel work units.
