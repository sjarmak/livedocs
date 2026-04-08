# PRD: Tribal Knowledge Mapping for Data Pipelines

**Bead:** live_docs-8dn
**Status:** Draft (risk-annotated)
**Reference:** [Meta Engineering — How Meta Used AI to Map Tribal Knowledge in Large-Scale Data Pipelines (2026-04-06)](https://engineering.fb.com/2026/04/06/developer-tools/how-meta-used-ai-to-map-tribal-knowledge-in-large-scale-data-pipelines/)

## 1. Problem

Large data pipelines accumulate *tribal knowledge*: ownership conventions, undocumented invariants, informal runbook steps, and on-call folklore. This knowledge lives in Slack threads, PR comments, incident post-mortems, and senior engineers' heads. When those engineers rotate, the cost is paid in outages, duplicated work, and fear of touching "haunted" jobs.

live_docs already extracts *structural* claims from source code (exports, types, signatures, deps) into per-repo SQLite claims DBs served via MCP. The gap: claims cover what the code *is*, not what the humans around it *know*. We need to extend the claims foundation to capture semi-structured human knowledge with the same verification-gated, queryable discipline.

## 2. Goals

- **G1.** Extract pipeline ownership, invariants, and operational knowledge into structured claims stored alongside existing code claims.
- **G2.** Reuse the existing claims DB schema, MCP tool surface, and drift-detection gates.
- **G3.** Every tribal claim carries a *provenance* (source doc, commit, message URL) and a *verification state* (verified / stale / unverified).
- **G4.** Agents querying MCP can ask "who owns this table?", "what are the known gotchas for job X?", "what breaks if I change partition key Y?" and get cited answers.

## 3. Non-Goals

- Full natural-language chat over a corporate wiki. This is a claims store, not a RAG chatbot.
- Replacing incident management, runbook, or on-call tooling.
- Automatic *resolution* of stale knowledge — only detection + surfacing.

## 4. Users

- **AI coding agents** working in live_docs-instrumented repos (primary).
- **On-call engineers** wanting a grounded answer instead of Slack archaeology.
- **New hires** onboarding to a pipeline they've never touched.

## 5. Design Options Considered

### Option A — Extend the claims schema with a `tribal_claim` table
Add a sibling table to the existing symbol/claim tables. Each row: `(subject, predicate, object, source_ref, confidence, verified_at, extractor)`. Reuse the extractor pipeline with new extractors for PR comments, incident docs, and Slack exports.

**Pros:** Minimal architectural churn. Single query surface via existing MCP server.
**Cons:** Mixes structural (deterministic) and semi-structured (probabilistic) data in one store. Verification semantics differ.

### Option B — Parallel "tribal.db" with the same MCP adapter pattern
Separate SQLite DB per repo (`<repo>.tribal.db`), same adapter pattern as `.claims.db`, new MCP tools (`tribal_lookup`, `tribal_owner_of`, `tribal_invariants_for`).

**Pros:** Clean separation of deterministic vs. probabilistic data. Independent verification cadence. Easy to disable or quarantine.
**Cons:** Two DBs to keep coherent. Cross-joins (e.g., "gotchas for this function") require a federation layer.

### Option C — Unified knowledge graph layer on top of both
New `knowledge/` package builds a virtual graph over `.claims.db` + `.tribal.db`, exposes a single MCP tool `kg_query`. Claims remain authoritative; tribal is enrichment.

**Pros:** Cleanest agent ergonomics — one query, cited results. Future-proofs for additional knowledge sources (metrics, traces).
**Cons:** Biggest scope. Graph layer is new surface area and a new failure mode.

**Recommendation:** **Option B** for v1, with a clear migration path to Option C once the tribal extractors prove out. Keeps blast radius small and preserves the claims DB as ground truth.

## 6. v1 Scope (Option B)

### 6.1 Inputs
- PR descriptions and review comments (via `gh api`)
- Incident post-mortems (configurable path glob, e.g. `docs/incidents/**/*.md`)
- CODEOWNERS + directory-level `OWNERS.md` files
- Git blame for last-touched-by attribution
- **Out of scope v1:** Slack, email, wikis — add via extractor plugins later.

### 6.2 Extractors (new)
- `extractor/tribal/ownership.go` — CODEOWNERS + OWNERS.md → `owns(path, team)` claims
- `extractor/tribal/incidents.go` — post-mortem frontmatter + LLM-extracted invariants → `invariant(subject, description, source)` claims
- `extractor/tribal/prcomments.go` — PR review threads → `gotcha(symbol, note, pr_url)` claims

All semantic extraction (invariant detection, gotcha classification) goes through a model call — **no regex heuristics for meaning** (ZFC).

### 6.3 Schema (`tribal.db`)
```
tribal_claim(
  id, subject, predicate, object,
  source_type, source_ref, source_commit,
  confidence, extracted_at, verified_at, stale
)
tribal_source(source_ref, kind, fetched_at, checksum)
```

### 6.4 MCP Tools (new)
- `tribal_lookup(subject)` — all claims for a symbol/path/table
- `tribal_owner_of(path)` — team + last active maintainer
- `tribal_invariants_for(subject)` — known invariants with sources
- `tribal_gotchas_for(symbol)` — cited warnings

### 6.5 Verification Gate
Reuse drift-detection: when the underlying code for a tribal claim's subject changes, the claim is marked `stale=true` and surfaced on next MCP query. Stale claims are returned but *clearly flagged* — agents decide whether to trust them.

### 6.6 CLI
- `livedocs tribal extract` — run tribal extractors
- `livedocs tribal verify` — recompute staleness

## 7. Risks (Premortem)

| # | Risk | Likelihood | Impact | Mitigation |
|---|------|------------|--------|------------|
| R1 | LLM-extracted invariants hallucinate, agents cite them as truth | High | High | Every tribal claim carries `source_ref`; MCP response must include source; low-confidence claims labeled `unverified` |
| R2 | Tribal DB drifts silently from code reality, becomes worse than nothing | High | High | Verification gate marks stale on code change; stale claims visually distinguished; `tribal verify` in CI |
| R3 | PII / confidential info from post-mortems leaks into tribal DB shared across repos | Medium | High | Per-repo DBs by default; explicit allowlist for cross-repo xref; redaction pass on extraction |
| R4 | Extractor cost balloons on large repos (LLM calls per PR comment) | Medium | Medium | Content-hash caching on source_ref; incremental extraction keyed on commit SHA |
| R5 | Teams game ownership claims (CODEOWNERS becomes political not factual) | Medium | Medium | Surface *both* CODEOWNERS and git-blame-derived ownership; show divergence |
| R6 | Option B → Option C migration never happens, we're stuck with two DBs forever | Medium | Low | Design `tribal.db` schema to be join-compatible with `claims.db` from day one |
| R7 | Agents over-trust tribal knowledge and skip reading code | Low | High | MCP responses must lead with structural claims, tribal claims as annotations |
| R8 | Extracting from Slack (v2) raises legal/retention concerns | Low | High | Explicitly out of scope v1; require legal review before v2 |

## 8. Reuse of Existing live_docs Components

| Component | Reuse | Notes |
|-----------|-------|-------|
| `extractor/` pipeline harness | ✅ | New extractors plug in as siblings |
| tree-sitter symbol index | ✅ | Tribal claims reference symbols by canonical ID |
| `db/` SQLite layer | ✅ | New schema, same connection/migration pattern |
| `mcpserver/adapter.go` | ✅ | New tool registrations, same mcp-go boundary |
| Drift detection (`drift/`, `anchor/`) | ✅ | Staleness computation is a drift check |
| `cmd/livedocs/` CLI | ✅ | `tribal` subcommand |
| GitHub Action | ✅ | Add tribal extraction job |

No net-new subsystems. Every new capability lands inside an existing architectural slot.

## 9. Open Questions

- **Q1.** Should `source_commit` point to the code commit or the document commit? (Proposal: document commit, with back-ref to code commit at extraction time.)
- **Q2.** Confidence scores from the model — store as-is or bucket into {high/medium/low}? (Proposal: bucket. Raw scores are noise.)
- **Q3.** How do we handle contradictory claims from different sources? (Proposal: return all, annotate conflict, let the querying agent reason.)
- **Q4.** Do we need per-team ACLs on tribal claims in v1? (Proposal: no — per-repo scoping is sufficient for v1.)

## 10. Success Metrics

- **M1.** Agents querying `tribal_owner_of` get a cited answer on ≥90% of pipeline paths in instrumented repos.
- **M2.** Stale-claim rate on recently-touched code is <5% (verification gate is working).
- **M3.** Zero confirmed hallucinated invariants surfaced without a source citation.
- **M4.** On-call engineers self-report the tool reduced "ask in Slack" rate on at least 2 pipelines in the first month.

## 11. Phasing

- **Phase 1 (v1):** Ownership + PR comment extractors, `tribal.db`, 4 MCP tools, CLI, staleness gate.
- **Phase 2:** Incident post-mortem extractor, cross-repo federation for shared pipelines.
- **Phase 3:** Unified knowledge graph (Option C), Slack connector (pending legal).

## 12. Decision

Proceed with **Option B, v1 scope as above**. Biggest watchlist items: **R1** (hallucinated invariants) and **R2** (silent drift). Both are mitigated by the same mechanism — mandatory provenance + reuse of the drift gate — which is also the cheapest mitigation available.
