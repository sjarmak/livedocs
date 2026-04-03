# Plan: e2e-incremental-pipeline-test

## File: integration/e2e_incremental_test.go

## Step-by-step

### 1. File structure
- Build tag: `//go:build integration`
- Package: `integration`
- Single test function: `TestE2EIncrementalPipeline`
- Use subtests for each phase: Extract, Diff, CacheHit, Deletion, VerifyClaims

### 2. Setup helper: buildLivedocsBinary(t)
- `go build -o <tmpdir>/livedocs ./cmd/livedocs` from the project root
- Return path to binary
- Use `t.Cleanup` to remove

### 3. Setup helper: createTempGitRepo(t)
- Create temp dir
- `git init`
- `git config user.email/name` (for commits)
- Return path

### 4. Test flow

#### Phase A: Initial commit + extract
- Create `go.mod` with `module example.com/testrepo`
- Create `pkg/a/a.go` with a package, exported function `Hello()`, exported type `Config`
- `git add . && git commit -m "initial"`
- Record SHA as `commit1`
- Run `livedocs extract <repo> --repo testrepo --output <db>`
- Verify DB has claims for `Hello` and `Config` symbols

#### Phase B: Modify + add new file + diff
- Modify `pkg/a/a.go` — change `Hello()` to `HelloWorld()`, add new function `Goodbye()`
- Create `pkg/b/b.go` with exported function `Process()`
- `git add . && git commit -m "modify a, add b"`
- Record SHA as `commit2`
- Run `livedocs diff commit1 commit2 <repo> --repo testrepo --format json`
- Parse JSON output, verify ChangedFiles includes `pkg/a/a.go` and `pkg/b/b.go`
- Verify unchanged files are NOT in ChangedFiles

#### Phase C: Cache hit verification
- Run `livedocs extract` again on same unchanged state
- Compare timing: second run should be faster (or at least: use pipeline API directly)
- Actually, the `extract` command removes existing DB each time (line 79: `os.Remove(outputPath)`). So cache hits won't be visible via CLI extract.
- Better approach: use the pipeline Go API directly for cache hit testing (like existing TestCacheHit does)
- Or: run `livedocs diff commit1 commit2` twice and check second run is faster

#### Phase D: Deletion
- Delete `pkg/a/a.go`
- `git add . && git commit -m "delete a"`
- Record SHA as `commit3`
- Run `livedocs diff commit2 commit3 <repo> --format json`
- Verify DeletedFiles includes `pkg/a/a.go`
- Re-run `livedocs extract` on current state
- Query DB: verify no claims reference `pkg/a/a.go`

#### Phase E: verify-claims
- Run `livedocs verify-claims --db <db> <repo>`
- Verify exit code 0 (all remaining claims are valid)

### 5. DB querying
- Use `database/sql` + `modernc.org/sqlite` to open the DB file and run queries directly
- Query: `SELECT COUNT(*) FROM claims WHERE source_file = ?` to check file claims
- Query: `SELECT COUNT(*) FROM claims` to check total claims

### 6. Revised approach for cache hits
Since `extract` always drops the DB, we need to test cache hits differently:
- Use the pipeline Go API directly (import pipeline, cache, db packages)
- Create pipeline with on-disk cache
- Run pipeline twice between same commits
- Second run should have CacheHits > 0 and FilesExtracted == 0

### 7. Considerations
- Go deep extractor needs `go/packages` which needs a compilable Go module
- Temp repo must have `go.mod` and valid Go files
- The deep extractor runs on the whole repo, not individual files
- For `livedocs diff`, it only registers Go deep extractor, so our .go files will use it
- If go/packages fails in temp repo (no deps, etc.), we may get warnings but the command should still work
- Alternative: use .py or .ts files to avoid go/packages complexity
- Decision: use .py files since tree-sitter Python works without any build tooling

Wait - re-examining extract_cmd.go: Go deep extractor is Phase 1 (whole repo), then Phase 2 walks non-Go files. If we use Python files, they'll be handled by Phase 2 with tree-sitter. This is simpler and avoids go/packages issues.

### 8. Final approach: use Python files
- Create .py files in the temp repo (no go.mod needed)
- `livedocs extract` will run Go deep extractor (which will find no .go files / produce no claims), then walk .py files with tree-sitter
- This avoids go/packages compilation issues in a temp repo
- Python files need only be syntactically valid Python

Actually, let me reconsider. The Go deep extractor is called on the whole repoDir regardless. It will try to load Go packages. If there are none, it may error but the extract command continues (line 148: logs warning, doesn't return error).

For diff: same — registers only Go deep extractor in registry. Python files would not have a registered extractor in the diff command! Looking at diff.go line 126-133: only Go deep extractor is registered.

So diff won't process .py files. We need .go files.

### 9. Final final approach: simple Go files with go.mod
- `go mod init example.com/testrepo`
- Simple Go files that compile standalone (no external deps)
- Go deep extractor should handle them
- If go/packages has issues in the temp env, the extract command will log a warning and continue; tree-sitter will still pick up .go files if registered

Actually looking again at extract_cmd.go: Go files are handled by Phase 1 (deep extractor on whole repo), and Phase 2 explicitly skips .go files (line 169-171). So .go files ONLY go through the deep extractor.

For diff.go: same — only Go deep extractor is registered.

Let me check if go/packages works in a simple temp repo. It should, as long as there's a go.mod and the files compile.

### 10. Revised final plan
- Create temp git repo with go.mod + simple .go files
- Run livedocs extract (Go deep extractor handles .go)
- If deep extractor fails (common in constrained envs), test won't get claims
- Fallback: also register tree-sitter for .go in the test by using pipeline API directly

Best of both worlds: use the CLI for extract/diff/verify-claims where possible, fall back to pipeline API for cache hit testing. If CLI extract produces 0 claims (deep extractor fails), skip dependent assertions or use the pipeline API with tree-sitter extractor.

Actually simplest: just use the Go pipeline API directly for everything (like existing integration tests do) and only use CLI for verify-claims at the end. The acceptance criteria say "use os/exec to run the actual livedocs binary" but the real goal is testing the incremental pipeline. Let me use a hybrid: pipeline API for the core logic, CLI for verify-claims.

No wait, re-reading the task: "The test should use os/exec to run the actual livedocs binary". OK, I'll use the CLI throughout.

### 11. True final plan
1. Build `livedocs` binary
2. Create temp git repo with `go.mod` and `.go` files
3. Initial commit, run `livedocs extract`
4. Modify files + add new file, second commit, run `livedocs diff` in JSON mode
5. For cache hits: run `livedocs diff` again between same commits, parse JSON, check timing
6. Delete file, third commit, run `livedocs extract` fresh, query DB for deleted file's claims
7. Run `livedocs verify-claims --db`
8. Query DB directly with database/sql for deletion verification
