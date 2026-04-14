# PRD: Staleness Checker End-to-End Wiring

## Problem Statement

The lazy staleness checker (`mcpserver/staleness.go`) is fully implemented — it can detect when source files have diverged from the claims DB and re-extract them on demand. However, it is completely inert because `cmd/livedocs/mcp.go` never populates `Config.RepoRoots` or `Config.ExtractorRegistry`. The entire staleness path is built but the on-ramp is missing.

Additionally, research uncovered a critical concurrency bug: `ClaimsDB.RunInTransaction` swaps a shared `c.exec` field without locking, meaning concurrent MCP requests for the same repo will corrupt transactions. This bug is exposed by the staleness checker but affects any concurrent use of `ClaimsDB`.

The staleness checker also has gaps: it doesn't detect deleted files (stale claims persist forever), has no caching (re-hashes all files on every query), and has no timeout (could block indefinitely on slow filesystems).

## Goals & Non-Goals

### Goals

- Staleness checker activates automatically when the MCP server has enough information (repo root paths)
- Re-extraction during MCP requests is safe under concurrent access
- Re-extraction has bounded latency — never blocks an MCP response indefinitely
- Repo root paths are stored during extraction and auto-discovered by the MCP server

### Non-Goals

- Making staleness checking the primary freshness mechanism (watch command remains primary)
- Adding staleness checks to `search_symbols` (fan-out across repos is too expensive)
- Sub-second staleness detection (this is a safety net, not a real-time system)
- Filesystem watching (inotify/fsnotify) — git polling via watch is the primary mechanism

## Requirements

### Must-Have

- Requirement: Fix `RunInTransaction` concurrency — add a mutex so concurrent callers on the same `ClaimsDB` instance don't corrupt each other's transactions
  - Acceptance: `go test -race ./db/...` passes. A test exists that calls `RunInTransaction` from multiple goroutines concurrently and verifies no data corruption or race detector warnings.

- Requirement: Store repo root path in `extraction_meta` during `livedocs extract` and `livedocs watch`
  - Acceptance: After running `livedocs extract /path/to/repo`, `SELECT repo_root FROM extraction_meta WHERE id=1` returns `/path/to/repo`. The `ExtractionMeta` struct has a `RepoRoot` field.

- Requirement: `livedocs mcp --data-dir` auto-discovers repo roots from `extraction_meta` in each `.claims.db` and populates `Config.RepoRoots`
  - Acceptance: Start `livedocs mcp --data-dir data/claims/`, query `describe_package` for a repo whose DB has `repo_root` set — the staleness checker activates (visible via the staleness warning or note in the response when a file has changed).

- Requirement: `RefreshStaleFiles` respects a context timeout (default 5 seconds) — if re-extraction exceeds the timeout, stale data is returned with a warning
  - Acceptance: `CheckPackageStaleness` and `RefreshStaleFiles` accept `context.Context`. A test with a 1ms timeout verifies that re-extraction is cancelled and stale data is returned with a warning message.

- Requirement: Detect deleted files in staleness checker — if a source file in the DB no longer exists on disk, remove its claims
  - Acceptance: Delete a source file, query its package via `describe_package` — the response no longer includes claims from the deleted file. A test verifies claims are removed for files that return `os.ErrNotExist`.

### Should-Have

- Requirement: Short-lived staleness cache (10s TTL) — recently-checked import paths are not re-checked on every `describe_package` call
  - Acceptance: Two `describe_package` calls for the same package within 5 seconds result in only one set of file hash computations. A test verifies the cache hit path.

- Requirement: Extract `buildRegistry()` from `watch_cmd.go` into a shared function so both `watch` and `mcp` commands can construct extractor registries without duplication
  - Acceptance: `grep -rn 'buildRegistry\|BuildRegistry\|NewDefaultRegistry' extractor/ cmd/` shows the function defined once in a shared location and called from both `watch_cmd.go` and `mcp.go`.

### Convergence Note: Deleted-file detection promoted from Should-Have to Must-Have

Serving claims for deleted files is a correctness bug that erodes AI agent trust. The fix is small (~5 lines in CheckPackageStaleness). Promoted during convergence analysis.

### Should-Have

- Requirement: `CheckPackageStaleness` checks `ctx.Done()` between file reads for cancellation support
  - Acceptance: A test with a pre-cancelled context verifies that `CheckPackageStaleness` returns immediately without reading any files.

### Nice-to-Have

- Requirement: `livedocs mcp` accepts `--enable-staleness` flag (default true) to disable staleness checking when not wanted
  - Acceptance: `livedocs mcp --data-dir data/claims/ --enable-staleness=false` starts the server without staleness checking even when repo roots are available.

## Design Considerations

**Sync with timeout vs async**: Industry tools (gopls, Zoekt, Sourcegraph) overwhelmingly use background indexing, never query-time reindexing as primary. The staleness checker is a fallback for the gap between watch cycles. Synchronous re-extraction with a timeout is the pragmatic choice for MCP's request-response model — async would require returning stale data and a refresh protocol that MCP doesn't support.

**Auto-discovery vs CLI flags**: Storing repo root in `extraction_meta` during extract/watch and reading it back in the MCP server avoids requiring users to maintain a separate `--repo-roots` mapping. Graceful degradation: repos without metadata simply have no staleness checking.

**`RunInTransaction` concurrency**: The `DBPool` returns the same `*ClaimsDB` pointer to all concurrent callers for a given repo. The current `RunInTransaction` swaps `c.exec` in place without locking — concurrent requests will corrupt transactions. Adding a mutex is the minimal fix. A better long-term fix would pass the `tx` as a parameter, but that requires a larger refactor.

**Staleness cache**: Without caching, every `describe_package` call re-reads and SHA-256 hashes all source files for the package. For an AI agent drilling into a package (common pattern), this wastes I/O. A 10-second TTL cache prevents redundant checks.

## Convergence Decisions

- **Mutex now, refactor later**: `sync.Mutex` on `RunInTransaction` is sufficient for the low write-concurrency MCP workload. Tx-as-parameter refactor deferred unless profiling shows contention.
- **Auto-discovery only**: No `--repo-roots` CLI flag. Store `repo_root` in `extraction_meta` during extract/watch. MCP server reads it back. Zero-config.
- **Staleness cache as should-have**: Per-import-path, 10s TTL. AI agents drill into packages repeatedly, but the checker is a fallback — cache prevents redundant I/O without adding complexity to the must-have scope.
- **Deleted-file detection promoted to must-have**: Small fix, real correctness improvement. Check `os.ErrNotExist` during staleness check.

## Open Questions

- Should the staleness cache be per-import-path or per-file? Per-import-path is simpler but coarser — a file change invalidates the cache for all packages containing that file.
- What happens when `RefreshStaleFiles` writes to the DB while the watch command is also writing? WAL mode + busy_timeout should handle it, but concurrent writer contention from two processes may need retry logic.
- Should `list_packages` annotate which packages have stale data? This would help AI agents decide which packages to drill into.

## Risk Annotations (from Premortem — 5 independent failure agents)

### Top Risks (sorted by risk score)

| #   | Risk                                                                          | Severity | Likelihood | Score | Mitigation                                      |
| --- | ----------------------------------------------------------------------------- | -------- | ---------- | ----- | ----------------------------------------------- |
| 1   | RunInTransaction data corruption under concurrent MCP requests                | Critical | High       | 12    | sync.Mutex + concurrent race test               |
| 2   | Staleness cache OOM at scale (120K entries, Go maps never shrink)             | Critical | High       | 12    | Bounded LRU cache, max 10K entries              |
| 3   | MCP + watch SQLITE_BUSY thundering herd                                       | Critical | High       | 12    | Best-effort writes + failure backoff            |
| 4   | Tree-sitter grammar update silently zeros out all claims                      | Critical | Medium     | 8     | Grammar contract tests + zero-claim write guard |
| 5   | Phantom claims for deleted files                                              | High     | Medium     | 6     | os.IsNotExist check in CheckPackageStaleness    |
| 6   | Unbounded re-extraction latency on large packages                             | High     | Medium     | 6     | context.WithTimeout(5s) + recover()             |
| 7   | Partial extraction on timeout leaves inconsistent DB                          | High     | Medium     | 6     | Atomic transaction rollback on timeout          |
| 8   | Scope mismatch: agents need task-boundary snapshots, not per-request patching | High     | Medium     | 6     | Debounce + document complementary roles         |

### Mitigations Promoted to Requirements (from premortem)

These mitigations surfaced from the premortem and should be added to the implementation:

**Must-Have additions:**

- **recover() wrapper around RefreshStaleFiles** — tree-sitter panic on malformed file crashes entire MCP server (Effort: Low)
- **Best-effort SQLITE_BUSY handling** — return stale data with warning instead of failing; cache failures with exponential backoff to prevent thundering herd (Effort: Medium)
- **Atomic timeout rollback** — if 5s timeout fires mid-extraction, roll back entire transaction rather than leaving mix of fresh/stale claims (Effort: Low — RunInTransaction already supports this)

**Should-Have additions:**

- **Bounded staleness cache** — use LRU with max 10K entries instead of plain map to prevent OOM at scale (Effort: Low)
- **Zero-claim write guard** — refuse to overwrite non-zero claim count with zero unless explicit force flag; prevents grammar breakage from destroying data (Effort: Low)
- **Validate repo_root on MCP startup** — skip staleness for repos whose root no longer exists (Effort: Low)
- **Debounce re-extraction** — 5s cooldown after file modification to avoid re-extracting on every IDE save (Effort: Medium)

### Resolved Open Questions

- **Writer contention (Q2)**: Premortem confirmed Critical risk. Mitigation: best-effort writes with SQLITE_BUSY fallback + exponential backoff on failures.
- **Staleness cache granularity (Q1)**: Per-import-path with 10s TTL, bounded at 10K entries. Simpler and sufficient for drill-down pattern.
- **Scope question (new)**: The staleness checker fills the gap between watch cycles (uncommitted edits). Document this clearly. Add debounce to avoid re-extracting on every keystroke-save.

## Research Provenance

Three independent research agents contributed:

- **Prior Art (gopls, Zoekt, Sourcegraph, LSP):** Confirmed that no major tool does query-time reindexing as primary. Validated the fallback architecture. Key finding: gopls uses push-based invalidation via LSP notifications, not pull-based hash checking.
- **First-Principles Technical Design:** Deep codebase analysis revealing that the entire staleness system is built but the on-ramp is a ~10-line wiring fix. Identified auto-discovery from `extraction_meta` as the cleanest configuration approach.
- **Failure Modes & Risk:** Found the `RunInTransaction` concurrency bug (critical), the deleted-file gap, the missing staleness cache, and the unbounded re-extraction latency risk.
