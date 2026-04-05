# Plan: db-concurrent-writes

## Step 1: Add enrichMu and enrichExec to ClaimsDB struct
- Add `enrichMu sync.Mutex` field
- Add `enrichExec dbExecutor` field (defaults to c.db in OpenClaimsDB)

## Step 2: Implement RunEnrichmentTransaction
- Same pattern as RunInTransaction but uses enrichMu and enrichExec
- Locks enrichMu, begins tx on c.db, swaps enrichExec to tx, runs fn, restores enrichExec
- Independent of txMu so structural and enrichment can proceed concurrently

## Step 3: Add EnrichExec() accessor
- Returns c.enrichExec so enrichment code can execute queries through the enrichment transaction
- Enrichment callers use `c.EnrichExec()` instead of the implicit `c.exec`

## Step 4: Implement WALCheckpoint
- Method on ClaimsDB that calls `PRAGMA wal_checkpoint(TRUNCATE)` on c.db
- No mutex needed (pragma runs on the connection pool, not through exec)

## Step 5: Write tests
- TestRunEnrichmentTransaction: basic enrichment tx commits correctly
- TestConcurrentStructuralAndEnrichment: goroutines run both tx types simultaneously, no deadlock
- TestWALCheckpoint: checkpoint executes without error

## Step 6: Run go build and go test, fix any issues
