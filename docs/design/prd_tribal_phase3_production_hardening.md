# PRD: Tribal Knowledge Phase 3 — Production Hardening

**Status:** Risk-annotated (diverge + converge + premortem complete). Ready for `/prd-build`.
**Date:** 2026-04-15
**Supersedes:** The "Phase 3 Nice-to-Have" section of `prd_tribal_knowledge_mapping.md`
**Related pilot:** `kubernetes/kubernetes` pilot produced 21 facts from 4 files in 50 LLM calls; 3 of 21 facts were duplicates of the same scheduler event-registration issue.

---

## Problem Statement

Phases 1 and 2 shipped: deterministic extractors, LLM-classified PR comment mining, tribal_search, tribal_propose_fact, and the corroboration gate. The Kubernetes pilot validated the pipeline end-to-end but exposed seven production-readiness gaps. Three of those gaps are genuinely blocking at scale; the other four are either architectural polish or a re-opening of a Phase 1 design tension (JIT vs. pre-computed index) that the research surfaced as unresolved.

The most concrete blocker is that the **corroboration gate is currently toothless**: every LLM-extracted fact stays at `corroboration=1` forever because the insert path never looks for existing facts to merge. This means the default-tier serving threshold (corroboration ≥ 2 for LLM facts) filters out 100% of LLM-mined tribal knowledge at default retrieval. The gate exists on paper but not in practice. Phase 3's central job is to make corroboration actually work.

The remaining gaps (incremental state, file prioritization, critic loop, semantic drift, cross-repo, runbook sources, correction CLI) sort into a Pareto frontier: the cheap additive wins (one column, one reused pattern, one SQL query, one CLI subcommand) are worth shipping, while the expensive speculative features (cross-repo fan-out, runbook mining, embedding-based dedup) are either security-blocked or premature given pilot volume.

---

## Goals

- Make the corroboration gate functional: independent evidence for the same tribal knowledge merges into a single fact with an accurate independent-source count.
- Enable incremental re-mining: rerunning `livedocs extract --tribal=llm` skips already-processed PRs instead of re-fetching and re-classifying everything. The incremental cursor also serves JIT mine-on-demand calls so agent-triggered mining is idempotent.
- Enable kubernetes-scale extraction: a budget of N LLM calls should cover the highest-value N files, not the first-N-alphabetical files.
- Ship the correction workflow (N3 from the original PRD): humans and agents can correct/supersede bad facts via CLI, with the correction recorded in `tribal_corrections`.
- Instrument Phase 5 decision-making: record embedding cosine scores alongside every string-hash cluster decision so the upgrade to semantic dedup is data-driven, not faith-based.
- Preserve the Phase 2 security posture: no feature may widen the blast radius of a hallucinated or PII-laden fact.

## Non-Goals

- **Cross-repo tribal facts (was N1)** — defer until a per-fact egress audit log and signed-reader-identity gate exist. The cross-repo fan-out creates a leak vector where internal-repo knowledge surfaces in external-repo queries and Phase 2's input-time PII redaction does not cover semantic PII (customer names, internal project codenames) that would leak via output facts.
- **Runbook / incident AnnotationSource (was N2)** — defer until a semantic PII pass (LLM or licensed PII service, not regex) plus a mineable-source allowlist exist. Runbooks contain on-call phone numbers, IP addresses, customer names, and incident playbooks that pattern-based redaction cannot catch.
- **Embedding-based semantic dedup (SemHash, Model2Vec, cosine clustering) as the PRIMARY cluster key** — defer to Phase 5. At 21-fact pilot volume, literal normalized-string hashing handles the case without threshold-tuning or false-merge risk. The `cluster_debug` table (see N4) captures the calibration data needed to justify the Phase 5 upgrade when the corpus reaches ~200 facts.
- **Cross-file pattern synthesizer as a mandatory pass** — ship behind a feature flag only (N1). The Kubernetes pilot's rationale-heavy kind distribution (11/6/4) is the wrong substrate for invariant synthesis; measure before promoting to default.
- **LLM-driven file prioritization** — ZFC says no. File ranking is mechanical arithmetic over signals the claims DB already computes. LLM stays out of the ranking loop.
- **Generation-time critic as a must-have gate** — the research agents converged on the observation that single-LLM self-critique has high false-positive rates on novel content (CRITIC paper, Gou et al., ICLR 2024; npj Digital Medicine 2025). At pilot volume almost nothing reaches `corroboration ≥ 3`, so a critic gate becomes the de facto filter on 100% of LLM facts — the exact anti-pattern the research warns against. Critic functionality downgrades to S4, gated on an empirical trigger (see S4 acceptance).
- **Replacing JIT retrieval with the persistent store.** The Phase 1 PRD's "both" resolution stands; JIT remains a first-class fallback. One new MCP tool (`tribal_mine_on_demand`) makes that fallback explicit, and the M3 incremental cursor makes its second invocation cheap.

---

## Design Overview

### The single schema change

```sql
ALTER TABLE tribal_facts ADD COLUMN cluster_key TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_tribal_facts_cluster ON tribal_facts(subject_id, kind, cluster_key);

ALTER TABLE source_files ADD COLUMN last_pr_mined INTEGER DEFAULT 0;
ALTER TABLE source_files ADD COLUMN pr_miner_version TEXT DEFAULT '';
```

Two tables touched, three columns added, one index added. **No new tables for the must-have path.** (N4 adds one append-only debug table for Phase 5 calibration — gated behind a config flag, off by default.)

### Write-time corroboration via `UpsertTribalFact`

```go
// UpsertTribalFact inserts a fact atomically OR increments corroboration on
// an existing fact with the same (subject_id, kind, cluster_key). Evidence
// rows are appended only if their source_ref is not already present for the
// matched fact. Corroboration is incremented once per new independent source.
func (c *ClaimsDB) UpsertTribalFact(fact TribalFact, evidence []TribalEvidence) (factID int64, merged bool, err error)
```

**`cluster_key` is a SHA-256 of a CONSERVATIVELY normalized body: lowercase + whitespace-collapse only. NO stopword strip, NO punctuation strip.** This is the keystone compromise from the convergence debate: under-clustering (false negatives) is degraded-but-correct; over-clustering (false positives) is silent data loss. Conservative normalization biases toward under-clustering by design. The `body` column always retains the verbatim original text, so upgrading the normalization function in Phase 5 is a single `UPDATE tribal_facts SET cluster_key = new_hash(body)` migration — the column is forward-compatible, only the values are not.

**Known false negative:** the word-order pair `"callers must hold the mutex"` vs `"the mutex must be held by callers"` hashes differently under this scheme. This is explicitly documented as acceptable pilot behavior; it will be caught by the N4 cluster_debug instrumentation (if embeddings would merge them, the cosine score will flag it) and addressed by the Phase 5 upgrade.

Callers in `extract_cmd.go`, `pr_comment.go`, and `tribal_propose.go` switch from `InsertTribalFact` to `UpsertTribalFact` with no other change.

**Independence invariant:** corroboration increments only when the new evidence row's `source_ref` is distinct from all existing evidence for that fact. Existing evidence is not duplicated. A fact re-mined from its own PR description on a later run cannot inflate itself.

### Incremental state via `source_files.last_pr_mined`

`PRCommentMiner.ExtractForFile` gains a `sinceCursor int` parameter threaded from `source_files.last_pr_mined` in the DB. The miner passes `--search "<filePath>"` to `gh pr list` to find PRs touching the file, filters to `number > sinceCursor`, fetches review comments only from those PRs, and returns the max PR number seen. The caller writes back to `source_files`.

**This cursor serves BOTH code paths.** Batch mining (`livedocs extract --tribal=llm`) uses it to skip already-processed PRs between runs. The JIT `tribal_mine_on_demand` MCP tool uses the same cursor so a second call on the same symbol is a near-zero-cost no-op. The contrarian lens's concern about "M3 serves only batch" is resolved: M3 plumbing is useful in both the batch and JIT worlds.

Version column (`pr_miner_version`) forces a full remine if the miner's extraction contract changes — the same pattern Phase 1 uses for `extractor_version` on structural claims.

**Force-push / squash-merge handling:** a `--force-remine` CLI flag resets `last_pr_mined = 0` for the target file(s). Facts keyed on dead PR URLs are not auto-deleted; they decay through the existing drift gate (`content_hash` mismatch → `stale`).

### File prioritization via SQL (with PageRank benchmark escape hatch)

New method `ClaimsDB.RankFilesForMining(repo string, limit int) ([]string, error)`:

```sql
SELECT sf.relative_path,
       COUNT(DISTINCT s.id) FILTER (WHERE s.visibility='public') AS public_surface,
       COUNT(DISTINCT c.id) AS fan_in,
       COALESCE((SELECT COUNT(*) FROM tribal_facts tf
                 JOIN symbols s2 ON s2.id=tf.subject_id
                 WHERE s2.import_path=sf.relative_path AND tf.status='active'), 0) AS existing_facts,
       (sf.last_pr_mined IS NULL OR sf.last_pr_mined = 0) AS never_mined
FROM source_files sf
JOIN symbols s ON s.import_path = sf.relative_path AND s.repo = sf.repo
LEFT JOIN claims c ON c.subject_id = s.id
WHERE sf.repo = ? AND sf.deleted = 0
GROUP BY sf.relative_path
ORDER BY (never_mined * 1000
          + public_surface * 3
          + fan_in
          - existing_facts * 5) DESC
LIMIT ?
```

`extract_cmd.go` replaces `filepath.WalkDir` with `RankFilesForMining(repoName, cfg.Tribal.MaxFilesPerRun)`. Never-mined files dominate; within that tier, high-surface-area and high-fan-in files win; files with abundant existing coverage are de-prioritized. No LLM call, no config required beyond the existing `DailyBudget`.

**PageRank escape hatch:** the prior-art lens's irreducible objection was that the SQL weights are hand-tuned magic numbers. M4's acceptance includes a one-shot benchmark: run a PageRank ranking over the `symbols.references → symbols.defined_in` graph using `gonum/graph` and compute the top-10 overlap with the SQL ranking. If overlap ≥ 80%, the constants don't matter and SQL ships. If overlap < 80%, switch to PageRank before shipping M4. This is a measurable, one-time decision.

### Tribal correction CLI (N3)

New `cmd/livedocs/tribal_cmd.go` subcommands:

```
livedocs tribal correct --fact-id=N --body="..." --reason="..." [--writer-identity=alice@example.com]
livedocs tribal supersede --fact-id=N --body="..." --reason="..." [--writer-identity=alice@example.com]
livedocs tribal delete --fact-id=N --reason="..." [--writer-identity=alice@example.com]
```

All three subcommands are thin wrappers around the existing `tribal_propose_fact` handler's action paths (create/correct/supersede are already implemented there). Delete is a new action — `UpdateFactStatus(id, "deleted")` + correction row. No new write logic, no new MCP tool.

### `tribal_mine_on_demand` MCP tool (JIT path, co-equal with batch)

New tool at `mcpserver/tribal_mine.go`:

```
tribal_mine_on_demand(symbol, repo) → runs PR comment mining for files
containing the given symbol, using the existing PRCommentMiner + UpsertTribalFact
path. Returns the set of newly-mined facts with full provenance envelopes.
```

The convergence debate elevated this from "fallback" to "co-equal path." It subsumes a class of queries where the agent knows exactly what symbol it needs context for and the pre-computed cache is a miss. The M3 cursor on `source_files.last_pr_mined` makes the second call on the same symbol a near-zero-cost no-op. Bounded by the same `DailyBudget` as batch mining.

### Sampling-based semantic drift (Should-Have)

New CLI subcommand `livedocs tribal reverify --sample=N --max-age=30d`. Implementation is a ~40-line addition to `drift/tribal.go`:

```sql
SELECT id FROM tribal_facts
WHERE status = 'active'
  AND model != ''  -- LLM-extracted only; deterministic facts are cheap to re-run
  AND julianday('now') - julianday(last_verified) > ?
ORDER BY RANDOM()
LIMIT ?
```

For each sampled fact, the reverifier fetches the current source slice referenced by `source_quote`'s `source_ref`, runs a single Haiku prompt asking "does this fact still describe this code?", and applies the verdict (accept → touch `last_verified`, downgrade → multiply `confidence` by 0.6, reject → `stale`). Cost: O(sample_size) per run, not O(facts) or O(commits).

The contrarian lens accepts S1 because it is **bounded** (explicit `--sample=N`), **off the hot path** (manual CLI invocation or cron), and **operates on aged facts** (not fresh extractions) — so the CRITIC-paper false-positive concern does not apply.

### Tribal critic loop (Should-Have, gated on empirical trigger)

Moved from M5 to S4. The convergence debate produced a specific gate: **ship S4 only after dogfood data shows (a) ≥ 50 hand-labeled facts with measurable singleton hallucination rate, AND (b) the critic's FP rate on novel/singleton content is < 10% AND it catches ≥ 30% of hallucinations the corroboration gate misses.** Absent that empirical calibration, S4 is building on sand; S1 (aged-fact sampler) does the same quality-review job on a bounded, cold-path budget.

When shipped, S4 wraps `semantic.Verifier` over `[]db.TribalFact`, runs ONCE per cluster (not per raw fact), and bypasses any cluster where `corroboration >= 3`. The bypass rule is critical: a fact with 3 independent human sources is more trustworthy than a single-LLM critic's opinion.

---

## Requirements

### Must-Have (blocks shipping Phase 3)

- **M1: `UpsertTribalFact` write path with corroboration merge + structural pre-hash scrub.**
  - **Acceptance:** Inserting two facts with the same `subject_id`, `kind`, and `cluster_key = sha256(scrub+normalize(body))` — but different `source_ref`s — produces exactly one row in `tribal_facts` with `corroboration=2` and two rows in `tribal_evidence`. Verified by `go test ./db/... -run TribalCorroboration`. Re-mining the same PR does not increment corroboration (evidence-dedup on `source_ref`). Verified by a separate idempotency test.
  - **Normalization pipeline** (per premortem F1 — structural scrubbing is safe, semantic normalization is not):
    1. Strip `@<identifier>` tokens (GitHub @-mentions)
    2. Strip `\w+\.go:\d+` file:line references
    3. Strip `L\d+` line references
    4. Strip trailing `.`, `!`, `?`, `:`, `;`
    5. Lowercase + whitespace-collapse
       Each transformation has a dedicated test asserting the pair `"@alice must hold mutex"` / `"must hold mutex"` and `"see event.go:142"` / `"see event.go:144"` both merge to the same `cluster_key`. **Stopword stripping and synonym merging remain out of scope.**
  - **Real-corpus acceptance test (required to ship M1):** `TribalCorroborationPilotCorpus` runs the M1 write path against a fixture of ≥ 100 real pilot facts drawn from the k8s scheduler mining run, with `ClusterDebugEnabled=true`. After the run, the test inspects `cluster_debug` for un-merged pairs with `body_token_jaccard > 0.7`. If > 20% of un-merged pairs exceed that threshold, M1 normalization is insufficient and ships blocked on a revised normalization. The synthetic word-order pair `"callers must hold the mutex"` / `"the mutex must be held by callers"` is documented as a known FN and remains separate, BUT `TribalCorroborationKnownFalseNegative` is inverted: it asserts the pair is separate AND that `cluster_debug` recorded a `body_token_jaccard >= 0.5` for the pair — keeping N4 instrumentation honest.
  - **Merge-rate watchdog:** `livedocs tribal stats` prints the histogram of corroboration values over LLM-extracted facts. If > 90% of LLM facts remain at `corroboration=1` after 14 days of active mining on a pilot corpus, the gate is declared defective and Phase 3.1 remediation is triggered. This is a hard alarm, not a soft KPI.

- **M2: `cluster_key` column + index + migration.**
  - **Acceptance:** Running `CreateTribalSchema` on a pre-Phase-3 claims DB adds the `cluster_key` column to `tribal_facts` with empty defaults, adds `last_pr_mined` and `pr_miner_version` columns to `source_files`, and creates `idx_tribal_facts_cluster`. `go test ./db/... -run TribalPhase3Migration` passes. Existing FTS5 search (`tribal_search_test.go`) and drift (`drift/tribal_test.go`) tests continue to pass unchanged.

- **M3: PR miner incremental state — full PR ID set, not just max (`source_files.last_pr_id_set`).**
  - **Schema change (per premortem F2):** Replace the single `last_pr_mined INTEGER` column with `last_pr_id_set BLOB` storing the full set of PR numbers from the last mining window (~40 bytes for top-10). This survives `gh pr list --search` relevance-ranking window shifts that break a `max()` cursor. `pr_miner_version TEXT` additionally encodes the pinned `gh` CLI version.
  - **Acceptance:** Running `livedocs extract --tribal=llm` twice on a repo produces zero LLM calls on the second run when no new PRs have landed. Verified by a fake `CommandRunner` that records call counts across two `ExtractForFile` invocations. `go test ./extractor/tribal/... -run PRCommentIncremental` passes. A third run with `--force-remine` resets state and produces calls equal to the first run. Calling `tribal_mine_on_demand` twice on the same symbol also produces zero LLM calls on the second call (shared cursor).
  - **Monotonicity assertion (per F2):** `TribalIncrementalCursorMonotonicity` test asserts that if a subsequent `gh pr list` window has a smaller max than the stored one (relevance-ranked shift, PR transfer, or renumbering), the affected file is flipped to `needs_remine` state and a `cursor_regression` counter is incremented. The cursor CAN advance, NEVER retreat — retreats are drift signals.
  - **`gh` CLI version preflight:** `livedocs extract --tribal=llm` checks `gh --version` against a pinned allowlist and fails fast on unknown versions. Unknown versions are ONLY accepted via `--accept-unknown-gh-version` flag, which writes the version to `pr_miner_version` so a future mismatch forces a full remine.
  - **PR-URL liveness in the drift gate:** when a fact is served, if its `source_ref` URL 404s or the `gh api` response body differs from the recorded `content_hash`, the fact is marked `stale`. Results cached for 24h; bounded by the same budget as mining.

- **M4: File prioritization via `RankFilesForMining` with invariant-preservation benchmark (per premortem F3).**
  - **Acceptance:** A fixture DB seeded with 20 source files of varying public_surface, fan_in, and existing_fact counts returns a deterministic top-10 ranking that matches a precomputed expected order. `go test ./db/... -run RankFilesForMining` passes. `livedocs extract --tribal=llm --max-files=10` processes the top-10 ranked files (verified by stdout trace), not the first 10 alphabetically.
  - **Tiered ORDER BY:** the SQL is refactored to use a two-stage ORDER BY — `ORDER BY never_mined DESC, (public_surface * 3 + fan_in - existing_facts * 5) DESC` — so it is syntactically impossible to benchmark the scalar score without first honoring the never-mined tier. The tier boost is structurally separated from the weighted score.
  - **Invariant-preservation benchmark (gating, replaces the top-K overlap gate):** before shipping M4, run the benchmark on a REAL kubernetes snapshot (≥ 1000 source files, ≥ 100 never-mined, ≥ 100 with existing facts — NOT the 4-file pilot DB). The benchmark asserts three invariants on the SQL ranking AND on any candidate replacement (including PageRank):
    1. `never_mined=1` files strictly precede `never_mined=0` files
    2. Files with `existing_facts ≥ N` decay out of the top-K within M mining runs (feedback loop)
    3. Generated/vendored paths (glob-matched) are excluded
       All three must hold on the real corpus. If the SQL formula satisfies them, ship SQL. If a candidate PageRank configuration also satisfies them AND its personalization vector is biased toward never-mined files AND damping/tolerance/max-iterations are explicitly specified, PageRank is an acceptable alternative. The top-K-overlap framing from the converge phase is replaced by this invariant check because overlap can hide the dominant tier term.
  - **Coverage-breadth SLO (continuous, not one-shot):** after shipping, the ranker's health is monitored continuously. 30-day trailing `MAX(age_of_oldest_never_mined_file_in_top_100)` must be bounded; `fraction_of_source_files_with_at_least_one_fact` must be monotonically non-decreasing until plateau. Alert fires if either stalls for > 7 days.
  - **Benchmark artifacts:** recorded in `.claude/prd-build-artifacts/m4-invariant-benchmark.md`.

- **M5: Correction CLI subcommands.** _(was M6 in the draft PRD)_
  - **Acceptance:** `livedocs tribal correct --fact-id=N --body=... --reason=...` inserts a `tribal_corrections` row with `action='correct'`, leaves the original fact visible (status='active'), and creates a new fact with the new body. `livedocs tribal supersede` sets the original to `status='superseded'` and creates a replacement. `livedocs tribal delete` sets status to `deleted` and records a correction row. All three verified by `go test ./cmd/livedocs/... -run TribalCorrectionCLI`.

- **M6: `source_quote` stability under corroboration merge.** _(was M7 in the draft PRD)_
  - **Acceptance:** A regression test (`go test ./db/... -run TribalCorroborationQuoteStability`) verifies that merging a second fact into an existing fact preserves the _first_ fact's `source_quote` (the earliest independent source) and records the later quote in the new evidence row's `source_ref`. The merge never rewrites `source_quote` or `body` on the existing row.

- **M7: `TribalMiningService` orchestration layer with differential JIT/batch parity test (per premortem F7).**
  - **Problem addressed:** the PRD's "JIT and batch share the same primitive" framing hid seven orchestration concerns — handle ownership, budget, errors, cursors, prioritization, normalization, transaction boundaries — that all diverge independently when each caller owns its own orchestration. The seven-way divergence produces cache-coherence bugs, budget double-spend, silent JIT error swallowing, cursor races, and normalization drift.
  - **Design:** New file `extractor/tribal/service.go` exposing `MineSymbol(ctx, repo, symbol, trigger)` and `MineFile(ctx, repo, path, trigger)`. Batch (`cmd/livedocs/extract_cmd.go`) and JIT (`mcpserver/tribal_mine.go`) become callers of this service; neither touches `PRCommentMiner`, `DailyBudget`, or cursor columns directly. `trigger` is an enum (`BatchSchedule | JITOnDemand | Backfill`) used ONLY for telemetry, never for behavior branching.
  - **Shared invariants enforced by the service:** (a) write-through cache invalidation — any write bumps a `tribal_facts_generation` counter that the MCP pool re-reads on every query; TTL-based caching is banned for tribal facts; (b) atomic budget — `DailyBudget.Acquire(ctx, n) (release, err)` with `UPDATE ... RETURNING` semantics, eager reservation deleted; (c) uniform error propagation — errors return structured `{"error": "rate_limited", "retry_after": ...}` responses, never silent `{"partial": true}`; (d) per-symbol transaction boundaries, identical for both callers; (e) normalization lives in exactly ONE package (`extractor/tribal/normalize`) and a CI grep test fails if any function named `normalize*` appears outside that path.
  - **Acceptance:** `tribal_jit_batch_parity_test.go` runs the same corpus through both entry points CONCURRENTLY against a shared DB file (under `go test -race`), then asserts byte-equal fact sets, cluster keys, `last_pr_id_set` values, and budget consumption. `go test -race ./extractor/tribal/...` passes. An additional unit test asserts that the `normalize*` function exists in exactly one package.
  - **Telemetry:** `TribalMiningService` emits `trigger` as a metric label. A dashboard alert fires if the ratio of facts-per-symbol between triggers exceeds 1.5× over a 24h window.

- **M8: `tribal_report_fact` MCP tool + `tribal_feedback` table + auto-labeling cron (per premortem F4).**
  - **Problem addressed:** S4's empirical trigger (≥ 50 hand-labeled facts) has no owner, no schedule, no fallback, and no realistic labeling path (the live_docs team lacks k8s domain expertise). The gate is unfalsifiable by construction; the default is "ship ungated" because no data ever arrives.
  - **Design:** New MCP tool `tribal_report_fact(fact_id, reason, details?)` where `reason ∈ {wrong, stale, misleading, offensive}`. Every report writes to a new `tribal_feedback(id, fact_id, reason, details, reporter, created_at)` table. `tribal_corrections` already exists — the rubric for "what counts as a label" unifies feedback reports AND correction rows as ground-truth sources.
  - **Operational hallucination rubric (must be written in Phase 3 docs, not deferred):** a labeled fact is `hallucination` if it meets any of — (i) a reporter flagged `reason=wrong`, (ii) a `tribal_corrections` row marks it with `action='supersede'` or `action='delete'`, (iii) the fact's body contradicts the current source at the quoted `source_ref`. A labeled fact is `correct` if it has been served ≥ 10 times with no reports over 30 days.
  - **Auto-labeling cron:** a weekly job `livedocs tribal s4-gate-status` aggregates `tribal_feedback` + `tribal_corrections` into a running hallucination rate, writes to `s4_gate_status` table, posts summary to `#live-docs-ops`. When the labeled-fact count crosses 50 AND measured FP rate / catch rate thresholds are met per S4, the cron auto-opens a "run S4 calibration experiment" issue. The gate trips on a schedule, not on human memory.
  - **Failsafe default (ships in Phase 3, not behind S4):** until S4 ships with measured thresholds, LLM-mined facts with `corroboration < 3` serve only at explicit opt-in tier via `min_confidence` parameter; default tier shows them ONLY with `degraded=true` flag in the response envelope. This makes the ungated state failsafe — agents don't see unreviewed facts unless the caller asks for them.
  - **Acceptance:** `go test ./mcpserver/... -run TribalReportFact` passes with tests for each `reason` value. `go test ./cmd/livedocs/... -run TribalS4GateStatus` passes with a fixture DB + mock date to verify the hallucination rate calculation. The `#live-docs-ops` post format is validated against a golden file.

### Should-Have (nice-to-ship, not blocking)

- **S1: Sampling-based semantic drift CLI.**
  - **Acceptance:** `livedocs tribal reverify --sample=20 --max-age=30d` samples at most 20 active LLM-extracted facts older than 30 days, runs one Haiku call per fact, and applies verdicts. Budget-tracked against `DailyBudget`. Tests cover: a fact whose code has not changed (accept → touch `last_verified`), a fact whose code has changed such that the fact is now wrong (reject → `stale`), a fact whose code is partially outdated (downgrade → `confidence *= 0.6`).

- **S2: `tribal_mine_on_demand` MCP tool.**
  - **Acceptance:** Calling the tool with `symbol=SomeFunction, repo=kubernetes` triggers the PR miner for the files containing `SomeFunction`, inserts facts via `UpsertTribalFact`, and returns the new facts' envelopes. Respects `DailyBudget`. Second call on the same symbol returns cached results and consumes zero LLM calls (shared M3 cursor). Integration test seeds a fake `CommandRunner` + mock LLM and asserts the fact count + call count on second invocation.

- **S3: `TribalConfig.MaxFilesPerRun` and `TribalConfig.CriticBudgetPercent` config fields.**
  - **Acceptance:** Both fields default correctly (`MaxFilesPerRun=100`, `CriticBudgetPercent=20`). Overriding in `.livedocs.yaml` is respected. `go test ./config/... -run TribalPhase3Fields` passes.

- **S4: Tribal critic loop reusing `semantic.Verifier`, gated on empirical trigger.**
  - **Acceptance:** Ship S4 ONLY after dogfood data records (a) ≥ 50 hand-labeled tribal facts from the pilot corpus with confidence scores and a ground-truth hallucination label, AND (b) a measured critic-FP rate < 10% on the singleton subset of those facts, AND (c) a measured critic-catch rate ≥ 30% on the hallucinated subset (i.e., the critic catches at least 30% of the hallucinations that the corroboration gate alone would pass through). Absent all three measurements, S4 is deferred to Phase 4. When shipped, the implementation is: `extractor/tribal/verifier.go` exposes `VerifyCluster(cluster []TribalFact) ([]Verdict, error)` that batches a single LLM call per cluster. Facts with `corroboration >= 3` bypass the LLM call (verified by a test asserting zero calls on a 3-source fixture). `go test ./extractor/tribal/... -run TribalCritic` passes. Budget gated by `TribalConfig.CriticBudgetPercent`. Calibration data stored in `.claude/prd-build-artifacts/s4-critic-calibration.md`.

### Nice-to-Have (Phase 3.5 — ship only if pilot signals value)

- **N1: Cross-file pattern synthesizer behind `--tribal-synth` flag.**
  - **Acceptance:** Running the synthesizer on a DB with at least 3 clusters where each has ≥ 2 distinct subjects produces zero or more `extractor='cross_file_synthesizer'` facts with `evidence` arrays referencing the source fact IDs. Ships off by default; measure kind-distribution improvement before promoting.

- **N2: Deterministic invariant extractor for `require.NoError`, `assert`, `panic("...")`, and `//nolint:` reason patterns. Ships INDEPENDENTLY of M1–M6 (zero LLM dependency, zero M1–M6 dependency).**
  - **Acceptance:** A new deterministic extractor at `extractor/tribal/assertion.go` scans Go source for assertion/panic/require statements and `//nolint:` exemption reasons and emits `kind='invariant'` facts with `model=NULL` and `confidence=1.0`. Integration test on a synthetic Go file produces ≥ 1 invariant fact per assertion type. This extractor has no budget, no LLM, no M1–M6 dependency — it can be implemented and shipped in parallel with any other Phase 3 work. Its purpose is to address the pilot's kind-distribution skew (4 invariants vs. 11 rationales) by adding a deterministic invariant source that balances the LLM-biased PR comment miner.

- **N3: Cross-repo tribal facts via xref index.**
  - **Status:** DEFERRED until the security reopen criteria (see Non-Goals) are met: per-fact egress audit log + signed-reader-identity gate + semantic-PII output pass.
  - **Acceptance criteria when reopened:** a fact written for repo A is discoverable via `tribal_context_for_symbol` from repo B when the symbol is an xref target AND the caller identity is authorized for repo A's visibility tier. A test verifies that an internal-only fact is NOT returned to a caller lacking the corresponding visibility.

- **N4: `cluster_debug` append-only calibration table with mechanically enforced lifecycle (per premortem F5).**
  - **Purpose:** records `(fact_id, cluster_key, nearest_body_match_id, body_token_jaccard, expires_at, created_at)` for each new fact to provide Phase 5 calibration data. `body_token_jaccard` is a pure-Go string similarity measure (no LLM/embeddings).
  - **Changed from draft:** ENABLED by default for pilots because M1's acceptance test requires it (see M1 real-corpus acceptance). It remains optional for production deployments.
  - **Separate DB file:** the table lives in `<repo>.cluster-debug.db` (attached via SQLite ATTACH when `ClusterDebugEnabled=true`), NOT in the main claims DB. Dropping it in Phase 5 is `rm <repo>.cluster-debug.db`, not a migration PR that three teams can block.
  - **Schema-level expiry:** `expires_at INTEGER NOT NULL DEFAULT (strftime('%s','now') + 7776000)` — 90 days. The DB opener refuses to attach the cluster-debug DB if any row is past its expiry unless an explicit `--extend-cluster-debug-expiry=<days>` flag is passed, which logs a boot warning every time.
  - **Size ceiling:** `UpsertTribalFact` refuses to insert into `cluster_debug` once row count exceeds `MaxClusterDebugRows = 50_000` with a structured warning. Prevents unbounded growth.
  - **Foreign key enforcement:** `fact_id` and `nearest_body_match_id` declared `REFERENCES tribal_facts(id) ON DELETE SET NULL`. `PRAGMA foreign_keys=ON` verified by the opener. Dangling pointers become NULL at write time, not at read time.
  - **Import-boundary lint:** a CI test in `db/tribal_test.go` greps the tree and fails if `cluster_debug` is referenced from any file outside an allowlist of exactly two paths — the writer (`extractor/tribal/upsert.go`) and a calibration reader behind `//go:build calibration`. Any PR that adds a reference from `cmd/`, `mcpserver/`, `renderer/`, or `drift/` fails the build.
  - **Phase 5 drop test, written in Phase 3:** `db/phase5_readiness_test.go` asserts `SELECT name FROM sqlite_master WHERE name='cluster_debug'` returns zero rows after running a Phase 5 migration fixture. `t.Skip()`'d until Phase 5 kickoff, at which point it becomes a gating CI check. Writing the drop test NOW means the drop is owned by Phase 3, not negotiated in Phase 5.
  - **Acceptance:** `go test ./db/... -run ClusterDebug` passes with tests for: schema attach/detach, expiry enforcement, size ceiling, foreign-key cascade, import-boundary lint, and the Phase 5 drop test compile-check.

- **N5: Runbook / incident AnnotationSource.**
  - **Status:** DEFERRED until the security reopen criteria (see Non-Goals) are met: semantic-PII pass (LLM or licensed service) + mineable-source allowlist.

---

## Risks & Mitigations

_(Diverge + converge + premortem. The premortem added R8–R11 and refined R2–R7 with specific failure narratives — see `premortem_tribal_phase3.md` for full narratives.)_

| ID      | Risk                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | Severity | Mitigation                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | Residual                                                                                                                                  |
| ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- |
| **R1**  | **Cross-repo fact leak via xref.** A fact written for internal repo A becomes discoverable via cross-repo queries from external repo B. Phase 2 input-time PII redaction does not cover semantic PII (customer names, codenames, financial details) in output facts.                                                                                                                                                                                                           | Critical | N3 is deferred until per-fact egress audit log + signed-reader-identity gate + semantic-PII output pass exist. The reopen criteria are explicit, not "someday."                                                                                                                                                                                                                                                                                                                                                                      | Low for Phase 3 (feature cut); reopens as Critical in Phase 4 if N3 is pursued without the preconditions.                                 |
| **R2**  | **Single-LLM self-critique has high FP rates on novel content.** The CRITIC paper (Gou et al., ICLR 2024) and npj Digital Medicine 2025 both measure that single-LLM critics preferentially reject long-tail/novel content — the exact content tribal mining is trying to surface. A must-have critic would become the de facto gatekeeper for ~100% of LLM facts at pilot volume (where almost nothing reaches `corroboration >= 3`).                                         | High     | Downgraded from M5 (must-have) to S4 (should-have) and gated on an empirical trigger: ship only when dogfood data shows measured FP rate < 10% AND catch rate ≥ 30%. The S1 sampling reverifier handles the same quality-review job on a bounded cold-path budget in the interim.                                                                                                                                                                                                                                                    | Low — the gate is explicit and measurable.                                                                                                |
| **R3**  | **Incremental state (`last_pr_mined`) drifts from GitHub's actual state under force-push / squash-merge / PR renumbering.** A cursor value that points at a now-dead PR blocks legitimate re-mining.                                                                                                                                                                                                                                                                           | Medium   | `--force-remine` escape hatch resets the cursor. Facts keyed on dead PR URLs decay through the existing drift gate (`content_hash` mismatch → `stale`). The cursor is advisory, not authoritative — the source of truth remains `gh pr list` output.                                                                                                                                                                                                                                                                                 | Low.                                                                                                                                      |
| **R4**  | **`cluster_key` normalization is fragile.** Word-order variations ("callers must hold the mutex" vs. "the mutex must be held by callers") hash to different keys under any non-semantic normalization, producing under-clustering.                                                                                                                                                                                                                                             | Medium   | Convergence decision: accept under-clustering as degraded-but-correct. Start with conservative normalization (lowercase + whitespace-collapse only, NO stopword strip, NO punctuation strip). A regression test documents the word-order pair as an intentional known-FN. N4's `cluster_debug` table records token-Jaccard scores to quantify the real-world FN rate. Phase 5 upgrades the normalization function via a single `UPDATE` migration once N4 data justifies it.                                                         | Medium (degraded but safe). Escalates to Low once N4 data is available.                                                                   |
| **R5**  | **Meta's published tribal knowledge blog punts on three of the Phase 3 gaps (dedup, prioritization, cost model), meaning there is no public ceiling to benchmark against.** Our decisions on these gaps are novel and may need empirical calibration.                                                                                                                                                                                                                          | Medium   | Q3 (dedup rate scaling experiment) and the M4 PageRank benchmark are the calibration levers. N4 instrumentation feeds Phase 5 decisions. All three answer "are we in the right ballpark" before we invest further.                                                                                                                                                                                                                                                                                                                   | Low — the calibration pipeline is part of the PRD, not a TODO.                                                                            |
| **R6**  | **SQL ranking weights in M4 are hand-tuned magic numbers.** `public_surface * 3 + fan_in - existing_facts * 5` has four constants with no principled derivation. At kubernetes scale, a bad ranking means the LLM budget is burned on low-value files.                                                                                                                                                                                                                         | Medium   | M4's acceptance requires a one-shot PageRank benchmark: top-10 overlap ≥ 80% → ship SQL; overlap < 80% → swap to `gonum/graph` PageRank. This replaces "trust the constants" with "measure and decide."                                                                                                                                                                                                                                                                                                                              | Low.                                                                                                                                      |
| **R7**  | **N4 `cluster_debug` table becomes load-bearing infrastructure (premortem F5).** Debug CLI, MCP "related facts" JOIN, and observability dashboards accrete on a table that ships as "temporary instrumentation." Phase 5 drop gets blocked by three teams simultaneously; parallel cluster-identification systems run in production for months.                                                                                                                                | High     | N4 updated with mechanical enforcement: separate DB file (`rm` not migration), schema `expires_at` default 90d with boot check, `MaxClusterDebugRows=50_000` size ceiling, FK with `ON DELETE SET NULL`, CI import-boundary lint grepping for `cluster_debug` outside an allowlist of two paths, Phase 5 drop test written in Phase 3 and `t.Skip()`'d until kickoff. Prose commitment converted into five independent mechanical constraints.                                                                                       | Low after M-level mitigations land.                                                                                                       |
| **R8**  | **Prose commitments aren't invariants (premortem theme across F4/F5/F6/F7).** Four of the seven premortem failure modes traced their root cause to PRD text that encoded important invariants as English sentences, not tests or schema constraints. The S4 empirical gate, the N4 TTL, the conservative-hash keystone, and the shared-orchestration promise all decayed independently under normal engineering pressure. "We documented it" theater made the drift invisible. | Critical | Every must-have requirement now has at least one mechanically-enforced check (CI test, schema constraint, CI lint, runtime alert) in its acceptance criteria. Prose-only commitments are explicitly disallowed. The premortem document flags which safeguards live in code vs. in prose, and the `/prd-build` decomposition will reject any work unit whose acceptance text says "per PRD" without a corresponding test.                                                                                                             | Medium — this is a writing-style risk and the mitigation is procedural. Residual risk is whether future PRD edits respect the convention. |
| **R9**  | **Instruments ship disabled, then nobody runs them (premortem theme across F1/F3/F6).** N4 ships disabled, the M4 benchmark runs once, the Q3 dedup-scaling experiment is a "candidate." All three are the instruments that would catch F1/F3/F6 in the first 30 days — and all three ship in a state requiring someone to remember to run them. That someone doesn't exist.                                                                                                   | High     | N4 is now enabled by default for pilot runs (M1 acceptance test requires it). The M4 benchmark is a continuous SLO (coverage-breadth monitoring), not a one-shot check. The Q3 experiment is promoted from "candidate" to "runs before M1 ships" and its output is a required artifact in `.claude/prd-build-artifacts/q3-dedup-scaling.md`. Weekly N4 histogram dashboard + merge-rate watchdog alert (fires if >90% of LLM facts remain at `corroboration=1` after 14d). Instruments shipped-disabled are now procedurally banned. | Low.                                                                                                                                      |
| **R10** | **Pilot-only validation hides production failure modes (premortem theme across F1/F6).** 21 facts × 4 files is too small a statistical regime to reveal the structural normalization failures (F1) or the conservative-hash FN rate divergence (F6). Every M-level acceptance test that validates only on pilot fixtures is a gate we already know will pass the wrong thing.                                                                                                  | High     | M1 now requires `TribalCorroborationPilotCorpus` against ≥ 100 real pilot facts with N4 enabled; M4 now requires its invariant benchmark on a real kubernetes snapshot (≥ 1000 files, ≥ 100 never-mined, ≥ 100 with existing facts). Synthetic fixtures remain useful for unit tests but cannot substitute for real-corpus acceptance on the gates that depend on statistical regime.                                                                                                                                                | Low for M1/M4. Medium for other requirements until each is audited against this standard.                                                 |
| **R11** | **Orchestration drift between batch and JIT despite "shared primitives" (premortem F2/F7).** Seven orchestration concerns — handle ownership, budget accounting, error propagation, cursor updates, prioritization, normalization function source, transaction boundaries — diverge independently when each caller owns its own orchestration. The first-order symptoms are cache-coherence bugs, budget double-spend, silent error swallowing, and cursor races.              | High     | New M7: `TribalMiningService` orchestration layer as the single choke point for all seven concerns. Differential parity test `tribal_jit_batch_parity_test.go` runs both entry points concurrently under `go test -race` and asserts byte-equal fact sets + cluster keys + cursor values + budget consumption. CI lint ensures exactly one `normalize*` function exists in the tree. Write-through cache invalidation bans TTL caching for tribal facts.                                                                             | Low after M7 lands.                                                                                                                       |

## Open Questions

- **Q1: What is Haiku's empirical confidence calibration?** The pilot produced confidence 0.60–0.92, but we don't know accuracy at 0.60. Required for choosing S4 critic thresholds. Candidate experiment: hand-label 50 facts from a pilot run and compute accuracy vs. self-rated confidence bucket. (This experiment is the S4 gate.)
- **Q2: What is the MCP tool call distribution in dogfood?** If `tribal_why_this_way` dominates, file prioritization's value is smaller than the SQL query would suggest. Instrument the MCP handlers before locking in M4's ranking weights. Combine with the M4 PageRank benchmark.
- **Q3: Does the dedup rate scale linearly with file count, or cluster around hot files?** Contrarian prediction: 40-file pilot will show ~2× duplication, not ~10×. Required to decide whether the conservative string-hash `cluster_key` is good enough for Phase 4 or whether embeddings are needed sooner. Candidate experiment: re-run the pilot on 40 files with M1 active and measure merge count + unmerged-pair count (the latter via N4 instrumentation).
- **Q4: What cosine cutoff would N4's `cluster_debug` data support for Phase 5?** Answer depends on running N4 instrumentation for ≥ 200 facts and inspecting the distribution of nearest-neighbor Jaccard scores on merged vs. unmerged facts.
- **Q5: Is `cluster_key` on the fact body sufficient, or does it need to be on `(body, source_quote)` together?** Two different comments might say the same thing in different quotes. Body-only is the simplest starting point; revisit after Q3 data is in.
- **Q6: What is the top-10 overlap between the SQL formula and PageRank on the kubernetes pilot?** This is the M4 acceptance gate — a one-shot benchmark that resolves R6.

## Research Provenance

Produced via 3-lens divergent research, then refined via a 3-advocate convergence debate.

### Diverge phase

- **Prior Art lens** — Meta's 2026-04 tribal knowledge blog, Zep/Graphiti (arXiv 2501.13956), SemDeDup (arXiv 2303.09540) + SemHash, Aider's PageRank repo-map, Microsoft GraphRAG, Vectara HHEM, Amundsen, DataHub. Argued for SemHash + Model2Vec embeddings, Aider PageRank prioritization, HHEM pairing with the critic, and unblocking cross-repo/runbook via a new mandatory LLM PII pass (M8).

- **First-Principles lens** — Grounded in `db/tribal.go`, `db/claims.go`, `mcpserver/tribal_tools.go`, `semantic/verifier.go`, `extractor/tribal/pr_comment.go`, `drift/tribal.go`. Argued that 5 of 7 gaps collapse into existing primitives with one new column (`cluster_key`) and one reused pattern (`semantic.Verifier`). Proposed the corroboration-outvotes-critic inversion and the SQL prioritization formula.

- **Failure Modes / Contrarian lens** — Cline, ForgeCode, CRITIC paper, Vectara HHEM leaderboard, Shumailov model collapse, Samsung ChatGPT incident. Argued for cutting dedup machinery (premature at 21 facts), cutting the critic entirely (single-LLM self-critique fails on novel content), promoting JIT over batch, cutting cross-repo/runbook on security grounds, and promoting the deterministic invariant extractor over more LLM mining.

### Converge phase (3-advocate debate, 2 rounds)

The three lenses were re-spawned as advocacy positions and debated the 6 key tensions. The debate produced material movement on 4 of the 6:

- **`cluster_key` normalization (tension 1):** Converged on conservative string-hash (lowercase + whitespace-collapse, NO stopword strip). The keystone argument was primitives' asymmetric-error framing: under-clustering is degraded-but-correct (recoverable), over-clustering is silent data loss (unrecoverable). All three advocates accepted this — minimum dropped its "identity function only" position, primitives specified the conservative normalization, priorart accepted the string-hash start in exchange for the N4 debug-logging instrumentation.

- **M3 incremental state (tension 3):** Converged on "keep M3 but repurpose it to serve BOTH batch and JIT." The contrarian's initial cut was softened when the advocate recognized that JIT-mine-on-demand also needs incremental state to be idempotent. M3 plumbing is useful in both worlds. Primitives's "won't run twice" challenge was what forced the clarification.

- **M5 critic loop (tension 2):** Converged on downgrading to S4, gated on empirical trigger. The CRITIC-paper false-positive evidence was strong enough that minimum refused to move on cutting M5-as-must-have; primitives conceded the downgrade in exchange for keeping M3. The gate language ("ship only when measured FP rate < 10% AND catch rate ≥ 30%") was proposed by primitives as the compromise.

- **Cross-repo / runbook (tension 4):** Converged on cut + explicit reopen criteria. Minimum's Samsung-incident framing was decisive; primitives accepted the cut; priorart dropped its M8 "mandatory LLM PII pass" compromise in favor of keeping N3/N5 as deferred-but-specified work.

Two tensions were preserved as refined trade-offs:

- **JIT vs batch (tension 3):** All three advocates agreed on "both," but the mix weight remains undetermined. At pilot volume JIT is primary; at kubernetes scale batch likely dominates. The PRD ships both code paths sharing the same `PRCommentMiner` + `UpsertTribalFact` primitives, and leaves the relative weight to be determined by dogfood telemetry (Q2).

- **M4 SQL weights vs PageRank (tension 3):** Priorart refused to move on "the magic numbers should be PageRank." The agreed resolution is a measurable benchmark: top-10 overlap on the kubernetes pilot. If ≥ 80%, ship SQL; if < 80%, swap to PageRank. This is Q6.

One tension was left unchanged:

- **Kind distribution skew (tension 5):** The pilot's 11 rationale / 6 quirk / 4 invariant skew was acknowledged by all three lenses as real but did not produce a must-have change. N2 (deterministic invariant extractor) ships INDEPENDENTLY of the LLM mining path, addressing the skew without undermining Phase 2. Minimum's preferred "promote N2 to must-have" did not carry because the extractor has zero dependencies and zero budget, so its "must-have" vs "nice-to-have" label is procedural, not architectural.

### Emergent design decisions (present after converge but not in diverge)

1. **Conservative normalization + N4 instrumentation** — neither lens proposed this pairing during diverge. It emerged from the debate when priorart accepted the string-hash start in exchange for N4 calibration instrumentation. The result is that Phase 3 ships the simpler write path while Phase 5 gets the data it needs to justify upgrading to embeddings.

2. **M3 cursor as JIT plumbing, not just batch plumbing** — the diverge phase framed M3 purely as a batch-extraction optimization. The debate revealed that JIT-mine-on-demand also needs it (to be idempotent on repeat symbol queries). This changes M3's acceptance criteria to test BOTH code paths.

3. **M4 PageRank benchmark as a gate, not a rewrite** — priorart's "port Aider PageRank" became a measurable one-shot test instead of a wholesale replacement. The SQL formula ships unless the benchmark says otherwise.

4. **S4 empirical trigger specified in the PRD text** — instead of leaving the critic loop as a vague must-have or a vague nice-to-have, the converge phase produced specific numeric conditions (≥ 50 labeled facts, FP rate < 10%, catch rate ≥ 30%) that determine when S4 is worth shipping.

### Premortem phase (7 parallel failure narratives)

7 failure agents (one per the critical concerns the pipeline surfaced) wrote prospective failure narratives from 6-12 months in the future. See `docs/design/premortem_tribal_phase3.md` for full text and the risk registry. The premortem produced material changes:

- **Promoted two new must-haves (M7, M8)** for risks that converge missed:
  - **M7** (`TribalMiningService` orchestration layer) addresses F2/F7: cursor drift and the seven-way JIT/batch behavioral divergence. The converge-phase "they share the primitive" framing was insufficient — orchestration concerns diverge independently unless a single service owns them.
  - **M8** (`tribal_report_fact` MCP tool + `tribal_feedback` table + auto-labeling cron) addresses F4: the S4 empirical gate was unfalsifiable by construction because no one owned the labeling experiment. M8 converts the gate into a dated experiment with auto-collected ground truth.

- **Amended M1** with a structural pre-hash scrub (@-mentions, `file.go:NNN` refs, trailing punctuation) and a real-corpus acceptance test. The converge-phase "conservative normalization is safe" argument held for the mutex word-order case but missed the five structural failure modes (F1) that actually appear in real PR comments.

- **Amended M3** to track the full PR ID set (not just `max()`) because `gh pr list --search` returns a relevance-ranked window, not a monotonic stream. Added cursor monotonicity assertion and PR-URL liveness check.

- **Amended M4** to replace the top-K overlap benchmark with an invariant-preservation benchmark on a real kubernetes snapshot. The converge-phase benchmark framing (F3) missed the dominant `never_mined` tier term because the pilot DB couldn't exercise it. The SQL now uses a two-stage ORDER BY to make the tier structural.

- **Amended N4** with five machine-enforceable lifecycle constraints (separate DB file, schema expiry default, size ceiling, FK enforcement, import-boundary lint, Phase 5 drop test written NOW). The converge-phase "drop in Phase 5" commitment (F5) was prose-only; five prose-to-code conversions replace it.

- **Added cross-cutting risks R8–R11** from premortem themes: prose commitments aren't invariants (F4/F5/F6/F7), instruments ship disabled then nobody runs them (F1/F3/F6), pilot-only validation hides production failure modes (F1/F6), and orchestration drift between "shared primitive" callers (F2/F7).

## Recommended Next Step

Run `/prd-build docs/design/prd_tribal_phase3_production_hardening.md` to decompose M1–M8, S1–S4, and N1–N5 into parallel work units. The premortem's mitigations are already encoded in the updated acceptance criteria, so the build can proceed without further risk analysis.
