# Convergence Report: Live Docs Productization

> 3-position debate (Developer Tool vs AI-Native vs Enterprise Wedge) across 3 rounds. 2026-03-31.

## 1. Resolved Points (Consensus)

**The extraction engine + claims DB is shared infrastructure.** All three positions agree the core value is the extraction pipeline (SCIP + tree-sitter + go/types → claims DB). The debate was about _which interface surface ships first_, not about the underlying technology. This is a healthy sign — we're not arguing about fundamentals.

**`livedocs check` CLI is essential for every position.** All three advocates stole or included Idea #1 (Doc Drift Score). The DevTool advocate built their entire position around it. The AI-Native advocate conceded it's the best demo/content-marketing surface. The Enterprise advocate said compliance audit trails require it as the data-generation mechanism. **Verdict: `livedocs check` is in v1 regardless of strategy.**

**MCP server is high-value and low-cost.** Both the DevTool and AI-Native advocates agreed `livedocs mcp` should be in v1 or v1.1. The DevTool advocate explicitly stole it for their bundle. The Enterprise advocate was neutral. Building it is ~1 week of work on top of the shared claims DB. **Verdict: `livedocs mcp` is in v1.**

**The freshness badge (#5) and grounding service (#30) are NOT v1.** DevTool dropped the badge (needs hosted infrastructure). AI-Native dropped the grounding service (platform sale is unrealistic). **Verdict: defer both to v1.1+.**

**Doc SLA contracts (#14) are NOT v1.** Enterprise dropped this voluntarily — it's a config layer that adds onboarding friction. **Verdict: defer to v1.1.**

**Signal-to-noise ratio is the make-or-break factor.** The DevTool advocate's strongest late-round insight: the entire playbook (badge, PR bot, CI check) fails if findings are noisy. False positives destroy trust. v1 must be conservative — only flag high-confidence structural drift (renamed/deleted symbols), not speculative semantic drift. All positions implicitly agreed.

## 2. Refined Trade-offs (Unresolved)

### Primary surface: CLI vs MCP

The DevTool advocate says optimize `livedocs check` first (broader reach, proven playbook, demo-able). The AI-Native advocate says optimize `livedocs mcp` first (higher-intent users, less competition, faster feedback loop).

**What tips the balance:** If the first 2 weeks of user feedback show MCP users retain better than CLI users, lead with MCP. If CLI demos generate more inbound, lead with CLI. Both ship in v1 — the question is which gets 60% of polish time.

**Recommendation:** Lead with CLI for launch (HN post, blog, content marketing all need `livedocs check` output). But ship MCP simultaneously and track which surface drives more sustained usage.

### Revenue model: Self-serve vs Enterprise

The DevTool advocate says self-serve Pro ($12/user/month) at 8-12 weeks. The Enterprise advocate says design partner contracts ($29/user/month) at 3-6 months but higher value.

**What tips the balance:** Whether we have enterprise relationships. If we can get warm intros to 3+ SOC 2 companies in week 1, pursue the Enterprise track in parallel. If not, self-serve is the only realistic path.

**Recommendation:** Ship free CLI + GitHub Action first. Add Pro tier (private repos, PR bot) in week 6. Pursue enterprise conversations opportunistically but don't gate v1 on them.

### CLAUDE.md auto-maintainer (#13): Feature or product?

The AI-Native advocate's strongest unique contribution. No other tool maintains AI context files. The pain is real and growing. But it's narrow — only useful to developers using Claude Code/Cursor.

**Recommendation:** Include as a highlighted feature of `livedocs check` and `livedocs mcp`, not a separate product. "Livedocs verifies your docs — including your CLAUDE.md" is a better positioning than "livedocs maintains your CLAUDE.md."

### Git blame for claims (#3): Compelling but expensive

The Enterprise advocate's strongest unique idea. Mapping claims to commits that validated them provides audit evidence AND developer insight. But it's technically complex — requires claim-to-symbol-to-commit provenance chain.

**Recommendation:** Defer to v1.1. Include the _data model_ for blame tracking in v1 (claims store the commit hash that last verified them), but don't build the full blame UI or audit report format yet.

## 3. Emerged Position: The Converged v1 Bundle

**Ideas in v1 (the "build these" list):**

| #   | Idea                            | Source    | Rationale                                    |
| --- | ------------------------------- | --------- | -------------------------------------------- |
| #1  | Doc Drift Score as CI Artifact  | DevTool   | All 3 positions agreed. Core value prop.     |
| #2  | MCP Server for Doc Verification | AI-Native | DevTool stole it. Low cost, high leverage.   |
| #13 | CLAUDE.md Auto-Maintainer       | AI-Native | Uncontested niche, no competing tool exists. |
| #18 | GoReleaser Single Binary        | DevTool   | Distribution essential. Table stakes.        |
| #21 | PR Comment: Doc Impact Analysis | DevTool   | Growth lever. Free for public repos.         |

**Ideas deferred to v1.1 (the "build next" list):**

| #   | Idea                   | Rationale                                                         |
| --- | ---------------------- | ----------------------------------------------------------------- |
| #5  | Freshness Badge        | Needs hosted infra. Add after validating demand.                  |
| #3  | Git Blame for Claims   | Technically complex. Include data model in v1, full UI in v1.1.   |
| #4  | Compliance Audit Trail | Report format on top of v1 drift scores. Enterprise tier feature. |

**Ideas deferred to v2+ (the "validate first" list):**

| #   | Idea                       | Rationale                                           |
| --- | -------------------------- | --------------------------------------------------- |
| #30 | AI Agent Grounding Service | Platform sale requires traction first.              |
| #14 | Documentation SLA Contract | Config layer, adds friction. After proven adoption. |

## 4. Strongest Arguments (Preserved)

**DevTool:** "Quantifying an invisible problem creates social pressure to fix it. Documentation staleness is the next invisible problem waiting to be quantified, after coverage, security, and dependency health."

**AI-Native:** "The 0.1% framing is wrong because it ignores conversion rates. 100K high-intent MCP developers who actively install tools to improve their agent convert at dramatically higher rates than 100M passive GitHub users."

**Enterprise:** "We need revenue, not scale. Five design partners at $29/user/month x 200 users = $29K MRR. That's enough to fund 12 months of development to then build the developer adoption flywheel."

## 5. Recommended Build Sequence

| Week | Deliverable                                  | Notes                                                            |
| ---- | -------------------------------------------- | ---------------------------------------------------------------- |
| 1-2  | GoReleaser binary + `livedocs check` CLI     | Zero-config, tree-sitter fast path, high-precision findings only |
| 3    | `livedocs mcp` server mode                   | 3 tools: query_claims, check_drift, verify_section               |
| 3-4  | CLAUDE.md/AGENTS.md auto-verification        | Highlighted feature of both CLI and MCP                          |
| 4-5  | GitHub Action + PR comment bot               | Free for public repos, Pro for private                           |
| 5-6  | Pro tier + self-serve billing                | Private repos, PR bot, basic dashboard                           |
| 7-8  | Compliance export + enterprise conversations | Audit report format, design partner outreach                     |

**Launch sequence:** Week 3 soft launch to AI-native developers (MCP directory). Week 5 public launch (HN post with kubernetes findings). Week 8 enterprise outreach with adoption data.

## 6. Debate Highlights (Per Advocate)

**DevTool:** The 10-week roadmap that sequences all three positions was the single most constructive contribution. Also the late-round insight about signal-to-noise ratio being the critical risk — if findings are noisy, no distribution strategy saves you.

**AI-Native:** The CLAUDE.md auto-maintainer insight (#13) is the most _novel_ contribution across all three positions. No one else identified that AI context files are the fastest-growing doc format with zero existing tooling. This could be the feature that makes the HN post memorable.

**Enterprise:** The revenue math ($29K MRR from 5 enterprise customers vs. thousands of self-serve users) is the most _sobering_ contribution. The reminder that 76.9% of devtools die from monetization failure keeps the other positions honest about their revenue timelines.

## 7. Dissent Record

**AI-Native dissent (unresolved):** The AI-Native advocate maintains that optimizing `livedocs mcp` as the primary surface (not `livedocs check`) would produce faster feedback loops and better retention. The convergence recommendation leads with CLI for launch marketing but the AI-Native advocate believes this is a mistake that optimizes for vanity (HN upvotes) over retention (daily MCP usage). This dissent should be tracked — if MCP usage outpaces CLI usage in the first month, reconsider the surface priority.

**Enterprise dissent (unresolved):** The Enterprise advocate maintains that pursuing 5 design partners in parallel with the OSS launch would de-risk the revenue timeline significantly. The convergence recommendation defers enterprise to week 7-8, which the Enterprise advocate believes wastes 6 weeks of potential sales pipeline. If warm intros to SOC 2 companies are available, start those conversations in week 1 regardless of product readiness.
