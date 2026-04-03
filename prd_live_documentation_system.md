# PRD: Live Documentation System

> **Status:** Post-implementation (v4). Updated 2026-04-02 after Phase 1-2 completion. Phases 0-2 fully implemented and tested. Phase 3-4 acceptance criteria updated to reflect actual CLI commands and available test corpus. See `premortem_live_docs_architecture.md` for failure narratives. Previous: convergence debate (`convergence_report.md`).

## Problem Statement

Repository documentation is fundamentally broken at scale. In the Kubernetes ecosystem (79 repos, millions of lines of code), 94% of Go packages have no README, and those that do are often 6-8 years stale despite daily code commits. The code-to-doc churn ratio is 77:1 — for every doc file updated, 77 code files change. Existing tools solve one-shot generation (GoDoc, Swagger) but not continuous maintenance. The result: developers either work without documentation or build mental models from outdated artifacts.

However, "staleness" is not uniformly dangerous — roughly 70% of old documentation is still correct (stable, not stale). The real problem is twofold: (1) docs that don't exist at all (the absence problem), and (2) a small number of specific claims in docs that become wrong at unpredictable times (the drift problem). These require different solutions.

## Goals & Non-Goals

### Goals

- Detect when existing documentation is factually incorrect (drift detection)
- Generate documentation for undocumented code units (absence filling)
- Keep costs proportional to change size, not repo size (incremental pipeline)
- Work at Kubernetes scale (5M+ lines Go, 2400+ packages, 79 repos)
- Integrate into existing git/CI workflows with minimal friction (<2s for hooks)

### Non-Goals

- Replacing human-written conceptual documentation, tutorials, or guides
- Achieving 100% automated doc coverage without human review
- Cross-repo doc propagation (deferred — hard problem, tackle after core works)
- Supporting non-git version control systems
- Real-time / pre-commit doc generation (too slow, blocks developers)

## Architecture (Post-Convergence)

The system uses **claims-backed generation with verification gates**:

1. **AST Extractors** parse source code into structured claims (two tiers)
2. **Claims DB** stores all documentation facts as machine-verifiable tuples
3. **Verifier** checks claims against source on every commit (AST for structural, contract anchors for semantic)
4. **Renderers** produce human-readable output (Markdown, HTML) from verified claims

### Two-Tier Claims Model

- **Structural claims (~80%)**: Extracted and verified mechanically via AST. Zero LLM cost. (exports, types, dependencies, signatures)
- **Semantic claims (~20%)**: Extracted via LLM, verified via contract anchors + adversarial LLM review. (package purpose, usage patterns, architectural rationale)

### Three-Tier Doc Model

- **Tier 1** (auto-ship, no review): Purely AST-derived content. Export lists, type signatures, dependency graphs. go/ast doesn't hallucinate.
- **Tier 2** (auto-generated, human-approved): LLM-generated semantic content. Every claim tagged with source provenance. Verification gate required.
- **Tier 3** (detection-only, human-written): Conceptual docs, tutorials, architecture guides. System detects drift but does not generate.

## Premortem-Driven Design Constraints

> These constraints emerged from the 5-lens premortem analysis (see `premortem_live_docs_architecture.md`). They override or refine earlier convergence decisions.

1. **Usefulness validation gate** _(premortem theme 5)_: Before building extractors, hand-write target documentation for 5 representative k8s packages. Verify Tier 1 output is meaningfully better than `go doc`. If not, fast-track Tier 2 semantic claims or pivot output format. Rationale: 4/5 failure lenses identified risk that Tier 1 structural docs duplicate GoDoc/IDE tooltips.

2. **Stateless post-commit hook** _(premortem theme 3)_: The hook must NOT touch SQLite. It compares `git diff --name-only` against a flat-file manifest. A background daemon handles extraction asynchronously. Rationale: sharing SQLite between hook and extraction causes WAL contention, checkpoint stalls, and 6-12s hook latency.

3. **Composite primary key, not SCIP** _(premortem theme 1)_: Primary identity is `repo + import_path + symbol_name`. SCIP symbol strings are a secondary indexed column for optional external tooling. Rationale: kubernetes staging `replace` directives produce 47+ SCIP symbol variants per logical entity, breaking cross-repo JOINs.

4. **Extractor+grammar version in cache keys** _(premortem theme 2)_: Cache key = `content_hash + extractor_version + grammar_version`. Tool upgrades automatically invalidate cached claims. Rationale: silent regressions from grammar updates and Go version changes produce wrong-but-cached claims.

5. **Deletion-aware reconciliation** _(premortem theme 3)_: Every `git diff` that shows file deletion/rename immediately tombstones affected claims. Not deferred to batch jobs. Rationale: phantom claims from deleted files accumulate silently and corrupt rendered docs.

6. **Tree-sitter predicate boundary** _(premortem theme 4)_: Tree-sitter may only emit: `defines`, `imports`, `exports`, `has_doc`, `is_test`, `is_generated`. Predicates requiring type resolution (`implements`, `has_signature` with resolved types) are deep-extractor-only. Deep extractor always wins on conflict. Rationale: two extractors producing different claims is worse than one slow extractor.

7. **Polyglot-ready schema from day 1** _(premortem theme 5)_: Schema includes `language` field, `visibility` enum (not boolean `exported`), and scope-based paths (not Go module paths). Validated by building a trivial TypeScript extractor in Phase 2 as a smoke test.

8. **Reduced test corpus** _(premortem ops)_: Foundation phases use 5-8 representative repos (kubernetes, client-go, api, apimachinery, website, klog). Full 79-repo corpus deferred until pipeline proven. Automated sync script with `--ff-only` and health checks.

9. **Memory budget for go/packages** _(premortem theme 3)_: Hard 8GB cap via cgroup/ulimit. If exceeded, use topological-layer extraction with intermediate disk writes. Profile on 500-package subset in week 1, not month 3.

## Requirements

### Must-Have

- **Claims database**: Per-repo SQLite files with composite primary key (`repo + import_path + symbol_name`). SCIP symbols as secondary index.
  - Acceptance: `go test ./db/...` passes. `livedocs extract --repo client-go ~/kubernetes/client-go` creates a SQLite file. `sqlite3 <file> "SELECT COUNT(*) FROM symbols"` returns >100. `sqlite3 <file> "SELECT COUNT(*) FROM claims"` returns >500. Schema has `language`, `visibility` columns (polyglot-ready).

- **Go deep extractor**: `go/packages` + `go/types` + `go/doc` extracts structural claims (exports, types, signatures, doc comments, dependencies).
  - Acceptance: `go test ./extractor/goextractor/...` passes. Running extractor on `~/kubernetes/client-go/tools/cache` produces claims with predicates `defines`, `has_signature`, `imports`, `exports`, `has_doc`. Memory stays under 8GB (validated by `go test -run TestMemory` or equivalent).

- **Tree-sitter extractor**: Universal fast-path extractor with strict predicate boundary (`defines`, `imports`, `exports`, `has_doc`, `is_test`, `is_generated` only).
  - Acceptance: `go test ./extractor/treesitter/...` passes. Extractor handles `.go`, `.ts`, `.py`, `.sh` files. Running on `extractor/treesitter/testdata/sample.go` produces claims. Uses CGO-based `smacker/go-tree-sitter` (wazero evaluated and rejected — see `validation/wazero_eval.md`). Build via `Dockerfile` or `make build` for CGO compilation.

- **Diff-triggered incremental pipeline**: On commit/PR, content-hash check → re-extract affected claims → re-verify → re-render.
  - Acceptance: `go test ./pipeline/...` passes. `livedocs diff --repo client-go <commit-a> <commit-b>` outputs only changed packages. Unchanged files are skipped (verified by checking cache hits in output). Pipeline completes in <30s for a 10-file diff on client-go.

- **Verification pipeline**: AST diffing for structural claims. Contract anchors for semantic claims.
  - Acceptance: `go test ./anchor/...` passes. `livedocs verify-claims ~/kubernetes/client-go` exits 0 when docs match code, exits 1 when drift detected. Output lists specific drifted claims with file:line references.

- **Content-hash caching**: SHA-256 of source + extractor version + grammar version. LRU eviction at 2GB cap. Deletion-aware reconciliation.
  - Acceptance: `go test ./cache/...` passes. Running extraction twice on same files → second run completes in <2s (cache hit). Changing extractor version invalidates cache (verified by test). Deleting a source file and re-running → claims for that file are tombstoned (`SELECT COUNT(*) FROM claims WHERE source_file='deleted.go'` returns 0).

- **Generated code exclusion**: Positive manifest of documentable code.
  - Acceptance: Files matching `*_generated.go`, `*_zz_generated*`, `*pb.go` are excluded from extraction. `go test ./extractor/... -run TestGeneratedExclusion` passes. Running extractor on `~/kubernetes/kubernetes/pkg/apis` skips generated files.

- **Verify-script compatibility**: Output integrates with k8s `hack/verify-*.sh` pattern.
  - Acceptance: `livedocs verify` exits 0 on pass, non-zero on failure. Output format is one-line-per-issue, parseable by CI. A `hack/verify-livedocs.sh` wrapper script exists and works.

- **Usefulness validation**: Hand-written target docs for 5 packages validated against `go doc`.
  - Acceptance: Files exist at `validation/tier1_samples/{api_core_v1,apimachinery_pkg_runtime,client_go_tools_cache,klog_v2,kubelet_config}.md`. Each contains content beyond what `go doc` provides (cross-package refs, interface satisfaction, "used by" lists). `validation/tier1_verdict.md` documents the go/no-go decision.

### Should-Have

- **Stateless post-commit hook**: Compares `git diff --name-only` against flat-file manifest. Prints affected doc paths in <2s.
  - Acceptance: `livedocs check` completes in <2s on a repo with 1000+ files. Does NOT open any SQLite database (verified by `strace` or by running with no DB present). Output lists affected doc paths.

- **Tier 1 README rendering**: Structural claims rendered as "enhanced go doc" with cross-package relationships.
  - Acceptance: `livedocs export --format markdown --repo client-go ~/kubernetes/client-go/tools/cache` produces a Markdown file. Output includes sections not in `go doc`: "Used By" (reverse dependencies), "Implements" (interface satisfaction), "Cross-Package References". `go test ./renderer/...` passes.

- **Drift detection on existing docs**: Contract anchors against existing READMEs.
  - Acceptance: `livedocs verify-claims --check-existing ~/kubernetes/kubernetes` scans existing README.md files. Output reports claims that contradict current code with severity levels. `go test ./drift/...` passes.

- **Claim-level staleness scoring**: Granular per-claim staleness.
  - Acceptance: `sqlite3 <db> "SELECT COUNT(*) FROM claims WHERE last_verified < (SELECT MAX(last_modified) FROM sources WHERE sources.file = claims.source_file)"` returns the stale count. `livedocs verify-claims --staleness` reports per-claim ages.

- **Staleness canary**: Sample 50 claims per run, halt if >2% stale.
  - Acceptance: `livedocs verify-claims --canary` samples 50 random claims, re-verifies against source, prints pass/fail rate. Exits non-zero if staleness >2%.

### Nice-to-Have

- **Doc-as-PR-comment**: Preview claim impact alongside code diffs.
  - Acceptance: `livedocs prbot --dry-run <pr-url>` outputs the comment body without posting. Comment shows added/removed/changed claims per changed file.

- **Documentation canary tests**: Quality assurance for generated docs.
  - Acceptance: `livedocs verify-claims --canary-questions` generates 5 questions per package that docs should answer, checks if rendered docs contain answers.

- **Adversarial doc fuzzing**: Generate questions docs should answer.
  - Acceptance: `livedocs fuzz --repo client-go tools/cache` generates edge-case questions and checks doc completeness.

## Design Considerations (Resolved via Debate)

**Generation vs. Detection** _(resolved)_: Not either/or. AST extraction populates claims (generation). Contract anchors verify claims (detection). Claims DB unifies both. Tier 1 (structural) auto-ships; Tier 2 (semantic) requires verification gate; Tier 3 (conceptual) is detection-only.

**Sequencing** _(resolved)_: Build extraction and verification in parallel, ship in sequence. AST extraction + claims DB + verify scripts arrive together in Phase 1. Tier 1 rendering ships in Phase 2.

**Trust calibration** _(resolved)_: Tier 1 content is purely AST-derived — compiler-verified truths that can auto-ship. Any LLM-generated content is Tier 2+ and requires verification. Nothing ships without passing drift checks.

**Cost management**: Structural claims (80%) cost zero LLM tokens. Semantic claims (20%) use LLM but only for changed packages (content-hash gated). Per-commit cost < $0.10 at k8s scale.

## Phased Implementation (Post-Premortem)

**Phase 0: Usefulness Validation** — DONE

- Hand-write target documentation for 5 representative k8s packages
- Compare against `go doc` output
- Decision gate: proceed with Tier 1
- Verification: `ls validation/tier1_samples/*.md | wc -l` == 5; `cat validation/tier1_verdict.md` contains go/no-go decision

**Phase 1: Foundation — Go extractor + Claims DB** — DONE

- Go deep extractor with `go/packages` + `go/types` + `go/doc`
- Per-repo SQLite claims DB with polyglot-ready schema
- Tree-sitter extractor (universal fast path, CGO-based — wazero rejected per `validation/wazero_eval.md`)
- Generated code exclusion (`*_generated.go`, `*_zz_generated*`, `*pb.go`)
- Test on reduced corpus (5 repos)
- Verification:
  - `go test ./db/... ./extractor/... ./extractor/goextractor/... ./extractor/treesitter/...` all pass
  - `make build` or `docker build .` succeeds (CGO required — see wazero eval)
  - `livedocs extract --repo client-go ~/kubernetes/client-go` produces SQLite with >100 symbols, >500 claims
  - `sqlite3 <db> ".schema symbols"` shows `language` and `visibility` columns
  - `go test -tags integration ./integration/... -run TestExtractClientGo` passes (29k+ claims)
  - `go test ./extractor/... -run TestGeneratedExclusion` passes

**Phase 2: Incremental pipeline + Tier 1 rendering** — DONE

- Content-hash caching with extractor+grammar version in keys
- Stateless post-commit hook (flat-file manifest, no SQLite)
- Deletion-aware reconciliation
- Tier 1 "enhanced go doc" rendering with Used By, Implements, Cross-Package References
- `livedocs verify-claims` with `--staleness`, `--canary`, `--check-existing`
- `hack/verify-livedocs.sh` CI wrapper
- Verification:
  - `go test ./pipeline/... ./cache/... ./renderer/... ./gitdiff/...` all pass
  - `livedocs diff --repo client-go <old> <new>` outputs only changed packages, completes in <30s
  - `livedocs check --manifest` completes in <2s (no SQLite access)
  - `livedocs export --format markdown --repo client-go --db <db> ~/kubernetes/client-go/tools/cache` produces Markdown with "Used By", "Implements", "Cross-Package References" sections
  - `go test -tags integration ./integration/... -run TestCacheHit` passes (second run <2s)
  - `go test -tags integration ./integration/... -run TestDiffClientGo` passes (<30s)

**Phase 3: Tier 2 semantic claims + full corpus**

- Wire `--tier2` flag to `livedocs extract` command using existing `semantic/` package
- Batch extraction across all 79 kubernetes repos
- End-to-end drift detection against existing READMEs
- Security filter for sensitive content in claims
- Verification:
  - `go test ./semantic/... ./drift/... ./anchor/...` all pass
  - `livedocs extract --tier2 --repo client-go ~/kubernetes/client-go/tools/cache` produces claims with `claim_tier='semantic'` and `confidence` score in the DB
  - Verification gate rejects claims with confidence <0.7: `sqlite3 <db> "SELECT COUNT(*) FROM claims WHERE claim_tier='semantic' AND confidence < 0.7"` returns 0
  - `livedocs extract --tier2` requires `ANTHROPIC_API_KEY` env var; exits with clear error if missing
  - `go test ./semantic/... -run TestGenerator` passes with mock LLM (no real API calls in unit tests)
  - Batch corpus extraction: `livedocs extract --repo <name> ~/kubernetes/<repo>` succeeds for all 79 repos. A script `scripts/extract-corpus.sh` loops through all repos, stores DBs in `data/claims/`, and produces a summary CSV (`data/corpus-summary.csv`) with columns: repo, symbols, structural_claims, semantic_claims, duration_ms, errors
  - `data/corpus-summary.csv` shows >0 symbols for each repo with Go source files
  - Total structural claims across corpus >100,000
  - `livedocs verify-claims --check-existing ~/kubernetes/kubernetes` scans existing README.md files, reports drift with severity levels, exits non-zero if drift found
  - `livedocs verify-claims ~/kubernetes/client-go` exits 0 when structural claims match source
  - Claims containing patterns matching `password|secret|token|credential|api_key` in object_text are flagged: `sqlite3 <db> "SELECT COUNT(*) FROM claims WHERE object_text LIKE '%password%' OR object_text LIKE '%secret%' OR object_text LIKE '%api_key%'"` returns 0 (filtered during extraction)

**Phase 4: Continuous maintenance + polyglot**

- Diff-triggered pipeline on every commit (CI integration)
- Staleness canary (halt if >2% stale)
- Python and Shell extraction validation on real repos
- End-to-end incremental pipeline test
- Verification:
  - `livedocs verify-claims --canary --db <db>` samples 50 claims, exits non-zero if >2% stale
  - Python extraction: `livedocs extract --repo kubernetes ~/kubernetes/kubernetes` extracts claims from `.py` files (hack/ scripts). `sqlite3 <db> "SELECT COUNT(*) FROM claims WHERE source_file LIKE '%.py'"` returns >0
  - Shell extraction: `livedocs extract --repo kubernetes ~/kubernetes/kubernetes` extracts claims from `.sh` files (hack/ scripts). `sqlite3 <db> "SELECT COUNT(*) FROM claims WHERE source_file LIKE '%.sh'"` returns >0
  - End-to-end incremental test (integration test with build tag): create a temp repo, commit file A, extract, commit modified file A + new file B, run `livedocs diff`, verify only A and B are re-extracted, run `livedocs verify-claims`, confirm consistency
  - `livedocs check --update-manifest` generates `.livedocs/manifest` for a repo; subsequent `livedocs check --manifest` detects doc impact from `git diff` in <2s
  - `hack/verify-livedocs.sh ~/kubernetes/client-go` exits 0 (no drift in a repo with no docs to drift against)

## Open Questions (Post-Convergence)

1. ~~Granularity of doc-code linkage~~ → _Resolved: claim-level tuples, not document-level_
2. How should the system handle cross-repo documentation dependencies? _(deferred — out of MVP scope)_
3. ~~Quality threshold for auto-merge~~ → _Resolved: Tier 1 (AST-only) auto-merges; Tier 2+ requires review_
4. ~~Bootstrap strategy~~ → _Resolved: AST extraction for all packages in Phase 1, LLM enrichment in Phase 3_
5. How to prevent "documentation theater"? _(open — needs usage analytics in Phase 4)_
6. ~~CGO-free build~~ → _Resolved: wazero evaluated and rejected (no Go grammar). CGO retained with Docker/goreleaser mitigations. See `validation/wazero_eval.md`_

## Research Provenance

### Phase 1: Divergent Research (5 independent agents)

| Lens                 | Key Contribution                                                                     |
| -------------------- | ------------------------------------------------------------------------------------ |
| Prior Art            | DocAgent (5-agent architecture), Mintlify Autopilot (closest product), $2.57B market |
| Architecture         | Three-layer pipeline design, content-hash caching, 94% undocumented packages         |
| Developer Experience | Three-tier doc model, sub-2s hook design, OWNERS-based routing                       |
| Failure Modes        | Generated code trap (27.6%), 77:1 churn ratio, security leakage risk                 |
| Contrarian           | "Stale ≠ wrong" reframe, drift detection > generation, code-as-documentation         |

### Phase 2: Convergent Debate (3 advocates, 2 rounds)

| Position         | Key Contribution                                                               | What They Conceded                                          |
| ---------------- | ------------------------------------------------------------------------------ | ----------------------------------------------------------- |
| Detection-First  | Verification gate principle: nothing ships without passing checks              | Bootstrapping is necessary for 94% undocumented packages    |
| Generation-First | Bootstrapping argument: k8s's own verify pattern proves generation comes first | Structured claims are the right intermediate representation |
| Claims DB        | Integration layer insight: claims unify generation and detection               | Two-tier model needed (structural 80% + semantic 20%)       |

**Final architecture**: Claims-backed generation with verification gates. All three positions integrated.
