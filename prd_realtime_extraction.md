# PRD: Real-Time Claims Extraction

## Problem Statement

livedocs extracts structural claims from source code into per-repo SQLite databases, then serves them to AI agents via MCP. Currently, extraction is a manual batch process: users clone repos, run `livedocs extract`, and get static databases that go stale. The `extract` command deletes the database on every run and rebuilds from scratch, ignoring the incremental pipeline infrastructure already built in `pipeline/pipeline.go`.

This means the core value proposition -- real-time, traceable documentation that stays current with code -- is undermined by a batch workflow. The incremental extraction pipeline exists but has no trigger mechanism, the CLI entry point bypasses it, and the database layer has correctness bugs (no transactions, no busy_timeout, destructive delete-before-create) that would block concurrent read/write use.

## Goals & Non-Goals

### Goals

- Claims databases stay current with source code changes automatically, without manual intervention
- MCP queries return fresh data (within a configurable staleness window) for all indexed repos
- Extraction is incremental: cost proportional to changes, not repo size
- Concurrent MCP reads and extraction writes work safely on the same database
- Works for both local development (single repo) and multi-repo (org-scale) use cases

### Non-Goals

- Sub-millisecond latency (30-second staleness is acceptable for v1)
- Replacing the batch `extract` command (it remains for initial indexing)
- Making the Go deep extractor incremental (it stays periodic/batch)
- Distributed extraction across multiple machines
- Filesystem watching via inotify/fsnotify (git-level detection is preferred)

## Requirements

### Must-Have

- Requirement: Add `PRAGMA busy_timeout = 5000` to all SQLite opens (ClaimsDB and cache)
  - Acceptance: `grep -r 'busy_timeout' db/ cache/` returns matches in both `db/claims.go` and `cache/sqlite.go`

- Requirement: Wrap `processFile` operations in a single SQLite transaction
  - Acceptance: A crash mid-extraction (kill -9 during extraction of a 1000-file repo) leaves the DB in a consistent state — no files with deleted claims but missing new claims

- Requirement: `livedocs extract` uses atomic file replacement (write to temp file, `os.Rename`) instead of `os.Remove` before extraction
  - Acceptance: Running `livedocs extract` while the MCP server is reading the same DB does not cause the MCP server to return errors or empty results

- Requirement: `livedocs watch` command that polls `git rev-parse HEAD` and triggers `pipeline.Run()` when HEAD changes
  - Acceptance: In a test repo, `livedocs watch --interval 5s` detects a new commit within 10 seconds and updates the claims DB. Verified by `livedocs diff` showing no drift after the update.

- Requirement: `livedocs watch` stores last-indexed commit SHA per repo and resumes correctly after restart
  - Acceptance: Kill `livedocs watch`, make 3 commits, restart — all 3 commits' changes are extracted on resume

### Should-Have

- Requirement: `livedocs init --hook` installs a git `post-commit` hook that triggers single-shot extraction
  - Acceptance: After `livedocs init --hook`, making a commit in the repo results in the claims DB being updated before the shell prompt returns

- Requirement: MCP DBPool invalidation — after extraction updates a DB, the MCP server picks up the new data without restart
  - Acceptance: Query MCP, add a new exported function, wait for `livedocs watch` to extract, query MCP again — the new function appears

- Requirement: MCP responses include freshness metadata (last extraction commit SHA and timestamp)
  - Acceptance: Every MCP tool response JSON includes `extracted_at` timestamp and `extracted_commit` SHA fields

- Requirement: `livedocs watch` supports multiple repos via a config file or directory of repos
  - Acceptance: Configure 5 repos in watch config, make commits in 3 of them, all 3 are re-extracted within 2 intervals

### Nice-to-Have

- Requirement: Lazy staleness check in MCP query path — if queried file's content hash differs from DB, re-extract before returning
  - Acceptance: Edit a file, immediately query its package via MCP (without waiting for watch cycle) — response includes the edit

- Requirement: Go deep extractor runs on a configurable schedule (default: every 10 minutes) alongside the fast tree-sitter watch path
  - Acceptance: `livedocs watch --deep-interval 10m` runs tree-sitter extraction per-commit and Go deep extraction every 10 minutes

- Requirement: Multi-tier freshness — repos queried recently get shorter poll intervals
  - Acceptance: A repo queried in the last hour is polled every 10s; a repo not queried in 24h is polled every 5m

## Design Considerations

**Eager vs. lazy extraction:** The primary mechanism is eager (extract on HEAD change via polling). Lazy extraction (on MCP query) is a nice-to-have safety net. The dual approach ensures commonly-queried repos are always fresh while rarely-queried repos don't waste cycles.

**Git polling vs. filesystem watching:** All research independently concluded that filesystem watchers (fsnotify/inotify) are unreliable for this use case — noisy during git operations, limited by kernel watch counts, and semantically wrong (can't distinguish autosaves from commits). Git-level polling is simpler, more reliable, and what every mature code indexing tool uses.

**Go deep extractor limitations:** The Go deep extractor does whole-program type analysis (O(N\*M) for implements checks). It cannot be made truly incremental without an interface-to-method-set index. For v1, it runs periodically alongside the fast tree-sitter path. Tree-sitter covers ~6 predicate types instantly; deep extraction covers the remaining ~4 on a timer.

**SQLite concurrency:** WAL mode + busy_timeout is sufficient for single-machine concurrent reads and writes. The MCP server reads while the watcher writes. No need for a separate read-optimized DB for v1 — WAL handles this natively.

## Open Questions

- What is the right default poll interval? 10s feels responsive; 30s reduces overhead. Should be configurable.
- How should force-pushes be handled when the stored last-indexed SHA no longer exists? Fallback to full re-extraction?
- Should branch switches be detected and handled differently from normal commits?
- For multi-repo at scale (500+ repos), should there be a single orchestrator with a priority queue or independent per-repo watchers?
- Cross-package Go type invalidation: is periodic full deep extraction sufficient, or do we need dependency-graph-aware cache keys?

## Research Provenance

Three independent research agents contributed:

- **Prior Art (Zoekt, gopls, Bazel, OpenGrok):** Confirmed git-polling as the industry standard. Identified the gopls lazy-invalidation pattern. Key finding: the incremental pipeline already exists and is ~50-100 lines from being usable.
- **First-Principles Technical Design:** Deep codebase analysis revealing the file-level vs. package-level extraction dichotomy, tree-sitter incremental parsing opportunity, and the exact data flow from event to DB update.
- **Failure Modes & Scale:** Found the `os.Remove` bug, missing `busy_timeout`, no transaction boundaries, and inotify limit risks. Grounded recommendations in crash-safety and concurrent-access scenarios.

Additionally, 30 brainstorm ideas explored the full solution space from FUSE filesystems to P2P gossip networks to OCI artifact distribution, ensuring the recommended approach was chosen from a broad landscape rather than default assumptions.
