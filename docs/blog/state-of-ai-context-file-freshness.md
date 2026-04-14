# Why Your CLAUDE.md Is Already Stale

We scanned the AI context files in 16 open-source repositories — React, VS Code, Next.js, PyTorch, Prisma, Deno, LangChain, Kotlin, and more. We extracted every file path reference from their CLAUDE.md, AGENTS.md, .cursorrules, and copilot-instructions.md files, then checked whether those paths still exist in the repo.

**The result: 1 in 9 references point to files or directories that no longer exist.**

This is a new kind of technical debt. We call it *context rot*.

## What are AI context files?

If you use Claude Code, Cursor, GitHub Copilot, or Windsurf, you've probably seen these files:

- **CLAUDE.md** — instructions for Claude Code
- **AGENTS.md** — scoped instructions for sub-agents
- **.cursorrules** — instructions for Cursor
- **copilot-instructions.md** — instructions for GitHub Copilot

They tell your AI coding agent where things are, how the project is structured, and what conventions to follow. They're the onboarding doc your agent reads before every session.

The problem: nobody updates them.

## The data

We ran [`livedocs verify`](https://github.com/live-docs/live_docs) against 10 high-profile repos with AI context files:

| Repo | Stars | Claims | Stale | Accuracy |
|------|------:|-------:|------:|---------:|
| facebook/react | 244K | 12 | 0 | 100% |
| microsoft/vscode | 183K | 20 | 4 | 80% |
| vercel/next.js | 139K | 86 | 8 | 91% |
| langchain-ai/langchain | 132K | 36 | 4 | 89% |
| excalidraw/excalidraw | 120K | 2 | 0 | 100% |
| denoland/deno | 106K | 38 | 3 | 92% |
| pytorch/pytorch | 99K | 14 | 3 | 79% |
| astral-sh/uv | 82K | 0 | 0 | 100% |
| astral-sh/ruff | 47K | 1 | 0 | 100% |
| prisma/prisma | 46K | 126 | 14 | 89% |
| **Total** | **1.2M** | **335** | **36** | **89%** |

Six of ten repos had at least one stale reference. The repos with the most structural claims — Prisma (126), Next.js (86), Deno (38) — also had the most drift. More claims means more surface area for rot.

We also scanned repos from the companies building these tools — Anthropic, JetBrains, and Cursor:

| Repo | Claims | Stale | Accuracy |
|------|-------:|------:|---------:|
| JetBrains/kotlin | 26 | 1 | 96% |
| JetBrains/Exposed | 11 | 0 | 100% |
| JetBrains/koog | 7 | 0 | 100% |
| anthropics/anthropic-cookbook | 0 | 0 | 100% |

Even the Kotlin repo — maintained by the team that ships AGENTS.md support in IntelliJ — has a stale reference. Context rot is universal.

## Why it matters

A stale CLAUDE.md doesn't throw an error. Your agent reads it, trusts it, and then wastes tokens searching for files that don't exist, following patterns that were refactored away, or referencing directories that were renamed three PRs ago.

The failure mode is silent: your agent produces slightly worse code, slightly more slowly, and you assume that's just how AI coding works.

Consider what happens when Next.js's CLAUDE.md references `scripts/pr-status/` — a directory that was deleted. An agent tasked with CI work might search for it, fail to find it, speculate about where it moved, and burn context window on a dead end. Multiply that by every stale reference across every session.

## What causes context rot

Context files rot for the same reason all documentation rots: they're decoupled from the code they describe. But AI context files rot *faster* because:

1. **They're new.** Most were created in the last 6 months. There are no established maintenance habits yet.
2. **They reference structure, not behavior.** Documentation that says "use React hooks" stays true for years. Documentation that says "tests are in `cli/tests/`" breaks the moment someone restructures the test directory. Deno did exactly this — their tests moved to a top-level `tests/` directory, but both CLAUDE.md and copilot-instructions.md still reference `cli/tests/`.
3. **Nobody owns them.** Code has tests and CI. READMEs have readers who file issues. AI context files have neither — they're read by machines that don't complain when instructions are wrong.
4. **They proliferate.** VS Code has AGENTS.md files in subdirectories. PyTorch has sub-CLAUDE.md files per module. Prisma and Next.js duplicate the same content across CLAUDE.md and AGENTS.md. More copies mean more drift.

## Fixing it

We built [livedocs](https://github.com/live-docs/live_docs) to detect context rot automatically. The `verify` command extracts every file path claim from your AI context files and checks whether it resolves to something real in the repo:

```bash
$ livedocs verify

CLAUDE.md (12 claims)
  line 34: scripts/pr-status/     STALE  (directory not found)
  line 51: turbopack/crates/README.md  STALE  (file not found)

AGENTS.md (8 claims)
  line 34: scripts/pr-status/     STALE  (directory not found)
  line 51: turbopack/crates/README.md  STALE  (file not found)

Accuracy: 91% (78/86 claims valid)
Verdict: FAIL — 8 stale references found
```

Three ways to use it:

**CLI** — run `livedocs verify` in any repo. Zero config, instant results.

```bash
go install github.com/live-docs/live_docs/cmd/livedocs@latest
cd your-repo
livedocs verify
```

**MCP server** — let your AI agent verify its own context files at the start of every session.

```bash
claude mcp add livedocs -- livedocs mcp
```

Then ask Claude: "Check if my CLAUDE.md is up to date." The `check_ai_context` tool does the same verification, returning structured results your agent can act on.

**GitHub Action** — catch drift in CI before it reaches your agents.

```yaml
- uses: live-docs/live_docs@v1
  with:
    fail-threshold: 0
```

## The pattern

Context rot follows a predictable pattern:

1. Someone writes a CLAUDE.md when setting up AI coding for a project
2. It's accurate on day one
3. The codebase evolves — directories get renamed, files move, patterns change
4. The CLAUDE.md stays frozen
5. Agents silently degrade

The fix is the same as for any documentation problem: automated verification. You wouldn't ship code without tests. You wouldn't deploy without CI. Don't feed your AI agent stale instructions without checking them first.

## Try it

```bash
go install github.com/live-docs/live_docs/cmd/livedocs@latest
livedocs verify
```

Your CLAUDE.md is probably already stale. Now you can prove it.

---

*[livedocs](https://github.com/live-docs/live_docs) is open source (Apache 2.0). Star the repo, file issues, and tell us what you find when you run `livedocs verify` on your projects.*
