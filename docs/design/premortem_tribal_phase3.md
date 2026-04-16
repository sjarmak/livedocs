# Premortem: Tribal Knowledge Phase 3

**Status:** Complete. 7 failure narratives from parallel agents; synthesis below.
**Date:** 2026-04-15
**Related:** `prd_tribal_phase3_production_hardening.md`

---

## Risk Registry

Severity: Critical=4, High=3, Medium=2, Low=1. Likelihood: High=3, Medium=2, Low=1. Risk Score = Severity × Likelihood.

| #   | Failure Lens                                                                                            | Severity | Likelihood  | Risk Score | Root Cause                                                                                 | Top Mitigation                                                                                                            |
| --- | ------------------------------------------------------------------------------------------------------- | -------- | ----------- | ---------- | ------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------- |
| F1  | M1 Still Toothless (@-mentions, trailing punct, line refs defeat conservative normalization)            | Critical | High        | **12**     | M1 shipped without exercising the write path against the real pilot corpus with N4 enabled | Structural pre-hash scrub (@-mentions, `file.go:NNN` refs, trailing punctuation) + N4-enabled real-corpus acceptance test |
| F4  | S4 Gate Never Trips (no owner for labeling, unfalsifiable gate)                                         | High     | High        | **9**      | Gate specified preconditions without owner/schedule/fallback                               | Rewrite as dated experiment with `tribal_report_fact` MCP tool auto-collecting labels from feedback/corrections           |
| F5  | cluster_debug Becomes Load-Bearing                                                                      | High     | High        | **9**      | Prose lifecycle commitment without machine-enforceable constraints                         | Schema expiry + import-boundary lint + separate DB file + Phase 5 drop test written NOW                                   |
| F7  | JIT + Batch Drift (cache, budget, cursor, errors, normalization all diverge despite "shared primitive") | High     | High        | **9**      | PRD specified shared primitive but not shared orchestration                                | Single `TribalMiningService` orchestration layer + differential parity integration test in CI                             |
| F6  | Conservative Hash Premise Backfires (62% singletons at scale)                                           | High     | Medium-High | **7**      | Keystone argument reasoned about tolerance, not about FN rate at scale                     | Weekly N4 histogram dashboard + shadow key tracking + invert the known-FN test to require instrumentation                 |
| F3  | PageRank Benchmark Trap (benchmark missed the dominant tier-boost term)                                 | High     | Medium      | **6**      | Benchmark specified overlap metric without exercising the formula's actual invariants      | Invariant-preservation testing on full corpus, not top-K overlap on pilot; tiered ORDER BY structure                      |
| F2  | Silent Cursor Drift (`gh --search` is relevance-ranked, not monotonic)                                  | High     | Medium      | **6**      | `last_pr_mined = max(prNumbers)` assumes monotonic sorted stream                           | Track full PR ID set, not just max; cursor monotonicity assertion; PR-URL liveness in drift gate                          |

**Highest-risk findings are F1, F4, F5, F7 — all four score ≥ 9.** F1 is the highest because it replays the original Phase 3 motivating bug (toothless gate) after shipping.

---

## Cross-Cutting Themes

### Theme A: Prose commitments aren't invariants (F4, F5, F6, F7)

Four of the seven failure modes come from the same underlying weakness: the PRD encoded important commitments as English sentences rather than as tests, alerts, schema constraints, or CI gates. The S4 empirical gate (F4), the N4 TTL (F5), the conservative-hash keystone (F6), and the shared-orchestration promise (F7) all live only in the PRD's prose. Each one decays independently under normal engineering pressure.

**Combined severity: Critical.** If more than one of these prose-only commitments drifts, Phase 3 ships an architecture that looks like the PRD but behaves differently. The "we documented it" theater makes the drift harder to see.

**Why this convergence is credible:** four independent failure agents, none of whom saw each other's work, all identified prose commitments as the root cause of the failure mode they were assigned. This is high-confidence signal that the PRD's writing style itself is the risk, not any individual decision.

### Theme B: Measurements ship disabled, then nobody runs them (F1, F3, F6)

N4 `cluster_debug` is disabled by default. The M4 PageRank benchmark runs once. The "dedup rate scaling experiment" (Q3) is a candidate. All three are instruments that, if running continuously, would catch the failures in F1/F3/F6 in the first 30 days. But they ship in a state that requires someone to remember to run them, and that someone doesn't exist.

**Combined severity: High.** Instruments that ship disabled are not instruments; they are documentation. The PRD's instrumentation strategy has the same reliability as its prose commitments.

### Theme C: Pilot volume (21 facts) cannot reveal production failure modes (F1, F6)

Both F1 and F6 turn on the observation that 21 facts × 4 files is too small to expose the failure mode the design was optimized for. At 500-3500 facts, F1's structural normalization failures appear; at 3500 facts, F6's conservative-hash FN rate diverges from the pilot's rate. The PRD's acceptance tests all pass against pilot-scale fixtures. Production is a different statistical regime.

**Combined severity: High.** Any M-level acceptance criterion that validates only on pilot fixtures is a gate we already know will pass the wrong thing.

### Theme D: Two code paths with "shared primitives" drift on the orchestration seven different ways (F2, F7)

F2 (cursor drift) and F7 (JIT/batch divergence) are the same failure mode at different granularities. The PRD's "JIT and batch share the same code path" framing hid seven orchestration concerns (handle ownership, budget, errors, cursors, prioritization, normalization, transactions) that were never specified to be shared. Any reasonable engineering decision on any of the seven concerns produces drift.

**Combined severity: High.** The second- and third-order failures from orchestration drift cascade: F2's cursor race compounds F7's normalization drift compounds F1's dedup failure. A single fix at the orchestration layer prevents multiple downstream cascades.

---

## Mitigation Priority List

Ranked by number of failure modes addressed, severity of those modes, and implementation cost.

| Rank | Mitigation                                                                                                                         | Failures Addressed                                                        | Cost       | Rationale                                                                                                                 |
| ---- | ---------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- | ---------- | ------------------------------------------------------------------------------------------------------------------------- |
| 1    | **Flip N4 `cluster_debug` to ON by default for pilot runs; add N4-enabled real-corpus test to M1 acceptance**                      | F1, F6                                                                    | Low        | One config flag flip + one test. Addresses the two highest-severity failures.                                             |
| 2    | **Add structural pre-hash scrub to M1 normalization: strip `@-mentions`, `file.go:NNN` refs, trailing punctuation before hashing** | F1 (primary), F6 (secondary)                                              | Low        | Three regex passes. Distinguishes "structural scrubbing" (safe) from "semantic normalization" (dangerous) per R4.         |
| 3    | **Single `TribalMiningService` orchestration layer + differential JIT/batch parity test in CI**                                    | F2, F7                                                                    | Medium     | ~300 lines of refactor + one new test file. Prevents an entire class of cascade failures.                                 |
| 4    | **Ship `tribal_report_fact` MCP tool + `tribal_feedback` table NOW (part of Phase 3)**                                             | F4 (primary), F1 (secondary — production signal for "the gate is broken") | Low        | One MCP tool + one table. Converts S4's unfalsifiable gate into a dated experiment with auto-collected labels.            |
| 5    | **Weekly N4 histogram dashboard + merge-rate watchdog alert (fires if <X% merged after 14d mining)**                               | F1, F6                                                                    | Low        | One cron + one alert. Turns Theme B's dormant instruments into an active feedback loop.                                   |
| 6    | **cluster_debug lifecycle: schema expiry + import-boundary lint + separate DB file + Phase 5 drop test landing in Phase 3**        | F5                                                                        | Low-Medium | Four mechanical enforcements. Encodes the PRD prose as machine-checked constraints.                                       |
| 7    | **M4 benchmark on real kubernetes snapshot (not pilot DB) + invariant-preservation testing (not top-K overlap)**                   | F3                                                                        | Medium     | Requires a kubernetes-scale fixture and explicit invariant tests for `never_mined` tier boost and `existing_facts` decay. |
| 8    | **Track full PR ID set in cursor (not just max) + cursor monotonicity assertion + pin `gh` CLI version**                           | F2                                                                        | Low        | Schema column + one check + a preflight. Addresses the highest-severity cursor failure.                                   |
| 9    | **Gate default-tier serving on a measured floor (alarm if `corroboration >= 2` filters >40% of LLM corpus)**                       | F1, F6                                                                    | Low        | One metric + one alert. Production canary for the toothless-gate regression.                                              |
| 10   | **Operationally define "hallucination" via a rubric with concrete k8s examples, land in Phase 3 docs**                             | F4                                                                        | Low        | Prerequisite for any labeling experiment.                                                                                 |

---

## Design Modification Recommendations

The top changes to apply to the PRD, ordered by risk reduction per unit of engineering effort.

### 1. Amend M1 to require real-corpus validation with N4 enabled

**Current:** M1 acceptance tests `TribalCorroboration` (synthetic pair merge), `TribalCorroborationKnownFalseNegative` (word-order pair stays separate), and `TribalCorroborationQuoteStability`.

**Required addition:** M1 acceptance must include `TribalCorroborationPilotCorpus` — a test that runs `UpsertTribalFact` against a fixture of ≥ 100 real pilot facts (drawn from the k8s scheduler pilot or equivalent) with `ClusterDebugEnabled=true`, asserts the merge rate falls within `[X%, Y%]` based on a pre-measured expected count, AND inspects the `cluster_debug` Jaccard distribution for un-merged pairs with `body_token_jaccard > 0.7`. If > 20% of un-merged pairs exceed that threshold, M1 normalization is insufficient and ships blocked on a revised normalization.

**Addresses:** F1, F6. **Cost:** Low (test + fixture).

### 2. Add structural pre-hash scrub to cluster_key normalization

**Current:** `cluster_key = SHA-256(lowercase + whitespace-collapse(body))`. NO stopword strip, NO punctuation strip.

**Required change:** Normalization now strips, in order:

1. `@<identifier>` tokens (GitHub @-mentions)
2. `\w+\.go:\d+` references (file:line citations)
3. `L\d+` line references
4. Trailing `.`, `!`, `?`, `:`, `;`
   Then applies lowercase + whitespace-collapse as before. Each transformation has a dedicated test. The PRD's R4 framing is refined: "structural scrubbing" of @-mentions, file-path citations, and trailing punctuation is safe (transformations preserve meaning by construction); "semantic normalization" of stopwords and synonyms remains out of scope.

**Addresses:** F1 (primary), F6 (secondary). **Cost:** Low.

### 3. Replace "shared primitive" with "single orchestration service"

**Current:** The PRD says batch and JIT "share the same `PRCommentMiner` + `UpsertTribalFact` code path; only the trigger differs."

**Required change:** Introduce a new Must-Have M7: `TribalMiningService` at `extractor/tribal/service.go` exposing `MineSymbol(ctx, repo, symbol, trigger)` and `MineFile(ctx, repo, path, trigger)`. Batch (`cmd/livedocs/extract_cmd.go`) and JIT (`mcpserver/tribal_mine.go`) become callers of this service; neither touches `PRCommentMiner`, `DailyBudget`, or cursor columns directly. `trigger` is an enum used only for telemetry. Acceptance: a differential integration test `tribal_jit_batch_parity_test.go` runs the same corpus through both entry points concurrently against a shared DB file (under `go test -race`) and asserts byte-equal fact sets, cluster keys, cursor values, and budget consumption.

**Addresses:** F2, F7 (both). **Cost:** Medium (~300 lines refactor + parity test).

### 4. Ship `tribal_report_fact` MCP tool as M8 (part of Phase 3, not deferred)

**Current:** S4 is gated on hand-labeling 50 facts, with no owner or labeling path.

**Required change:** Add M8 — a new MCP tool `tribal_report_fact(fact_id, reason, details?)` where `reason ∈ {wrong, stale, misleading, offensive}`. Every report writes to a new `tribal_feedback` table. A new weekly cron `s4_gate_status` aggregates `tribal_feedback` + `tribal_corrections` into a running hallucination rate and posts to `#live-docs-ops`. When the labeled-fact count (feedback + corrections) crosses 50, the cron auto-opens a "run S4 calibration experiment" issue. The S4 gate now trips on a schedule, not on a human remembering.

Additionally: until S4 ships with measured thresholds, LLM-mined facts with `corroboration < 3` serve only at explicit opt-in tier (`filter: llm_unverified`), never at default. This makes the ungated state failsafe.

**Addresses:** F4 (primary), F1 (secondary — production signal for "the gate is broken"). **Cost:** Low-Medium (one MCP tool + one table + one cron + default-tier filter change).

### 5. Bake N4 lifecycle into mechanical constraints

**Current:** N4 is "disabled by default; Phase 5 drops it via migration."

**Required change:**

- Schema: `CREATE TABLE cluster_debug ... expires_at INTEGER NOT NULL DEFAULT (strftime('%s','now') + 7776000)`. DB opener fails if any row is past expiry unless an opt-in flag logs a boot warning.
- Import-boundary lint: a test in `db/tribal_test.go` greps the tree and fails CI if `cluster_debug` is referenced from any file outside an allowlist of exactly two paths (the writer + an opt-in calibration reader behind `//go:build calibration`).
- Separate DB file: `cluster_debug` lives in `<repo>.cluster-debug.db`, attached only when `ClusterDebugEnabled=true`. Dropping it in Phase 5 is `rm`, not a migration PR that three teams can block.
- Size ceiling: `UpsertTribalFact` refuses to insert into `cluster_debug` past `MaxClusterDebugRows = 50_000` with a structured warning.
- Phase 5 drop test: `db/phase5_readiness_test.go` asserts the table does not exist after the Phase 5 migration. `t.Skip()`'d until Phase 5 kickoff; the test itself lands in Phase 3.

**Addresses:** F5. **Cost:** Low-Medium (four mechanical enforcements, each ~30 lines).

### 6. Amend M3 cursor semantics to survive `gh --search` relevance ranking

**Current:** `source_files.last_pr_mined = max(prNumbers)`.

**Required change:**

- Replace single `last_pr_mined INTEGER` column with `last_pr_id_set BLOB` storing the full set of PR numbers in the last window (~40 bytes for top-10).
- On each new mining run, diff the set. If old IDs fall out while new IDs enter AND the new max < stored max, flip the file to `needs_remine` and log a `cursor_regression` counter.
- Preflight check: verify `gh --version` is a known-good version; fail fast on unknown versions rather than silently adopting new `--search` semantics.
- `pr_miner_version` now includes the pinned `gh` CLI version.
- Add PR-URL liveness to the drift gate: when a fact is served, if its `source_ref` URL 404s, mark the fact `stale`. Budget: 1 `gh api` call per served fact, cached for 24h.

**Addresses:** F2. **Cost:** Low (one schema column change + one preflight + one drift gate addition).

### 7. Reframe M4 benchmark from "top-K overlap" to "invariant preservation on real corpus"

**Current:** Top-10 overlap on pilot DB, 80% threshold.

**Required change:**

- Benchmark runs on a real kubernetes snapshot (≥ 1000 source files, ≥ 100 never-mined, ≥ 100 with existing facts).
- Benchmark measures invariant preservation, not overlap:
  - Invariant 1: `never_mined=1` files strictly precede `never_mined=0` files in the ranking
  - Invariant 2: Files with `existing_facts ≥ N` decay out of the top-K within M runs
  - Invariant 3: Generated/vendored paths are excluded
- Any candidate replacement (including PageRank) must satisfy all three invariants on the real corpus before shipping. If PageRank, specify damping, convergence tolerance, maximum iterations, and personalization vector explicitly.
- Add a coverage-breadth SLO: 30-day trailing coverage (fraction of source files with ≥ 1 active fact) must grow monotonically until plateau, and `MAX(age_of_oldest_never_mined_file_in_top_100)` must be bounded.
- Continuous, not one-shot: the production ranker is whichever one currently satisfies the invariants, re-measured weekly.

**Addresses:** F3. **Cost:** Medium (real-corpus fixture + invariant tests + SLO wiring).

---

## Blocks-Shipping vs. Residual Risks

### Must block Phase 3 shipping (addressed in must-have changes):

- **F1:** amend M1 with structural scrubbing + real-corpus acceptance test (mitigation 1 + 2)
- **F4:** ship M8 (`tribal_report_fact`) and default-tier filter change before Phase 3 reaches production pilots (mitigation 4)
- **F7:** single `TribalMiningService` + differential parity test (mitigation 3)

### Should block (addressed in should-have changes):

- **F5:** cluster_debug lifecycle mechanics (mitigation 5) — ship in Phase 3 even though the failure only manifests in Phase 5
- **F6:** weekly N4 histogram + merge-rate watchdog (mitigation 5 of list above) — ship in Phase 3 to catch the premise-breakage

### Acceptable residual risks (known, monitored, unblocked):

- **F2:** cursor drift — Medium likelihood because it requires a `gh` CLI behavior change or history rewrite; the mitigation (full ID set tracking) is clean but non-urgent
- **F3:** PageRank trap — Medium likelihood because it only trips if the SQL formula benchmark fails; the keystone mitigation is "don't benchmark on the pilot DB"

---

## Full Failure Narratives

_(Narratives are preserved in full for reference. Key findings are in the risk registry and themes above.)_

### F1: M1 Still Toothless

M1 shipped on schedule. All synthetic tests passed. The three duplicate scheduler facts from the original pilot merged correctly. By day 11, the pilot operator reported "tribal_context_for_symbol returns empty on scheduler code paths" — the same bug Phase 3 was designed to fix. Investigation found 374 of 380 LLM facts had `corroboration=1` after 30 days of active mining. The six with `corroboration ≥ 2` were false positives from a squash-merge cursor bug (F2), not real merges.

Hand-sampling 40 unmerged pairs revealed the real distribution: `@alice pointed out that X` vs `X` (defeat by @-mention prefix), `must hold the mutex` vs `must hold the mutex.` (trailing period), `nil slice panics` vs `empty slice panics` (synonym variance), `see event.go:142` vs `see event.go:144` (line-number drift). The documented mutex word-order case was literally zero of the 40 samples. The R4 mitigation rule ("no aggressive fix without N4 data") blocked the remediation because N4 was disabled by default.

After three months the infra team quietly reconfigured the default tier to `all_llm` to make the tool return something, reintroducing the Phase 2 hallucination surface without an ADR. Root cause: M1's acceptance tests validated against a synthetic pair and the 3-duplicate pilot case, but didn't require running the write path against the full pilot corpus with N4 enabled.

### F2: Silent Cursor Drift

2026-07-15: a kube SRE reports `tribal_context_for_symbol("kubelet")` returning a rationale about lock ordering that the scheduler team had undone in a March squash-merge. An agent trusted the stale fact and shipped a PR that reintroduced the exact race condition the fact warned against; canary flaked and the regression shipped.

Root cause: the March 2026 `gh` CLI 2.68 release changed `--search` to use relevance ranking instead of `created-desc`. `last_pr_mined = max(prNumbers)` stopped being monotonic — an old PR bubbling into the search window could make the max decrease. The cursor advanced past PRs that were never fetched. The drift gate (`content_hash` mismatch → stale) can't catch this because `source_quote` is verbatim and M6 guarantees it's never rewritten — so a force-push that changes comment IDs but preserves quoted text looks identical to the drift walker.

847 facts across 6 repos had evidence referencing dead/renumbered PRs. The `--force-remine` escape hatch existed but was slow (~47 minutes per repo) and used only twice in production. The batch path had been exiting 1 since May with errors buried in systemd logs nobody read; JIT silently degraded to "serve cached" on the same errors.

### F3: PageRank Benchmark Trap

M4's benchmark ran once on the 4-file pilot DB. Overlap came out 78% (below the 80% threshold), so the team followed the escape hatch and swapped SQL for PageRank. Three months later: 94% of 312 facts lived in the same 47 files. 11,000+ files had never produced a fact.

Root cause: the benchmark measured top-10 overlap but the SQL formula's dominant term was `never_mined * 1000` — a tier boost that the pilot DB's 4 fully-mined files couldn't exercise. PageRank silently erased the "never-mined files first" invariant when it shipped. `gonum/graph` PageRank under uniform personalization ranked utility sinks (`klog.Info`, `fmt.Errorf`, `context.Context`) at the top. The `-existing_facts * 5` feedback loop was also gone, so the top-10 was deterministic and re-mined forever.

The fix was a revert to SQL. Three months of Haiku budget (~$1,800) was spent re-reading the same 47 files. The pilot's 78% overlap number was also statistical noise — top-10 on a 4-file corpus is actually top-4. Phase 3's central goal (making the corroboration gate work) had been silently blocked by the M4 revert's own invariant loss.

### F4: S4 Gate Never Trips

Phase 3 shipped on schedule. S4 sat in the backlog with a one-line status: "gated on empirical trigger." No sprint owned the labeling experiment. An intern tried and bounced off it — reading a rationale fact about k8s scheduler preemption required knowledge nobody on the live_docs team had. Someone proposed Claude-Opus auto-labeling; the security-reviewer agent caught the circularity.

S1 (aged-fact reverifier) shipped and produced weekly cron output ("3 facts flagged, 2 accepted, 0 rejected"), creating a felt sense of quality review that was actually drift detection, not hallucination detection. Three `tribal-facts-weird` tickets were triaged individually without ever being aggregated against a rate, because no one had defined what "hallucination" meant operationally.

2026-07-09: an external contributor's PR was reviewed by a Claude Code agent that called `tribal_why_this_way` and retrieved a confident invariant: "this controller must never be reconciled concurrently with the garbage-collector controller." The fact was `corroboration=2`, `confidence=0.87`, with a source_quote from a comment that had been removed in a 2025 refactor. The agent rewrote worker-pool initialization to serialize; 36 hours later canary deadlocked.

The team shipped S4 under pressure using feedback + correction rows as ground truth (62 labels in 4 days, measured FP rate 14% — above the 10% gate). Everyone agreed the gate violation was correct. Root cause: empirical preconditions without owner/schedule/fallback = deferred indefinitely.

### F5: `cluster_debug` Becomes Load-Bearing

N4 shipped disabled-by-default. In July 2026 a pilot flipped it on. Within a week, Dan hacked up `livedocs tribal debug-cluster <fact-id>` to answer reviewer questions about missed merges; it became the default debugging tool. In August, Priya noticed `nearest_body_match_id` was exactly the pointer she needed for a "related facts" feature in `tribal_why_this_way`; she added a two-line JOIN and the MCP tool average call count dropped 15%. Observability built a Grafana dashboard on `body_token_jaccard` percentiles; the 0.42 median became a KPI.

October: Phase 5 planning opened. The migration PR proposed `DROP TABLE cluster_debug`. It got three blocking reviews in two hours — platform, MCP-tools, observability all had dependencies. The table had grown to 2.1 GB. `nearest_body_match_id` had no FK and produced dangling pointers on superseded facts; the MCP "related facts" feature returned nulls at random and had been misattributed to agent flakiness for weeks.

The "migrate the valuable parts" compromise was scoped at two weeks; three months later Phase 5 was still blocked. Two parallel cluster-identification systems were running in production. Root cause: the lifecycle commitment was documented but never encoded as a machine-enforceable constraint.

### F6: Conservative Hash Premise Backfires

By January 2027, 3,500 LLM facts. Only 412 (11.8%) had merged into multi-source clusters. 3,088 were singletons; 2,156 of those (62% of all LLM facts) were filtered out by the corroboration gate at default tier. N4 Jaccard histograms showed a clean bimodal distribution: 1,140 facts at Jaccard 0.28 (correctly unmerged) and 1,850 facts at Jaccard 0.71 (should-have-merged but didn't). Spot-checking 120 random facts from the 0.71 mode: 78% were human-confirmed as "should have merged."

The 21-fact pilot's 14% duplication rate had not survived scale. The keystone argument — "under-clustering is degraded-but-correct" — had assumed under-clustering was rare AND that rare degradation was tolerable. The first assumption was wrong, which broke the second.

Debate: re-hash in place (retroactive merge semantics are a nightmare — whose quote wins? what about existing corrections?), wait for 10k facts (product was already demanding recall), rebuild Phase 2 mining. The team shipped a bulk UPDATE via `tribal migrate-cluster-keys` with a manual review queue. 847 merges were auto-applied, 162 queued for human adjudication, 23 quarantined because they contradicted existing correction rows.

Root cause: the keystone converge-phase argument treated "under-clustering vs over-clustering" as the only axis, never empirically measuring the FN rate at scale.

### F7: JIT + Batch Drift

The PRD's shared-primitive claim held for about six weeks. 2026-07-15: a user reports that `livedocs tribal context kubelet` and `tribal_mine_on_demand("kubelet", "kubernetes")` return different fact sets for the same symbol. Diagnosis: six divergences at once.

1. **Cache coherence:** MCP pool's tribal-fact cache had a 15-minute TTL and no invalidation on `UpsertTribalFact`. Batch writes were invisible to JIT reads until TTL expired.
2. **Budget double-spend:** batch reserved eagerly, JIT per-call. On 2026-07-12 they collectively spent 1,340 PR fetches against a 1,000-PR daily cap.
3. **Error handling:** batch propagated `gh api` errors to exit code; JIT wrapped in `defer recover()` and returned `{"partial": true}` which MCP clients rendered as empty.
4. **Cursor race:** batch wrote `last_pr_mined` at end-of-file; JIT wrote per-call. A JIT call wrote 8421, concurrent batch rolled it back to 8400, next JIT re-mined 8401-8421 producing duplicates.
5. **Prioritization:** batch used `RankFilesForMining`; JIT just mined whatever file contained the symbol. Different fact sets for the same symbol.
6. **Normalization drift:** `cluster_key` computed by `tribal/normalize.go` in batch; JIT had copy-pasted the function during an early import cycle and never updated after a 2026-06-28 bug fix to strip GitHub suggestion blocks. Byte-identical bodies hashed differently across paths.
7. **Transaction boundaries:** batch held 30s per-file write txns; JIT held per-call 2s txns. The effective isolation was "read committed across handles, read uncommitted within handle" — invented by accident.

Root cause: the PRD specified a shared primitive but not a shared orchestration layer, so seven concerns around the primitive diverged independently with no single place to enforce parity.
