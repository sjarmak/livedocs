# Research: e2e-incremental-pipeline-test

## Existing Test Patterns

### integration/pipeline_test.go
- Build tag: `//go:build integration`
- Package: `integration`
- Uses real kubernetes repos at `~/kubernetes/` as test corpus
- Helper functions: `clientGoRoot()`, `kubeRoot()`, `collectGoFiles()`, `newExtractorRegistry()`, `openTestDBs()`
- Tests: `TestExtractClientGo`, `TestDiffClientGo`, `TestCacheHit`, `TestGeneratedExclusion_RealRepo`, `TestCheckStateless_Performance`
- Pipeline instantiation: `pipeline.New(pipeline.Config{...})` then `p.Run(ctx, fromSHA, toSHA)`
- Uses `recentGoChangeSHAs()` to find real git commits that touch .go files

### integration/ts_claims_test.go
- No build tag (always runs)
- Tests tree-sitter extraction + DB round-trip for TypeScript
- Helper: `tempClaimsDB()` creates on-disk SQLite in t.TempDir()
- Helper: `storeClaims()` upserts symbols and inserts claims

## Pipeline Architecture

### pipeline/pipeline.go
- `Pipeline.Run(ctx, fromCommit, toCommit)` is the main entry point
- Steps: git diff -> handle deletions -> process changed files (hash, cache check, extract, store)
- `processFile()`: reads file, SHA-256 hash, cache check via `cache.Store.Hit()`, extract via `registry.ExtractFile()`, store claims, update cache
- `markDeleted()`: tombstones in both cache and claims DB
- Returns `Result` with counters: FilesChanged, FilesExtracted, FilesDeleted, FilesSkipped, CacheHits, ClaimsStored

### gitdiff/gitdiff.go
- `DiffBetween(repoDir, from, to)` runs `git diff --name-status`
- `ChangedPaths()` returns non-deleted paths
- `DeletedPaths()` returns deleted paths + old paths of renames

### cache/cache.go
- Interface `Store` with `Hit()`, `Put()`, `MarkDeleted()`, `Reconcile()`, `Evict()`, `TotalSize()`, `Close()`
- Composite key: content hash + extractor version + grammar version

### db/claims.go
- `ClaimsDB` with methods: `UpsertSymbol`, `InsertClaim`, `GetClaimsByFile`, `MarkFileDeleted`, `DeleteClaimsByExtractorAndFile`, `GetSourceFile`, etc.
- `SourceFile` table tracks content hashes for incremental indexing
- `GetClaimsByFile(sourceFile)` returns all claims for a file path
- `SearchSymbolsByName(pattern)` with LIKE wildcards

## CLI Commands

### cmd/livedocs/extract_cmd.go
- `livedocs extract <path>` — walks repo, runs Go deep extractor + tree-sitter for non-Go
- Flags: `--repo`, `--output`/`-o`
- Creates on-disk SQLite DB, removes existing before starting
- Registers Go deep extractor + TS/Python/Shell tree-sitter extractors

### cmd/livedocs/diff.go
- `livedocs diff <from> <to> [repo-path]` — runs pipeline between two commits
- Flags: `--format` (text/json), `--repo`
- Uses in-memory DBs (ephemeral per run)

### cmd/livedocs/verify_claims.go
- `livedocs verify-claims [path]` — verifies claims DB against source code
- Flags: `--db`, `--staleness`, `--canary`, `--check-existing`
- Basic mode: checks every claim's source file exists and is not stale
- Exits 0 if all claims match, exits 1 if drift detected

## Key Finding: Test Approach
The acceptance criteria say to use `os/exec` to run the actual `livedocs` binary. This means:
1. Build the binary first with `go build ./cmd/livedocs`
2. Create a temp git repo with Go source files
3. Run `livedocs extract` to populate a DB
4. Modify files, commit, run `livedocs diff`
5. Run `livedocs verify-claims --db <db>` and check exit code

The tree-sitter extractor (used by pipeline for .go files via FastExtractor) works on individual files. The Go deep extractor uses `go/packages` which needs a valid Go module. For simplicity in a temp repo, we should use the tree-sitter path by registering FastExtractor only.

**Important**: The `extract` command uses the Go deep extractor for .go files, not tree-sitter. The `diff` command also uses the deep extractor. This means our temp repo needs to be a valid Go module for `go/packages` to work. Alternative: use non-Go files (e.g., .ts, .py) or ensure the temp repo has go.mod.

Actually, looking more carefully at `extract_cmd.go`, it uses `GoDeepExtractor` for .go files via Phase 1, then walks non-Go files for tree-sitter. The diff command also registers the Go deep extractor. So our temp repo needs either: a valid Go module, or we use Python/TypeScript files instead.

Simplest approach: create a Go module in the temp repo with `go mod init`, write simple .go files.
