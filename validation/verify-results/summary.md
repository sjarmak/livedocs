# livedocs verify — External Repo Validation Results

**Date**: 2026-04-01 (re-run with false positive fixes)
**Tool**: `/home/ds/live_docs/livedocs verify`
**Repos tested**: 6

## Overview

| Repo                          | AI Context Files | Claims | Valid | Stale | Accuracy | Verdict  |
| ----------------------------- | ---------------- | ------ | ----- | ----- | -------- | -------- |
| anthropics/anthropic-cookbook | 3                | 0      | 0     | 0     | 100%     | PASS     |
| anthropics/claude-code        | 0                | —      | —     | —     | —        | no files |
| getcursor/cursor              | 0                | —      | —     | —     | —        | no files |
| JetBrains/kotlin              | 17               | 26     | 25    | 1     | 96%      | FAIL     |
| JetBrains/Exposed             | 1                | 11     | 11    | 0     | 100%     | PASS     |
| JetBrains/koog                | 4                | 7      | 7     | 0     | 100%     | PASS     |

**Totals**: 4 repos had AI context files with verifiable claims. 3 of 4 pass. 1 stale reference across 44 claims.

## Improvement vs Previous Run

| Metric              | Before (pre-fix) | After (post-fix) | Change     |
| ------------------- | ---------------- | ---------------- | ---------- |
| Total stale refs    | 21               | 1                | -20 (95%)  |
| False positive rate | ~90% (19/21)     | 0% (0/1)         | eliminated |
| Repos passing       | 0/4              | 3/4              | +3         |
| Overall accuracy    | 66% (40/61)      | 98% (43/44)      | +32pp      |

### What was fixed

The updated binary now correctly handles:

1. **Ellipsis/glob patterns** (`tests/.../fakes/`, `src/gradle*/`) — no longer flagged as stale
2. **Abbreviated/relative paths** (`shared/Assert.kt`, `testbase/KGPBaseTest.kt`) — fuzzy matching finds files deep in the tree
3. **Branch name examples** (`feature/agent-memory`) — no longer misinterpreted as file paths
4. **Markdown link display text** (`util/buildProject.kt`) — verifies the link target, not display text
5. **Non-path references** (anthropic-cookbook `skills/CLAUDE.md` reference) — correctly excluded

## Repos Without AI Context Files

- **anthropics/claude-code**: No CLAUDE.md, AGENTS.md, or .cursorrules found.
- **getcursor/cursor**: No AI context files found.

## Detailed Findings

### anthropics/anthropic-cookbook (0 stale refs)

3 AI context files found but 0 verifiable file path claims extracted. The previous false positive (`docs/skills_cookbook_plan.md`) is no longer flagged. Verdict: **PASS**.

### JetBrains/kotlin (1 stale ref — genuine drift)

17 AI context files with 26 verifiable claims. The only remaining stale reference is `compiler/testData/psi/` in `compiler/psi/AGENTS.md` line 126 — this directory genuinely does not exist. All previous false positives (ellipsis patterns, glob wildcards, abbreviated paths, link display text) are now correctly handled.

**Corrected accuracy**: 96% (25/26), with the 1 remaining flag being genuine drift.

### JetBrains/Exposed (0 stale refs)

11 claims, all valid. The 3 previously flagged abbreviated paths (`shared/Assert.kt`, `shared/MiscTable.kt`, `shared/ForeignKeyTables.kt`) are now resolved correctly via fuzzy path matching. Verdict: **PASS**.

### JetBrains/koog (0 stale refs)

7 claims across 4 files, all valid. The 4 previously flagged branch name examples (`feature/agent-memory`, `fix/tool-registry-bug`) are no longer misinterpreted as file paths. The abbreviated path `calculator/Calculator.kt` is also resolved correctly. Verdict: **PASS**.

## False Positive Analysis

| Category                   | Before | After | Status     |
| -------------------------- | ------ | ----- | ---------- |
| **Genuine drift**          | 1-2    | 1     | retained   |
| **Ellipsis/glob patterns** | 4      | 0     | fixed      |
| **Abbreviated paths**      | 10     | 0     | fixed      |
| **Branch name examples**   | 4      | 0     | fixed      |
| **Link display text**      | 1      | 0     | fixed      |
| **Total false positives**  | 19     | 0     | eliminated |

## Files

- Human-readable outputs: `*-human.txt`
- JSON outputs: `*.json`
- All in `/home/ds/live_docs/validation/verify-results/`
