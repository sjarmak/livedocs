# PRD: Staleness Hardening & Cleanup

## Problem Statement

The staleness wiring PRD landed 7 units that activate the lazy staleness checker end-to-end. Code review surfaced several hardening gaps and deferred risk mitigations that should be addressed before the staleness system is production-ready. These are small, well-scoped items — most are under 30 lines — but collectively they close real correctness and reliability gaps.

## Goals & Non-Goals

### Goals

- Close reviewer-identified gaps from the staleness wiring PRD build
- Implement deferred risk mitigations from the premortem analysis
- Reach test coverage parity for new code paths

### Non-Goals

- New features or architectural changes
- Performance optimization beyond what's specified
- Changes to the watch command or primary extraction pipeline

## Requirements

### Must-Have

- Requirement: Add unit tests for `discoverRepoRoots` in `cmd/livedocs/mcp.go`
  - Acceptance: A new `cmd/livedocs/mcp_test.go` file exists with a `TestDiscoverRepoRoots` table-driven test covering all 6 code paths: (1) glob error / empty dir, (2) DB open failure, (3) GetExtractionMeta failure, (4) empty RepoRoot, (5) RepoRoot directory doesn't exist, (6) valid RepoRoot. Uses a temp directory with fixture `.claims.db` files. `go test -race ./cmd/livedocs/...` passes.

- Requirement: Zero-claim write guard — refuse to overwrite non-zero claim count with zero during re-extraction unless an explicit force flag is passed
  - Acceptance: `reExtractFile` in `mcpserver/staleness.go` checks the new claim count before writing. If the extractor returns 0 claims but the DB has >0 claims for that file, the transaction is skipped and an error is returned (e.g. "refusing to overwrite N claims with 0 for file X — possible grammar failure"). A test verifies: file has 5 existing claims, extractor returns 0 claims, reExtractFile returns an error and existing claims are preserved. `go test -race ./mcpserver/...` passes.

- Requirement: Validate repo_root paths on MCP startup — skip staleness for repos whose root directory no longer exists on disk
  - Acceptance: `discoverRepoRoots` in `cmd/livedocs/mcp.go` already validates with `os.Stat`. Verify this is working correctly and add a test case: create a `.claims.db` with a `repo_root` pointing to a non-existent directory, call `discoverRepoRoots`, verify the repo is not included in the returned map and a warning is logged. This is covered by the `discoverRepoRoots` test suite above.

- Requirement: Remove dead `nowFunc` field from StalenessChecker struct
  - Acceptance: `StalenessChecker` struct in `mcpserver/staleness.go` does not have a `nowFunc` field. The cache's `nowFunc` is the only clock override. `go build ./...` succeeds. `go test -race ./mcpserver/...` passes.

- Requirement: Log `MarkFileDeleted` errors instead of silently discarding them
  - Acceptance: In `CheckPackageStaleness` in `mcpserver/staleness.go`, when `cdb.MarkFileDeleted` returns an error, it is logged (e.g. via `log.Printf`) rather than assigned to `_`. `go build ./...` succeeds.

### Should-Have

- Requirement: Debounce re-extraction — if a file was re-extracted within the last 5 seconds, skip re-extraction even if the hash has changed
  - Acceptance: `StalenessChecker` tracks last re-extraction time per file path. `RefreshStaleFiles` skips files that were re-extracted within the last 5 seconds. A test verifies: re-extract a file, modify it immediately, call RefreshStaleFiles again within 5s — the file is skipped. After 5s, it is re-extracted. `go test -race ./mcpserver/...` passes.

- Requirement: `list_packages` annotates which packages have stale data
  - Acceptance: `ListPackagesHandler` in `mcpserver/tools.go` checks staleness for each listed package (when StalenessChecker is available) and includes a `stale: true` field in the JSON response for packages with changed files. A test verifies the annotation appears. `go test -race ./mcpserver/...` passes.

## Design Considerations

All items are intentionally small and isolated. No cross-cutting changes. Each can be implemented and tested independently.

The zero-claim write guard is the highest-value item — it prevents a silent data-loss scenario where a tree-sitter grammar update or parse failure wipes all claims for a file. The guard is conservative: it only blocks zero-claim overwrites, not reductions (e.g. 10 claims to 3 is fine).

The debounce prevents redundant re-extraction when an IDE saves a file rapidly (e.g. auto-save every keystroke). Without debounce, each save triggers a full re-extract cycle.
