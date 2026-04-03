# Test Results: e2e-incremental-pipeline-test

## Command
```
go test -tags integration -v -timeout 120s ./integration/ -run TestE2EIncrementalPipeline
```

## Result: PASS (1.15s)

### Subtests

| Subtest | Status | Duration |
|---------|--------|----------|
| InitialExtract | PASS | 0.12s |
| DiffChangedPackages | PASS | 0.02s |
| CacheHitVerification | PASS | 0.03s |
| CacheHitVerification/PipelineAPICacheHit | PASS | 0.00s |
| DeletionReconciliation | PASS | 0.07s |
| VerifyClaims | PASS | 0.09s |

### Key Metrics

- Initial extract: 11 claims, 3 symbols (Hello, Config, package symbol)
- Diff: 2 files changed (pkg/a/a.go, pkg/b/b.go), 28 claims stored
- Pipeline cache hit: run 1 extracted=2, run 2 extracted=0 cached=2 (667us)
- Deletion: pkg/a/a.go correctly in deleted files, 0 claims for deleted file
- Post-deletion: pkg/b/b.go has 11 claims, symbols Process and Result preserved
- verify-claims: OK: 11 claim(s) verified (exit 0)

### Acceptance Criteria

- [x] go test -tags integration ./integration/... -run TestE2EIncrementalPipeline passes
- [x] Test creates temp git repo with Go files, runs extract, modifies files, runs diff, verifies only changed packages are reported
- [x] Test verifies cache hits: second extraction of unchanged files completes faster (pipeline API: 2nd run 0 extracted, all cached)
- [x] Test verifies deletion: after deleting a source file and re-extracting, claims for that file are removed
- [x] Test runs verify-claims on the resulting DB and gets exit code 0
