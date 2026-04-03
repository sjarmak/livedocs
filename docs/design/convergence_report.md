# Convergence Report: Live Documentation System Architecture

## Debate Summary

Three positions debated over 2 rounds:

- **Detection-First** (drift detector, not doc generator)
- **Generation-First** (incremental doc generation pipeline)
- **Structured Claims DB** (documentation as machine-verifiable tuples)

## 1. Resolved Points

### All three agree: Structured claims as the intermediate representation

**Decisive argument:** Claims-DB advocate demonstrated that `(subject, predicate, object, source_file, confidence, last_verified)` tuples are mechanically verifiable, queryable, and renderable — solving problems for both generation (what to produce) and detection (what to verify). Generation-First adopted this as "an implementation detail that strengthens generation." Detection-First adopted it as "Layer 0 — structured facts."

### All three agree: AST extraction is the foundation

**Decisive argument:** `go/ast` + `go/doc` can extract ~80% of structural claims (exports, types, dependencies, package relationships) with zero LLM cost. This eliminates the bootstrapping cost concern and provides a machine-verified truth layer. No debater contested this.

### All three agree: Diff-triggered incremental updates, not full regeneration

**Decisive argument:** Architecture agent's cost analysis ($18+ per full pass) and content-hash caching proposal were accepted by all positions. Updates should be proportional to change size.

### All three agree: Two-tier claims model

**Decisive argument:** Claims-DB advocate's Round 2 refinement — structural claims (~80%, mechanically verifiable) and semantic claims (~20%, LLM-verified) — was accepted by all. Detection-First's contract anchors are the right mechanism for verifying semantic claims.

### Detection-First conceded: Bootstrapping is necessary

**Decisive argument:** Generation-First's "you can't detect drift in nothing" for 94% of undocumented packages. Detection-First updated to include "minimal generation capability — but detection-gated."

## 2. Refined Trade-offs

### Tier 1 auto-ship vs. verification gate

**The tension:** Generation-First argues mechanically-derived docs (export lists, dependency graphs) are AST-extracted facts that don't need review — "go/ast doesn't hallucinate." Detection-First argues even Tier 1 needs verification because tier assignment heuristics can misclassify, and a small package with subtle concurrency semantics could get wrong auto-docs.

**What tips the balance:** If Tier 1 is strictly limited to AST-derived structural facts (no LLM prose), Generation-First's argument holds — these are compiler-verified truths. If Tier 1 includes any LLM-generated summaries (even one-sentence package purpose), Detection-First's concern is valid.

**Resolution:** Define Tier 1 as **purely AST-derived, zero-LLM content**. Export lists, type signatures, dependency graphs, import maps. These can auto-ship. Any LLM-generated content (including one-sentence summaries) is Tier 2 minimum and requires verification.

### Sequencing: generate-then-verify vs. verify-infrastructure-then-generate

**The tension:** Generation-First says "sequence matters — create content before building verification." Detection-First says "build trust first, then earn the right to generate."

**What tips the balance:** Generation-First made the decisive observation that k8s's own `hack/verify-generated-docs.sh` _verifies generated docs_ — it's already a generate-then-verify pattern. Detection-First's own exemplar proves generation comes first.

**Resolution:** Build them in parallel, ship in sequence. Week 1-2: AST extraction populates claims DB AND builds verify-script-compatible checks. Week 3-4: Render Tier 1 docs from verified claims. The infrastructure arrives together.

### Scope of "documentation"

**The tension:** Detection-First argues explanatory docs (architectural rationale, migration guides) are the most valuable and can't be tuple-ified. Claims-DB acknowledges this as the semantic 20%. Generation-First punts this to Tier 3 (detection/assist only).

**Resolution:** All agree that semantic/explanatory documentation is the hardest category. For MVP, explicitly exclude it. Focus on structural documentation (Tier 1) and API references (Tier 2). Explanatory docs remain human-written with drift detection only.

## 3. Emerged Positions

### The "Verify-Compatible Generation" compromise

Not proposed by any single debater but emerged from the intersection: generated docs should be structured as verify-script-compatible output, so k8s maintainers see it as an extension of their existing `hack/verify-*.sh` pattern. This dramatically reduces adoption friction.

### The "Facts-First, Prose-Never" radical position

An implicit position that emerged: if 80% of claims are structural and mechanically renderable, and the remaining 20% semantic claims are the risky ones — maybe the system should NEVER generate prose. Just render structured facts as formatted Markdown tables, lists, and graphs. Prose is reserved for humans. This eliminates the hallucination problem entirely.

## 4. Strongest Arguments (preserved)

| Position             | Strongest Contribution                                                                                                                                                                                                                     |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Detection-First**  | "Detection provides the trust infrastructure that makes generation safe. Without it, generation is a liability." — The verification gate principle is non-negotiable regardless of architecture.                                           |
| **Generation-First** | "You can't detect drift in nothing. 94% is too large a gap. And k8s's own verify pattern proves generation comes first." — The bootstrapping argument and the observation that verify-generated-docs.sh IS a generate-then-verify pattern. |
| **Claims DB**        | "Detection extracts → Claims DB stores and verifies → Generation renders. The claims DB is the integration layer that makes the other two composable." — The insight that structured claims unify all three approaches.                    |

## 5. Recommended Path

### Architecture: Claims-backed generation with verification gates

```
┌─────────────────────────────────────────────────────────┐
│                    LIVE DOCS SYSTEM                      │
│                                                          │
│  ┌──────────┐    ┌──────────────┐    ┌───────────────┐  │
│  │ AST/Code │───►│  Claims DB   │───►│   Renderers   │  │
│  │ Extractors│    │ (SQLite/PG)  │    │ (Markdown,    │  │
│  │ (go/ast,  │    │              │    │  HTML, etc.)  │  │
│  │ tree-sit) │    │ structural   │    └───────────────┘  │
│  └──────────┘    │ + semantic   │                        │
│       ▲          │ claims       │    ┌───────────────┐  │
│       │          └──────┬───────┘    │  Drift Alerts  │  │
│  ┌────┴─────┐          │            │  (CI/hooks)    │  │
│  │ git diff │     ┌────▼────┐       └───────────────┘  │
│  │ trigger  │     │Verifier │──────────────▲            │
│  └──────────┘     │(AST +   │              │            │
│                   │contract │──────────────┘            │
│                   │anchors) │                            │
│                   └─────────┘                            │
└─────────────────────────────────────────────────────────┘
```

### Phased Implementation

**Phase 1 (Week 1-2): Foundation — AST extraction + Claims DB**

- Build AST extractors for Go (`go/ast`, `go/doc`)
- Design claims schema (structural + semantic tiers)
- Populate structural claims for all 2400+ k8s packages
- Build verify-script-compatible checker
- Deliverable: queryable claims DB, `hack/verify-live-docs.sh`

**Phase 2 (Week 3-4): Tier 1 rendering + drift detection on existing docs**

- Render structural claims as Markdown (package cards: exports, deps, types)
- Run drift detection against existing ~150 READMEs
- Ship Tier 1 docs (purely AST-derived, zero LLM) — auto-merge safe
- Deliverable: READMEs for 2400 packages (structural only), drift report

**Phase 3 (Month 2): Tier 2 generation with verification**

- LLM-generated semantic claims (package purpose, usage patterns)
- Every semantic claim tagged with source provenance
- Verification gate: structural check + adversarial LLM review
- Human review required for semantic content before merge
- Deliverable: enriched READMEs with verified semantic content

**Phase 4 (Month 3+): Continuous maintenance**

- Diff-triggered pipeline: git commit → content-hash check → re-extract affected claims → re-verify → re-render
- Sub-2-second post-commit hook warnings
- Drift detection on semantic claims via contract anchors
- Canary testing (brainstorm #10) for doc quality assurance

### Explicit choices on unresolved trade-offs

| Trade-off                   | Choice                              | Rationale                                  | Revisit when...                         |
| --------------------------- | ----------------------------------- | ------------------------------------------ | --------------------------------------- |
| Tier 1 auto-ship            | Yes, for purely AST-derived content | go/ast doesn't hallucinate                 | LLM content enters Tier 1               |
| Prose generation            | Deferred to Phase 3+                | Trust must be earned incrementally         | Phase 2 drift detection proves reliable |
| Cross-repo propagation      | Out of scope for MVP                | Hard problem, tackle after core works      | Single-repo system is stable            |
| Explanatory/conceptual docs | Detection-only, human-written       | Semantic claims too risky to auto-generate | Quality benchmarks improve past 85%     |

## 6. Debate Highlights

- **Detection-First**: Forced the group to take verification seriously. The "nothing ships without passing drift checks" principle was adopted unanimously. Their observation that the 70% "stale but correct" stat means detection must be granular (claim-level, not document-level) refined the entire architecture.

- **Generation-First**: Won the bootstrapping argument decisively. Their observation that k8s's own verify script IS a generate-then-verify pattern turned Detection-First's own evidence against them. The three-tier model provided the trust gradient that made all positions comfortable.

- **Claims DB**: Provided the architectural breakthrough. The insight that structured claims are the integration layer — generation populates them, detection verifies them, renderers consume them — unified all three positions into a coherent system. Their Round 2 update (two-tier claims: structural 80% + semantic 20%) was the key refinement that resolved the "what can be automated" question.
