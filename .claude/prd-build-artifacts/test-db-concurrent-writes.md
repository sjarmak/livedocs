# Test Results: db-concurrent-writes

## Test Run
- Command: `go test -race ./db/... -timeout 60s`
- Result: PASS (2.078s)

## New Tests Added

| Test | Status | Purpose |
|------|--------|---------|
| TestRunEnrichmentTransaction | PASS | Verifies enrichment tx commits semantic claims via EnrichExec |
| TestRunEnrichmentTransaction_Rollback | PASS | Verifies enrichment tx rolls back on error |
| TestConcurrentStructuralAndEnrichment | PASS | Concurrent structural + enrichment goroutines, no deadlock |
| TestWALCheckpoint | PASS | WAL checkpoint executes without error, DB functional afterward |

## Acceptance Criteria Verification

- [x] ClaimsDB has separate mutexes: txMu (structural) and enrichMu (enrichment)
- [x] RunEnrichmentTransaction(fn func() error) error uses enrichMu
- [x] Structural RunInTransaction never blocks on enrichment (independent mutexes, verified by concurrent test)
- [x] Concurrent test verifies no deadlock between structural and enrichment paths
- [x] WALCheckpoint() forces PRAGMA wal_checkpoint(TRUNCATE)
- [x] WALCheckpoint test passes
- [x] go build ./... succeeds
- [x] go test ./db/... passes (all 33 tests pass with -race)

## Key Implementation Note

Added `_txlock=immediate` to the SQLite DSN so that `BEGIN` acquires a write lock upfront. This allows the busy_timeout to apply when another writer holds the lock, preventing SQLITE_BUSY errors during concurrent writes.
