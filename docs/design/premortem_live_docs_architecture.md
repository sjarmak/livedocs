# Premortem: Live Docs Architecture

> 5-lens prospective failure analysis of the claims-backed documentation system.
> Generated 2026-03-31 via independent failure agents (no agent saw others' narratives).

---

## Risk Registry

| #   | Failure Lens             | Severity     | Likelihood | Risk Score | Root Cause                                                                                               | Top Mitigation                                                                                      |
| --- | ------------------------ | ------------ | ---------- | ---------- | -------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| 1   | Technical Architecture   | Critical (4) | High (3)   | **12**     | SCIP symbol format + claims-as-triples data model wrong for kubernetes's build structure                 | Replace SCIP primary key with `repo+import_path+name`; add doc usefulness acceptance test           |
| 2   | Integration & Dependency | Critical (4) | High (3)   | **12**     | CGO tree-sitter bindings + hand-rolled SCIP formatter = two fragile surfaces with no stability contracts | WASM tree-sitter runtime; include extractor+grammar version in cache keys                           |
| 3   | Operational              | Critical (4) | High (3)   | **12**     | Single SQLite DB shared between latency-sensitive hook and long-running extraction                       | Split into hook-optimized read DB + heavyweight claims store; make hook stateless                   |
| 4   | Scope & Requirements     | Critical (4) | Medium (2) | **8**      | Tier 1 structural docs duplicate GoDoc/IDE — the 80% we extract is the least valuable 80%                | Validate usefulness before building; fast-track semantic claims; user research on k8s maintainers   |
| 5   | Scale & Evolution        | Critical (4) | High (3)   | **12**     | Go-specific assumptions baked into "language-agnostic" schema                                            | Language-prefix symbols; build minimal TS extractor in Phase 2 as smoke test; partition DB per-repo |

---

## Cross-Cutting Themes

### Theme 1: SCIP Symbol Format is a Liability, Not an Asset (Lenses 1, 2, 5)

All three lenses independently identified SCIP symbol strings as a failure vector — but for different reasons. The technical lens found kubernetes staging `replace` directives produce 47+ symbol variants per logical package, breaking cross-repo JOINs. The dependency lens found the pre-1.0 SCIP spec (v0.7.0→v0.8.0) adds fields that break hand-rolled formatters. The scale lens found Go-specific module path encoding makes the schema non-portable to other languages.

**Combined severity:** This is the single highest-confidence risk. Three independent analysts converged on it. The SCIP symbol format was chosen for cross-repo joins, but the kubernetes build system actively fights canonical symbol resolution. The format adds complexity that produces worse results than a simpler `repo+import_path+name` composite key.

### Theme 2: Content-Hash Cache is Fundamentally Broken for Go (Lenses 1, 2, 3)

File-level content hashing assumes file independence, but Go claims have cross-package type dependencies. The technical lens found 12-15% stale claims from unresolved cross-package references. The dependency lens found tool upgrades (grammar changes, Go version changes) produce silently wrong claims that the cache never invalidates. The operational lens found deleted files leave phantom claims forever.

**Combined severity:** The cache is the incremental pipeline's foundation. If it's wrong, everything downstream is wrong. Three independent failure modes (cross-file deps, tool version drift, file deletion) all exploit the same gap: the cache key doesn't capture the full input set.

### Theme 3: go/packages Memory Explosion on kubernetes (Lenses 1, 3, 5)

The technical lens found 14GB RSS on a single repo. The operational lens found it crowds out the dev machine (16GB total) and triggers swap. The scale lens found full 79-repo extraction peaks at 22GB. All three independently recommend per-package or topological-layer extraction with intermediate disk writes.

**Combined severity:** This isn't an edge case — it's the expected behavior of the primary test corpus. The 8-16GB estimate from the PRD is optimistic for the full monorepo.

### Theme 4: Tree-sitter Fast Path Produces Different Claims Than Deep Extractor (Lenses 1, 2)

The technical lens found tree-sitter misses interface satisfaction, struct embedding, and build tags — producing claim inconsistencies with `go/types`. The dependency lens found grammar updates silently rename AST node types, dropping claims without errors. Both found the content-hash cache can't reconcile two sources of truth.

**Combined severity:** Two extractors producing different claims for the same code is worse than one slow extractor. The "fast path" creates a consistency problem that undermines trust in all output.

### Theme 5: Tier 1 Structural Docs May Be Worthless (Lenses 1, 4)

The technical lens found rendered Tier 1 output "reads like a symbol index, not documentation" — less useful than `go doc`. The scope lens found this duplicates GoDoc and IDE tooltips. Both conclude the 80% structural claims are the least valuable 80%.

**Combined severity:** If the MVP output (Tier 1) doesn't provide value over existing tools, the project has no validation signal. All the infrastructure complexity is in service of output nobody wants.

---

## Mitigation Priority List

Ranked by: failure modes addressed, severity, and implementation cost.

| Priority | Mitigation                                                     | Failure Modes Addressed                                                   | Cost                             |
| -------- | -------------------------------------------------------------- | ------------------------------------------------------------------------- | -------------------------------- |
| 1        | **Include extractor version + grammar version in cache keys**  | Silent regressions (2), stale claims (1, 3), phantom claims (3)           | Low                              |
| 2        | **Replace SCIP primary key with `repo+import_path+name`**      | Symbol fragmentation (1), cross-repo JOIN failures (1, 5), spec drift (2) | Low (if done before data exists) |
| 3        | **Split SQLite into hook-read-DB + claims-write-DB**           | Hook latency (3), WAL contention (3), checkpoint stalls (3)               | Medium                           |
| 4        | **Define tree-sitter's semantic boundary explicitly**          | Two-source-of-truth (1, 2), silent claim inconsistency (1)                | Low                              |
| 5        | **Run doc usefulness acceptance test before building**         | Building wrong thing (4), wasted effort on Tier 1 (1, 4)                  | Low                              |
| 6        | **Replace CGO tree-sitter with WASM runtime**                  | CGO build fragility (2), grammar version pinning (2), CI breakage (2)     | Medium                           |
| 7        | **Profile go/packages on 500-package subset in week 1**        | Memory explosion (1, 3, 5), swap/OOM (3)                                  | Low                              |
| 8        | **Add deletion-aware reconciliation to every git diff**        | Phantom claims (3), ghost symbols (3)                                     | Low                              |
| 9        | **Build minimal TS extractor in Phase 2 as schema smoke test** | Go-specific schema assumptions (5), late migration (5)                    | Medium                           |
| 10       | **Cap test corpus to 5-8 repos until pipeline proven**         | Operational burden (3), disk/sync overhead (3), false positives (3)       | Low                              |
| 11       | **Package-level cache keys (transitive input hash)**           | Cross-file dependency staleness (1, 3)                                    | High                             |
| 12       | **Use WASM or conformance tests for SCIP symbol generation**   | Spec drift (2), formatter divergence (2)                                  | Medium                           |

---

## Design Modification Recommendations

### 1. Simplify the Identity Model (addresses lenses 1, 2, 5)

**Change:** Replace SCIP symbol strings as the primary key with `repo + import_path + symbol_name` composite key. Store SCIP symbols as a secondary indexed column for optional external tooling interoperability.

**Why:** The kubernetes build system (staging symlinks, `replace` directives, multi-module repos) produces dozens of SCIP symbol variants per logical entity. A simpler key that doesn't encode version strings eliminates fragmentation at the source. Cross-repo joins become trivial (`WHERE import_path = 'k8s.io/api/core/v1' AND name = 'Pod'`) instead of requiring a normalization layer.

**Effort:** Low if done before any data exists. The schema change is ~10 lines. Deferring this creates a multi-million-row migration problem (as the scale lens predicted).

### 2. Make the Hook Stateless (addresses lenses 3, 1)

**Change:** The post-commit hook should not touch SQLite at all. It should: (a) run `git diff --name-only HEAD~1`, (b) compare against a flat-file manifest of known paths, (c) print affected doc paths. A background daemon reads an append-only change log and runs extraction/verification asynchronously.

**Why:** Any design where a latency-sensitive hook shares a database with a long-running batch job will fail under real concurrent use. Decoupling them entirely eliminates the contention problem rather than mitigating it.

**Effort:** Low. The hook becomes a simple shell script. The background daemon is new work but simpler than the WAL-tuning workarounds.

### 3. Validate Usefulness Before Building Infrastructure (addresses lenses 4, 1)

**Change:** Before building extractors, hand-write the documentation you want to produce for 5 representative kubernetes packages. Then verify: (a) Can the claims schema represent this? (b) Can AST extraction populate it? (c) Is it meaningfully better than `go doc` output? If Tier 1 structural output fails this test, fast-track Tier 2 semantic claims or pivot to a different output format.

**Why:** Four of five failure lenses identified a risk that the system produces correct but useless output. Validating the output _before_ building the pipeline avoids months of infrastructure work in service of docs nobody reads.

**Effort:** Low — 2-3 hours of manual writing. High information value.

### 4. Eliminate the Two-Extractor Consistency Problem (addresses lenses 1, 2)

**Change:** Define a strict predicate boundary: tree-sitter may only emit predicates it can produce accurately (`defines`, `imports`, `exports`, `has_doc`, `is_test`, `is_generated`). Predicates requiring type resolution (`implements`, `has_signature` with resolved types) are deep-extractor-only. When both extractors have run, deep-extractor claims always win. Add a `claim_source` field and a consistency check before rendering.

**Why:** Two extractors producing different claims for the same symbol is worse than one slow extractor. The fast path's value is speed, not depth — constraining its output prevents the two-source-of-truth problem entirely.

**Effort:** Low. It's a design constraint, not new code.

### 5. Use WASM Tree-sitter and Vendor Everything (addresses lens 2)

**Change:** Replace CGO-based tree-sitter bindings with WASM runtime (`wazero`). Vendor grammar `.wasm` blobs and pin versions. Include grammar version in cache keys. Add a CI job that tests against upstream grammar HEAD weekly.

**Why:** CGO creates platform-specific build complexity, and the Go binding ecosystem (smacker vs official) is fragile. WASM eliminates CGO entirely, makes grammar versions trivially pinnable, and the ~2-3x performance penalty still fits within the 200ms budget. Grammar version in cache keys ensures upgrades invalidate stale claims automatically.

**Effort:** Medium. Requires evaluating `wazero` performance and building the grammar-version-aware cache key.

---

## Full Failure Narratives

### 1. Technical Architecture Failure

**What happened:**

The first crack appeared in week 2, when the Go stdlib extractor was tested against `k8s.io/client-go`. The `go/packages` loader, configured with `NeedTypes | NeedDeps | NeedSyntax`, pulled in the transitive dependency graph and consumed 11GB of RAM for a single repository. Extrapolating to the full kubernetes monorepo with its 2400+ packages was clearly infeasible on a developer workstation. The team scaled back to per-directory loading with `NeedTypes` only, which capped memory at ~3GB but severed cross-package type resolution. Claims like `"func NewInformer returns SharedInformer"` degraded to `"func NewInformer returns <unresolved>"` whenever the return type was defined in a different module. This produced roughly 15% of claims with dangling `object_id` references — symbols that the extractor encountered but couldn't fully resolve. The team spent two weeks building a post-hoc symbol stitching pass that re-resolved these dangling references by scanning the full symbols table, but this created a new O(n²) bottleneck: with 800K+ symbols across 79 repos, the stitching pass alone took 45 minutes per full index run.

Meanwhile, SCIP symbol format — chosen for its cross-repo precision — turned into a tar pit. Kubernetes uses `staging/` symlinks where packages like `k8s.io/api` are developed inside `kubernetes/kubernetes/staging/src/k8s.io/api` but published as standalone modules with their own version tags. SCIP encodes the module path and version into the symbol string, so the same Go type `metav1.ObjectMeta` produced distinct SCIP symbols depending on whether it was resolved through the staging path or the published module. The `UNIQUE(scip_symbol)` constraint in the `symbols` table meant these were two different entities, fragmenting claims across phantom duplicates. Cross-repo joins — the project's core value proposition — returned incomplete results because a claim in `client-go` referenced the published symbol while the definition lived under the staging symbol. The team built a normalization layer that stripped version strings and collapsed staging paths, but `replace` directives in 23 of the 79 repos introduced further variants that the normalizer missed. By week 6, the team had catalogued 312 distinct normalization edge cases and was still discovering new ones weekly.

The tree-sitter fast path, designed for per-commit verification in 50-200ms, delivered on speed but not on trust. Tree-sitter parsed Go syntax accurately, but structural extraction at the AST level couldn't capture Go-specific semantics that users actually cared about: interface satisfaction, struct embedding promotion, and build-tag-conditional compilation. The claims it produced were technically correct but shallow. When the project attempted to render package-level documentation from Tier 1 claims alone, the output read like a symbol index, not documentation. The team realized the structural/semantic split was a false dichotomy: the claims that made documentation _useful_ all fell into the semantic tier requiring LLM processing, while the structural tier produced facts that any IDE already provided.

**Root cause:** The decision to model documentation as individual subject-predicate-object claims (a triple store) rather than as versioned, hierarchical document fragments anchored to code ranges.

**Warning signs:**

- `go/packages` exceeds 8GB RAM on first multi-module repo
- First cross-repo join returns <70% expected results due to SCIP version mismatches
- Tree-sitter claim counts 3-5x higher than `go doc` paragraphs for same package
- `symbols` table exceeds 1.5x count of unique logical symbols
- Rendered Tier 1 docs fail basic usefulness test against `go doc`

**Mitigations:**

- Replace claims triple model with versioned document fragments anchored to code ranges
- Two-phase `go/packages`: cheap metadata pass first, deep type extraction per-package on demand
- Replace SCIP with `repo+import_path+name` primary key
- Define doc usefulness acceptance test before building extractors
- Consider DuckDB over SQLite for analytical query patterns

**Severity:** Critical | **Likelihood:** High

---

### 2. Integration & Dependency Failure

**What happened:**

The project launched with Go 1.26.1 and `smacker/go-tree-sitter` bindings wrapping tree-sitter 0.22.x via CGO. By month two, the pipeline worked. Then three things happened in rapid succession. First, tree-sitter released v1.0, changing the C ABI and grammar format. The single-maintainer Go bindings fell behind by two months. During that gap, `tree-sitter-go` shipped a grammar update that split `type_spec` into `type_alias_declaration` and `type_definition` — a change that didn't cause parse errors but silently dropped all type-alias claims. The content-hash cache saw unchanged files and served stale claims.

Second, Go 1.27 shipped with changes to `go/types` behavior around generic type aliases. Kubernetes adopted it within weeks. The extractor's assumptions about `go/doc` method grouping broke for 14% of packages. The CGO dependency made CI fragile — GitHub Actions needed `apt-get install libtree-sitter-dev` pinned to the distro's version, creating a three-way version mismatch. Cross-compilation for macOS required brew's version. Two weeks of month three were spent getting CI green after an Ubuntu runner image update.

Third, SCIP v0.8.0 added a `Descriptor.Suffix` field changing overloaded function disambiguation. The hand-rolled formatter diverged from the spec silently. By month four, more time was spent chasing dependency breakage than building features. Tier 2 never started.

**Root cause:** Depending on CGO-based tree-sitter bindings from a single-maintainer library while hand-rolling SCIP formatting created two fragile surfaces with no stability contracts and no automated compatibility detection.

**Warning signs:**

- smacker/go-tree-sitter has single maintainer with no release cadence
- Tree-sitter core was pre-1.0 with v1.0 ABI break on the roadmap
- SCIP spec v0.7.0 with field additions in minor versions
- CGO CI setup taking >30 minutes is a friction indicator
- Hard-coded AST node type names with no grammar-version-aware test suite
- Cache keys don't include extractor or grammar version

**Mitigations:**

- Replace CGO with WASM tree-sitter runtime (wazero)
- Grammar-version-aware integration tests with fixture files
- Include extractor+grammar version in cache keys
- Use scip-go for symbol generation or add conformance tests
- Test against Go N and Go N-1 in CI matrix
- Abstract AST node types behind grammar-version mapping layer

**Severity:** Critical | **Likelihood:** High

---

### 3. Operational Failure

**What happened:**

The pipeline worked beautifully in isolation. Single packages, small repos — all clean. The post-commit hook shipped in week 6 at 400ms. Then month 3: deep extraction hit `kubernetes/kubernetes` for the first time while the developer was actually using the hook during normal work.

Deep extraction consumed 14GB RSS on a 16GB machine. The system swapped. Worse, extraction held SQLite write transactions open for minutes, and WAL checkpoints acquired exclusive locks stalling readers for 3-8 seconds. Deferring checkpoints grew the WAL to 1.2GB, making every read slow. Hook latency climbed to 6-12 seconds.

The content-hash cache had no deletion awareness. Over four months tracking kubernetes `master`, ~340 files were deleted or renamed. Their claims persisted as ghosts — documenting functions that no longer existed. Finding stale claims required an O(n) scan over 12,000+ files. The 79-repo corpus added 52GB of clones needing periodic `git pull` that failed silently, producing false verification failures. The developer spent month 5 fighting infrastructure instead of building Tier 2 semantic claims.

**Root cause:** Sharing a single SQLite database between the latency-sensitive hook and long-running extraction, without concurrency isolation or read/write path separation.

**Warning signs:**

- Hook exceeding 2s during deep extraction on mid-size repo
- WAL file growing past 100MB
- Extraction RSS exceeding 50% of physical memory
- Any claim referencing a file that returns ENOENT
- git pull failure going unnoticed for >24 hours

**Mitigations:**

- Split into hook-optimized read DB + heavyweight claims store
- Make post-commit hook stateless (git diff + flat manifest, no SQLite)
- Deletion-aware reconciliation on every git diff
- Cap extraction memory with ulimit/cgroup at 8GB
- Reduce test corpus to 5-8 repos until pipeline proven
- Set WAL size ceiling (64MB) with explicit checkpoint scheduling

**Severity:** Critical | **Likelihood:** High

---

### 4. Scope & Requirements Failure

**What happened:**

The system shipped Tier 1 documentation for all 2,400+ kubernetes packages by the end of month 2 — on schedule, technically correct, and completely ignored. The rendered Markdown for each package listed exported types, function signatures, import dependencies, and struct fields. It was accurate. It was also indistinguishable from what `go doc k8s.io/client-go/tools/cache` already produced, what pkg.go.dev already displayed, and what any Go developer's IDE already showed on hover. When the team shared sample output with three kubernetes SIG leads for feedback, the response was consistent: "This is just GoDoc in a README. Why would I read this instead of using my editor?" One reviewer noted that the dependency graph was mildly interesting but not worth a README — "I'd use `go mod graph` if I needed this."

The claims DB — with its SCIP symbol resolution, content-hash caching, two-tier extraction pipeline, and cross-repo JOINs — had produced output that a 50-line shell script wrapping `go doc` could approximate at 90% fidelity. The remaining 10% (cross-package type resolution, interface satisfaction mappings) was information developers rarely needed in document form. The project had spent two months building extraction infrastructure for the structural 80% of claims while deferring the semantic 20% (package purpose, usage guidance, architectural context, gotchas) to Phase 4. But the semantic claims were the only ones developers actually wanted. The convergence debate's "Facts-First, Prose-Never" position — render structured facts, never generate prose — turned out to be a precise description of how to build documentation nobody reads. Facts without narrative context are reference material, and Go already has excellent reference tooling.

By month 4, the project pivoted to Tier 2 semantic extraction, but the infrastructure built for Tier 1 was poorly suited. The claims schema was optimized for atomic, mechanically-verifiable facts — not for the paragraph-level, context-dependent content that semantic documentation requires. The SCIP symbol format, the content-hash cache, the tree-sitter fast path — all were designed for the wrong output type. The pivot required rethinking the data model, and the sunk-cost pressure to preserve existing infrastructure led to awkward compromises that satisfied neither the structural nor semantic use case well.

**Root cause:** Prioritizing the mechanically extractable 80% (structural claims) over the actually-valuable 20% (semantic claims) because the former was easier to build, not because it was what users needed.

**Warning signs:**

- `go doc` output for any test package covers >80% of what Tier 1 renders — testable in hour 1
- No kubernetes maintainer or SIG lead was consulted about desired doc format before building
- The convergence debate focused on what's _extractable_ rather than what's _useful_
- Zero user stories in the PRD describe a developer reading Tier 1 output and making a better decision
- The "Facts-First, Prose-Never" position was adopted without testing whether facts-only docs solve any real developer problem

**Mitigations:**

- **User validation before infrastructure**: show 5 kubernetes maintainers hand-written sample output for 3 packages and ask "would you read this?" before building anything
- **Fast-track Tier 2 to Phase 1**: even a low-confidence LLM-generated one-sentence package purpose line adds more value than perfect type signatures
- **Redefine Tier 1 as "enhanced go doc"**: instead of a parallel system, build a thin layer that adds cross-package relationship context to existing `go doc` output — dep graphs, interface satisfaction, "used by" lists
- **Measure value, not coverage**: success metric should be "developers found useful information" not "packages with READMEs"
- **Kill the claims DB if `go doc` + a relationship overlay produces equivalent output**: the simplest system that solves the real problem wins

**Severity:** Critical | **Likelihood:** Medium

---

### 5. Scale & Evolution Failure

**What happened:**

Phases 1-2 went well. The Go extractor produced ~2.1M claims for the main repo. Trouble started in Phase 3 when all 79 repos were indexed. SQLite ballooned to 4.2GB with 8M claims rows. Cross-repo JOINs degraded from milliseconds to 3-12 seconds. The version normalization table grew to 1,400+ entries across two quarterly kubernetes releases, each requiring manual verification. Stale normalization entries caused phantom drift alerts that eroded trust.

Phase 4 never shipped. When TypeScript extractors began for `kubernetes/website` Hugo templates and dashboard UI, the "language-agnostic" schema revealed deep Go assumptions: SCIP symbols assumed Go module paths, `package_path` encoded Go's `module@version/package` hierarchy, the extractor interface required a `types.Info` equivalent that TypeScript's compiler API doesn't produce. Adapting the schema meant migrating 8M+ rows, and the extractor interface refactor touched every rendering query. The content-hash cache (1.6GB, no eviction) required full invalidation after schema migration. Full re-extraction of 79 repos took 14 hours at 22GB RAM peak. The project stalled with a half-migrated database and incompatible TypeScript claims.

**Root cause:** The claims schema and SCIP symbol encoding were designed around Go's module system semantics rather than being truly language-agnostic from day one.

**Warning signs:**

- `package_path` uses Go module paths with no language discriminator
- Extractor interface returns Go `types.Info`-shaped data
- Version normalization table grows linearly with external release cadence
- Cross-repo JOINs >500ms at 2M rows
- Content-hash cache growing monotonically with no TTL
- Tree-sitter parse-error rate >5% on Hugo `.tmpl` files

**Mitigations:**

- Add language-prefix to all symbols and make package_path a URI with scheme
- Replace version normalization with deterministic function using `go list -m -json`
- Partition SQLite per-repo with lightweight cross-repo index
- Define extractor interface as JSON schema; validate with trivial Python extractor before finalizing
- Add cache eviction policy (LRU, 2GB max)
- Build minimal TypeScript extractor in Phase 2 as schema compatibility smoke test

**Severity:** Critical | **Likelihood:** High

---

## Confidence Assessment

**Strongest signals:** The SCIP symbol fragmentation risk (Theme 1) and the go/packages memory explosion (Theme 3) were surfaced by 3 independent lenses each. These are near-certain to manifest and should be treated as design constraints, not risks.

**Most surprising finding:** The scope/requirements failure (Theme 5) — the possibility that Tier 1 structural docs are genuinely useless — was surfaced independently by both the technical and scope lenses. This is the risk most likely to be dismissed ("we'll get to semantic claims later") and most damaging if true ("we built months of infrastructure for output nobody reads").

**Weakest coverage:** Security and compliance risks were not explored (5 lenses used, security lens omitted). For a documentation system operating on read-only clones, security risk is lower than average, but the eventual LLM integration (Tier 2) introduces prompt injection and data exfiltration vectors that deserve future analysis.
