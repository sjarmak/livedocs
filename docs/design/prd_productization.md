# PRD: Live Docs Productization

> Synthesized from 5-lens divergent research (2026-03-31). Lenses: Prior Art & Industry, UX & Workflow, Technical Architecture, Failure Modes & Risks, Business Model & Growth.

## Problem Statement

We have a working claims-backed documentation system tested at kubernetes scale (1.1M claims, 704K symbols, 16,929 files). No existing tool uses code intelligence data (SCIP, tree-sitter, go/types) for documentation _verification_ — this is whitespace between static doc generators, API platforms, code-coupled tools, and code intelligence platforms. The question is how to productize this as a developer tool that achieves adoption and sustains a business.

## Goals & Non-Goals

### Goals

- Ship a developer tool that detects documentation drift with machine-verifiable evidence
- Achieve "zero-to-value in under 5 minutes" for any repo with existing READMEs
- Build a sustainable business with free OSS core and paid cloud/enterprise tiers
- Support Go (deep), TypeScript, Python, Shell (tree-sitter) at launch
- Integrate into existing CI/CD workflows without requiring behavior change

### Non-Goals

- Replace existing doc generators (Sphinx, GoDoc, MkDocs) — complement them
- Build a documentation hosting platform (Mintlify/ReadMe territory)
- Build an IDE extension (docs drift is a diff-level problem, not a keystroke-level problem)
- Support every language at launch (tree-sitter provides baseline; deep extractors added per-language)
- Generate complete documentation from scratch (verification first, generation second)

## Core Insight

**The aha moment is detection, not generation.** "Your README references `NewInformer` but that function was renamed to `NewInformerWithOptions` 8 months ago" is an immediate, visceral finding. Detection earns trust; generation spends it. Ship `livedocs check` first, `livedocs generate` second.

## Requirements

### Must-Have

- **`livedocs check`** — drift detection against existing docs, tree-sitter fast path, <30 seconds on first run, exit code 1 for CI compatibility
- **`livedocs init`** — scaffold `.livedocs.yaml`, run first extraction, create claims DB
- **Single static binary** — GoReleaser + goreleaser-cross for CGO (tree-sitter), distribute via GitHub Releases + Homebrew tap
- **GitHub Action** — `uses: livedocs/check-action@v1` for CI integration
- **Per-repo SQLite** — `.livedocs/claims.db` (gitignored), generated locally, incremental updates
- **Zero-config mode** — auto-detect language, find READMEs, scan without `.livedocs.yaml`
- **Graceful LLM degradation** — Tier 1 (structural) works without API key; Tier 2 (semantic) requires `ANTHROPIC_API_KEY`

### Should-Have

- **PR comment bot** — GitHub App that comments on PRs with invalidated claims (the viral growth lever)
- **Documentation freshness badge** — SVG badge for READMEs showing drift score (the Codecov badge equivalent)
- **`livedocs mcp`** — MCP server mode exposing claims DB to AI assistants (query_claims, check_drift, render_doc)
- **`livedocs render`** — generate Tier 1 Markdown from claims (cross-package relationships, not duplicating `go doc`)
- **Progressive config** — `.livedocs.yaml` works empty (sane defaults), adds constraints as needed

### Nice-to-Have

- **Hosted dashboard** — freshness metrics over time, team scores, trend analysis (enterprise upsell)
- **Compliance export** — SOC 2/ISO 27001 audit evidence packages from drift reports
- **Backstage plugin** — TechDocs integration for developer portal users
- **SCIP import** — consume pre-built SCIP indexes for instant multi-language support
- **`livedocs watch`** — git hook mode for continuous local extraction

## Design Considerations

### Key Tensions

1. **Detection vs. Generation**: UX research says lead with detection; risks research warns Tier 1 structural output may not be valuable enough alone. Resolution: detection is the wedge, generation is the upsell, semantic claims (Tier 2) are the differentiator.

2. **CGO vs. Portability**: Tree-sitter requires CGO. Pure Go alternatives are 7,500x slower. Resolution: accept CGO, use goreleaser-cross for distribution. Only one C dependency (tree-sitter); SQLite is already pure Go (modernc.org).

3. **Cold Start**: Full extraction takes minutes to hours. Resolution: `livedocs check` uses tree-sitter-only fast path against existing READMEs — value in <30 seconds. Full claims DB is a background/CI step.

4. **DB Size**: 677MB for kubernetes. Resolution: per-repo SQLite as build artifact (like .git/objects). Users generate locally, never distribute. Typical repos: 5-50MB.

5. **"Good Enough" Problem**: `go doc` and IDE hover already exist. Resolution: position as verification ("are your docs still true?"), not generation ("here are your docs"). Cross-package relationships, reverse deps, and interface satisfaction maps are the structural differentiation.

### Pricing Model (Codecov Analogy)

| Tier         | Price           | Features                                                                            |
| ------------ | --------------- | ----------------------------------------------------------------------------------- |
| Free         | $0              | CLI, public repos, structural claims (Tier 1), drift detection, GitHub Action       |
| Pro          | $12/user/month  | Private repos, PR comment bot, freshness badge, hosted dashboard, historical trends |
| Enterprise   | $29/user/month  | SSO, audit logs, compliance export, semantic claims (Tier 2), self-hosted, SLA      |
| Usage add-on | $0.10/pkg/month | Semantic claims (LLM) for additional packages                                       |

### Go-to-Market Sequence

1. **Week 1**: Open source CLI on GitHub (Apache 2.0). Include GitHub Action.
2. **Week 2**: Run against kubernetes, prometheus, etcd, 5+ major Go projects. Generate drift reports. Submit PRs with freshness badges.
3. **Week 3**: Hacker News launch: "We extracted 1.1M claims from Kubernetes and found 43 stale references."
4. **Month 2**: GitHub Marketplace listing. PR comment bot (paid GitHub App).
5. **Month 3**: Hosted dashboard launch (Pro tier).

## Critical Risk: Market Validation

The risks lens identified the strongest signal: **11K lines of Go code built before validating anyone wants what it produces.** Before writing productization code:

1. Run `livedocs check` against 10 real-world OSS repos (not kubernetes). Measure: time to first finding, signal-to-noise ratio, maintainer reaction.
2. Find 5 design partners (50-500 dev teams, Go-heavy) willing to commit to a 3-month pilot.
3. Talk to 5 engineering leaders at SOC 2 companies about "documentation freshness evidence" for audits.

If these validation steps fail, the product does not have a market — pivot to being a library/SDK consumed by other tools, or an MCP-only integration for AI assistants.

## Open Questions

1. Will developers adopt a "doc freshness" metric like they adopted code coverage?
2. Is Go-first focusing or limiting? TypeScript has the largest potential user base.
3. What's the right boundary between free CLI and paid cloud?
4. Can a small team compete with Mintlify ($27.7M funded) if they pivot to verification?
5. Does the claims DB survive the shift to AI-native workflows where LLMs read code directly?

## Research Provenance

| Lens          | Key Contribution                                                                |
| ------------- | ------------------------------------------------------------------------------- |
| Prior Art     | Whitespace mapping: verification is unoccupied. JetBrains abandoned Writerside. |
| UX & Workflow | "Aha moment is detection, not generation." CLI-first, PR bot as growth lever.   |
| Technical     | Single binary via GoReleaser. MCP server as high-leverage integration.          |
| Failure Modes | Market validation gap. "Good enough" risk. Cold start problem.                  |
| Business      | "Codecov for Documentation" positioning. Badge + HN + GitHub Action playbook.   |

**Convergence points** (3+ lenses agree): CLI-first distribution, detection before generation, free OSS core, GitHub App as growth lever, compliance as enterprise unlock.

**Divergence points**: Risks lens says "kill Tier 1, ship Tier 2 from day one" vs. UX lens says "detection earns trust before generation." Resolution: lead with structural detection (instant, no LLM cost), but ensure semantic claims are available early for users who want them.
