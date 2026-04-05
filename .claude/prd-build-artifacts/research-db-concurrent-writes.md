# Research: db-concurrent-writes

## Current State

### ClaimsDB struct (db/claims.go)
- Single `txMu sync.Mutex` serializes all `RunInTransaction` calls
- `exec dbExecutor` field swapped between `*sql.DB` and `*sql.Tx` inside RunInTransaction
- WAL mode already enabled on open (`PRAGMA journal_mode=WAL`)
- busy_timeout set to 5000ms

### RunInTransaction pattern
- Locks txMu, begins tx, swaps c.exec to tx, runs fn, restores c.exec, commits/rollbacks
- All ClaimsDB methods use `c.exec` so they transparently run inside or outside tx
- Problem: enrichment writes and structural extraction writes contend on the same mutex

### Key insight
- The `c.exec` swap pattern means only ONE transaction can be active at a time per ClaimsDB instance
- For concurrent structural + enrichment, we need separate transaction contexts
- RunEnrichmentTransaction must use its own tx variable, NOT swap c.exec (that would race with RunInTransaction)

### WAL checkpoint
- SQLite supports `PRAGMA wal_checkpoint(TRUNCATE)` to force checkpoint
- Should be called on the raw `c.db` (outside any transaction)

## Design Decision

RunEnrichmentTransaction will:
1. Lock enrichMu (separate from txMu)
2. Begin its own transaction on c.db directly
3. Pass the tx to fn via a parameter or use it directly
4. NOT swap c.exec (to avoid racing with structural transactions)

Since enrichment methods also use c.exec, we need a different approach:
- Option A: RunEnrichmentTransaction takes `func(tx *sql.Tx) error` so the caller uses the tx directly
- Option B: Add a separate enrichExec field

Option A is cleaner and avoids shared mutable state. The enrichment caller gets a raw *sql.Tx and calls Exec/Query on it directly. This keeps structural path unchanged.

Wait -- looking more carefully, the acceptance criteria says "RunEnrichmentTransaction(fn func() error) error" with the same signature as RunInTransaction. This means enrichment code should also be able to use ClaimsDB methods transparently.

Better approach: Use a separate enrichExec field. RunEnrichmentTransaction swaps enrichExec instead of exec. But then existing methods use c.exec, not c.enrichExec.

Simplest correct approach: RunEnrichmentTransaction takes `func() error` and uses a dedicated enrichExec field that enrichment-specific methods can reference. But that means existing methods won't work inside enrichment transactions.

Actually, re-reading the acceptance criteria more carefully: "uses enrichMu instead of txMu". The simplest interpretation that satisfies "structural extraction via RunInTransaction never blocks waiting on enrichment and vice versa" is:

1. Add enrichMu sync.Mutex to ClaimsDB
2. Add enrichExec dbExecutor field (defaults to c.db, swapped to tx in RunEnrichmentTransaction)
3. RunEnrichmentTransaction locks enrichMu, begins tx, swaps enrichExec, runs fn, restores
4. Enrichment callers use enrichExec-based methods (or the same methods but via a wrapper)

But the simplest that matches the spec signature `RunEnrichmentTransaction(fn func() error) error`: the fn closure captures the ClaimsDB and calls methods on it. Those methods use c.exec. If enrichment also swaps c.exec, it conflicts with structural.

Final decision: Two separate executor fields. enrichExec for enrichment path. Add a helper or let enrichment-specific code use c.enrichExec directly through a getter. The fn in RunEnrichmentTransaction can call `c.EnrichExec()` to get the executor.

Actually the cleanest approach: make RunEnrichmentTransaction swap a separate `enrichExec` field, and provide an `EnrichExec() dbExecutor` method. Enrichment code uses `c.EnrichExec().Exec(...)` etc.
