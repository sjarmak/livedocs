# Codex Cross-Review: `prd_tribal_knowledge_mapping.md`

Reviewed against `docs/design/prd_tribal_knowledge_mapping.md` at commit `86bc42f`.

## Findings

### 1. Critical: the corroboration gate is not implementable as written

**Refs:** PRD lines 21, 47, 63, 95, 118, 165-167, 184-195.

The design makes `corroboration` a required field and then uses it as the main safety control for Phase 2 (`corroboration >= 2` from independent sources before default-tier serving). But the PRD never defines how two extractions of the same tribal fact collapse into one row. `tribal_facts` has no merge key, and `tribal_evidence.source_ref` is described as "unique per fact" only in a comment, not as an enforced invariant. As a result, the implementation has only two bad choices:

1. Insert a fresh fact every time, which leaves every LLM fact stuck at `corroboration=1` forever.
2. Manually bump `corroboration`, which can be inflated by replaying the same evidence because independence is not modeled.

That means the main mitigation for R1 and R4 does not actually exist in the Phase 1/2 design. The risk table assumes a safety property the schema and acceptance criteria do not make possible.

**Recommendation:** add a deterministic fact-merge identity and an explicit evidence-dedup invariant before any corroboration-based serving rule ships.

### 2. High: M5 depends on symbol-liveness semantics the base store does not define

**Refs:** PRD lines 36, 41, 51, 101-106, 155-156, 180, 203-206.

`tribal_facts` attaches to `symbols.id`, and M5 says facts become `quarantined` when the underlying symbol disappears. But the PRD never defines a stable symbol fingerprint to store with the fact, a symbol tombstone model, or a rename/move strategy. The acceptance text talks about "symbol fingerprint still matches" even though no such fingerprint exists in the schema being proposed.

This is not a cosmetic omission. Without a stored identity beyond raw `subject_id`, the drift pass cannot reliably distinguish:

1. the same symbol after a move/rename,
2. a deleted symbol whose row still exists in the base store,
3. a different symbol that now occupies the same conceptual slot.

So the headline promise of non-destructive tribal drift is under-specified at exactly the point where it needs a durable identity model.

**Recommendation:** either store a stable symbol identity/fingerprint with each tribal fact and define rename/delete handling explicitly, or scope M5 to a weaker, file-anchored notion of liveness.

### 3. High: M2's acceptance criteria are impossible or content-dependent

**Refs:** PRD lines 18-21, 42-43, 88-91, 149-150.

Phase 1's extractor list emits `ownership`, `rationale`, `quirk`, and `todo`. It does not emit `invariant` or `deprecation`, yet M2 requires `livedocs extract --tribal=deterministic` to produce at least one `tribal_facts` row "of each kind." If "each kind" means the schema kinds, the acceptance test is impossible. If it means "each Phase 1 kind," the PRD should say so.

The second problem is that M2 binds correctness to the current contents of the `live_docs` repo instead of to deterministic fixtures. Today this repo has no `CODEOWNERS` file, so a repo-level smoke test cannot validate the `codeowners` extractor at all. Similar brittleness applies to inline markers: success depends on incidental repository content, not on whether the extractor itself is correct.

**Recommendation:** split M2 into fixture-backed extractor tests plus a separate repo-level smoke test, and rewrite the acceptance text in terms of the kinds Phase 1 actually produces.

### 4. High: the correction loop is declared authoritative but never reaches the read path

**Refs:** PRD lines 22, 71-79, 106, 118, 164-165, 173-174, 198.

The goals say corrections are "authoritative overlays," and R7 relies on delete/supersede for contributor privacy. But none of M1-M6 require `tribal_context_for_symbol`, `tribal_owners`, or `tribal_why_this_way` to apply `tribal_corrections` when serving facts. The schema stores corrections, but the v1 requirements never specify how corrected, deleted, or superseded facts stop being shown to agents.

That creates a safety gap: the system can record that a fact is wrong or privacy-sensitive without actually changing what readers receive. Because the only write-back tool is also deferred to Phase 2, this is not merely "missing UX"; it is missing semantics.

**Recommendation:** either downgrade corrections from a Phase 1 goal, or make read-time correction overlay behavior part of the Phase 1 MCP contract.

## Bottom Line

The deterministic-only split is directionally right, and extending `.claims.db` still looks better than forking a new store. The blocking problem is that three of the core safety/control loops are specified at the policy level but not at the identity/invariant level: corroboration, drift quarantine, and correction overlays. Those need to be made mechanically true in the schema and acceptance tests, not just described in the narrative.
