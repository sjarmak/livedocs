# Test Results: enrich-incremental-support

## Test Run
- `go test ./sourcegraph/...` — PASS (0.077s)
- `go test ./cmd/livedocs/...` — PASS (1.156s)
- `go build ./...` — PASS

## New Tests Added (all PASS)
1. **TestSymbolIDs_FilterExact** — 3 symbols inserted, 2 IDs passed, only those 2 enriched
2. **TestSymbolIDs_Empty_UnchangedBehavior** — nil SymbolIDs uses normal selectSymbols path
3. **TestTombstone_CreationAndRetry** — router error creates tombstone; second run retries (not skipped)
4. **TestTombstone_ReplacementOnSuccess** — tombstone removed on successful enrichment
5. **TestTombstone_PermanentAfterThreeFailures** — 3 consecutive failures escalate to enrichment_permanently_failed
6. **TestTombstone_PermanentSkippedUntilSourceChange** — permanent tombstone is cache hit; hash change triggers retry
7. **TestBatchSizeCap** — 60 symbols inserted, at most 50 enriched (batch cap)
8. **TestDefaultPredicates** — exported function returns 4 predicates

## Existing Tests (all still PASS)
All 12 pre-existing tests continue to pass unchanged.

## Schema Changes
- Added `enrichment_failed`, `enrichment_permanently_failed` to predicate CHECK constraint
- Added `meta` to claim_tier CHECK constraint
